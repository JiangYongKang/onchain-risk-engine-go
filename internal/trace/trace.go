package trace

import (
	"fmt"
	"sort"
	"strings"

	"github.com/highcumontoa/onchain-risk-engine-go/internal/ledger"
)

// 默认安全上限：默认足以覆盖数万节点的长链/大环而不截断。
// 截断发生时 Result 会显式标记 Truncated/LimitName/Frontier，
// 调用方不会把截断结果误当成完整链路。
const (
	defaultMaxNodes = 200_000
	defaultMaxHops  = 500_000
	// maxCycles 是单次溯源输出环的硬上限；超过时 CyclesTruncated=true。
	maxCycles = 100_000
)

// edge 是内部使用的有向资金边。
type edge struct {
	txID   string
	from   string
	to     string
	amount int64
	time   int64
}

// view 是为溯源构建的只读图视图。
type view struct {
	edges  []edge           // 全序边
	byTxID map[string]edge  // txID -> edge
	adj    map[string][]int // 出边在 edges 的下标（时间序）
	radj   map[string][]int // 入边在 edges 的下标（时间序）
}

// buildView 由规范化交易构建确定性图视图（内部再按全序排序一次）。
func buildView(txs []ledger.Tx) *view {
	es := make([]edge, len(txs))
	for i, t := range txs {
		es[i] = edge{txID: t.ID, from: t.From, to: t.To, amount: t.Amount, time: t.Timestamp}
	}
	sort.SliceStable(es, func(i, j int) bool {
		if es[i].time != es[j].time {
			return es[i].time < es[j].time
		}
		return es[i].txID < es[j].txID
	})
	v := &view{
		edges:  es,
		byTxID: map[string]edge{},
		adj:    map[string][]int{},
		radj:   map[string][]int{},
	}
	for idx, e := range es {
		v.byTxID[e.txID] = e
		v.adj[e.from] = append(v.adj[e.from], idx)
		v.radj[e.to] = append(v.radj[e.to], idx)
	}
	return v
}

// normalizeLimits 填充缺省上限并做硬性封顶，返回实际生效上限。
func normalizeLimits(l Limits) Limits {
	if l.MaxNodes <= 0 {
		l.MaxNodes = defaultMaxNodes
	}
	if l.MaxHops <= 0 {
		l.MaxHops = defaultMaxHops
	}
	if l.MaxHops < l.MaxNodes {
		// 连通子图中边数至少与访问推进同步，避免 hops 上限使节点上限形同虚设。
		l.MaxHops = l.MaxNodes
	}
	return l
}

// traversal 保存单方向 BFS 的中间状态。
type traversal struct {
	visited  map[string]bool
	depth    map[string]int // 距锚点跳数
	parent   map[string]int // 首次发现该节点时经过的边下标
	order    []string       // 节点发现顺序（BFS 层序）
	trunc    bool
	limit    string
	frontier map[string]struct{}
}

// bfs 从 starts 出发沿给定方向做 BFS：downstream=true 沿出边，false 沿入边。
// 邻接按时间序展开，因此节点发现顺序与收集结果对相同输入完全确定。
func (v *view) bfs(starts []string, downstream bool, lim Limits) *traversal {
	tr := &traversal{
		visited: map[string]bool{}, depth: map[string]int{},
		parent: map[string]int{}, frontier: map[string]struct{}{},
	}
	queue := make([]string, 0)
	for _, s := range starts {
		if s == "" || tr.visited[s] {
			continue
		}
		tr.visited[s] = true
		tr.depth[s] = 0
		queue = append(queue, s)
		tr.order = append(tr.order, s)
	}

	hopsUsed := 0
	for head := 0; head < len(queue); head++ {
		u := queue[head]
		var indices []int
		if downstream {
			indices = v.adj[u]
		} else {
			indices = v.radj[u]
		}
		for _, ei := range indices {
			e := v.edges[ei]
			// 沿展开方向，邻居是这条边的另一端。
			w := e.from
			if downstream {
				w = e.to
			}
			hopsUsed++
			if hopsUsed > lim.MaxHops {
				tr.trunc = true
				tr.limit = "hops"
				tr.frontier[w] = struct{}{}
				return tr
			}
			if !tr.visited[w] {
				if len(tr.visited) >= lim.MaxNodes {
					tr.trunc = true
					tr.limit = "nodes"
					tr.frontier[w] = struct{}{}
					return tr
				}
				tr.visited[w] = true
				tr.depth[w] = tr.depth[u] + 1
				tr.parent[w] = ei
				queue = append(queue, w)
				tr.order = append(tr.order, w)
			}
		}
	}
	return tr
}

