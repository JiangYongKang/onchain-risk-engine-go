package trace

import (
	"fmt"
	"testing"
	"time"

	"github.com/highcumontoa/onchain-risk-engine-go/internal/ledger"
)

func toTxs(recs []ledger.Record) []ledger.Tx {
	l := ledger.New()
	res := l.Ingest(recs)
	if len(res.Rejected) != 0 {
		panic(fmt.Sprintf("unexpected rejects: %+v", res.Rejected))
	}
	return l.Txs()
}

func hopTxIDs(hs []Hop) []string {
	out := make([]string, 0, len(hs))
	for _, h := range hs {
		out = append(out, h.TxID)
	}
	return out
}

// TestTraceAccountSmallPath 核对小图上账户溯源的上下游与先后顺序。
func TestTraceAccountSmallPath(t *testing.T) {
	// s1,s2 -> x -> m -> n；另有 x->s1 与 s1->x 构成 2 环。
	recs := []ledger.Record{
		{ID: "e1", Timestamp: 1, From: "s1", To: "x", Amount: "10"},
		{ID: "e2", Timestamp: 2, From: "s2", To: "x", Amount: "20"},
		{ID: "e3", Timestamp: 3, From: "x", To: "m", Amount: "30"},
		{ID: "e4", Timestamp: 4, From: "m", To: "n", Amount: "30"},
		{ID: "e5", Timestamp: 5, From: "x", To: "s1", Amount: "5"},
	}
	txs := toTxs(recs)
	t.Logf("输入 %d 笔交易，锚点账户 x，向上游/下游溯源", len(txs))

	res := Trace(txs, Target{Kind: TargetAccount, Account: "x"}, Limits{})
	if !res.Found {
		t.Fatal("x 应被找到")
	}
	// 上游可达：s1,s2（x 自身为根）。下游可达：m,n（x->s1 回到上游也计入下游子图）。
	t.Logf("上游可达=%v 跳=%v", res.Upstream.Reachable, hopTxIDs(res.Upstream.Hops))
	t.Logf("下游可达=%v 跳=%v", res.Downstream.Reachable, hopTxIDs(res.Downstream.Hops))
	if res.Upstream.Truncated || res.Downstream.Truncated {
		t.Fatal("小图不应截断")
	}
	upSet := map[string]bool{}
	for _, a := range res.Upstream.Reachable {
		upSet[a] = true
	}
	if !upSet["s1"] || !upSet["s2"] {
		t.Fatalf("上游应包含 s1,s2：%v", res.Upstream.Reachable)
	}
	downSet := map[string]bool{}
	for _, a := range res.Downstream.Reachable {
		downSet[a] = true
	}
	if !downSet["m"] || !downSet["n"] {
		t.Fatalf("下游应包含 m,n：%v", res.Downstream.Reachable)
	}
	// 下游跳按 (时间,ID) 全序给出；回边 e1(s1->x) 也在下游覆盖子图内，
	// 因为 x->s1 后资金可经 e1 回到 x（环形回流），不能漏。校验关键路径
	// e3(x->m) 早于 e4(m->n)，且两条都在。
	dnIDs := hopTxIDs(res.Downstream.Hops)
	idx := map[string]int{}
	for i, id := range dnIDs {
		idx[id] = i
	}
	if _, ok := idx["e3"]; !ok {
		t.Fatalf("下游缺少 e3：%v", dnIDs)
	}
	if _, ok := idx["e4"]; !ok {
		t.Fatalf("下游缺少 e4：%v", dnIDs)
	}
	if idx["e3"] >= idx["e4"] {
		t.Fatalf("资金先后顺序错误，e3 应早于 e4：%v", dnIDs)
	}
	// 2 环 s1<->x 必须被识别。
	t.Logf("识别环 %d 个：", len(res.Cycles))
	found2 := false
	for _, c := range res.Cycles {
		t.Logf("  环 账户=%v 交易=%v 金额=%d", c.Accounts, c.TxIDs, c.Amount)
		if fmt.Sprint(c.Accounts) == "[s1 x]" {
			found2 = true
		}
	}
	if !found2 {
		t.Fatal("未还原 s1<->x 环形流转")
	}
}

