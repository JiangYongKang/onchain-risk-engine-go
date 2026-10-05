package graph

// Edge 是交易图中的一条有向资金边。
type Edge struct {
	TxID   string
	From   string
	To     string
	Amount int64
	Time   int64
}

// PatternKind 资金流模式类别。
type PatternKind string

const (
	PatternFanIn   PatternKind = "fan_in"  // 资金汇聚
	PatternFanOut  PatternKind = "fan_out" // 资金拆分
	PatternCycle   PatternKind = "cycle"   // 环路
	PatternHotspot PatternKind = "hotspot" // 异常集中
)

// Pattern 是图上识别出的一个风险模式，含可核对依据。
type Pattern struct {
	Kind     PatternKind
	Account  string
	Accounts []string // 涉及的账户（确定性排序）
	TxIDs    []string // 涉及的交易（确定性排序）
	Detail   string
	Value    int64 // 支撑数值（如金额合计、入度等）
}

// Graph 是构建后只读的有向交易图。
type Graph struct {
	accounts []string
	edges    []Edge
	// adj[u] = 从 u 出发的边在 edges 中的下标（按输入顺序，即时间顺序）。
	adj map[string][]int
	// radj[u] = 指向 u 的边在 edges 中的下标（按时间顺序），供溯源反向遍历。
	radj map[string][]int
	// pair[u][v] = u->v 的边在 edges 中的下标（取最早一条，确定性）。
	pair map[string]map[string]int
	// nbrs[u] = u 的不同出边邻居（按各自最早边的时间序，确定性）。
	// 环路检测按“邻居账户”而非“边”扩展，避免平行边导致路径数爆炸。
	nbrs map[string][]string
	// byTx[txID] = 该交易对应边在 edges 中的下标。
	byTx map[string]int
}
