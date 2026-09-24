package sim

import (
	"testing"

	"github.com/highcumontoa/onchain-risk-engine-go/ledger"
	"github.com/highcumontoa/onchain-risk-engine-go/rules"
)

func setup() (*ledger.Ledger, *rules.Replayer, *Simulator) {
	l := ledger.New()
	l.Ingest(
		ledger.RawRecord{"tx_id": "t1", "from": "alice", "to": "bob", "amount": 100, "tx_time": 1},
		ledger.RawRecord{"tx_id": "t2", "from": "bob", "to": "carol", "amount": 40, "tx_time": 2},
	)
	r := rules.NewReplayer(rules.Default())
	s := New(l, r)
	s.Apply()
	return l, r, s
}

func TestApplyDeterministic(t *testing.T) {
	_, _, s := setup()
	a := s.Account("alice")
	b := s.Account("bob")
	t.Logf("after apply: alice=%+v bob=%+v", a, b)
	if a.Balance != -100 || a.TotalOut != 100 {
		t.Fatalf("unexpected alice state: %+v", a)
	}
	if b.Balance != 60 || b.TotalIn != 100 || b.TotalOut != 40 {
		t.Fatalf("unexpected bob state: %+v", b)
	}
	// 重复 Apply 结果一致（从快照全量重建）。
	s.Apply()
	if s.Account("bob") != b {
		t.Fatalf("re-apply changed state: %+v vs %+v", s.Account("bob"), b)
	}
	t.Log("re-apply idempotent: state unchanged")
}

func TestSimulateDoesNotMutate(t *testing.T) {
	l, _, s := setup()
	before := s.Account("carol")
	sim := s.Simulate(ledger.RawRecord{"tx_id": "t3", "from": "carol", "to": "dave", "amount": 25, "tx_time": 3})
	t.Logf("simulated ingest=%+v", sim.Ingest)
	t.Logf("simulated carol=%+v dave=%+v", sim.Accounts["carol"], sim.Accounts["dave"])
	t.Logf("simulated verdict ruleVersion=%d score=%d level=%s",
		sim.Verdict.RuleVersion, sim.Verdict.Report.Score, sim.Verdict.Report.Level)
	if sim.Ingest.Outcome != ledger.OutcomeApplied {
		t.Fatalf("expected simulated applied, got %+v", sim.Ingest)
	}
	if sim.Accounts["carol"].Balance != before.Balance-25 {
		t.Fatalf("simulation did not reflect new tx: %+v", sim.Accounts["carol"])
	}
	// 真实状态不变。
	if s.Account("carol") != before {
		t.Fatalf("simulation mutated real state: %+v vs %+v", s.Account("carol"), before)
	}
	if l.Len() != 2 {
		t.Fatalf("simulation mutated ledger: len=%d", l.Len())
	}
	t.Logf("real carol unchanged=%+v ledger len=%d", s.Account("carol"), l.Len())
}

func TestSimulateDuplicate(t *testing.T) {
	_, _, s := setup()
	sim := s.Simulate(ledger.RawRecord{"tx_id": "t1", "from": "alice", "to": "bob", "amount": 100, "tx_time": 1})
	t.Logf("duplicate simulate ingest=%+v", sim.Ingest)
	if sim.Ingest.Outcome != ledger.OutcomeDuplicate {
		t.Fatalf("expected duplicate, got %+v", sim.Ingest)
	}
	if sim.Accounts["alice"].Balance != -100 {
		t.Fatalf("duplicate simulation changed balance: %+v", sim.Accounts["alice"])
	}
}

func TestSimulateRejected(t *testing.T) {
	_, _, s := setup()
	sim := s.Simulate(ledger.RawRecord{"tx_id": "bad", "from": "alice", "amount": -1})
	t.Logf("rejected simulate ingest=%+v", sim.Ingest)
	if sim.Ingest.Outcome != ledger.OutcomeRejected {
		t.Fatalf("expected rejected, got %+v", sim.Ingest)
	}
}

func TestSimulateWithUpdatedRules(t *testing.T) {
	l, r, s := setup()
	// 构造汇聚场景：再补两笔进入 bob。
	l.Ingest(
		ledger.RawRecord{"tx_id": "t3", "from": "x", "to": "bob", "amount": 10, "tx_time": 3},
		ledger.RawRecord{"tx_id": "t4", "from": "y", "to": "bob", "amount": 10, "tx_time": 4},
	)
	s.Apply()
	sim1 := s.Simulate(ledger.RawRecord{"tx_id": "t5", "from": "z", "to": "bob", "amount": 5, "tx_time": 5})
	t.Logf("rules v1 verdict score=%d findings=%d", sim1.Verdict.Report.Score, len(sim1.Verdict.Report.Findings))

	v2 := rules.Default()
	v2.Version = 2
	v2.Weights.FanIn = 80 // 提高汇聚权重
	if err := r.UpdateRules(v2); err != nil {
		t.Fatal(err)
	}
	sim2 := s.Simulate(ledger.RawRecord{"tx_id": "t5", "from": "z", "to": "bob", "amount": 5, "tx_time": 5})
	t.Logf("rules v2 verdict score=%d findings=%d", sim2.Verdict.Report.Score, len(sim2.Verdict.Report.Findings))
	if sim2.Verdict.RuleVersion != 2 {
		t.Fatalf("expected rule version 2, got %d", sim2.Verdict.RuleVersion)
	}
	if sim1.Verdict.Report.Score == sim2.Verdict.Report.Score {
		t.Fatal("expected rule update to change simulated score")
	}
	// 旧结论仍可从历史追溯。
	for _, c := range r.History() {
		t.Logf("history ruleVersion=%d score=%d level=%s", c.RuleVersion, c.Report.Score, c.Report.Level)
	}
}