// TestTraceTxAnchor 核对以单笔交易为锚点：钱从哪来、又流向哪。
func TestTraceTxAnchor(t *testing.T) {
	// a->x (anchor t0), x->b, b->c
	recs := []ledger.Record{
		{ID: "u", Timestamp: 1, From: "a", To: "x", Amount: "10"},
		{ID: "t0", Timestamp: 2, From: "x", To: "b", Amount: "10"},
		{ID: "d", Timestamp: 3, From: "b", To: "c", Amount: "10"},
	}
	txs := toTxs(recs)
	res := Trace(txs, Target{Kind: TargetTx, TxID: "t0"}, Limits{})
	if !res.Found || res.AnchorTx == nil || res.AnchorTx.From != "x" || res.AnchorTx.To != "b" {
		t.Fatalf("锚点交易还原错误：%+v", res.AnchorTx)
	}
	// 上游从 x 出发：能追到 a（经 u）。
	upOK, downOK := false, false
	for _, a := range res.Upstream.Reachable {
		if a == "a" {
			upOK = true
		}
	}
	for _, a := range res.Downstream.Reachable {
		if a == "c" {
			downOK = true
		}
	}
	t.Logf("锚点 t0(x->b)：上游可达=%v 下游可达=%v", res.Upstream.Reachable, res.Downstream.Reachable)
	if !upOK || !downOK {
		t.Fatal("交易锚点未正确还原资金来源与去向")
	}
	if res.Upstream.Truncated || res.Downstream.Truncated {
		t.Fatal("不应截断")
	}

	// 不存在的交易：Found=false 且有说明，不报错。
	miss := Trace(txs, Target{Kind: TargetTx, TxID: "nope"}, Limits{})
	if miss.Found || len(miss.Notes) == 0 {
		t.Fatal("不存在的交易应明确标记未找到")
	}
}

// TestTraceVeryLongChain 重点边界：几万节点的真实长链必须完整还原、不漏段、不卡。
func TestTraceVeryLongChain(t *testing.T) {
	const n = 20_000
	recs := make([]ledger.Record, 0, n)
	for i := 0; i < n; i++ {
		recs = append(recs, ledger.Record{
			ID: fmt.Sprintf("h%06d", i), Timestamp: int64(i),
			From: fmt.Sprintf("n%06d", i), To: fmt.Sprintf("n%06d", i+1), Amount: "1",
		})
	}
	txs := toTxs(recs)
	t.Logf("构造 %d 跳无环长链，从中间节点 n%06d 溯源（上下游各约一半）", n, n/2)

	done := make(chan struct{})
	var res Result
	go func() {
		res = Trace(txs, Target{Kind: TargetAccount, Account: fmt.Sprintf("n%06d", n/2)}, Limits{})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("超长链溯源超时（可能卡住）")
	}
	// 上游应覆盖 [n0 .. nMid]，下游覆盖 [nMid .. nN]，均不得截断、不得漏段。
	if res.Upstream.Truncated || res.Downstream.Truncated {
		t.Fatalf("默认上限下 %d 链不应截断：up=%s down=%s",
			n, res.Upstream.LimitName, res.Downstream.LimitName)
	}
	if len(res.Upstream.Reachable) != n/2+1 {
		t.Fatalf("上游应覆盖 %d 个账户，实际 %d", n/2+1, len(res.Upstream.Reachable))
	}
	if len(res.Downstream.Reachable) != n-n/2+1 {
		t.Fatalf("下游应覆盖 %d 个账户，实际 %d", n-n/2+1, len(res.Downstream.Reachable))
	}
	// 端点必须可达（证明没有悄悄少算某一段）。
	firstUp := res.Upstream.Reachable[len(res.Upstream.Reachable)-1]
	lastDown := res.Downstream.Reachable[len(res.Downstream.Reachable)-1]
	if firstUp != "n000000" {
		t.Fatalf("上游应收敛到源头 n000000，实际末端=%s", firstUp)
	}
	if lastDown != fmt.Sprintf("n%06d", n) {
		t.Fatalf("下游应收敛到末端 n%06d，实际=%s", n, lastDown)
	}
	// 全链无环。
	if len(res.Cycles) != 0 {
		t.Fatalf("无环长链不应发现环，实际 %d", len(res.Cycles))
	}
	t.Logf("长链完整：上游 %d 账户/%d 跳，下游 %d 账户/%d 跳，环=0",
		len(res.Upstream.Reachable), len(res.Upstream.Hops),
		len(res.Downstream.Reachable), len(res.Downstream.Hops))
}

