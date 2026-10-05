package graph

import (
	"fmt"
	"testing"
	"time"
)

// buildChain 构造 n 跳直线链 n0->n1->...->nn。
func buildChain(n int) *Graph {
	edges := make([]Edge, 0, n)
	for i := 0; i < n; i++ {
		edges = append(edges, Edge{
			TxID: fmt.Sprintf("c%06d", i), From: fmt.Sprintf("n%06d", i),
			To: fmt.Sprintf("n%06d", i+1), Amount: int64(i + 1), Time: int64(i),
		})
	}
	return Build(edges)
}

// buildCycle 构造 n 节点大环 n0->n1->...->n(n-1)->n0。
func buildCycle(n int) *Graph {
	edges := make([]Edge, 0, n)
	for i := 0; i < n; i++ {
		edges = append(edges, Edge{
			TxID: fmt.Sprintf("r%06d", i), From: fmt.Sprintf("m%06d", i),
			To: fmt.Sprintf("m%06d", (i+1)%n), Amount: 1, Time: int64(i),
		})
	}
	return Build(edges)
}

func TestTraceAccountSmallChainExplainable(t *testing.T) {
	g := Build([]Edge{
		{TxID: "t1", From: "a", To: "b", Amount: 10, Time: 1},
		{TxID: "t2", From: "b", To: "c", Amount: 8, Time: 2},
		{TxID: "t3", From: "c", To: "d", Amount: 6, Time: 3},
		{TxID: "t0", From: "z", To: "a", Amount: 20, Time: 0},
	})
	tr := g.TraceAccount("b", TraceOptions{})
	t.Log("输入：z->a->b->c->d 四跳链，从账户 b 溯源")
	for _, s := range tr.Inbound {
		t.Logf("  流入跳: depth=%d %s -%d-> %s (tx=%s t=%d)", s.Depth, s.From, s.Amount, s.To, s.TxID, s.Time)
	}
	for _, s := range tr.Outbound {
		t.Logf("  流出跳: depth=%d %s -%d-> %s (tx=%s t=%d)", s.Depth, s.From, s.Amount, s.To, s.TxID, s.Time)
	}
	t.Logf("结论: 涉及账户=%v 截断=%v", tr.Accounts, tr.Truncated)

	if len(tr.Inbound) != 2 || len(tr.Outbound) != 2 {
		t.Fatalf("链路还原不完整：in=%d out=%d", len(tr.Inbound), len(tr.Outbound))
	}
	// 时间升序。
	if tr.Inbound[0].TxID != "t0" || tr.Inbound[1].TxID != "t1" ||
		tr.Outbound[0].TxID != "t2" || tr.Outbound[1].TxID != "t3" {
		t.Fatalf("链路顺序错误：in=%v out=%v", tr.Inbound, tr.Outbound)
	}
	if tr.Truncated {
		t.Fatal("未触限不应标记截断")
	}
	if fmt.Sprint(tr.Accounts) != "[a b c d z]" {
		t.Fatalf("涉及账户错误：%v", tr.Accounts)
	}
}

func TestTraceLongChainComplete(t *testing.T) {
	const n = 30000 // 3 万跳直线链
	g := buildChain(n)
	start := time.Now()
	tr := g.TraceAccount("n000000", TraceOptions{})
	elapsed := time.Since(start)

	t.Logf("输入：%d 跳直线链，从链首溯源", n)
	t.Logf("结论: 流出跳数=%d 涉及账户=%d 截断=%v 生效上限(depth=%d,nodes=%d) 耗时=%v",
		len(tr.Outbound), len(tr.Accounts), tr.Truncated, tr.MaxDepth, tr.MaxNodes, elapsed)

	if tr.Truncated {
		t.Fatal("默认上限下 3 万跳链不应被截断")
	}
	if len(tr.Outbound) != n {
		t.Fatalf("应完整还原 %d 跳，实际 %d", n, len(tr.Outbound))
	}
	if len(tr.Accounts) != n+1 {
		t.Fatalf("应覆盖 %d 个账户，实际 %d", n+1, len(tr.Accounts))
	}
	// 严格按时间顺序还原，且深度递增。
	for i, s := range tr.Outbound {
		if s.Time != int64(i) || s.Depth != i+1 {
			t.Fatalf("第 %d 跳顺序/深度错误：%+v", i, s)
		}
	}
	// 从链尾反向溯源同样完整。
	back := g.TraceAccount(fmt.Sprintf("n%06d", n), TraceOptions{})
	if len(back.Inbound) != n || back.Truncated {
		t.Fatalf("反向溯源不完整：in=%d truncated=%v", len(back.Inbound), back.Truncated)
	}
	t.Logf("反向溯源（链尾）: 流入跳数=%d 截断=%v", len(back.Inbound), back.Truncated)
}

