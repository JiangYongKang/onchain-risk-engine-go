package ledger

import (
	"fmt"
	"testing"
)

func reasonIndex(rs []RejectedRecord) map[RejectReason]int {
	m := map[RejectReason]int{}
	for _, r := range rs {
		m[r.Reason]++
	}
	return m
}

func TestIngestValidationReasons(t *testing.T) {
	l := New()
	input := []Record{
		{ID: "", Timestamp: 1, From: "a", To: "b", Amount: "10"},    // 缺 ID
		{ID: "r2", Timestamp: 1, From: "", To: "b", Amount: "10"},   // 缺 from
		{ID: "r3", Timestamp: 1, From: "a", To: "", Amount: "10"},   // 缺 to
		{ID: "r4", Timestamp: 1, From: "a", To: "a", Amount: "10"},  // 自转
		{ID: "r5", Timestamp: -1, From: "a", To: "b", Amount: "10"}, // 非法时间戳
		{ID: "r6", Timestamp: 1, From: "a", To: "b", Amount: ""},    // 金额缺失
		{ID: "r7", Timestamp: 1, From: "a", To: "b", Amount: "abc"}, // 金额类型非法
		{ID: "r8", Timestamp: 1, From: "a", To: "b", Amount: "0"},   // 金额非正
		{ID: "ok1", Timestamp: 1, From: "a", To: "b", Amount: "10"}, // 正常
	}
	res := l.Ingest(input)
	t.Logf("输入=%d 条；接受=%d；拒绝=%d", len(input), len(res.Accepted), len(res.Rejected))
	idx := reasonIndex(res.Rejected)
	want := []RejectReason{
		ReasonMissingID, ReasonMissingFrom, ReasonMissingTo, ReasonSelfTransfer,
		ReasonBadTimestamp, ReasonBadAmount,
	}
	// BadAmount 同时覆盖：缺失、非数字、非正。
	for _, r := range res.Rejected {
		t.Logf("拒绝记录 id=%q 原因=%s 详情=%s", r.Record.ID, r.Reason, r.Detail)
	}
	for _, w := range want {
		if idx[w] == 0 {
			t.Fatalf("期望出现拒绝原因 %s，实际计数=%d", w, idx[w])
		}
	}
	if idx[ReasonBadAmount] != 3 {
		t.Fatalf("invalid_amount 应出现 3 次，实际 %d", idx[ReasonBadAmount])
	}
	if len(res.Accepted) != 1 {
		t.Fatalf("应只接受 1 条，实际 %d", len(res.Accepted))
	}
}

func TestIngestDuplicateIdempotent(t *testing.T) {
	l := New()
	first := []Record{{ID: "t1", Timestamp: 10, From: "a", To: "b", Amount: "100"}}
	// 重复投递：相同内容再次来，以及批内重复。
	r1 := l.Ingest(first)
	r2 := l.Ingest(first)
	r3 := l.Ingest([]Record{first[0], first[0]})

	t.Logf("首次接受=%d；重复投递接受=%d 拒绝原因=%s；批内重复拒绝数=%d",
		len(r1.Accepted), len(r2.Accepted), r2.Rejected[0].Reason, len(r3.Rejected))

	if len(r1.Accepted) != 1 || len(r2.Accepted) != 0 ||
		r2.Rejected[0].Reason != ReasonDuplicate || len(r3.Rejected) != 2 {
		t.Fatal("重复投递的拒绝行为不符合预期")
	}
	if len(l.Txs()) != 1 {
		t.Fatalf("重复处理后账本仍应只有 1 条交易，实际 %d", len(l.Txs()))
	}
	// 被拒绝过的坏记录重投，仍按同一原因拒绝，不会被接受。
	bad := []Record{{ID: "bad", Timestamp: 1, From: "a", To: "b", Amount: "x"}}
	l.Ingest(bad)
	again := l.Ingest(bad)
	if len(again.Rejected) != 1 || again.Rejected[0].Reason != ReasonBadAmount {
		t.Fatal("坏记录重投结论不稳定")
	}
	if l.Has("bad") {
		t.Fatal("坏记录不应进入已接受集合")
	}
}

func TestIngestOutOfOrderDeterministic(t *testing.T) {
	l := New()
	// 乱序分两批到达。
	batchA := []Record{
		{ID: "t3", Timestamp: 30, From: "c", To: "d", Amount: "30"},
		{ID: "t1", Timestamp: 10, From: "a", To: "b", Amount: "10"},
	}
	batchB := []Record{
		{ID: "t2", Timestamp: 20, From: "b", To: "c", Amount: "20"}, // 夹在中间
		{ID: "t4", Timestamp: 40, From: "d", To: "a", Amount: "40"},
	}
	l.Ingest(batchA)
	l.Ingest(batchB)

	txs := l.Txs()
	t.Log("乱序输入后的规范化顺序：")
	for i, x := range txs {
		t.Logf("  #%d id=%s ts=%d %s->%s %d", i, x.ID, x.Timestamp, x.From, x.To, x.Amount)
	}
	gotIDs := make([]string, len(txs))
	gotTS := make([]int64, len(txs))
	for i, x := range txs {
		gotIDs[i] = x.ID
		gotTS[i] = x.Timestamp
	}
	wantIDs := []string{"t1", "t2", "t3", "t4"}
	if fmt.Sprint(gotIDs) != fmt.Sprint(wantIDs) {
		t.Fatalf("顺序不确定/错误：%v", gotIDs)
	}

	// 对照组：同数据但一次性按相反顺序投递，最终状态必须一致。
	l2 := New()
	l2.Ingest([]Record{batchB[1], batchB[0], batchA[1], batchA[0]})
	txs2 := l2.Txs()
	if fmt.Sprint(txs) != fmt.Sprint(txs2) {
		t.Fatalf("不同到达顺序产生了不同最终状态：%v vs %v", txs, txs2)
	}
	t.Logf("乱序重投最终状态一致，共 %d 条", len(txs2))
}

func TestIngestLargeAmountSafe(t *testing.T) {
	l := New()
	// 超大但合法的金额（int64 范围内）应接受；越界应按非法拒绝。
	res := l.Ingest([]Record{
		{ID: "big", Timestamp: 1, From: "a", To: "b", Amount: "9223372036854775807"},
		{ID: "overflow", Timestamp: 1, From: "a", To: "b", Amount: "9223372036854775808"},
	})
	if len(res.Accepted) != 1 || res.Rejected[0].Reason != ReasonBadAmount {
		t.Fatalf("超大金额处理错误：%+v", res)
	}
}
