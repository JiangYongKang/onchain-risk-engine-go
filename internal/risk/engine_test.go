package risk

import (
	"fmt"
	"sync"
	"testing"

	"github.com/highcumontoa/onchain-risk-engine-go/internal/graph"
	"github.com/highcumontoa/onchain-risk-engine-go/internal/ledger"
)

func seedLedger() *ledger.Ledger {
	l := ledger.New()
	l.Ingest([]ledger.Record{
		{ID: "t1", Timestamp: 1, From: "s1", To: "x", Amount: "100"},
		{ID: "t2", Timestamp: 2, From: "s2", To: "x", Amount: "200"},
		{ID: "t3", Timestamp: 3, From: "s3", To: "x", Amount: "300"}, // x 汇聚
		{ID: "t4", Timestamp: 4, From: "x", To: "s1", Amount: "50"},  // x->s1 与 t1 构成 2 环
	})
	return l
}

func reportSummary(r Report) string {
	s := fmt.Sprintf("v=%d hash=%s accounts=%d", r.RuleVersion.Version, r.InputHash, len(r.Accounts))
	for _, a := range r.Accounts {
		s += fmt.Sprintf(" | %s:%d/%s[", a.Account, a.Score, a.Level)
		for _, p := range a.Patterns {
			s += string(p.Kind) + ","
		}
		s += "]"
	}
	return s
}

func TestAnalyzeExplainable(t *testing.T) {
	e := NewEngine(DefaultRuleSet())
	l := seedLedger()
	rep := e.Analyze(l)
	t.Logf("输入=4 笔交易（3 来源汇聚到 x，x->s1 与 s1->x 构成环路）")
	t.Logf("最终结论: %s", reportSummary(rep))
	var x *AccountRisk
	for i := range rep.Accounts {
		if rep.Accounts[i].Account == "x" {
			x = &rep.Accounts[i]
		}
	}
	if x == nil {
		t.Fatal("x 应有风险结论")
	}
	hasFanIn := false
	for _, pp := range x.Patterns {
		if pp.Kind == "fan_in" {
			hasFanIn = true
		}
	}
	if !hasFanIn {
		t.Fatal("x 应命中资金汇聚")
	}
	// 2 环 s1<->x 按确定性规则只归属到字典序最小账户 s1，每个环只报一次。
	var s1 *AccountRisk
	for i := range rep.Accounts {
		if rep.Accounts[i].Account == "s1" {
			s1 = &rep.Accounts[i]
		}
	}
	if s1 == nil || s1.Score < 40 {
		t.Fatalf("环路应归属到 s1 且得分>=40：%+v", s1)
	}
	t.Logf("账户 x 判定依据（得分=%d 等级=%s）：", x.Score, x.Level)
	for _, p := range x.Patterns {
		t.Logf("  - %s：%s（tx=%v）", p.Kind, p.Detail, p.TxIDs)
	}
}

func TestConcurrentAnalyzeDeterministic(t *testing.T) {
	e := NewEngine(DefaultRuleSet())
	l := seedLedger()
	const readers, writers = 40, 8

	start := make(chan struct{})
	var readerWg, writerWg sync.WaitGroup
	results := make([]string, readers)

	// 40 个读 goroutine 在同一屏障后并发分析同一份数据；
	// 8 个写 goroutine 并发重复摄取（全部应为 duplicate_id 拒绝，不改变状态）。
	for i := 0; i < readers; i++ {
		readerWg.Add(1)
		go func(idx int) {
			defer readerWg.Done()
			<-start
			results[idx] = reportSummary(e.Analyze(l))
		}(i)
	}
	dup := []ledger.Record{{ID: "t1", Timestamp: 1, From: "s1", To: "x", Amount: "100"}}
	for i := 0; i < writers; i++ {
		writerWg.Add(1)
		go func() {
			defer writerWg.Done()
			<-start
			r := l.Ingest(dup)
			if len(r.Accepted) != 0 ||
				(len(r.Rejected) != 1 || r.Rejected[0].Reason != ledger.ReasonDuplicate) {
				t.Errorf("并发重复摄取结论错误：%+v", r)
			}
		}()
	}
	close(start)
	writerWg.Wait()
	readerWg.Wait()

	base := results[0]
	for i, r := range results {
		if r != base {
			t.Fatalf("并发分析结论不一致：#0=%s vs #%d=%s", base, i, r)
		}
	}
	t.Logf("%d 个并发读+%d 个并发写，分析结论完全一致: %s", readers, writers, base)
}

func TestRuleUpdateAndReplay(t *testing.T) {
	e := NewEngine(DefaultRuleSet())
	l := seedLedger()

	v1 := e.Analyze(l)
	t.Logf("旧规则 v1 结论: %s", reportSummary(v1))

	// 发布 v2：大幅提高汇聚门槛并下调环路权重，x 的结论应改变且可复现。
	v2Rules := DefaultRuleSet()
	v2Rules.Version = RuleVersion{Version: 2, Name: "strict-cycle-only-v2"}
	v2Rules.FanInSources = 10 // x 只有 3 个来源，不再命中汇聚
	v2Rules.Weights[graph.PatternCycle] = 10
	ver := e.UpdateRules(v2Rules)
	if ver.Version != 2 {
		t.Fatalf("新版本号错误：%d", ver.Version)
	}
	v2 := e.Analyze(l)
	t.Logf("新规则 v2 结论: %s", reportSummary(v2))

	if v1.InputHash != v2.InputHash {
		t.Fatal("规则变化不应改变输入指纹")
	}
	if reportSummary(v1) == reportSummary(v2) {
		t.Fatal("规则变化后结论应当不同")
	}
	if len(e.History()) != 2 {
		t.Fatalf("历史应保留 2 个版本，实际 %d", len(e.History()))
	}

	// 回放：用 v1 规则重算同一份输入，必须逐字复现旧结论。
	replayed := e.Replay(DefaultRuleSet(), l)
	if reportSummary(replayed) != reportSummary(v1) {
		t.Fatalf("回放旧规则未复现旧结论：%s vs %s",
			reportSummary(replayed), reportSummary(v1))
	}
	t.Logf("回放 v1 成功复现旧结论: %s", reportSummary(replayed))
	t.Logf("当前生效=%v，可追溯版本数=%d", e.CurrentRules().Version, len(e.History()))

	// 同版本重复登记不膨胀历史。
	e.UpdateRules(v2Rules)
	if len(e.History()) != 2 {
		t.Fatal("重复提交同版本不应重复登记")
	}
}
