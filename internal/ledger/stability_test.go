package ledger

import (
	"fmt"
	"math/rand"
	"testing"
)

// finalReasons 按多种分批/顺序方式增量摄取同一记录集合，汇总每条记录
// （按下标）在最终账本下应有的拒绝原因。为了能跨重排对比，用
// recordKey 在不同排列间标识“同一条记录”。
func recordKey(r Record) string {
	return fmt.Sprintf("%s|%d|%s|%s|%s", r.ID, r.Timestamp, r.From, r.To, r.Amount)
}

// ingestPermutation 按给定排列逐条（每条单独一批）摄取，返回
// 每条记录在“它所在那次调用”得到的原因（按排列下标）。
func ingestPermutation(t *testing.T, recs []Record, perm []int) map[string]RejectReason {
	t.Helper()
	l := New()
	got := map[string]RejectReason{}
	for _, pi := range perm {
		r := recs[pi]
		res := l.Ingest([]Record{r})
		k := recordKey(r)
		if len(res.Rejected) == 1 {
			// 只记录第一次得到的结论用于结构性原因核对；duplicate 与
			// 是否接受依赖调用时序，最终稳定性用 FinalTx 集合 + Classify 核对。
			if _, seen := got[k]; !seen {
				got[k] = res.Rejected[0].Reason
			}
		} else {
			got[k] = ReasonNone
		}
	}
	return got
}

// TestRejectReasonStableAcrossBatches 核对：同一条坏记录无论与什么数据混批、
// 按什么顺序投递，它得到的结构原因始终只有一个；且同一份记录集合无论怎么
// 分批，最终账本的接受集合完全相同。
func TestRejectReasonStableAcrossBatches(t *testing.T) {
	recs := []Record{
		{ID: "dup", Timestamp: 5, From: "a", To: "b", Amount: "100"}, // 有效（同ID冲突的“输家”）
		{ID: "dup", Timestamp: 1, From: "a", To: "c", Amount: "10"},  // 有效（内容全序最小，赢家）
		{ID: "dup", Timestamp: 1, From: "a", To: "", Amount: "10"},   // 结构坏（缺 to）
		{ID: "dup", Timestamp: 1, From: "a", To: "b", Amount: "x"},   // 结构坏（坏金额）
		{ID: "dup", Timestamp: 1, From: "a", To: "a", Amount: "10"},  // 自转
		{ID: "u1", Timestamp: 2, From: "m", To: "n", Amount: "7"},    // 无关数据
		{ID: "", Timestamp: 2, From: "p", To: "q", Amount: "7"},      // 缺 ID
	}
	// Classify 给出纯内容视角的权威最终原因。
	wantFinal := Classify(recs)
	t.Log("==== Classify 权威最终原因（输入 -> 原因）====")
	for i, r := range recs {
		t.Logf("  %-40q -> %s", recordKey(r), wantFinal[i])
	}

	// 结构性坏记录的原因在任何排列下都必须是其结构原因（绝不被顶成 duplicate）。
	structural := map[string]RejectReason{}
	for i, r := range recs {
		if wantFinal[i] != ReasonNone && wantFinal[i] != ReasonDuplicate {
			structural[recordKey(r)] = wantFinal[i]
		}
	}

	rng := rand.New(rand.NewSource(42))
	var refTxs []Tx
	for trial := 0; trial < 30; trial++ {
		perm := rng.Perm(len(recs))
		// 再随机切分成 1~4 批。
		l := New()
		idx := 0
		batches := 1 + rng.Intn(4)
		for b := 0; b < batches && idx < len(perm); b++ {
			size := 1 + rng.Intn(len(perm)-idx)
			if b == batches-1 {
				size = len(perm) - idx
			}
			batch := make([]Record, 0, size)
			for j := 0; j < size; j++ {
				batch = append(batch, recs[perm[idx]])
				idx++
			}
			l.Ingest(batch)
		}
		gotTxs := l.Txs()
		if trial == 0 {
			refTxs = gotTxs
		} else if fmt.Sprint(gotTxs) != fmt.Sprint(refTxs) {
			t.Fatalf("第 %d 次排列的最终账本与基准不一致：\n%v\nvs\n%v", trial, gotTxs, refTxs)
		}

		// 结构性坏记录逐条单独重投核对：原因恒定。
		for k, want := range structural {
			// 找到该记录内容
			var rr Record
			for _, r := range recs {
				if recordKey(r) == k {
					rr = r
				}
			}
			res := l.Ingest([]Record{rr})
			if len(res.Rejected) != 1 || res.Rejected[0].Reason != want {
				t.Fatalf("坏记录 %s 在混批后重投原因漂移：got=%v want=%s", k, res.Rejected, want)
			}
		}
	}
	t.Logf("30 种随机分批/顺序的最终账本完全一致：接受 %d 笔，dup 赢家=%v",
		len(refTxs), []string{refTxs[0].ID})
	if len(refTxs) != 2 { // dup 赢家 + u1
		t.Fatalf("最终应接受 2 笔，实际 %d: %v", len(refTxs), refTxs)
	}
	if refTxs[0].ID != "dup" || refTxs[0].To != "c" {
		t.Fatalf("dup 冲突赢家应为时间戳1、to=c 的记录，实际 %+v", refTxs[0])
	}
}

// TestSameIDConflictReplaced 核对同 ID 有效冲突：后来的更优记录替换旧记录，
// Replaced 明确报告，且最终视图与“一次性全量摄取”一致。
func TestSameIDConflictReplaced(t *testing.T) {
	old := Record{ID: "t", Timestamp: 9, From: "a", To: "z", Amount: "5"}
	better := Record{ID: "t", Timestamp: 1, From: "a", To: "b", Amount: "5"}

	// 顺序一：旧的先来。
	l1 := New()
	r1 := l1.Ingest([]Record{old})
	r2 := l1.Ingest([]Record{better})
	if len(r1.Accepted) != 1 || len(r2.Accepted) != 1 || len(r2.Replaced) != 1 {
		t.Fatalf("替换流程不符合预期：%+v %+v", r1, r2)
	}
	if r2.Replaced[0].Timestamp != 9 {
		t.Fatalf("被替换的应是旧记录 ts=9，实际 %+v", r2.Replaced[0])
	}
	t.Logf("旧记录被更优记录替换：replaced=%v accepted=%v", r2.Replaced, r2.Accepted)

	// 顺序二：更优的先来，旧的后来 -> 旧的 duplicate，无替换。
	l2 := New()
	l2.Ingest([]Record{better})
	r3 := l2.Ingest([]Record{old})
	if len(r3.Accepted) != 0 || r3.Rejected[0].Reason != ReasonDuplicate || len(r3.Replaced) != 0 {
		t.Fatalf("逆序冲突结论错误：%+v", r3)
	}

	if fmt.Sprint(l1.Txs()) != fmt.Sprint(l2.Txs()) {
		t.Fatalf("两种顺序最终账本不一致：%v vs %v", l1.Txs(), l2.Txs())
	}
	// 与一次性全量摄取对比。
	l3 := New()
	l3.Ingest([]Record{old, better})
	if fmt.Sprint(l3.Txs()) != fmt.Sprint(l1.Txs()) {
		t.Fatalf("分批与全量结果不一致：%v vs %v", l3.Txs(), l1.Txs())
	}
	t.Logf("冲突定胜负与投递顺序无关，最终账本=%v", l1.Txs())
}
