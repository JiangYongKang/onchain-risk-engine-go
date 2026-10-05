package graph

import "sort"

// 溯源（下钻）安全上限。
//
// 默认值按“几万节点级链路/环也必须给出完整正确结论”设定；
// 硬封顶防止恶意或失控输入耗尽资源。触限结果会显式标记 Truncated，
// 并在结果中回显实际生效的上限，调用方不会误以为拿到了完整链路。
const (
	// DefaultTraceMaxDepth 是默认最大跳数（距起点的边数）。
	DefaultTraceMaxDepth = 100_000
	// DefaultTraceMaxNodes 是单方向默认最多访问的不同账户数。
	DefaultTraceMaxNodes = 200_000
	// traceDepthHardCap / traceNodesHardCap 是硬性封顶，超过一律按封顶值计。
	traceDepthHardCap = 1_000_000
	traceNodesHardCap = 2_000_000
)

// TraceOptions 控制溯源范围的安全上限。
type TraceOptions struct {
	MaxDepth int // 距起点的最大跳数（<=0 取 DefaultTraceMaxDepth，硬封顶 traceDepthHardCap）
	MaxNodes int // 每个方向最多访问的不同账户数（<=0 取 DefaultTraceMaxNodes，硬封顶 traceNodesHardCap）
}

func (o TraceOptions) normalized() (maxDepth, maxNodes int) {
	maxDepth = o.MaxDepth
	if maxDepth <= 0 {
		maxDepth = DefaultTraceMaxDepth
	}
	if maxDepth > traceDepthHardCap {
		maxDepth = traceDepthHardCap
	}
	maxNodes = o.MaxNodes
	if maxNodes <= 0 {
		maxNodes = DefaultTraceMaxNodes
	}
	if maxNodes > traceNodesHardCap {
		maxNodes = traceNodesHardCap
	}
	return maxDepth, maxNodes
}

// TraceStep 是溯源链路中的一跳。
type TraceStep struct {
	TxID   string
	From   string
	To     string
	Amount int64
	Time   int64
	Depth  int // 距起点的跳数（>=1；起点交易本身为 0）
}

// TraceResult 是一次溯源的完整结论。
//
// 一致性约定：
//   - Inbound / Outbound 均按 (Time, TxID) 升序，即资金实际发生顺序；
//   - 结果为确定性输出：同一图、同一起点、同一上限 => 逐字节一致；
//   - Truncated 为 true 时，MaxDepth/MaxNodes 回显实际生效上限，
//     调用方可据此放大上限重跑以获取完整链路。
type TraceResult struct {
	Root      string      // 起点账户或交易 ID
	RootKind  string      // "account" 或 "tx"
	RootTx    *TraceStep  // 起点为交易时为该交易本身（Depth=0），否则为 nil
	Inbound   []TraceStep // 资金如何流入起点（上游链路，时间升序）
	Outbound  []TraceStep // 资金从起点流向何处（下游链路，时间升序）
	Accounts  []string    // 链路涉及的全部账户（含起点，字典序）
	Truncated bool        // 是否因触及安全上限而被截断
	MaxDepth  int         // 实际生效的深度上限
	MaxNodes  int         // 实际生效的节点上限
}

