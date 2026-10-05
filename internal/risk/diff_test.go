package risk

import (
	"fmt"
	"testing"

	"github.com/highcumontoa/onchain-risk-engine-go/internal/graph"
	"github.com/highcumontoa/onchain-risk-engine-go/internal/ledger"
)

func diffSeed() *ledger.Ledger {
	l := ledger.New()
	l.Ingest([]ledger.Record{
		{ID: "t1", Timestamp: 1, From: "s1", To: "x", Amount: "100"},
		{ID: "t2", Timestamp: 2, From: "s2", To: "x", Amount: "200"},
		{ID: "t3", Timestamp: 3, From: "s3", To: "x", Amount: "300"}, // x 汇聚
		{ID: "t4", Timestamp: 4, From: "x", To: "s1", Amount: "50"},  // x<->s1 环
	})
	return l
}

func TestCompareVersionsDifferences(t *testing.T) {
	e := NewEngine(DefaultRuleSet())
	l := diffSeed()
	old := e.Analyze(l)

	v2 := DefaultRuleSet()
	v2.Version = RuleVersion{Version: 2, Name: "no-fanin-hotspot-off"}
	v2.FanInSources = 10 // x 的 3 来源不再命中 fan_in
	v2.HotspotAmount = 0
	e.UpdateRules(v2)
	newRep := e.Analyze(l)

	t.Logf("v1: %s", reportSummary(old))
	t.Logf("v2: %s", reportSummary(newRep))

	d := CompareVersions(old, newRep)
	if d.OldVersion.Version != 1 || d.NewVersion.Version != 2 {
		t.Fatalf("版本标注错误：%v -> %v", d.OldVersion, d.NewVersion)
	}
	if d.InputHash != old.InputHash {
		t.Fatal("跨版本对比的输入指纹应一致")
	}
	t.Logf("发生变化账户=%d 未变化=%d", len(d.Changed), len(d.Unchanged))

	// x 在 v1 命中 fan_in（+20），v2 不再命中 -> 分数下降、模式被列为少命中。
	var xd *AccountDiff
	for i := range d.Changed {
		if d.Changed[i].Account == "x" {
			xd = &d.Changed[i]
		}
	}
	if xd == nil {
		t.Fatal("x 应出现在变化列表")
	}
	t.Logf("账户 x：分数 %d->%d，等级 %s->%s，少命中=%d 多命中=%d",
		xd.OldScore, xd.NewScore, xd.OldLevel, xd.NewLevel,
		len(xd.RemovedPatterns), len(xd.AddedPatterns))
	var removedFanIn bool
	for _, p := range xd.RemovedPatterns {
		t.Logf("   少命中依据: [%s] sig=%s tx=%v", p.Kind, p.Signature, p.TxIDs)
		if p.Kind == graph.PatternFanIn {
			removedFanIn = true
			if len(p.TxIDs) != 3 {
				t.Fatalf("fan_in 依据应带 3 笔交易，实际 %v", p.TxIDs)
			}
		}
	}
	if !removedFanIn {
		t.Fatal("x 应在 v2 少命中 fan_in，并带交易依据")
	}
	if len(xd.AddedPatterns) != 0 {
		t.Fatalf("x 不应有多命中的新模式：%+v", xd.AddedPatterns)
	}
	if xd.NewScore >= xd.OldScore {
		t.Fatalf("去掉 fan_in 后分数应下降：%d -> %d", xd.OldScore, xd.NewScore)
	}
}

// TestCompareVersionsDeterministic 核对对比是纯函数：先算谁、跑多少遍、
// 是否交换计算顺序，old->new 差异逐字节一致；反向对比是其镜像。
func TestCompareVersionsDeterministic(t *testing.T) {
	e := NewEngine(DefaultRuleSet())
	l := diffSeed()
	v2 := DefaultRuleSet()
	v2.Version = RuleVersion{Version: 2, Name: "v2"}
	v2.FanInSources = 10 // x 的 3 来源不再命中 fan_in，产生非空差异
	e.UpdateRules(v2)

	// 以不同顺序得到两份报告。
	first := e.Analyze(l)
	second := e.Replay(DefaultRuleSet(), l) // v1，但在之后计算
	old1 := second
	new1 := first
	d1 := CompareVersions(old1, new1)

	// 再算一遍，且互换变量来源（重新生成新对象）。
	old2 := e.Replay(DefaultRuleSet(), l)
	new2 := e.Analyze(l)
	d2 := CompareVersions(old2, new2)
	for k := 0; k < 5; k++ {
		dd := CompareVersions(e.Replay(DefaultRuleSet(), l), e.Analyze(l))
		if fmt.Sprint(dd) != fmt.Sprint(d1) {
			t.Fatalf("第 %d 次对比结果与基准不一致", k)
		}
	}
	if fmt.Sprint(d1) != fmt.Sprint(d2) {
		t.Fatal("先算谁不影响差异：d1 != d2")
	}
	t.Logf("跨版本差异稳定可复现：变化账户=%v", changedNames(d1))

	// 反向对比：new->old 的“多命中”恰为 old->new 的“少命中”，且 Changed 集合相同。
	rev := CompareVersions(new1, old1)
	if len(rev.Changed) != len(d1.Changed) {
		t.Fatal("反向对比的变化账户数应相同")
	}
	for i := range d1.Changed {
		a := d1.Changed[i]
		b := rev.Changed[i] // 同序（按账户名排序）
		if a.Account != b.Account {
			t.Fatalf("反向对比账户顺序漂移：%s vs %s", a.Account, b.Account)
		}
		if len(a.AddedPatterns) != len(b.RemovedPatterns) ||
			len(a.RemovedPatterns) != len(b.AddedPatterns) {
			t.Fatalf("账户 %s 反向对比的模式增减不对称", a.Account)
		}
	}
	t.Log("反向对比为正向镜像，方向固定为 old -> new")
}

func changedNames(d Diff) []string {
	out := make([]string, 0, len(d.Changed))
	for _, c := range d.Changed {
		out = append(out, c.Account)
	}
	return out
}

// TestCompareVersionsIdentical 核对相同结论对比时无变化项。
func TestCompareVersionsIdentical(t *testing.T) {
	e := NewEngine(DefaultRuleSet())
	l := diffSeed()
	r := e.Analyze(l)
	d := CompareVersions(r, e.Replay(DefaultRuleSet(), l))
	if len(d.Changed) != 0 {
		t.Fatalf("相同结论对比不应有变化项：%+v", d.Changed)
	}
	if len(d.Unchanged) != len(r.Accounts) {
		t.Fatalf("全部账户应标记未变化：%d vs %d", len(d.Unchanged), len(r.Accounts))
	}
	t.Logf("相同版本对比：%d 个账户全部未变化", len(d.Unchanged))
}
