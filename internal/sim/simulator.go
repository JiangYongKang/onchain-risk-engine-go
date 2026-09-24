package sim

import (
	"fmt"
	"sort"
	"sync"

	"github.com/highcumontoa/onchain-risk-engine-go/internal/graph"
	"github.com/highcumontoa/onchain-risk-engine-go/internal/ledger"
)

// SimError 模拟失败原因。
type SimError string

const (
	ErrInsufficientFunds SimError = "insufficient_funds"
	ErrInvalidAmount     SimError = "invalid_amount"
	ErrUnknownAccount    SimError = "unknown_account"
	ErrDepthExceeded     SimError = "depth_exceeded"
)

// MaxChainDepth 是链式模拟的硬性长度上限，防止超深输入导致资源耗尽。
const MaxChainDepth = 100

// ChainStep 描述链式模拟中的一跳。
type ChainStep struct {
	From   string
	To     string
	Amount int64
}

// Simulator 是账户资金变动与交易模拟器。
// 创建时对账本做一次快照；后续模拟只影响模拟器自身，Reset 可回到快照状态。
type Simulator struct {
	mu      sync.RWMutex
	base    []ledger.Tx // 账本快照（只读）
	sim     []ledger.Tx // 模拟产生的转账
	seq     int         // 模拟交易序号
	known   map[string]struct{}
	bal     map[string]int64 // base 余额，sim 变动叠加在其上
	simBal  map[string]int64 // 仅模拟部分的净变动
	maxTime int64
}

// New 基于账本创建模拟器（初始余额由已确认交易确定性推导）。
func New(l *ledger.Ledger) *Simulator {
	base := l.Txs()
	s := &Simulator{
		base:   base,
		known:  map[string]struct{}{},
		bal:    map[string]int64{},
		simBal: map[string]int64{},
	}
	for _, t := range base {
		s.known[t.From] = struct{}{}
		s.known[t.To] = struct{}{}
		s.bal[t.From] -= t.Amount
		s.bal[t.To] += t.Amount
		if t.Timestamp > s.maxTime {
			s.maxTime = t.Timestamp
		}
	}
	return s
}

// balancesLocked 返回当前总余额（调用方持锁）。
func (s *Simulator) balancesLocked() map[string]int64 {
	out := make(map[string]int64, len(s.bal)+len(s.simBal))
	for a, v := range s.bal {
		out[a] = v
	}
	for a, v := range s.simBal {
		out[a] += v
	}
	return out
}

// Balances 返回所有账户余额的确定性快照。
func (s *Simulator) Balances() map[string]int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.balancesLocked()
}

// validateLocked 校验单笔模拟转账（调用方持锁）。
func (s *Simulator) validateLocked(from, to string, amount int64) SimError {
	if amount <= 0 {
		return ErrInvalidAmount
	}
	if _, ok := s.known[from]; !ok {
		return ErrUnknownAccount
	}
	if _, ok := s.known[to]; !ok {
		return ErrUnknownAccount
	}
	if s.bal[from]+s.simBal[from] < amount {
		return ErrInsufficientFunds
	}
	return ""
}

// applyLocked 追加一笔已校验的模拟转账（调用方持锁）。
func (s *Simulator) applyLocked(from, to string, amount int64) {
	s.seq++
	s.sim = append(s.sim, ledger.Tx{
		ID:        fmt.Sprintf("sim-%06d", s.seq),
		Timestamp: s.maxTime + int64(s.seq), // 排在全部真实交易之后，顺序确定
		From:      from,
		To:        to,
		Amount:    amount,
	})
	s.simBal[from] -= amount
	s.simBal[to] += amount
}

// Transfer 模拟一笔资金变动，成功后返回新的完整余额；非法变动返回明确错误，
// 且失败时模拟器状态完全不变。
func (s *Simulator) Transfer(from, to string, amount int64) (map[string]int64, SimError) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if reason := s.validateLocked(from, to, amount); reason != "" {
		return nil, reason
	}
	s.applyLocked(from, to, amount)
	return s.balancesLocked(), ""
}

// Chain 原子地模拟一条资金链：任一跳非法则整链不生效。
// steps 长度超过 maxDepth（<=0 时取 MaxChainDepth，并硬性封顶 MaxChainDepth）
// 返回 ErrDepthExceeded；含环路也允许执行（只要每跳资金充足），不会无限循环，
// 因为跳数是有限的有限列表。
func (s *Simulator) Chain(steps []ChainStep, maxDepth int) (map[string]int64, SimError) {
	if maxDepth <= 0 || maxDepth > MaxChainDepth {
		maxDepth = MaxChainDepth
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(steps) == 0 {
		return s.balancesLocked(), ""
	}
	if len(steps) > maxDepth {
		return nil, ErrDepthExceeded
	}

	// 在临时视图上校验全部跳，全部通过后才提交，保证原子性。
	delta := map[string]int64{}
	for i, st := range steps {
		if st.Amount <= 0 {
			return nil, ErrInvalidAmount
		}
		if _, ok := s.known[st.From]; !ok {
			return nil, ErrUnknownAccount
		}
		if _, ok := s.known[st.To]; !ok {
			return nil, ErrUnknownAccount
		}
		delta[st.From] -= st.Amount
		delta[st.To] += st.Amount
		if s.bal[st.From]+s.simBal[st.From]+delta[st.From] < 0 {
			return nil, ErrInsufficientFunds
		}
		_ = i
	}
	for _, st := range steps {
		s.applyLocked(st.From, st.To, st.Amount)
	}
	return s.balancesLocked(), ""
}

// Txs 返回“真实交易 + 模拟交易”的确定性排序快照。
func (s *Simulator) Txs() []ledger.Tx {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ledger.Tx, 0, len(s.base)+len(s.sim))
	out = append(out, s.base...)
	out = append(out, s.sim...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Timestamp != out[j].Timestamp {
			return out[i].Timestamp < out[j].Timestamp
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Graph 返回当前模拟状态对应的交易图。
func (s *Simulator) Graph() *graph.Graph {
	txs := s.Txs()
	edges := make([]graph.Edge, len(txs))
	for i, t := range txs {
		edges[i] = graph.Edge{TxID: t.ID, From: t.From, To: t.To, Amount: t.Amount, Time: t.Timestamp}
	}
	return graph.Build(edges)
}

// Reset 回滚到账本初始状态（清空全部模拟交易）。
func (s *Simulator) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sim = nil
	s.seq = 0
	s.simBal = map[string]int64{}
}
