package trace

// TargetKind 标识溯源锚点类型。
type TargetKind string

const (
	// TargetAccount 以某个被标记账户为锚点，向上游/下游展开。
	TargetAccount TargetKind = "account"
	// TargetTx 以某一笔交易为锚点，还原其上下游资金路径。
	TargetTx TargetKind = "tx"
)

// Target 描述一次溯源请求的锚点。
type Target struct {
	Kind TargetKind
	// Account：锚点账户；Tx：锚点交易 ID。
	Account string
	TxID    string
}

// Hop 是溯源路径上的一跳，对应一笔真实交易。
type Hop struct {
	TxID      string
	From      string
	To        string
	Amount    int64
	Time      int64
	Direction string // "up"（流入锚点方向）或 "down"（流出锚点方向）
	// Depth：该跳距锚点的跳数（锚点直接邻接为 1）。
	Depth int
}

// Cycle 是溯源覆盖子图中发现的一个有向环（按环上账户序列表示）。
type Cycle struct {
	Accounts []string // 环上账户（字典序最小者为起点，确定方向）
	TxIDs    []string // 环上各跳交易，与 Accounts 顺序对应（最后一跳回到起点）
	Hops     []Hop    // 按发生顺序排列的环上各跳
	Amount   int64    // 环上金额合计
}

// Limits 是溯源搜索的显式安全上限。
// 零值字段取包级默认值；上限触发时结果会明确标记截断，绝不静默少算。
type Limits struct {
	// MaxNodes 每个方向最多访问的账户数（含锚点）。
	MaxNodes int
	// MaxHops 每个方向最多收集的跳数（含回环边）。
	MaxHops int
}

// DirectionResult 是单个方向（上游/下游）的溯源结果。
type DirectionResult struct {
	// Reachable 为 true 表示在未截断范围内访问完成；
	// Truncated 为 true 表示因上限停止，Frontier 是未能继续展开的边界账户。
	Reachable []string
	Truncated bool
	LimitName string // 触发的上限名（"nodes" / "hops" / ""）
	Frontier  []string
	Hops      []Hop // 按 (Time, TxID) 排序的全部路径跳
}

// Result 是一次溯源的完整结论。
type Result struct {
	Target     Target
	Found      bool // 锚点账户/交易是否存在
	AnchorTx   *Hop // Target==tx 时，锚点交易本身
	Upstream   DirectionResult
	Downstream DirectionResult
	Cycles     []Cycle // 覆盖子图内的环（含大环，完整列出或明确截断说明）
	// CyclesTruncated 为 true 时表示环枚举受安全上限限制，可能不完整。
	CyclesTruncated bool
	Notes           []string // 截断等需要明确告知调用方的事项
}
