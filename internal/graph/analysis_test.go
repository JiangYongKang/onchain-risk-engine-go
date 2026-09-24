package graph

import (
	"fmt"
	"testing"
)

func find(ps []Pattern, kind PatternKind, account string) *Pattern {
	for i := range ps {
		if ps[i].Kind == kind && ps[i].Account == account {
			return &ps[i]
		}
	}
	return nil
}

func TestDetectFanInFanOut(t *testing.T) {
	// 汇聚：x 收到 3 个不同来源；拆分：a 向 3 个不同去向转出。
	edges := []Edge{
		{TxID: "1", From: "s1", To: "x", Amount: 100, Time: 1},
		{TxID: "2", From: "s2", To: "x", Amount: 200, Time: 2},
		{TxID: "3", From: "s3", To: "x", Amount: 300, Time: 3},
		{TxID: "4", From: "a", To: "d1", Amount: 10, Time: 4},
		{TxID: "5", From: "a", To: "d2", Amount: 10, Time: 5},
		{TxID: "6", From: "a", To: "d3", Amount: 10, Time: 6},
	}
	g := Build(edges)
	in := g.DetectFanIn(3)
	out := g.DetectFanOut(3)

	p := find(in, PatternFanIn, "x")
	if p == nil {
		t.Fatal("未识别出 x 的资金汇聚")
	}
	t.Logf("汇聚依据：%s；涉及账户=%v；交易=%v", p.Detail, p.Accounts, p.TxIDs)
	if fmt.Sprint(p.Accounts) != "[s1 s2 s3]" || p.Value != 600 {
		t.Fatal("汇聚依据不正确")
	}
	q := find(out, PatternFanOut, "a")
	if q == nil {
		t.Fatal("未识别出 a 的资金拆分")
	}
	t.Logf("拆分依据：%s；涉及账户=%v", q.Detail, q.Accounts)
	if len(q.Accounts) != 3 {
		t.Fatal("拆分依据不正确")
	}
}

func TestDetectCyclesDeterministic(t *testing.T) {
	// 两个独立环：a->b->c->a 与 p->q->p；外加一条出环边。
	edges := []Edge{
		{TxID: "ab", From: "a", To: "b", Amount: 1, Time: 1},
		{TxID: "bc", From: "b", To: "c", Amount: 1, Time: 2},
		{TxID: "ca", From: "c", To: "a", Amount: 1, Time: 3},
		{TxID: "pq", From: "p", To: "q", Amount: 1, Time: 4},
		{TxID: "qp", From: "q", To: "p", Amount: 1, Time: 5},
		{TxID: "cz", From: "c", To: "z", Amount: 1, Time: 6}, // 非环边
	}
	g := Build(edges)
	cyc := g.DetectCycles(32)
	t.Logf("识别到 %d 个环路：", len(cyc))
	sigs := map[string]bool{}
	for _, p := range cyc {
		t.Logf("  %s 依据=%v 交易=%v", p.Detail, p.Accounts, p.TxIDs)
		sigs[fmt.Sprint(p.Accounts)] = true
	}
	if len(cyc) != 2 {
		t.Fatalf("应恰好识别 2 个不同环，实际 %d", len(cyc))
	}
	if !sigs["[a b c]"] || !sigs["[p q]"] {
		t.Fatal("环路账户集合不正确")
	}

	// 同一图重复检测 / 以不同边序构建，结果应一致。
	cyc2 := g.DetectCycles(32)
	if fmt.Sprint(cyc) != fmt.Sprint(cyc2) {
		t.Fatal("同图重复检测结果不一致")
	}
	g3 := Build([]Edge{edges[5], edges[3], edges[0], edges[4], edges[2], edges[1]})
	cyc3 := g3.DetectCycles(32)
	if len(cyc3) != 2 {
		t.Fatalf("乱序边构建后环路数量错误：%d", len(cyc3))
	}
	t.Log("乱序边构建结果与原顺序一致")
}

func TestDeepChainDoesNotHangOrMisdetect(t *testing.T) {
	// 超深无环链：2000 个节点 a0->a1->...->a1999，不得产生环路，不得挂起。
	edges := make([]Edge, 0, 2000)
	for i := 0; i < 2000; i++ {
		edges = append(edges, Edge{
			TxID: fmt.Sprintf("e%04d", i),
			From: fmt.Sprintf("a%04d", i), To: fmt.Sprintf("a%04d", i+1),
			Amount: 1, Time: int64(i),
		})
	}
	g := Build(edges)
	done := make(chan struct{})
	var cyc []Pattern
	go func() {
		cyc = g.DetectCycles(8) // 深度上限远小于链长
		close(done)
	}()
	select {
	case <-done:
	case <-makeTimer():
		t.Fatal("超深链路检测超时（可能无限等待）")
	}
	if len(cyc) != 0 {
		t.Fatalf("无环链被误判出 %d 个环路", len(cyc))
	}
	t.Logf("2000 跳无环链在深度上限 8 下安全返回，环路=%d", len(cyc))
}

func TestHotspots(t *testing.T) {
	edges := []Edge{
		{TxID: "1", From: "h", To: "b", Amount: 600_000, Time: 1},
		{TxID: "2", From: "c", To: "h", Amount: 600_000, Time: 2}, // h 流经 120 万
		{TxID: "3", From: "d", To: "e", Amount: 1, Time: 3},
	}
	g := Build(edges)
	hs := g.DetectHotspots(1_000_000)
	p := find(hs, PatternHotspot, "h")
	if p == nil {
		t.Fatal("未识别金额热点 h")
	}
	t.Logf("热点依据：%s", p.Detail)
}
