package risk

import (
	"fmt"
	"testing"

	"github.com/highcumontoa/onchain-risk-engine-go/internal/graph"
)

func v2StrictRules() RuleSet {
	r := DefaultRuleSet()
	r.Version = RuleVersion{Version: 2, Name: "strict-v2"}
	r.FanInSources = 10                // x 的 3 个来源不再命中汇聚
	r.Weights[graph.PatternCycle] = 10 // 环路权重 40 -> 10，s1 等级下降
	return r
}

func findDiff(d ReportDiff, account string) *AccountDiff {
	for i := range d.Accounts {
		if d.Accounts[i].Account == account {
			return &d.Accounts[i]
		}
	}
	return nil
}

func TestDiffReportsContent(t *testing.T) {
	e := NewEngine(DefaultRuleSet())
	l := seedLedger()

	d := e.Compare(DefaultRuleSet(), v2StrictRules(), l)
	t.Logf("输入：4 笔交易（3 来源汇聚 x + s1<->x 环路）；对比 v1 -> v2（汇聚门槛 3->10，环权重 40->10）")
	if !d.SameInput {
		t.Fatal("同一份快照对比，SameInput 应为 true")
	}
	for _, ad := range d.Accounts {
		t.Logf("账户=%s 得分 %d->%d 等级 %s->%s 等级变化=%v",
			ad.Account, ad.OldScore, ad.NewScore, ad.OldLevel, ad.NewLevel, ad.LevelChanged)
		for _, p := range ad.PatternsAdded {
			t.Logf("  多命中: [%s] %s", p.Kind, p.Detail)
		}
		for _, p := range ad.PatternsRemoved {
			t.Logf("  少命中: [%s] %s", p.Kind, p.Detail)
		}
	}

	// x：v2 下不再命中汇聚，应出现在差异里且是“少命中 fan_in”。
	x := findDiff(d, "x")
	if x == nil {
		t.Fatal("x 的结论应发生变化")
	}
	if len(x.PatternsRemoved) != 1 || x.PatternsRemoved[0].Kind != graph.PatternFanIn {
		t.Fatalf("x 应少命中 fan_in：%+v", x.PatternsRemoved)
	}
	if len(x.PatternsAdded) != 0 {
		t.Fatalf("x 不应多命中模式：%+v", x.PatternsAdded)
	}
	if x.OldScore <= x.NewScore {
		t.Fatalf("x 少命中模式后得分应下降：%d -> %d", x.OldScore, x.NewScore)
	}

	// s1：环权重 40->10，得分变化必须被列出。
	s1 := findDiff(d, "s1")
	if s1 == nil || s1.OldScore == s1.NewScore {
		t.Fatalf("s1 的得分变化应被列出：%+v", s1)
	}

	// 未受影响的账户不得出现在差异里。
	for _, ad := range d.Accounts {
		if ad.Account != "x" && ad.Account != "s1" {
			t.Fatalf("无关账户不应进入差异：%s", ad.Account)
		}
	}
}

func TestDiffDeterministicAcrossOrderAndRepeats(t *testing.T) {
	e := NewEngine(DefaultRuleSet())
	l := seedLedger()
	v1, v2 := DefaultRuleSet(), v2StrictRules()

	base := e.Compare(v1, v2, l)
	// 重复 10 遍，结果逐字节一致。
	for i := 0; i < 10; i++ {
		if got := e.Compare(v1, v2, l); fmt.Sprint(got) != fmt.Sprint(base) {
			t.Fatalf("第 %d 遍对比结果不一致", i)
		}
	}
	// 交换计算顺序（先算 v2 报告再算 v1），差异结论不变。
	ra := analyze(normalize(v2), l.Txs())
	rb := analyze(normalize(v1), l.Txs())
	swapped := DiffReports(rb, ra) // 仍按 (旧=v1, 新=v2) 传参
	if fmt.Sprint(swapped) != fmt.Sprint(base) {
		t.Fatal("先算哪个版本不应影响对比结论")
	}
	t.Logf("对比重复 10 遍 + 交换计算顺序，结论逐字节一致：差异账户数=%d", len(base.Accounts))
}

func TestDiffSameRulesEmpty(t *testing.T) {
	e := NewEngine(DefaultRuleSet())
	l := seedLedger()
	d := e.Compare(DefaultRuleSet(), DefaultRuleSet(), l)
	t.Logf("同一版本自对比：差异账户数=%d SameInput=%v", len(d.Accounts), d.SameInput)
	if len(d.Accounts) != 0 {
		t.Fatalf("相同规则不应产生差异：%+v", d.Accounts)
	}
	if !d.SameInput {
		t.Fatal("SameInput 应为 true")
	}
}
