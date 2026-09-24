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
	// pair[u][v] = u->v 的边在 edges 中的下标（取最早一条，确定性）。
	pair map[string]map[string]int
}