// traceDirection 从 root 出发沿一个方向做迭代式 BFS（不用递归，超深链路安全）。
//
// 规则：
//   - 每条被遍历到的边记录为一个 TraceStep（含指向已访问账户的“闭环边”，
//     因此大环的收口跳也会出现在结果里）；
//   - 每个账户只扩展一次（环不会导致无限循环）；
//   - 深度超限的边不记录并标记截断；节点预算耗尽时停止扩展并标记截断。
func (g *Graph) traceDirection(root string, forward bool, maxDepth, maxNodes, excludeEdge int) (steps []TraceStep, visited map[string]struct{}, truncated bool) {
	visited = map[string]struct{}{root: {}}
	type item struct {
		acct  string
		depth int
	}
	queue := []item{{root, 0}}
	for len(queue) > 0 {
		u := queue[0]
		queue = queue[1:]

		var edgeIdxs []int
		if forward {
			edgeIdxs = g.adj[u.acct]
		} else {
			edgeIdxs = g.radj[u.acct]
		}
		if u.depth >= maxDepth {
			if len(edgeIdxs) > 0 {
				truncated = true // 还有更深的链路未展开
			}
			continue
		}
		for _, ei := range edgeIdxs {
			if ei == excludeEdge {
				continue // 起点交易本身不重复计入链路
			}
			e := g.edges[ei]
			steps = append(steps, TraceStep{
				TxID: e.TxID, From: e.From, To: e.To,
				Amount: e.Amount, Time: e.Time, Depth: u.depth + 1,
			})
			next := e.To
			if !forward {
				next = e.From
			}
			if _, ok := visited[next]; ok {
				continue // 环收口：边已记录，不再扩展
			}
			if len(visited) >= maxNodes {
				truncated = true // 节点预算耗尽，无法继续扩展
				continue
			}
			visited[next] = struct{}{}
			queue = append(queue, item{next, u.depth + 1})
		}
	}
	return steps, visited, truncated
}

func sortSteps(steps []TraceStep) {
	sort.SliceStable(steps, func(i, j int) bool {
		if steps[i].Time != steps[j].Time {
			return steps[i].Time < steps[j].Time
		}
		return steps[i].TxID < steps[j].TxID
	})
}

// TraceAccount 从账户向两端还原资金流：资金怎么流进来（Inbound）、
// 又流向哪里（Outbound），均按发生顺序排列。
func (g *Graph) TraceAccount(account string, opts TraceOptions) TraceResult {
	maxDepth, maxNodes := opts.normalized()
	in, inVisited, inTrunc := g.traceDirection(account, false, maxDepth, maxNodes, -1)
	out, outVisited, outTrunc := g.traceDirection(account, true, maxDepth, maxNodes, -1)

	acctSet := map[string]struct{}{account: {}}
	for a := range inVisited {
		acctSet[a] = struct{}{}
	}
	for a := range outVisited {
		acctSet[a] = struct{}{}
	}

	sortSteps(in)
	sortSteps(out)
	return TraceResult{
		Root:      account,
		RootKind:  "account",
		Inbound:   in,
		Outbound:  out,
		Accounts:  sortedKeys(acctSet),
		Truncated: inTrunc || outTrunc,
		MaxDepth:  maxDepth,
		MaxNodes:  maxNodes,
	}
}

// TraceTx 从一笔交易向两端还原资金流：上游如何汇入该交易（Inbound），
// 该交易的资金又流向何处（Outbound）。起点交易本身不重复计入两个方向
// （在环上沿任一方向绕回起点边时停止该方向的扩展）。
// 第二个返回值表示该交易是否存在；不存在时返回空结果与 false。
func (g *Graph) TraceTx(txID string, opts TraceOptions) (TraceResult, bool) {
	idx, ok := g.byTx[txID]
	if !ok {
		return TraceResult{}, false
	}
	e := g.edges[idx]
	rootStep := TraceStep{TxID: e.TxID, From: e.From, To: e.To, Amount: e.Amount, Time: e.Time, Depth: 0}

	maxDepth, maxNodes := opts.normalized()
	in, inVisited, inTrunc := g.traceDirection(e.From, false, maxDepth, maxNodes, idx)
	out, outVisited, outTrunc := g.traceDirection(e.To, true, maxDepth, maxNodes, idx)

	acctSet := map[string]struct{}{e.From: {}, e.To: {}}
	for a := range inVisited {
		acctSet[a] = struct{}{}
	}
	for a := range outVisited {
		acctSet[a] = struct{}{}
	}

	sortSteps(in)
	sortSteps(out)
	return TraceResult{
		Root:      txID,
		RootKind:  "tx",
		RootTx:    &rootStep,
		Inbound:   in,
		Outbound:  out,
		Accounts:  sortedKeys(acctSet),
		Truncated: inTrunc || outTrunc,
		MaxDepth:  maxDepth,
		MaxNodes:  maxNodes,
	}, true
}
