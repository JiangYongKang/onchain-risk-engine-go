package rules

import (
	"fmt"
	"testing"

	"github.com/highcumontoa/onchain-risk-engine-go/graph"
	"github.com/highcumontoa/onchain-risk-engine-go/ledger"
)

func sampleLedger(t *testing.T) []ledger.Transfer {
	t.Helper()
	l := ledger.New()
	// 两个来源向 hub 转账：默认阈值 3 不触发汇聚，放宽到 2 后触发。
	l.Ingest(
		ledger.RawRecord{"tx_id": "t1", "from": "a", "to": "hub", "amount": 100, "tx_time": 1},
		ledger.RawRecord{"tx_id": "t2", "from": "b", "to": "hub", "amount": 200, "tx_time": 2},
	)
	return l.Snapshot()
}

func logConclusion(t *testing.T, c Conclusion) {
	t.Helper()
	t.Logf("conclusion ruleVersion=%d score=%d level=%s accounts=%v",
		c.RuleVersion, c.Report.Score, c.Report.Level, c.Report.Accounts)
	for _, f := range c.Report.Findings {
		t.Logf("  evidence kind=%s detail=%s", f.Kind, f.Detail)
	}
}

func TestRuleUpdateAndReplay(t *testing.T) {
	snap := sampleLedger(t)
	t.Logf("input snapshot=%v", snap)

	r := NewReplayer(Default())

	// 旧规则：汇聚阈值 3，两个来源不触发。
	c1 := r.Run(snap)
	logConclusion(t, c1)
	if c1.RuleVersion != 1 {
		t.Fatalf("expected rule version 1, got %d", c1.RuleVersion)
	}

	// 新规则：汇聚阈值降为 2，应触发新结论。
	v2 := Default()
	v2.Version = 2
	v2.Thresholds.FanInMinSources = 2
	if err := r.UpdateRules(v2); err != nil {
		t.Fatalf("update rules: %v", err)
	}
	c2 := r.Run(snap)
	logConclusion(t, c2)
	if c2.RuleVersion != 2 {
		t.Fatalf("expected rule version 2, got %d", c2.RuleVersion)
	}
	if fmt.Sprint(c1.Report.Findings) == fmt.Sprint(c2.Report.Findings) {
		t.Fatal("expected rule change to alter conclusion")
	}

	// 回放旧规则：同一输入必须复现旧结论。
	c1r, err := r.Replay(1, snap)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	t.Logf("replayed v1 conclusion:")
	logConclusion(t, c1r)
	if fmt.Sprint(c1r.Report) != fmt.Sprint(c1.Report) {
		t.Fatalf("replay of v1 not reproducible:\n%+v\nvs\n%+v", c1r.Report, c1.Report)
	}

	// 历史可追溯：v1、v2、v1 回放三条都在。
	h := r.History()
	if len(h) != 3 || h[0].RuleVersion != 1 || h[1].RuleVersion != 2 || h[2].RuleVersion != 1 {
		t.Fatalf("unexpected history: %+v", h)
	}
	t.Logf("history preserved: versions=[%d %d %d]", h[0].RuleVersion, h[1].RuleVersion, h[2].RuleVersion)
}

func TestRuleVersionMustIncrease(t *testing.T) {
	r := NewReplayer(Default())
	v := Default() // 版本仍为 1，不递增
	if err := r.UpdateRules(v); err == nil {
		t.Fatal("expected error for non-increasing version")
	} else {
		t.Logf("non-increasing version rejected: %v", err)
	}
}

func TestReplayUnknownVersion(t *testing.T) {
	r := NewReplayer(Default())
	_, err := r.Replay(99, nil)
	if err == nil {
		t.Fatal("expected error for unknown version")
	}
	t.Logf("unknown version rejected: %v", err)
}

func TestConcurrentRunConsistent(t *testing.T) {
	snap := sampleLedger(t)
	r := NewReplayer(Default())
	const n = 16
	results := make([]Conclusion, n)
	done := make(chan int, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			results[i] = r.Run(snap)
			done <- i
		}(i)
	}
	for i := 0; i < n; i++ {
		<-done
	}
	for i := 1; i < n; i++ {
		if fmt.Sprint(results[i].Report) != fmt.Sprint(results[0].Report) {
			t.Fatalf("concurrent run %d diverged", i)
		}
	}
	if len(r.History()) != n {
		t.Fatalf("expected %d history entries, got %d", n, len(r.History()))
	}
	t.Logf("conclusion: %d concurrent runs identical, score=%d level=%s",
		n, results[0].Report.Score, results[0].Report.Level)
}

func TestEngineDeterminism(t *testing.T) {
	snap := sampleLedger(t)
	e := NewEngine(Default())
	r1 := e.Analyze(snap)
	r2 := e.Analyze(snap)
	if fmt.Sprint(r1) != fmt.Sprint(r2) {
		t.Fatal("same engine, same input, different reports")
	}
	t.Logf("deterministic report score=%d level=%s", r1.Score, r1.Level)
}

var _ = graph.Report{} // 保持 graph 引用，便于扩展断言
