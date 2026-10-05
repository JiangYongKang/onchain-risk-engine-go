package incremental

import (
	"github.com/highcumontoa/onchain-risk-engine-go/internal/graph"
	"github.com/highcumontoa/onchain-risk-engine-go/internal/ledger"
	"github.com/highcumontoa/onchain-risk-engine-go/internal/risk"
)

// New 创建空的增量视图。
func New() *Accumulator {
	return &Accumulator{lg: ledger.New(), byID: map[string]int{}}
}

// rebuildLocked 依据内部账本快照重算全序边集合（调用方持锁）。
// 不做任何“打补丁”式的增量推导，而是始终以同一权威全序源为准，
// 从构造上杜绝增量视图与全量视图漂移。复杂度 O(E log E)，与全量重算同阶。
func (a *Accumulator) rebuildLocked() {
	txs := a.lg.Txs() // 已按 (Timestamp, ID) 全序
	edges := make([]graph.Edge, len(txs))
	byID := make(map[string]int, len(txs))
	for i, t := range txs {
		edges[i] = graph.Edge{TxID: t.ID, From: t.From, To: t.To, Amount: t.Amount, Time: t.Timestamp}
		byID[t.ID] = i
	}
	a.edges = edges
	a.byID = byID
}

// Ingest 增量喂入一批原始记录：复用 ledger 的规范化、拒绝原因与同 ID
// 确定性冲突规则。因此“同一条记录无论怎么分批、什么顺序、和哪些数据混在一起”，
// 最终视图与拒绝原因都与一次性全量摄取完全一致。
//
// 返回 ledger.IngestResult（含本批接受/拒绝/替换明细）与视图净变化；
// 被拒绝的记录不进入分析视图。重复喂入同一批记录是幂等的。
func (a *Accumulator) Ingest(records []ledger.Record) (ledger.IngestResult, IngestOutcome) {
	a.mu.Lock()
	defer a.mu.Unlock()

	res := a.lg.Ingest(records)
	out := IngestOutcome{
		Added:   append([]ledger.Tx(nil), res.Accepted...),
		Removed: append([]ledger.Tx(nil), res.Replaced...),
	}
	a.rebuildLocked()
	out.EdgeCount = len(a.edges)
	return res, out
}

// txsLocked 返回当前边对应的规范化交易（调用方持锁）。
func (a *Accumulator) txsLocked() []ledger.Tx {
	out := make([]ledger.Tx, len(a.edges))
	for i, e := range a.edges {
		out[i] = ledger.Tx{ID: e.TxID, Timestamp: e.Time, From: e.From, To: e.To, Amount: e.Amount}
	}
	return out
}

// Analyze 用给定规则集对当前增量视图出报告。
//
// 一致性：内部直接调用与全量分析相同的纯函数 risk.AnalyzeTxs，因此
// “任意分批/顺序增量摄取后的结论” == “把当前全部交易一次性全量重算的结论”，
// 逐字节相等。读者只可能看到某次 Ingest 之前或之后的完整状态，
// 不会看到边集更新到一半的中间结论（rebuild 在同一把写锁内完成）。
func (a *Accumulator) Analyze(rules risk.RuleSet) risk.Report {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return risk.AnalyzeTxs(rules, a.txsLocked())
}

// Txs 返回当前视图的全序快照（深拷贝，调用方可长期持有）。
func (a *Accumulator) Txs() []ledger.Tx {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.txsLocked()
}

// Edges 返回当前视图的全序边快照（深拷贝）。
func (a *Accumulator) Edges() []graph.Edge {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]graph.Edge, len(a.edges))
	copy(out, a.edges)
	return out
}

// Has 报告某交易 ID 是否在当前视图中。
func (a *Accumulator) Has(id string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	_, ok := a.byID[id]
	return ok
}

// Len 返回当前视图的交易数。
func (a *Accumulator) Len() int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.edges)
}

// Reset 清空视图。
func (a *Accumulator) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lg = ledger.New()
	a.edges = nil
	a.byID = map[string]int{}
}
