package e2e

import (
	"fmt"
	"testing"

	"github.com/highcumontoa/onchain-risk-engine-go/internal/graph"
	"github.com/highcumontoa/onchain-risk-engine-go/internal/incremental"
	"github.com/highcumontoa/onchain-risk-engine-go/internal/ledger"
	"github.com/highcumontoa/onchain-risk-engine-go/internal/risk"
	"github.com/highcumontoa/onchain-risk-engine-go/internal/trace"
)

// TestNewCapabilitiesDrillDownDiffIncremental 串联三项新能力：
// 对被标记账户下钻溯源 -> 跨版本对比差异 -> 分批增量与全量结论一致。
func TestNewCapabilitiesDrillDownDiffIncremental(t *testing.T) {
	// 资金链路：s1,s2,s3 -> x -> m -> n；x->s1 与 s1->x 构成环。
	recs := []ledger.Record{
		{ID: "f1", Timestamp: 1, From: "s1", To: "x", Amount: "100"},
		{ID: "f2", Timestamp: 2, From: "s2", To: "x", Amount: "200"},
		{ID: "f3", Timestamp: 3, From: "s3", To: "x", Amount: "300"},
		{ID: "f4", Timestamp: 4, From: "x", To: "m", Amount: "500"},
		{ID: "f5", Timestamp: 5, From: "m", To: "n", Amount: "500"},
		{ID: "f6", Timestamp: 6, From: "x", To: "s1", Amount: "50"},
	}
	l := ledger.New()
	if r := l.Ingest(recs); len(r.Rejected) != 0 {
		t.Fatalf("不应有拒绝：%+v", r.Rejected)
	}

	// 1) v1 分析，定位被标记账户 x。
	eng := risk.NewEngine(risk.DefaultRuleSet())
	v1 := eng.Analyze(l)
	var marked string
	for _, a := range v1.Accounts {
		if a.Level != risk.LevelLow {
			marked = a.Account
		}
	}
	if marked == "" {
		marked = "x"
	}
	t.Logf("==== 被标记账户=%s，对其下钻溯源 ====", marked)
	tr := trace.Trace(l.Txs(), trace.Target{Kind: trace.TargetAccount, Account: marked}, trace.Limits{})
	t.Logf("上游账户=%v 下游账户=%v 环数=%d",
		tr.Upstream.Reachable, tr.Downstream.Reachable, len(tr.Cycles))
	for _, c := range tr.Cycles {
		t.Logf("  资金环：%v，交易=%v，金额=%d", c.Accounts, c.TxIDs, c.Amount)
	}
	if len(tr.Upstream.Reachable) < 4 || len(tr.Downstream.Reachable) < 3 {
		t.Fatalf("溯源上下游不完整：up=%v down=%v", tr.Upstream.Reachable, tr.Downstream.Reachable)
	}
	if len(tr.Cycles) != 1 || fmt.Sprint(tr.Cycles[0].Accounts) != "[s1 x]" {
		t.Fatalf("应还原 s1<->x 环，实际 %+v", tr.Cycles)
	}

	// 2) 发布 v2（提高汇聚门槛），跨版本对比差异。
	v2rules := risk.DefaultRuleSet()
	v2rules.Version = risk.RuleVersion{Version: 2, Name: "no-fanin"}
	v2rules.FanInSources = 10
	eng.UpdateRules(v2rules)
	v2 := eng.Analyze(l)
	diff := risk.CompareVersions(v1, v2)
	t.Logf("==== 跨版本对比：v%d -> v%d，变化账户=%d ====",
		diff.OldVersion.Version, diff.NewVersion.Version, len(diff.Changed))
	var xdiff *risk.AccountDiff
	for i := range diff.Changed {
		cd := diff.Changed[i]
		t.Logf("  账户=%s 分数 %d->%d 等级 %s->%s 少命中=%d 多命中=%d",
			cd.Account, cd.OldScore, cd.NewScore, cd.OldLevel, cd.NewLevel,
			len(cd.RemovedPatterns), len(cd.AddedPatterns))
		if cd.Account == "x" {
			xdiff = &diff.Changed[i]
		}
	}
	if xdiff == nil {
		t.Fatal("x 应在 v2 发生变化")
	}
	var sawFanIn bool
	for _, p := range xdiff.RemovedPatterns {
		if p.Kind == graph.PatternFanIn {
			sawFanIn = true
		}
	}
	if !sawFanIn {
		t.Fatal("v2 应少命中 fan_in 并给出依据")
	}
	// 对比可复现：再算两次逐字节一致。
	if fmt.Sprint(risk.CompareVersions(eng.Replay(risk.DefaultRuleSet(), l), eng.Analyze(l))) !=
		fmt.Sprint(diff) {
		t.Fatal("跨版本对比不可复现")
	}

	// 3) 同样的数据分 3 批增量摄取，最终报告必须与全量逐字节一致。
	acc := incremental.New()
	acc.Ingest(recs[0:2])
	acc.Ingest(recs[2:4])
	acc.Ingest(recs[4:6])
	inc := acc.Analyze(risk.DefaultRuleSet())
	if fmt.Sprint(inc) != fmt.Sprint(v1) {
		t.Fatalf("增量结论与全量不一致：\ninc=%v\nfull=%v", inc, v1)
	}
	t.Logf("==== 分批增量（3 批）结论与全量逐字节一致：账户=%d 指纹=%s ====",
		len(inc.Accounts), inc.InputHash)
}
