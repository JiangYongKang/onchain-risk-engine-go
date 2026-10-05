package e2e

import (
	"fmt"
	"sync"
	"testing"

	"github.com/highcumontoa/onchain-risk-engine-go/internal/graph"
	"github.com/highcumontoa/onchain-risk-engine-go/internal/ledger"
	"github.com/highcumontoa/onchain-risk-engine-go/internal/risk"
	"github.com/highcumontoa/onchain-risk-engine-go/internal/sim"
)

// TestNewCapabilitiesEndToEnd 串联新能力：
// 分批摄取 -> 增量重算对拍全量 -> 对 flagged 账户下钻溯源 ->
// 规则升级后跨版本对比 -> 并发摄取+分析+模拟的一致性。
func TestNewCapabilitiesEndToEnd(t *testing.T) {
	// 1) 分 4 批摄取：s1..s4 汇聚到 hub，hub 拆分到 d1..d4，另有环 r0->r1->r2->r0。
	l := ledger.New()
	batches := [][]ledger.Record{
		{{ID: "in1", Timestamp: 1, From: "s1", To: "hub", Amount: "100"},
			{ID: "in2", Timestamp: 2, From: "s2", To: "hub", Amount: "200"}},
		{{ID: "in3", Timestamp: 3, From: "s3", To: "hub", Amount: "300"},
			{ID: "in4", Timestamp: 4, From: "s4", To: "hub", Amount: "400"}},
		{{ID: "out1", Timestamp: 5, From: "hub", To: "d1", Amount: "50"},
			{ID: "out2", Timestamp: 6, From: "hub", To: "d2", Amount: "60"},
			{ID: "out3", Timestamp: 7, From: "hub", To: "d3", Amount: "70"}},
		{{ID: "out4", Timestamp: 8, From: "hub", To: "d4", Amount: "80"},
			{ID: "cyc1", Timestamp: 9, From: "r0", To: "r1", Amount: "10"},
			{ID: "cyc2", Timestamp: 10, From: "r1", To: "r2", Amount: "10"},
			{ID: "cyc3", Timestamp: 11, From: "r2", To: "r0", Amount: "10"}},
	}
	inc := risk.NewIncremental(risk.DefaultRuleSet())
	eng := risk.NewEngine(risk.DefaultRuleSet())
	for i, b := range batches {
		res := l.Ingest(b)
		inc.AddFromLedger(l)
		// 每批之后：增量结论必须与全量重算逐字节一致。
		full := eng.Analyze(l)
		got := inc.Report()
		if fmt.Sprint(got) != fmt.Sprint(full) {
			t.Fatalf("第 %d 批后增量与全量结论不一致", i+1)
		}
		t.Logf("批次%d：接受 %d 笔，增量结论 == 全量结论（hash=%s）",
			i+1, len(res.Accepted), full.InputHash)
	}

	// 2) 对 flagged 账户 hub 下钻溯源。
	rep := inc.Report()
	t.Logf("==== 全量结论（v1）====")
	for _, a := range rep.Accounts {
		t.Logf("账户=%s 得分=%d 等级=%s", a.Account, a.Score, a.Level)
	}
	g := sim.New(l).Graph()
	tr := g.TraceAccount("hub", graph.TraceOptions{})
	t.Logf("==== 对 hub 溯源 ====")
	for _, s := range tr.Inbound {
		t.Logf("  流入: %s -%d-> hub (tx=%s t=%d depth=%d)", s.From, s.Amount, s.TxID, s.Time, s.Depth)
	}
	for _, s := range tr.Outbound {
		t.Logf("  流出: hub -%d-> %s (tx=%s t=%d depth=%d)", s.Amount, s.To, s.TxID, s.Time, s.Depth)
	}
	t.Logf("溯源结论: 流入 %d 跳 / 流出 %d 跳 / 涉及账户 %d / 截断=%v",
		len(tr.Inbound), len(tr.Outbound), len(tr.Accounts), tr.Truncated)
	if len(tr.Inbound) != 4 || len(tr.Outbound) != 4 || tr.Truncated {
		t.Fatalf("hub 溯源不完整：in=%d out=%d truncated=%v",
			len(tr.Inbound), len(tr.Outbound), tr.Truncated)
	}
	// 从单笔交易溯源：cyc2 位于 3 环上，两个方向都会绕环展开到起点边为止。
	txTr, ok := g.TraceTx("cyc2", graph.TraceOptions{})
	if !ok || len(txTr.Inbound) != 2 || len(txTr.Outbound) != 2 {
		t.Fatalf("cyc2 交易溯源错误：%+v ok=%v", txTr, ok)
	}
	for _, s := range txTr.Inbound {
		if s.TxID == "cyc2" {
			t.Fatal("起点交易不应重复计入上游链路")
		}
	}
	t.Logf("交易 cyc2 溯源: 上游=%s,%s 下游=%s,%s（环上绕回起点边即停，根交易不重复计入）",
		txTr.Inbound[0].TxID, txTr.Inbound[1].TxID, txTr.Outbound[0].TxID, txTr.Outbound[1].TxID)

	// 3) 规则升级 v2（汇聚门槛 3->5），跨版本对比。
	v2 := risk.DefaultRuleSet()
	v2.Version = risk.RuleVersion{Version: 2, Name: "stricter-fanin-v2"}
	v2.FanInSources = 5
	diff := eng.Compare(risk.DefaultRuleSet(), v2, l)
	t.Logf("==== 跨版本对比 v1 -> v2（汇聚门槛 3->5）====")
	for _, ad := range diff.Accounts {
		t.Logf("账户=%s 得分 %d->%d 等级 %s->%s", ad.Account, ad.OldScore, ad.NewScore, ad.OldLevel, ad.NewLevel)
		for _, p := range ad.PatternsRemoved {
			t.Logf("  少命中: [%s] %s", p.Kind, p.Detail)
		}
		for _, p := range ad.PatternsAdded {
			t.Logf("  多命中: [%s] %s", p.Kind, p.Detail)
		}
	}
	if !diff.SameInput || len(diff.Accounts) == 0 {
		t.Fatalf("跨版本对比应有差异且输入一致：%+v", diff)
	}
	// 对比可复现：再算一遍必须逐字节一致。
	if fmt.Sprint(eng.Compare(risk.DefaultRuleSet(), v2, l)) != fmt.Sprint(diff) {
		t.Fatal("跨版本对比不可复现")
	}

	// 4) 并发：一边摄取新批次，一边分析 + 模拟；最终状态一致、无中间态。
	const extra = 300
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { // 摄取
		defer wg.Done()
		for i := 0; i < extra; i++ {
			l.Ingest([]ledger.Record{{
				ID: fmt.Sprintf("x%04d", i), Timestamp: int64(100 + i),
				From: fmt.Sprintf("xs%02d", i%10), To: "hub", Amount: "1",
			}})
		}
	}()
	go func() { // 分析
		defer wg.Done()
		for i := 0; i < 100; i++ {
			r := eng.Analyze(l)
			if r.RuleVersion.Version != 1 { // 永不出现残缺报告
				t.Errorf("报告残缺：%+v", r.RuleVersion)
				return
			}
		}
	}()
	go func() { // 模拟
		defer wg.Done()
		s := sim.New(l)
		for i := 0; i < 50; i++ {
			s.Transfer("d1", "d2", 1)
			_ = s.Balances()
		}
	}()
	wg.Wait()

	// 摄取结束后：增量重算与全量重算最终一致。
	inc.AddFromLedger(l)
	finalInc, finalFull := inc.Report(), eng.Analyze(l)
	if fmt.Sprint(finalInc) != fmt.Sprint(finalFull) {
		t.Fatal("并发摄取结束后增量与全量结论不一致")
	}
	t.Logf("并发摄取(%d 笔)+分析+模拟结束，最终增量==全量：hash=%s 账户数=%d",
		extra, finalFull.InputHash, len(finalFull.Accounts))
}
