package graph

import (
	"fmt"
	"sort"
)

// maxPatterns 是每类模式返回数量的安全上限，防止超大输入导致资源耗尽。
const maxPatterns = 1024

// Build 根据规范化交易确定性地构建图。
// 调用方应保证 edges 已按 (Time, TxID) 排序（ledger.Txs 提供该保证）；
// Build 内部仍会再排序一次，使任何调用方都得到确定结果。
func Build(edges []Edge) *Graph {
	es := make([]Edge, len(edges))
	copy(es, edges)
	sort.SliceStable(es, func(i, j int) bool {
		if es[i].Time != es[j].Time {
			return es[i].Time < es[j].Time
		}
		return es[i].TxID < es[j].TxID
	})

	seen := map[string]struct{}{}
	accounts := make([]string, 0)
	adj := map[string][]int{}
	radj := map[string][]int{}
	pair := map[string]map[string]int{}
	nbrs := map[string][]string{}
	byTx := make(map[string]int, len(es))
	for idx, e := range es {
		for _, a := range []string{e.From, e.To} {
			if _, ok := seen[a]; !ok {
				seen[a] = struct{}{}
				accounts = append(accounts, a)
			}
		}
		adj[e.From] = append(adj[e.From], idx)
		radj[e.To] = append(radj[e.To], idx)
		byTx[e.TxID] = idx
		if pair[e.From] == nil {
			pair[e.From] = map[string]int{}
		}
		// 时间序中最早的一条 u->v 边，确定且唯一。
		if _, ok := pair[e.From][e.To]; !ok {
			pair[e.From][e.To] = idx
			nbrs[e.From] = append(nbrs[e.From], e.To)
		}
	}
	sort.Strings(accounts)
	return &Graph{accounts: accounts, edges: es, adj: adj, radj: radj, pair: pair, nbrs: nbrs, byTx: byTx}
}

// Accounts 返回账户列表（确定性排序）。
func (g *Graph) Accounts() []string { return g.accounts }

// Edges 返回边列表（按时间、TxID 排序）。
func (g *Graph) Edges() []Edge { return g.edges }

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedTxIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	out = append(out, ids...)
	sort.Strings(out)
	return out
}

// DetectFanIn 识别资金汇聚：收到 >= minSources 个不同来源入账的账户。
// 判定依据：来源账户集合、入账交易集合、入账总金额。
func (g *Graph) DetectFanIn(minSources int) []Pattern {
	if minSources <= 0 {
		minSources = 1
	}
	type agg struct {
		sources map[string]struct{}
		txids   []string
		total   int64
	}
	acc := map[string]*agg{}
	for _, e := range g.edges {
		a := acc[e.To]
		if a == nil {
			a = &agg{sources: map[string]struct{}{}}
			acc[e.To] = a
		}
		a.sources[e.From] = struct{}{}
		a.txids = append(a.txids, e.TxID)
		a.total += e.Amount
	}
	out := make([]Pattern, 0)
	for _, acct := range g.accounts {
		a := acc[acct]
		if a == nil || len(a.sources) < minSources {
			continue
		}
		out = append(out, Pattern{
			Kind:     PatternFanIn,
			Account:  acct,
			Accounts: sortedKeys(a.sources),
			TxIDs:    sortedTxIDs(a.txids),
			Value:    a.total,
			Detail: fmt.Sprintf("%d distinct sources, %d inflow tx(s), total in %d",
				len(a.sources), len(a.txids), a.total),
		})
		if len(out) >= maxPatterns {
			break
		}
	}
	return out
}

// DetectFanOut 识别资金拆分：向 >= minTargets 个不同去向转出的账户。
func (g *Graph) DetectFanOut(minTargets int) []Pattern {
	if minTargets <= 0 {
		minTargets = 1
	}
	type agg struct {
		targets map[string]struct{}
		txids   []string
		total   int64
	}
	acc := map[string]*agg{}
	for _, e := range g.edges {
		a := acc[e.From]
		if a == nil {
			a = &agg{targets: map[string]struct{}{}}
			acc[e.From] = a
		}
		a.targets[e.To] = struct{}{}
		a.txids = append(a.txids, e.TxID)
		a.total += e.Amount
	}
	out := make([]Pattern, 0)
	for _, acct := range g.accounts {
		a := acc[acct]
		if a == nil || len(a.targets) < minTargets {
			continue
		}
		out = append(out, Pattern{
			Kind:     PatternFanOut,
			Account:  acct,
			Accounts: sortedKeys(a.targets),
			TxIDs:    sortedTxIDs(a.txids),
			Value:    a.total,
			Detail: fmt.Sprintf("%d distinct targets, %d outflow tx(s), total out %d",
				len(a.targets), len(a.txids), a.total),
		})
		if len(out) >= maxPatterns {
			break
		}
	}
	return out
}

