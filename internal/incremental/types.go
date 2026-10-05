package incremental

import (
	"sync"

	"github.com/highcumontoa/onchain-risk-engine-go/internal/graph"
	"github.com/highcumontoa/onchain-risk-engine-go/internal/ledger"
)

// IngestOutcome 描述增量喂入一批记录后分析视图的净变化。
type IngestOutcome struct {
	// Added 为新进入分析视图的规范化交易。
	Added []ledger.Tx
	// Removed 为被同 ID 确定性替换而移出的旧交易（见 ledger 的同 ID 冲突规则）。
	Removed []ledger.Tx
	// EdgeCount 为当前分析视图中的交易总数（去重后）。
	EdgeCount int
}

// Accumulator 是按批增量维护的交易视图。
//
// 语义：任意时刻 Analyze 得到的结论，与把“当前全部已接受交易”一次性全量
// 重算的结论逐字节一致；重复喂入同一批记录不改变视图（幂等）。
// 所有方法并发安全；分析期间继续摄取，读者只会看到摄取前或摄取后的完整状态，
// 不会看到只算了一半的中间结论。
type Accumulator struct {
	mu    sync.RWMutex
	lg    *ledger.Ledger // 复用规范化与确定性拒绝/冲突规则
	edges []graph.Edge   // 按 (Time, TxID) 全序维护
	byID  map[string]int // txID -> edges 下标
}
