// Package graph 在账本快照上构建交易图并做关联分析。
package graph

import (
	"sort"

	"github.com/highcumontoa/onchain-risk-engine-go/ledger"
)

// Graph 是有向交易图：节点为账户，边为转账。
type Graph struct {
	Out   map[string][]Edge // 出边，按 (To, TxID) 排序，保证确定性
	In    map[string][]Edge // 入边，按 (From, TxID) 排序
	Nodes []string          // 排序后的节点列表
}

// Edge 是一条交易边。
type Edge struct {
	From   string
	To     string
	Amount int64
	TxID   string
	TxTime int64
}

// Build 从账本快照构建确定性图。输入先按 (TxTime, TxID) 排序，
// 使乱序到达的摄取不影响图结构。
func Build(tx []ledger.Transfer) *Graph {
	sorted := make([]ledger.Transfer, len(tx))
	copy(sorted, tx)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].TxTime != sorted[j].TxTime {
			return sorted[i].TxTime < sorted[j].TxTime
		}
		return sorted[i].TxID < sorted[j].TxID
	})

	g := &Graph{
		Out: make(map[string][]Edge),
		In:  make(map[string][]Edge),
	}
	nodeSet := make(map[string]struct{})
	for _, t := range sorted {
		e := Edge{From: t.From, To: t.To, Amount: t.Amount, TxID: t.TxID, TxTime: t.TxTime}
		g.Out[t.From] = append(g.Out[t.From], e)
		g.In[t.To] = append(g.In[t.To], e)
		nodeSet[t.From] = struct{}{}
		nodeSet[t.To] = struct{}{}
	}
	for n := range nodeSet {
		g.Nodes = append(g.Nodes, n)
	}
	sort.Strings(g.Nodes)
	for _, edges := range g.Out {
		sort.Slice(edges, func(i, j int) bool {
			if edges[i].To != edges[j].To {
				return edges[i].To < edges[j].To
			}
			return edges[i].TxID < edges[j].TxID
		})
	}
	for _, edges := range g.In {
		sort.Slice(edges, func(i, j int) bool {
			if edges[i].From != edges[j].From {
				return edges[i].From < edges[j].From
			}
			return edges[i].TxID < edges[j].TxID
		})
	}
	return g
}