// hopsInSubgraph 收集覆盖子图（visited）内的全部边，按 (Time, TxID) 排序，
// 每条边都不遗漏——这是长链路与环形流转被完整还原的保证。
func (v *view) hopsInSubgraph(visited map[string]bool, dir string, depth map[string]int) []Hop {
	out := make([]Hop, 0)
	for _, e := range v.edges {
		if visited[e.from] && visited[e.to] {
			d := depth[e.from]
			if depth[e.to] > d {
				d = depth[e.to]
			}
			out = append(out, Hop{
				TxID: e.txID, From: e.from, To: e.to, Amount: e.amount,
				Time: e.time, Direction: dir, Depth: d,
			})
		}
	}
	return out // edges 已全序，结果天然按 (Time, TxID)
}

// kosaraju 在 active 诱导子图上做 SCC 分解（迭代实现，避免数万节点深递归）。
// 返回各连通分量的节点集合；分量顺序与其最小节点的升序一致（确定性）。
func (v *view) kosaraju(active map[string]bool) [][]string {
	nodes := make([]string, 0, len(active))
	for a := range active {
		nodes = append(nodes, a)
	}
	sort.Strings(nodes)

	finish := make([]string, 0, len(nodes))
	color := map[string]int{} // 0 未访问 1 在栈 2 完成
	neighborsIn := func(u string, out bool) []string {
		var idxs []int
		if out {
			idxs = v.adj[u]
		} else {
			idxs = v.radj[u]
		}
		res := make([]string, 0, len(idxs))
		for _, ei := range idxs {
			var w string
			if out {
				w = v.edges[ei].to
			} else {
				w = v.edges[ei].from
			}
			if active[w] {
				res = append(res, w)
			}
		}
		return res
	}
	for _, root := range nodes {
		if color[root] != 0 {
			continue
		}
		type frame struct {
			u   string
			idx int
		}
		stk := []frame{{u: root}}
		color[root] = 1
		for len(stk) > 0 {
			top := &stk[len(stk)-1]
			nbs := neighborsIn(top.u, true)
			if top.idx >= len(nbs) {
				color[top.u] = 2
				finish = append(finish, top.u)
				stk = stk[:len(stk)-1]
				continue
			}
			w := nbs[top.idx]
			top.idx++
			if color[w] == 0 {
				color[w] = 1
				stk = append(stk, frame{u: w})
			}
		}
	}

	compOf := map[string]int{}
	comps := [][]string{}
	for i := len(finish) - 1; i >= 0; i-- {
		root := finish[i]
		if _, done := compOf[root]; done {
			continue
		}
		cid := len(comps)
		members := []string{}
		stk := []string{root}
		compOf[root] = cid
		for len(stk) > 0 {
			u := stk[len(stk)-1]
			stk = stk[:len(stk)-1]
			members = append(members, u)
			for _, w := range neighborsIn(u, false) {
				if _, done := compOf[w]; !done {
					compOf[w] = cid
					stk = append(stk, w)
				}
			}
		}
		sort.Strings(members)
		comps = append(comps, members)
	}
	sort.Slice(comps, func(i, j int) bool { return comps[i][0] < comps[j][0] })
	return comps
}