// TestTraceLargeRing 重点边界：几万节点的大环必须被正确还原为单个环、不截断、不卡。
func TestTraceLargeRing(t *testing.T) {
	const n = 20_000
	recs := make([]ledger.Record, 0, n)
	for i := 0; i < n; i++ {
		recs = append(recs, ledger.Record{
			ID: fmt.Sprintf("r%06d", i), Timestamp: int64(i),
			From: fmt.Sprintf("c%06d", i), To: fmt.Sprintf("c%06d", (i+1)%n), Amount: "1",
		})
	}
	txs := toTxs(recs)
	t.Logf("构造 %d 节点大环，从 c000000 溯源", n)

	done := make(chan struct{})
	var res Result
	go func() {
		res = Trace(txs, Target{Kind: TargetAccount, Account: "c000000"}, Limits{})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("大环溯源超时")
	}
	if res.Upstream.Truncated || res.Downstream.Truncated {
		t.Fatalf("大环在默认上限下不应截断：up=%s down=%s",
			res.Upstream.LimitName, res.Downstream.LimitName)
	}
	if len(res.Cycles) != 1 {
		t.Fatalf("大环应恰好识别为 1 个环，实际 %d（截断=%v）", len(res.Cycles), res.CyclesTruncated)
	}
	c := res.Cycles[0]
	if len(c.Accounts) != n || len(c.Hops) != n {
		t.Fatalf("环规模错误：账户=%d 跳=%d，期望 %d", len(c.Accounts), len(c.Hops), n)
	}
	if c.Accounts[0] != "c000000" {
		t.Fatalf("环起点应为字典序最小账户 c000000，实际 %s", c.Accounts[0])
	}
	if c.Amount != int64(n) {
		t.Fatalf("环金额合计应为 %d，实际 %d", n, c.Amount)
	}
	t.Logf("大环完整还原：%d 账户/%d 跳，金额合计=%d", len(c.Accounts), len(c.Hops), c.Amount)
}

// TestTraceTruncationExplicit 核对触发上限时必须明确告知截断与边界，
// 不能让调用方误以为拿到完整链路。
func TestTraceTruncationExplicit(t *testing.T) {
	const n = 100
	recs := make([]ledger.Record, 0, n)
	for i := 0; i < n; i++ {
		recs = append(recs, ledger.Record{
			ID: fmt.Sprintf("h%03d", i), Timestamp: int64(i),
			From: fmt.Sprintf("n%03d", i), To: fmt.Sprintf("n%03d", i+1), Amount: "1",
		})
	}
	txs := toTxs(recs)
	// 下游只允许访问 10 个节点（含锚点），链还有更长 -> 必须截断并给出边界。
	res := Trace(txs, Target{Kind: TargetAccount, Account: "n000"}, Limits{MaxNodes: 10, MaxHops: 10})
	if !res.Downstream.Truncated {
		t.Fatal("下游应被节点上限截断")
	}
	if res.Downstream.LimitName != "nodes" {
		t.Fatalf("截断原因应为 nodes，实际 %s", res.Downstream.LimitName)
	}
	if len(res.Downstream.Frontier) == 0 {
		t.Fatal("截断时必须给出未能继续展开的边界账户")
	}
	if len(res.Notes) == 0 {
		t.Fatal("截断必须在 Notes 中明确说明")
	}
	t.Logf("截断已显式标记：limit=%s frontier=%v notes=%v",
		res.Downstream.LimitName, res.Downstream.Frontier, res.Notes)
}

