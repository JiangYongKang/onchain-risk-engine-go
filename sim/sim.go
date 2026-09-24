// Package sim 提供账户资金变动与交易的本地模拟。
package sim

import (
	"sync"

	"github.com/highcumontoa/onchain-risk-engine-go/ledger"
	"github.com/highcumontoa/onchain-risk-engine-go/rules"
)

// Account 是模拟账户状态。
type Account struct {
	Balance  int64
	TotalIn  int64
	TotalOut int64
	TxCount  int
}

// Simulator 在本地账本与规则回放器之上做交易模拟。
type Simulator struct {
	mu       sync.RWMutex
	ledger   *ledger.Ledger
	replayer *rules.Replayer
	accounts map[string]*Account
}

// New 创建模拟器。
func New(l *ledger.Ledger, r *rules.Replayer) *Simulator {
	return &Simulator{ledger: l, replayer: r, accounts: make(map[string]*Account)}
}

// Apply 把账本快照中的转账确定性重放到账户余额。
// 可重复调用，每次从快照全量重建，结果一致。
func (s *Simulator) Apply() {
	tx := s.ledger.Snapshot()
	fresh := make(map[string]*Account)
	applyTo := func(t ledger.Transfer) {
		from := fresh[t.From]
		if from == nil {
			from = &Account{}
			fresh[t.From] = from
		}
		to := fresh[t.To]
		if to == nil {
			to = &Account{}
			fresh[t.To] = to
		}
		from.Balance -= t.Amount
		from.TotalOut += t.Amount
		from.TxCount++
		to.Balance += t.Amount
		to.TotalIn += t.Amount
		to.TxCount++
	}
	for _, t := range tx {
		applyTo(t)
	}
	s.mu.Lock()
	s.accounts = fresh
	s.mu.Unlock()
}

// Simulate 模拟一笔新交易：返回摄取结果、模拟后的账户变化与最新风险结论，
// 不改变真实账本与账户状态（在快照副本上试算）。
func (s *Simulator) Simulate(rec ledger.RawRecord) Simulation {
	t, res := ledger.Normalize(rec)
	sim := Simulation{Ingest: res}
	if res.Outcome == ledger.OutcomeRejected {
		return sim
	}

	// 在快照副本上试算，不污染真实账本。
	tx := s.ledger.Snapshot()
	dup := false
	for _, existing := range tx {
		if existing.TxID == t.TxID {
			dup = true
			break
		}
	}
	if dup {
		sim.Ingest = ledger.Result{TxID: t.TxID, Outcome: ledger.OutcomeDuplicate, Detail: "simulated duplicate"}
	} else {
		sim.Ingest = ledger.Result{TxID: t.TxID, Outcome: ledger.OutcomeApplied}
		tx = append(tx, t)
	}

	// 试算账户变化。
	s.mu.RLock()
	base := make(map[string]Account, len(s.accounts))
	for k, v := range s.accounts {
		base[k] = *v
	}
	s.mu.RUnlock()
	if !dup {
		from := base[t.From]
		from.Balance -= t.Amount
		from.TotalOut += t.Amount
		from.TxCount++
		base[t.From] = from
		to := base[t.To]
		to.Balance += t.Amount
		to.TotalIn += t.Amount
		to.TxCount++
		base[t.To] = to
	}
	sim.Accounts = base

	// 用当前规则对试算后的账本出结论（不写入历史）。
	sim.Verdict = s.replayer.Run(tx)
	return sim
}

// Simulation 是一次模拟的结果。
type Simulation struct {
	Ingest   ledger.Result
	Accounts map[string]Account
	Verdict  rules.Conclusion
}

// Account 返回账户当前状态快照。
func (s *Simulator) Account(addr string) Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if a, ok := s.accounts[addr]; ok {
		return *a
	}
	return Account{}
}
