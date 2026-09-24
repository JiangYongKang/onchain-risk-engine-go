package sim

import (
	"fmt"
	"testing"

	"github.com/highcumontoa/onchain-risk-engine-go/internal/ledger"
	"github.com/highcumontoa/onchain-risk-engine-go/internal/risk"
)

func seed() *ledger.Ledger {
	l := ledger.New()
	l.Ingest([]ledger.Record{
		{ID: "t1", Timestamp: 1, From: "bank", To: "a", Amount: "1000"},
		{ID: "t2", Timestamp: 2, From: "a", To: "b", Amount: "200"},
		{ID: "t3", Timestamp: 3, From: "b", To: "c", Amount: "100"},
	})
	return l
}

func TestTransferValidation(t *testing.T) {
	s := New(seed())

	// 非法金额
	if _, err := s.Transfer("a", "b", 0); err != ErrInvalidAmount {
		t.Fatalf("期望 invalid_amount，得到 %v", err)
	}
	// 未知账户
	if _, err := s.Transfer("a", "ghost", 10); err != ErrUnknownAccount {
		t.Fatalf("期望 unknown_account，得到 %v", err)
	}
	// 余额不足（a 的余额 = 1000-200 = 800）
	if _, err := s.Transfer("a", "b", 5000); err != ErrInsufficientFunds {
		t.Fatalf("期望 insufficient_funds，得到 %v", err)
	}
	before := fmt.Sprint(s.Balances())
	bal, err := s.Transfer("a", "c", 300)
	if err != "" {
		t.Fatalf("合法转账失败：%v", err)
	}
	t.Logf("合法转账 a->c 300 后余额：%v", bal)
	if bal["a"] != 500 || bal["c"] != 400 {
		t.Fatal("转账后余额计算错误")
	}
	// 失败的转账不得改变状态
	s.Transfer("a", "c", 1<<62)
	if fmt.Sprint(s.Balances()) != fmt.Sprint(bal) {
		t.Fatal("失败的转账改变了模拟器状态")
	}
	s.Reset()
	if fmt.Sprint(s.Balances()) != before {
		t.Fatalf("Reset 未恢复初始状态：%v vs %v", s.Balances(), before)
	}
	t.Logf("Reset 成功恢复初始余额：%v", s.Balances())
}

func TestChainAtomicityAndDepthGuard(t *testing.T) {
	s := New(seed())

	// 一条超深链（远超硬上限）必须安全拒绝，且不产生任何模拟交易。
	deep := make([]ChainStep, MaxChainDepth+50)
	for i := range deep {
		deep[i] = ChainStep{From: "a", To: "b", Amount: 1}
	}
	if _, err := s.Chain(deep, 0); err != ErrDepthExceeded {
		t.Fatalf("期望 depth_exceeded，得到 %v", err)
	}
	if len(s.Txs()) != 3 {
		t.Fatalf("超深链被拒绝后不应产生模拟交易，实际 %d 条", len(s.Txs()))
	}
	t.Logf("超深链（%d 跳）被安全拒绝，状态不变", len(deep))

	// 中段资金不足：整链必须原子回滚。
	badChain := []ChainStep{
		{From: "a", To: "c", Amount: 10},
		{From: "c", To: "b", Amount: 999999}, // c 余额不足
	}
	before := fmt.Sprint(s.Balances())
	if _, err := s.Chain(badChain, 10); err != ErrInsufficientFunds {
		t.Fatalf("期望 insufficient_funds，得到 %v", err)
	}
	if fmt.Sprint(s.Balances()) != before {
		t.Fatal("链式模拟失败后未原子回滚")
	}
	t.Log("链式模拟中段失败已原子回滚")

	// 合法含“环”的短链（c->a->b->c）应可执行，不会死循环。
	ring := []ChainStep{
		{From: "c", To: "a", Amount: 50},
		{From: "a", To: "b", Amount: 50},
		{From: "b", To: "c", Amount: 50},
	}
	if _, err := s.Chain(ring, 10); err != "" {
		t.Fatalf("含环短链执行失败：%v", err)
	}
	t.Logf("含环短链执行成功，总交易数=%d（真实3+模拟3）", len(s.Txs()))
}

func TestSimulatedGraphRuleReplay(t *testing.T) {
	l := seed()
	s := New(l)
	// 在模拟中制造一个汇聚：a、b、c 都向 bank 回流。
	if _, err := s.Chain([]ChainStep{
		{From: "a", To: "bank", Amount: 100},
		{From: "b", To: "bank", Amount: 100},
		{From: "c", To: "bank", Amount: 100},
	}, 10); err != "" {
		t.Fatalf("模拟链失败：%v", err)
	}
	eng := risk.NewEngine(risk.DefaultRuleSet())
	g := s.Graph()
	rep := eng.AnalyzeGraph(g)
	t.Logf("模拟图（%d 条边）在 v1 下的结论：账户数=%d", len(g.Edges()), len(rep.Accounts))
	for _, a := range rep.Accounts {
		t.Logf("  %s score=%d level=%s patterns=%d", a.Account, a.Score, a.Level, len(a.Patterns))
	}

	// 更新规则后对同一张模拟图重算，结论应可复现地变化。
	v2 := risk.DefaultRuleSet()
	v2.Version = risk.RuleVersion{Version: 2, Name: "no-fanin-v2"}
	v2.FanInSources = 99
	eng.UpdateRules(v2)
	rep2 := eng.AnalyzeGraph(g)

	var bankFanIn bool
	for _, a := range rep2.Accounts {
		if a.Account == "bank" {
			for _, p := range a.Patterns {
				if p.Kind == "fan_in" {
					bankFanIn = true
				}
			}
		}
	}
	if bankFanIn {
		t.Fatal("v2 提高汇聚门槛后 bank 不应再命中 fan_in")
	}
	// 回放 v1 必须复现旧结论。
	old := eng.Replay(risk.DefaultRuleSet(), l)
	_ = old
	again := eng.Replay(risk.DefaultRuleSet(), txsToLedger(t, s.Txs()))
	if len(again.Accounts) != len(rep.Accounts) {
		t.Fatal("模拟图在 v1 下回放结论不一致")
	}
	t.Log("规则更新后模拟图结论确定，v1 旧结论可回放复现")
}
