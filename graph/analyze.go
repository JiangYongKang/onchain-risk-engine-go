package graph

import (
	"fmt"
	"sort"
)

// PatternKind 识别出的风险模式类别。
type PatternKind string

const (
	PatternFanIn         PatternKind = "fan_in"        // 资金汇聚
	PatternFanOut        PatternKind = "fan_out"       // 资金拆分
	PatternCycle         PatternKind = "cycle"         // 资金环路
	PatternConcentration PatternKind = "concentration" // 异常集中
)

// Finding 是一条可核对的风险判定依据。
type Finding struct {
	Kind     PatternKind
	Accounts []string // 涉及账户，排序后输出
	Detail   string   // 人类可读的判定依据（含数值）
	Volume   int64    // 涉及资金量
}

// Thresholds 是分析所需的阈值参数（来自规则集）。
type Thresholds struct {
	FanInMinSources    int   // 汇聚：最少不同来源数
	FanOutMinTargets   int   // 拆分：最少不同去向数
	ConcentrationShare int   // 集中：单账户占总流水百分比阈值（0-100）
	MinCycleVolume     int64 // 环路：环上最小总流水
	MaxNodes           int   // 图规模上限保护（0 表示不限制）
}

// Analyze 在图上执行全部模式识别，输出确定性排序的发现列表。
// 全部算法均为迭代实现并带规模保护，超深链路、环路或超大输入
// 不会造成崩溃或无限等待。
func Analyze(g *Graph, th Thresholds) []Finding {
	var findings []Finding
	findings = append(findings, findFanIn(g, th)...)
	findings = append(findings, findFanOut(g, th)...)
	findings = append(findings, findCycles(g, th)...)
	findings = append(findings, findConcentration(g, th)...)
	// 确定性排序：先按类别，再按涉及账户，再按金额。
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Kind != findings[j].Kind {
			return findings[i].Kind < findings[j].Kind
		}
		ai, aj := fmt.Sprint(findings[i].Accounts), fmt.Sprint(findings[j].Accounts)
		if ai != aj {
			return ai < aj
		}
		return findings[i].Volume < findings[j].Volume
	})
	return findings
}

// findFanIn 识别资金汇聚：单账户从 >= FanInMinSources 个不同来源收款。
func findFanIn(g *Graph, th Thresholds) []Finding {
	var out []Finding
	for _, n := range g.Nodes {
		sources := make(map[string]int64)
		for _, e := range g.In[n] {
			sources[e.From] += e.Amount
		}
		if len(sources) >= th.FanInMinSources && th.FanInMinSources > 0 {
			var total int64
			accts := make([]string, 0, len(sources))
			for s, v := range sources {
				total += v
				accts = append(accts, s)
			}
			sort.Strings(accts)
			out = append(out, Finding{
				Kind:     PatternFanIn,
				Accounts: append([]string{n}, accts...),
				Detail: fmt.Sprintf("account %s received from %d distinct sources (threshold %d), total inflow %d",
					n, len(sources), th.FanInMinSources, total),
				Volume: total,
			})
		}
	}
	return out
}

// findFanOut 识别资金拆分：单账户向 >= FanOutMinTargets 个不同去向付款。
func findFanOut(g *Graph, th Thresholds) []Finding {
	var out []Finding
	for _, n := range g.Nodes {
		targets := make(map[string]int64)
		for _, e := range g.Out[n] {
			targets[e.To] += e.Amount
		}
		if len(targets) >= th.FanOutMinTargets && th.FanOutMinTargets > 0 {
			var total int64
			accts := make([]string, 0, len(targets))
			for t, v := range targets {
				total += v
				accts = append(accts, t)
			}
			sort.Strings(accts)
			out = append(out, Finding{
				Kind:     PatternFanOut,
				Accounts: append([]string{n}, accts...),
				Detail: fmt.Sprintf("account %s sent to %d distinct targets (threshold %d), total outflow %d",
					n, len(targets), th.FanOutMinTargets, total),
				Volume: total,
			})
		}
	}
	return out
}