// TestTraceDeterministicAndOrderIndependent 核对结论与输入边序无关、重复运行一致。
func TestTraceDeterministicAndOrderIndependent(t *testing.T) {
	recs := []ledger.Record{
		{ID: "a", Timestamp: 3, From: "u", To: "x", Amount: "1"},
		{ID: "b", Timestamp: 1, From: "v", To: "x", Amount: "1"},
		{ID: "c", Timestamp: 2, From: "x", To: "w", Amount: "1"},
		{ID: "d", Timestamp: 4, From: "w", To: "x", Amount: "1"}, // 环 x->w->x
	}
	r1 := Trace(toTxs(recs), Target{Kind: TargetAccount, Account: "x"}, Limits{})
	r2 := Trace(toTxs([]ledger.Record{recs[3], recs[1], recs[2], recs[0]}),
		Target{Kind: TargetAccount, Account: "x"}, Limits{})
	if fmt.Sprint(r1) != fmt.Sprint(r2) {
		t.Fatal("不同输入边序产生了不同溯源结论")
	}
	r3 := Trace(toTxs(recs), Target{Kind: TargetAccount, Account: "x"}, Limits{})
	if fmt.Sprint(r1) != fmt.Sprint(r3) {
		t.Fatal("重复运行溯源结论不一致")
	}
	t.Logf("溯源确定且与输入顺序无关：环=%v 上游=%v 下游=%v",
		r1.Cycles[0].Accounts, r1.Upstream.Reachable, r1.Downstream.Reachable)
}

// TestTraceMultipleCyclesCorrect 核对含多个不同规模环（含弦/共享节点）时
// 每个简单环恰好枚举一次，不漏不重。
func TestTraceMultipleCyclesCorrect(t *testing.T) {
	// 两个不相交环：a->b->c->a（3环）与 p->q->p（2环）；
	// 另有一个带弦的 4 节点图：d->e->f->g->d 加弦 d->f，
	// 产生两个简单环 [d e f g]（外框）与 [d f g]（走弦）。
	recs := []ledger.Record{
		{ID: "1", Timestamp: 1, From: "a", To: "b", Amount: "1"},
		{ID: "2", Timestamp: 2, From: "b", To: "c", Amount: "1"},
		{ID: "3", Timestamp: 3, From: "c", To: "a", Amount: "1"},
		{ID: "4", Timestamp: 4, From: "p", To: "q", Amount: "1"},
		{ID: "5", Timestamp: 5, From: "q", To: "p", Amount: "1"},
		{ID: "6", Timestamp: 6, From: "d", To: "e", Amount: "1"},
		{ID: "7", Timestamp: 7, From: "e", To: "f", Amount: "1"},
		{ID: "8", Timestamp: 8, From: "f", To: "g", Amount: "1"},
		{ID: "9", Timestamp: 9, From: "g", To: "d", Amount: "1"},
		{ID: "10", Timestamp: 10, From: "d", To: "f", Amount: "1"}, // 弦
	}
	txs := toTxs(recs)
	// 分别锚定两个连通块统计，再用一个能覆盖全部的方式：直接对 d 与 a 各溯源，
	// 并验证带弦图枚举出两个简单环。
	resD := Trace(txs, Target{Kind: TargetAccount, Account: "d"}, Limits{})
	gotD := map[string]bool{}
	for _, c := range resD.Cycles {
		gotD[fmt.Sprint(c.Accounts)] = true
		t.Logf("d 连通块环：%v tx=%v", c.Accounts, c.TxIDs)
	}
	if !gotD["[d e f g]"] || !gotD["[d f g]"] {
		t.Fatalf("带弦图应枚举出 [d e f g] 与 [d f g] 两个环，实际 %v", gotD)
	}
	if len(gotD) != 2 {
		t.Fatalf("d 连通块应恰好 2 个简单环，实际 %d：%v", len(gotD), gotD)
	}
	resA := Trace(txs, Target{Kind: TargetAccount, Account: "a"}, Limits{})
	if len(resA.Cycles) != 1 || fmt.Sprint(resA.Cycles[0].Accounts) != "[a b c]" {
		t.Fatalf("a 连通块应只有 3 环，实际 %+v", resA.Cycles)
	}
	// 锚点在环上且环可达 2 环连通块时不串台：p 块单独核对。
	resP := Trace(txs, Target{Kind: TargetAccount, Account: "p"}, Limits{})
	if len(resP.Cycles) != 1 || fmt.Sprint(resP.Cycles[0].Accounts) != "[p q]" {
		t.Fatalf("p 连通块应只有 2 环，实际 %+v", resP.Cycles)
	}
	t.Log("多环（含带弦图）枚举正确：每个简单环恰好一次")
}