// johnsonCircuits 在单个非平凡 SCC 的节点集上用 Johnson(1975) 枚举简单环。
// 时间复杂度关于“边数 ×（环数+1）”线性：纯链无环 SCC 不进入这里；
// 简单大环只枚举一次，数万节点也是 O(V+E)。每个环以其最小节点为根恰好一次。
func (v *view) johnsonCircuits(component []string) []Cycle {
	active := map[string]bool{}
	for _, a := range component {
		active[a] = true
	}
	cycles := []Cycle{}

	for len(active) > 0 {
		if len(cycles) >= maxCycles {
			break
		}
		subComps := v.kosaraju(active)
		var chosen []string
		for _, c := range subComps {
			if len(c) > 1 || (len(c) == 1 && v.hasSelfLoop(c[0], active)) {
				chosen = c
				break
			}
		}
		if chosen == nil {
			break
		}
		chosenSet := map[string]bool{}
		for _, a := range chosen {
			chosenSet[a] = true
		}
		s := chosen[0]

		blocked := map[string]bool{}
		blockMap := map[string]map[string]bool{}
		path := []string{}

		var unblock func(u string)
		unblock = func(u string) {
			blocked[u] = false
			for w := range blockMap[u] {
				delete(blockMap[u], w)
				if blocked[w] {
					unblock(w)
				}
			}
		}
		var circuit func(u string) bool
		circuit = func(u string) bool {
			found := false
			path = append(path, u)
			blocked[u] = true
			for _, ei := range v.adj[u] {
				if len(cycles) >= maxCycles {
					break
				}
				w := v.edges[ei].to
				if !chosenSet[w] {
					continue
				}
				if w == s {
					cyc := append([]string(nil), path...)
					cycles = append(cycles, v.cycleFromAccounts(cyc))
					found = true
				} else if !blocked[w] {
					if circuit(w) {
						found = true
					}
				}
			}
			if found {
				unblock(u)
			} else {
				for _, ei := range v.adj[u] {
					w := v.edges[ei].to
					if !chosenSet[w] {
						continue
					}
					if blockMap[w] == nil {
						blockMap[w] = map[string]bool{}
					}
					blockMap[w][u] = true
				}
			}
			path = path[:len(path)-1]
			return found
		}
		circuit(s)
		delete(active, s)
	}
	return cycles
}

// hasSelfLoop 报告 u 在 active 子图内是否有自转边。
func (v *view) hasSelfLoop(u string, active map[string]bool) bool {
	for _, ei := range v.adj[u] {
		if v.edges[ei].to == u && active[u] {
			return true
		}
	}
	return false
}

// cycleFromAccounts 由一条环上账户序列构造带证据的 Cycle。
// 环上每对相邻账户取它们之间最早的一条真实边（确定唯一）。
func (v *view) cycleFromAccounts(accts []string) Cycle {
	c := Cycle{Accounts: append([]string(nil), accts...)}
	for i := range accts {
		u, w := accts[i], accts[(i+1)%len(accts)]
		for _, ei := range v.adj[u] {
			if v.edges[ei].to == w {
				e := v.edges[ei]
				c.TxIDs = append(c.TxIDs, e.txID)
				c.Amount += e.amount
				break
			}
		}
	}
	hopEdges := make([]edge, 0, len(c.TxIDs))
	for _, id := range c.TxIDs {
		hopEdges = append(hopEdges, v.byTxID[id])
	}
	sort.SliceStable(hopEdges, func(i, j int) bool {
		if hopEdges[i].time != hopEdges[j].time {
			return hopEdges[i].time < hopEdges[j].time
		}
		return hopEdges[i].txID < hopEdges[j].txID
	})
	for _, e := range hopEdges {
		c.Hops = append(c.Hops, Hop{TxID: e.txID, From: e.from, To: e.to, Amount: e.amount, Time: e.time})
	}
	sort.Strings(c.TxIDs)
	return c
}

// findCycles 在覆盖子图内枚举全部有向简单环。
// 先用 Kosaraju 做 O(V+E) 的 SCC 分解：平凡（无环）分量直接跳过，
// 因此数万节点的长链不会触发环枚举；再在每个环 SCC 内用 Johnson 枚举，
// 简单大环线性完成。输出按环签名排序，逐字节确定。
func (v *view) findCycles(covered map[string]bool) ([]Cycle, bool) {
	cycles := []Cycle{}
	truncated := false
	for _, comp := range v.kosaraju(covered) {
		if len(comp) <= 1 && !v.hasSelfLoop(comp[0], covered) {
			continue
		}
		got := v.johnsonCircuits(comp)
		cycles = append(cycles, got...)
		if len(got) >= maxCycles {
			truncated = true
			break
		}
	}
	sort.Slice(cycles, func(i, j int) bool {
		return strings.Join(cycles[i].Accounts, ",") < strings.Join(cycles[j].Accounts, ",")
	})
	return cycles, truncated
}