// findCycles 用迭代式 Tarjan SCC 识别环路：大小 > 1 的强连通分量，
// 或带自环的单节点。递归实现会在超深链路上爆栈，因此使用显式栈。
func findCycles(g *Graph, th Thresholds) []Finding {
	if th.MaxNodes > 0 && len(g.Nodes) > th.MaxNodes {
		return []Finding{{
			Kind:   PatternCycle,
			Detail: fmt.Sprintf("graph has %d nodes, exceeds MaxNodes %d; cycle scan skipped (safeguard)", len(g.Nodes), th.MaxNodes),
		}}
	}
	sccs := tarjanSCC(g)
	var out []Finding
	for _, comp := range sccs {
		isCycle := len(comp) > 1
		if !isCycle { // 单节点需检查自环
			for _, e := range g.Out[comp[0]] {
				if e.To == comp[0] {
					isCycle = true
					break
				}
			}
		}
		if !isCycle {
			continue
		}
		inComp := make(map[string]bool, len(comp))
		for _, n := range comp {
			inComp[n] = true
		}
		var vol int64
		for _, n := range comp {
			for _, e := range g.Out[n] {
				if inComp[e.To] {
					vol += e.Amount
				}
			}
		}
		if vol < th.MinCycleVolume {
			continue
		}
		accts := append([]string(nil), comp...)
		sort.Strings(accts)
		out = append(out, Finding{
			Kind:     PatternCycle,
			Accounts: accts,
			Detail: fmt.Sprintf("cycle of %d accounts %v with internal volume %d (min %d)",
				len(accts), accts, vol, th.MinCycleVolume),
			Volume: vol,
		})
	}
	return out
}

// tarjanSCC 迭代式 Tarjan 强连通分量算法，显式栈避免递归爆栈。
// 节点按排序顺序访问，结果确定。
func tarjanSCC(g *Graph) [][]string {
	index := make(map[string]int, len(g.Nodes))
	low := make(map[string]int, len(g.Nodes))
	onStack := make(map[string]bool, len(g.Nodes))
	var stack []string
	counter := 0
	var sccs [][]string

	type frame struct {
		node string
		next int // 下一条待访问出边的下标
	}
	for _, start := range g.Nodes {
		if _, seen := index[start]; seen {
			continue
		}
		var callStack []frame
		index[start] = counter
		low[start] = counter
		counter++
		stack = append(stack, start)
		onStack[start] = true
		callStack = append(callStack, frame{node: start})

		for len(callStack) > 0 {
			top := &callStack[len(callStack)-1]
			edges := g.Out[top.node]
			if top.next < len(edges) {
				e := edges[top.next]
				top.next++
				if _, seen := index[e.To]; !seen {
					index[e.To] = counter
					low[e.To] = counter
					counter++
					stack = append(stack, e.To)
					onStack[e.To] = true
					callStack = append(callStack, frame{node: e.To})
				} else if onStack[e.To] {
					if index[e.To] < low[top.node] {
						low[top.node] = index[e.To]
					}
				}
			} else {
				// 子节点处理完毕，回传 lowlink。
				callStack = callStack[:len(callStack)-1]
				if len(callStack) > 0 {
					parent := callStack[len(callStack)-1].node
					if low[top.node] < low[parent] {
						low[parent] = low[top.node]
					}
				}
				if low[top.node] == index[top.node] {
					var comp []string
					for {
						w := stack[len(stack)-1]
						stack = stack[:len(stack)-1]
						onStack[w] = false
						comp = append(comp, w)
						if w == top.node {
							break
						}
					}
					sccs = append(sccs, comp)
				}
			}
		}
	}
	return sccs
}

// findConcentration 识别异常集中：单账户流入占总流水 >= ConcentrationShare%。
func findConcentration(g *Graph, th Thresholds) []Finding {
	if th.ConcentrationShare <= 0 {
		return nil
	}
	inflow := make(map[string]int64)
	var total int64
	for _, n := range g.Nodes {
		for _, e := range g.In[n] {
			inflow[n] += e.Amount
			total += e.Amount
		}
	}
	if total == 0 {
		return nil
	}
	var out []Finding
	for _, n := range g.Nodes {
		v := inflow[n]
		if v == 0 {
			continue
		}
		share := v * 100 / total
		if int(share) >= th.ConcentrationShare {
			out = append(out, Finding{
				Kind:     PatternConcentration,
				Accounts: []string{n},
				Detail: fmt.Sprintf("account %s holds %d%% of total inflow (%d/%d, threshold %d%%)",
					n, share, v, total, th.ConcentrationShare),
				Volume: v,
			})
		}
	}
	return out
}
