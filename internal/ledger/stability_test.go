package ledger

import (
	"fmt"
	"testing"
)

// TestRejectReasonStableAcrossBatches 是拒绝原因稳定性的核心测试：
// 同一批记录，用多种分批方式、多种到达顺序、与不同数据混批投递。
//
// 不变量：
//   - 内容非法的记录：在每一种投递方案里都必然被拒，且原因永远相同
//     （只由自身内容决定）；
//   - 内容合法的记录：只会“被接受”或因幂等去重得到 duplicate_id，
//     绝不会得到其他原因。
func TestRejectReasonStableAcrossBatches(t *testing.T) {
	pool := []Record{
		{ID: "ok1", Timestamp: 1, From: "a", To: "b", Amount: "10"},
		{ID: "ok2", Timestamp: 2, From: "b", To: "c", Amount: "20"},
		{ID: "badAmt", Timestamp: 3, From: "c", To: "d", Amount: "xyz"}, // 非法金额
		{ID: "twin", Timestamp: 4, From: "d", To: "e", Amount: "30"},    // 合法
		{ID: "twin", Timestamp: 4, From: "d", To: "e", Amount: "abc"},   // 同 ID 非法孪生
		{ID: "self", Timestamp: 5, From: "x", To: "x", Amount: "1"},     // 自转
		{ID: "", Timestamp: 6, From: "a", To: "b", Amount: "1"},         // 缺 ID
	}
	// 内容非法的记录及其唯一可能的原因。
	invalidWant := map[string]RejectReason{
		"badAmt|3|c|d|xyz": ReasonBadAmount,
		"twin|4|d|e|abc":   ReasonBadAmount,
		"self|5|x|x|1":     ReasonSelfTransfer,
		"|6|a|b|1":         ReasonMissingID,
	}
	fingerprint := func(r Record) string {
		return fmt.Sprintf("%s|%d|%s|%s|%s", r.ID, r.Timestamp, r.From, r.To, r.Amount)
	}

	plans := map[string][][]Record{
		"单批":     {pool},
		"逐条":     {{pool[0]}, {pool[1]}, {pool[2]}, {pool[3]}, {pool[4]}, {pool[5]}, {pool[6]}},
		"反序两批":   {{pool[6], pool[5], pool[4]}, {pool[3], pool[2], pool[1], pool[0]}},
		"非法孪生先投": {{pool[4]}, {pool[0], pool[1], pool[2], pool[3], pool[5], pool[6]}},
		"合法孪生先投": {{pool[3]}, {pool[0], pool[1], pool[2], pool[4], pool[5], pool[6]}},
		"混入重复数据": {{pool[0], pool[0], pool[1]}, {pool[2], pool[3], pool[4], pool[5], pool[6], pool[2]}},
	}

	t.Logf("输入：%d 条记录（合法/非法金额/共享ID孪生/自转/缺ID），%d 种投递方案", len(pool), len(plans))
	for name, batches := range plans {
		l := New()
		invalidSeen := map[string]int{}
		for _, b := range batches {
			for _, rej := range l.Ingest(b).Rejected {
				fp := fingerprint(rej.Record)
				if want, isInvalid := invalidWant[fp]; isInvalid {
					// 非法记录：原因必须是那个唯一原因。
					if rej.Reason != want {
						t.Fatalf("方案[%s]非法记录{%s}原因=%s，应为 %s", name, fp, rej.Reason, want)
					}
					invalidSeen[fp]++
					continue
				}
				// 合法记录：只允许 duplicate_id（幂等去重），不得是别的原因。
				if rej.Reason != ReasonDuplicate {
					t.Fatalf("方案[%s]合法记录{%s}被以 %s 拒绝", name, fp, rej.Reason)
				}
			}
		}
		// 每条非法记录在该方案里都必须至少出现一次拒绝（永远不会“侥幸进入”）。
		for fp := range invalidWant {
			if invalidSeen[fp] == 0 {
				t.Fatalf("方案[%s]中非法记录{%s}未被拒绝", name, fp)
			}
		}
		t.Logf("方案[%s]：%d 条非法记录原因稳定，合法记录仅可能被 duplicate_id 拒绝",
			name, len(invalidSeen))
	}
	t.Log("结论：非法记录拒绝原因只由自身内容决定，与分批/顺序/混批无关")
}

// TestRejectReasonStableValidTwin 合法记录与非法孪生同批时：
// 合法记录必须被接受（而不是被非法孪生的占位误拒），非法孪生报自己的原因。
func TestRejectReasonStableValidTwin(t *testing.T) {
	l := New()
	res := l.Ingest([]Record{
		{ID: "k", Timestamp: 1, From: "a", To: "b", Amount: "bad"}, // 非法孪生先到
		{ID: "k", Timestamp: 1, From: "a", To: "b", Amount: "100"}, // 合法记录
	})
	t.Logf("输入：同 ID 非法+合法孪生同批（非法在前）")
	for _, r := range res.Rejected {
		t.Logf("  拒绝: id=%s amount=%s 原因=%s", r.Record.ID, r.Record.Amount, r.Reason)
	}
	if len(res.Accepted) != 1 || res.Accepted[0].Amount != 100 {
		t.Fatalf("合法记录应被接受：accepted=%+v", res.Accepted)
	}
	if len(res.Rejected) != 1 || res.Rejected[0].Reason != ReasonBadAmount {
		t.Fatalf("非法孪生应报 invalid_amount：%+v", res.Rejected)
	}
	t.Log("结论：合法记录被接受，非法孪生报 invalid_amount（不再互相干扰）")
}
