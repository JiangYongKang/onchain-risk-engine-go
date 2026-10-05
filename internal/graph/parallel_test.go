package graph

import (
	"fmt"
	"testing"
	"time"
)

// TestDetectCyclesParallelEdgesNoBlowup 回归测试：同一对账户间的重复转账
// （平行边）不得让环路检测的耗时随深度指数爆炸。
func TestDetectCyclesParallelEdgesNoBlowup(t *testing.T) {
	// 50 节点环，每对相邻账户间 3 条平行边；另挂一条 200 跳平行边长尾。
	edges := make([]Edge, 0, 50*3+200*3)
	tx := 0
	add := func(from, to string) {
		edges = append(edges, Edge{
			TxID: fmt.Sprintf("p%06d", tx), From: from, To: to, Amount: 1, Time: int64(tx),
		})
		tx++
	}
	for i := 0; i < 50; i++ {
		for k := 0; k < 3; k++ {
			add(fmt.Sprintf("r%02d", i), fmt.Sprintf("r%02d", (i+1)%50))
		}
	}
	for i := 0; i < 200; i++ {
		for k := 0; k < 3; k++ {
			add(fmt.Sprintf("t%04d", i), fmt.Sprintf("t%04d", i+1))
		}
	}
	g := Build(edges)

	start := time.Now()
	cycles := g.DetectCycles(64) // 深度 64 > 环长 50，环应被检出
	elapsed := time.Since(start)
	t.Logf("输入：50 节点环（每跳 3 条平行边）+ 200 跳平行边长尾，共 %d 条边", len(edges))
	t.Logf("结论：检出环 %d 个，耗时 %v", len(cycles), elapsed)

	if len(cycles) != 1 {
		t.Fatalf("应恰好检出 1 个环，实际 %d", len(cycles))
	}
	if len(cycles[0].Accounts) != 50 {
		t.Fatalf("环应含 50 个账户，实际 %d", len(cycles[0].Accounts))
	}
	if elapsed > 5*time.Second {
		t.Fatalf("平行边导致耗时爆炸：%v", elapsed)
	}
}