// DetectCycles 识别有向环路。
//
// 确定性与安全性：
//   - 只在“环上字典序最小账户 == DFS 根账户”时记录环，保证每个不同环恰好一次；
//   - maxDepth 限制搜索深度，超深链路安全终止而非无限等待（<=0 时取默认 32，
//     并硬性封顶 maxDepthHardCap）；
//   - 结果按环账户序列字典序排序。
func (g *Graph) DetectCycles(maxDepth int) []Pattern {
	const defaultDepth, hardCap = 32, 256
	if maxDepth <= 0 {
		maxDepth = defaultDepth
	}
	if maxDepth > hardCap {
		maxDepth = hardCap
	}

	found := map[string][]string{} // 环签名（账户序列 join）-> 账户集合
	onPath := map[string]bool{}
	indexOnPath := map[string]int{}
	stack := make([]string, 0, maxDepth+1)

	var dfs func(u string, depth int)
	dfs = func(u string, depth int) {
		if len(found) >= maxPatterns || depth > maxDepth {
			return
		}
		onPath[u] = true
		indexOnPath[u] = len(stack)
		stack = append(stack, u)

		// 按“不同邻居账户”扩展而非按边：同一对账户间的平行边只走一次，
		// 否则重复转账（真实数据中极常见）会让简单路径数随深度指数爆炸。
		for _, v := range g.nbrs[u] {
			if len(found) >= maxPatterns {
				return
			}
			if onPath[v] {
				cyc := append([]string(nil), stack[indexOnPath[v]:]...)
				// 只允许字典序最小的账户作为该环的“发现根”，避免重复报告。
				min := cyc[0]
				for _, a := range cyc[1:] {
					if a < min {
						min = a
					}
				}
				if min != stack[0] {
					continue
				}
				sig := fmt.Sprintf("%v", cyc)
				if _, ok := found[sig]; !ok {
					found[sig] = cyc
				}
				continue
			}
			if depth+1 <= maxDepth {
				dfs(v, depth+1)
			}
		}

		stack = stack[:len(stack)-1]
		delete(indexOnPath, u)
		onPath[u] = false
	}

	for _, root := range g.accounts {
		if len(found) >= maxPatterns {
			break
		}
		dfs(root, 1)
	}

	sigs := make([]string, 0, len(found))
	for s := range found {
		sigs = append(sigs, s)
	}
	sort.Strings(sigs)

	out := make([]Pattern, 0, len(sigs))
	for _, s := range sigs {
		cyc := found[s]
		// 收集环上账户之间（按最早边）的交易，作为可核对依据。
		txids := make([]string, 0, len(cyc))
		var amount int64
		for i := range cyc {
			u, v := cyc[i], cyc[(i+1)%len(cyc)]
			if m := g.pair[u]; m != nil {
				if ei, ok := m[v]; ok {
					txids = append(txids, g.edges[ei].TxID)
					amount += g.edges[ei].Amount
				}
			}
		}
		out = append(out, Pattern{
			Kind:     PatternCycle,
			Account:  cyc[0],
			Accounts: append([]string(nil), cyc...),
			TxIDs:    sortedTxIDs(txids),
			Value:    amount,
			Detail:   fmt.Sprintf("cycle of %d account(s): %v", len(cyc), cyc),
		})
	}
	return out
}

// DetectHotspots 识别异常集中账户。
//
// 两条可解释判据（满足其一）：
//  1. 总流经金额（流入+流出）>= threshold（threshold>0 时生效）；
//  2. 交易笔数 >= 3 且 >= 3*全体有交易账户笔数中位数（相对异常）。
func (g *Graph) DetectHotspots(threshold int64) []Pattern {
	type agg struct {
		count int
		total int64
		txids []string
	}
	acc := map[string]*agg{}
	for _, e := range g.edges {
		for _, a := range []string{e.From, e.To} {
			x := acc[a]
			if x == nil {
				x = &agg{}
				acc[a] = x
			}
			x.count++
			x.total += e.Amount
			x.txids = append(x.txids, e.TxID)
		}
	}
	counts := make([]int, 0, len(acc))
	for _, a := range acc {
		counts = append(counts, a.count)
	}
	sort.Ints(counts)
	median := 0
	if n := len(counts); n > 0 {
		median = counts[n/2]
	}

	out := make([]Pattern, 0)
	for _, acct := range g.accounts {
		a := acc[acct]
		if a == nil {
			continue
		}
		hitAmount := threshold > 0 && a.total >= threshold
		hitCount := a.count >= 3 && median > 0 && a.count >= 3*median
		if !hitAmount && !hitCount {
			continue
		}
		reasons := make([]string, 0, 2)
		if hitAmount {
			reasons = append(reasons, fmt.Sprintf("flow %d >= threshold %d", a.total, threshold))
		}
		if hitCount {
			reasons = append(reasons, fmt.Sprintf("tx count %d >= 3x median %d", a.count, median))
		}
		out = append(out, Pattern{
			Kind:     PatternHotspot,
			Account:  acct,
			Accounts: []string{acct},
			TxIDs:    sortedTxIDs(a.txids),
			Value:    a.total,
			Detail: fmt.Sprintf("flow total %d over %d tx(s); %v",
				a.total, a.count, reasons),
		})
		if len(out) >= maxPatterns {
			break
		}
	}
	return out
}