func TestTraceLargeCycleTerminates(t *testing.T) {
	const n = 20000 // 2 万节点大环
	g := buildCycle(n)
	start := time.Now()
	tr := g.TraceAccount("m000000", TraceOptions{})
	elapsed := time.Since(start)

	t.Logf("输入：%d 节点大环，从环上一账户溯源", n)
	t.Logf("结论: 流出跳数=%d 流入跳数=%d 涉及账户=%d 截断=%v 耗时=%v",
		len(tr.Outbound), len(tr.Inbound), len(tr.Accounts), tr.Truncated, elapsed)

	if tr.Truncated {
		t.Fatal("默认上限下大环不应被截断")
	}
	// 每个方向恰好 n 跳：n-1 跳走到环上其余节点 + 1 跳闭环边。
	if len(tr.Outbound) != n || len(tr.Inbound) != n {
		t.Fatalf("大环还跳数错误：out=%d in=%d（应各 %d）", len(tr.Outbound), len(tr.Inbound), n)
	}
	if len(tr.Accounts) != n {
		t.Fatalf("应覆盖全部 %d 个环上账户，实际 %d", n, len(tr.Accounts))
	}
	// 闭环边必须出现在结果里：最后一跳指回起点。
	last := tr.Outbound[len(tr.Outbound)-1]
	if last.To != "m000000" {
		t.Fatalf("大环收口跳缺失：最后一跳=%+v", last)
	}
	t.Logf("闭环边确认: %s -%d-> %s (tx=%s)", last.From, last.Amount, last.To, last.TxID)
}

func TestTraceTruncationExplicit(t *testing.T) {
	g := buildChain(1000)
	tr := g.TraceAccount("n000000", TraceOptions{MaxDepth: 10, MaxNodes: 5})
	t.Logf("输入：1000 跳链，上限 depth=10 nodes=5")
	t.Logf("结论: 流出跳数=%d 截断=%v 生效上限=(%d,%d)",
		len(tr.Outbound), tr.Truncated, tr.MaxDepth, tr.MaxNodes)

	if !tr.Truncated {
		t.Fatal("触及上限必须显式标记 Truncated")
	}
	if tr.MaxDepth != 10 || tr.MaxNodes != 5 {
		t.Fatalf("生效上限未如实回显：(%d,%d)", tr.MaxDepth, tr.MaxNodes)
	}
	if len(tr.Outbound) >= 1000 {
		t.Fatal("截断结果不应伪装成完整链路")
	}
	// 节点预算 5 => 最多覆盖 5 个账户（含起点）。
	if len(tr.Accounts) > 5 {
		t.Fatalf("节点上限被突破：%d 个账户", len(tr.Accounts))
	}
}

func TestTraceTxBothDirections(t *testing.T) {
	g := Build([]Edge{
		{TxID: "t1", From: "a", To: "b", Amount: 10, Time: 1},
		{TxID: "t2", From: "b", To: "c", Amount: 9, Time: 2},
		{TxID: "t3", From: "c", To: "d", Amount: 8, Time: 3},
	})
	tr, ok := g.TraceTx("t2", TraceOptions{})
	if !ok {
		t.Fatal("t2 应存在")
	}
	t.Log("输入：a->b->c->d，从交易 t2 (b->c) 溯源")
	t.Logf("结论: 根交易=%+v 上游=%d 跳 下游=%d 跳", *tr.RootTx, len(tr.Inbound), len(tr.Outbound))

	if tr.RootTx == nil || tr.RootTx.TxID != "t2" || tr.RootTx.Depth != 0 {
		t.Fatalf("根交易信息错误：%+v", tr.RootTx)
	}
	if len(tr.Inbound) != 1 || tr.Inbound[0].TxID != "t1" {
		t.Fatalf("上游链路错误：%v", tr.Inbound)
	}
	if len(tr.Outbound) != 1 || tr.Outbound[0].TxID != "t3" {
		t.Fatalf("下游链路错误：%v", tr.Outbound)
	}

	if _, ok := g.TraceTx("nope", TraceOptions{}); ok {
		t.Fatal("不存在的交易应返回 ok=false")
	}
}

func TestTraceDeterministic(t *testing.T) {
	g := buildCycle(5000)
	a := g.TraceAccount("m000100", TraceOptions{})
	b := g.TraceAccount("m000100", TraceOptions{})
	if fmt.Sprint(a) != fmt.Sprint(b) {
		t.Fatal("同一图同一起点两次溯源结论不一致")
	}
	t.Logf("5000 节点环重复溯源结论一致：out=%d in=%d accounts=%d",
		len(a.Outbound), len(a.Inbound), len(a.Accounts))
}