func sortedSet(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Trace 针对被标记账户或指定交易做资金下钻溯源。
//
// txs 应为规范化交易（ledger.Txs 快照）；limits 零值字段取默认值。
// 结论与输入顺序无关：内部按 (Time, TxID) 全序处理，重复运行逐字节一致。
func Trace(txs []ledger.Tx, t Target, limits Limits) Result {
	lim := normalizeLimits(limits)
	v := buildView(txs)
	res := Result{Target: t}

	var upStarts, downStarts []string
	switch t.Kind {
	case TargetTx:
		e, ok := v.byTxID[t.TxID]
		if !ok {
			res.Notes = append(res.Notes, fmt.Sprintf("tx %q not found in ledger snapshot", t.TxID))
			return res
		}
		res.Found = true
		anchor := Hop{TxID: e.txID, From: e.from, To: e.to, Amount: e.amount, Time: e.time}
		res.AnchorTx = &anchor
		// 上游：钱怎么流进这笔交易的转出方；下游：这笔交易的收款方又流向哪。
		upStarts = []string{e.from}
		downStarts = []string{e.to}
	case TargetAccount:
		if t.Account == "" {
			res.Notes = append(res.Notes, "account target is empty")
			return res
		}
		if _, ok := v.adj[t.Account]; !ok {
			if _, ok2 := v.radj[t.Account]; !ok2 {
				res.Notes = append(res.Notes, fmt.Sprintf("account %q not found in ledger snapshot", t.Account))
				return res
			}
		}
		res.Found = true
		upStarts = []string{t.Account}
		downStarts = []string{t.Account}
	default:
		res.Notes = append(res.Notes, fmt.Sprintf("unknown trace target kind: %q", t.Kind))
		return res
	}

	up := v.bfs(upStarts, false, lim)
	down := v.bfs(downStarts, true, lim)

	res.Upstream = DirectionResult{
		Reachable: append([]string(nil), up.order...),
		Truncated: up.trunc, LimitName: up.limit,
		Frontier: sortedSet(up.frontier),
		Hops:     v.hopsInSubgraph(up.visited, "up", up.depth),
	}
	res.Downstream = DirectionResult{
		Reachable: append([]string(nil), down.order...),
		Truncated: down.trunc, LimitName: down.limit,
		Frontier: sortedSet(down.frontier),
		Hops:     v.hopsInSubgraph(down.visited, "down", down.depth),
	}
	// Reachable 用发现顺序（层序）更贴近“先后”；为稳定起见也已由确定 BFS 给出。

	// 覆盖子图 = 上游 ∪ 下游（交易锚点时两端都纳入）。
	covered := map[string]bool{}
	for a := range up.visited {
		covered[a] = true
	}
	for a := range down.visited {
		covered[a] = true
	}
	if t.Kind == TargetTx {
		if e, ok := v.byTxID[t.TxID]; ok {
			covered[e.from] = true
			covered[e.to] = true
		}
	}

	cycles, cycTrunc := v.findCycles(covered)
	res.Cycles = cycles
	if cycTrunc {
		res.CyclesTruncated = true
		res.Notes = append(res.Notes,
			fmt.Sprintf("cycle enumeration hit hard cap %d; cycle list may be incomplete", maxCycles))
	}
	if up.trunc {
		res.Notes = append(res.Notes, fmt.Sprintf(
			"upstream trace truncated by %s limit (maxNodes=%d,maxHops=%d); frontier accounts listed",
			up.limit, lim.MaxNodes, lim.MaxHops))
	}
	if down.trunc {
		res.Notes = append(res.Notes, fmt.Sprintf(
			"downstream trace truncated by %s limit (maxNodes=%d,maxHops=%d); frontier accounts listed",
			down.limit, lim.MaxNodes, lim.MaxHops))
	}
	return res
}
