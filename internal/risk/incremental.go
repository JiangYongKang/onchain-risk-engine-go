package risk

import (
	"sort"
	"sync"

	"github.com/highcumontoa/onchain-risk-engine-go/internal/graph"
	"github.com/highcumontoa/onchain-risk-engine-go/internal/ledger"
)

// Incremental 是增量重算器：随批次喂入交易，随时给出与全量重算完全一致的结论。
//
// 一致性设计（为什么增量与全量绝不会打架）：
//   - 增量路径与全量路径共用同一个纯函数 analyze；Incremental 只负责
//     变更检测（哪些交易是新到的）与结果缓存，不维护任何独立的
//     “增量状态机”，因此不存在两套算法产生两套结论的可能；
//   - 结论按 (Timestamp, ID) 全序计算，与交易到达顺序、批次划分无关；
//   - 无新数据时直接返回缓存（深拷贝），有新数据时才整体重算。
//
// 并发安全：Add 与 Report 可与其他摄取/分析并发进行；Report 返回的
// 永远是一份完整一致的快照，不会暴露算到一半的中间状态。
type Incremental struct {
	mu     sync.Mutex
	rules  RuleSet
	txs    []ledger.Tx // 已纳入的全部交易（追加顺序，Report 时排序）
	seen   map[string]struct{}
	cached Report
	valid  bool
}

// NewIncremental 创建增量重算器，规则集按值固化（与 Engine 同样的 normalize 语义）。
func NewIncremental(rules RuleSet) *Incremental {
	return &Incremental{rules: normalize(rules), seen: map[string]struct{}{}}
}

// Add 纳入一批交易（按 ID 幂等去重，重复纳入不改变状态），返回新纳入的笔数。
func (inc *Incremental) Add(txs []ledger.Tx) int {
	inc.mu.Lock()
	defer inc.mu.Unlock()
	added := 0
	for _, tx := range txs {
		if _, ok := inc.seen[tx.ID]; ok {
			continue
		}
		inc.seen[tx.ID] = struct{}{}
		inc.txs = append(inc.txs, tx)
		added++
	}
	if added > 0 {
		inc.valid = false
	}
	return added
}

// AddFromLedger 从账本快照拉取增量（只纳入新出现的交易），返回新纳入的笔数。
// 可反复调用：账本每投一批新数据后调用一次即可。
func (inc *Incremental) AddFromLedger(l *ledger.Ledger) int {
	return inc.Add(l.Txs())
}

// sortedTxsLocked 返回按 (Timestamp, ID) 全序排列的交易副本（调用方持锁）。
// 与 ledger.Txs() 的排序键一致，保证输入指纹与全量重算相同。
func (inc *Incremental) sortedTxsLocked() []ledger.Tx {
	out := make([]ledger.Tx, len(inc.txs))
	copy(out, inc.txs)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Timestamp != out[j].Timestamp {
			return out[i].Timestamp < out[j].Timestamp
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func clonePatterns(ps []graph.Pattern) []graph.Pattern {
	out := make([]graph.Pattern, len(ps))
	for i, p := range ps {
		out[i] = p
		out[i].Accounts = append([]string(nil), p.Accounts...)
		out[i].TxIDs = append([]string(nil), p.TxIDs...)
	}
	return out
}

func cloneReport(r Report) Report {
	out := r
	out.Accounts = make([]AccountRisk, len(r.Accounts))
	for i, a := range r.Accounts {
		out.Accounts[i] = a
		out.Accounts[i].Patterns = clonePatterns(a.Patterns)
	}
	return out
}

// Report 返回当前结论。与“把全部已纳入交易交给 Engine.Analyze 全量重算”
// 的结果逐字节一致（同一份 analyze、同一份排序键、同一个输入指纹）。
// 无新数据时返回缓存的深拷贝，调用方修改返回值不影响内部状态。
func (inc *Incremental) Report() Report {
	inc.mu.Lock()
	defer inc.mu.Unlock()
	if !inc.valid {
		inc.cached = analyze(inc.rules, inc.sortedTxsLocked())
		inc.valid = true
	}
	return cloneReport(inc.cached)
}

// Size 返回已纳入的交易笔数。
func (inc *Incremental) Size() int {
	inc.mu.Lock()
	defer inc.mu.Unlock()
	return len(inc.txs)
}

// Rules 返回固化规则集快照。
func (inc *Incremental) Rules() RuleSet {
	inc.mu.Lock()
	defer inc.mu.Unlock()
	return inc.rules
}
