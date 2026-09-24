package e2e

import (
	"fmt"
	"sync"
	"testing"

	"github.com/highcumontoa/onchain-risk-engine-go/internal/ledger"
	"github.com/highcumontoa/onchain-risk-engine-go/internal/risk"
	"github.com/highcumontoa/onchain-risk-engine-go/internal/sim"
)

// TestEndToEnd 串联：乱序/重复摄取 -> 并发确定性分析 -> 资金模拟 -> 规则更新与回放。
func TestEndToEnd(t *testing.T) {
	// 1) 乱序 + 重复 + 异常混合输入，分三批增量投递。
	l := ledger.New()
	batches := [][]ledger.Record{
		{ // 第一批：环路交易的后半段先到（乱序）
			{ID: "t3", Timestamp: 3, From: "c", To: "a", Amount: "30"},
			{ID: "t1", Timestamp: 1, From: "a", To: "b", Amount: "10"},
			{ID: "bad1", Timestamp: 1, From: "a", To: "b", Amount: "NaN"},
		},
		{ // 第二批：汇聚到 c 的另外两笔 + 重复的 t1
			{ID: "t4", Timestamp: 4, From: "x", To: "c", Amount: "40"},
			{ID: "t5", Timestamp: 5, From: "y", To: "c", Amount: "50"},
			{ID: "t1", Timestamp: 1, From: "a", To: "b", Amount: "10"},
		},
		{ // 第三批：补齐环路 a->b->c->a
			{ID: "t2", Timestamp: 2, From: "b", To: "c", Amount: "20"},
			{ID: "", Timestamp: 9, From: "a", To: "b", Amount: "1"},
		},
	}
	t.Log("==== 输入（乱序/重复/异常混合）====")
	var rejected []ledger.RejectedRecord
	for bi, b := range batches {
		res := l.Ingest(b)
		for _, r := range res.Rejected {
			t.Logf("批次%d 拒绝 id=%q 原因=%s", bi+1, r.Record.ID, r.Reason)
		}
		rejected = append(rejected, res.Rejected...)
	}
	reasons := map[ledger.RejectReason]int{}
	for _, r := range rejected {
		reasons[r.Reason]++
	}
	if reasons[ledger.ReasonDuplicate] != 1 ||
		reasons[ledger.ReasonBadAmount] != 1 ||
		reasons[ledger.ReasonMissingID] != 1 {
		t.Fatalf("拒绝原因分布不符合预期：%v", reasons)
	}
	txs := l.Txs()
	t.Logf("规范化后接受 %d 笔，顺序=%v", len(txs), []string{txs[0].ID, txs[1].ID, txs[2].ID, txs[3].ID, txs[4].ID})
	if len(txs) != 5 {
		t.Fatalf("应接受 5 笔，实际 %d", len(txs))
	}

	// 2) 并发分析同一份数据，n 份结论必须逐字节一致。
	eng := risk.NewEngine(risk.DefaultRuleSet())
	const n = 20
	start := make(chan struct{})
	var wg sync.WaitGroup
	reps := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			reps[idx] = fmt.Sprint(eng.Analyze(l))
		}(i)
	}
	close(start)
	wg.Wait()
	for i := 1; i < n; i++ {
		if reps[i] != reps[0] {
			t.Fatalf("并发分析结论不一致：#0 vs #%d", i)
		}
	}
	base := eng.Analyze(l)
	t.Log("==== v1 分析结论（判定依据）====")
	for _, a := range base.Accounts {
		t.Logf("账户=%s 得分=%d 等级=%s", a.Account, a.Score, a.Level)
		for _, p := range a.Patterns {
			t.Logf("   依据: [%s] %s tx=%v", p.Kind, p.Detail, p.TxIDs)
		}
	}

	// 3) 资金模拟：对真实状态做假设性变动，再在模拟图上分析。
	s := sim.New(l)
	if bal, err := s.Transfer("x", "c", 40); err != "" {
		// x 在真实账本中只有出账 40，余额为负（出资方），这里预期失败并记录。
		t.Logf("模拟转账 x->c 被拒绝（%v），符合余额约束", err)
	} else {
		t.Logf("模拟转账后余额=%v", bal)
	}
	g0 := s.Graph()
	if len(g0.Edges()) != 5 {
		t.Fatalf("失败的模拟不应改变图，边数=%d", len(g0.Edges()))
	}

	// 4) 规则更新：v2 关闭金额热点、提高汇聚门槛；同输入得到可复现新结论。
	v2 := risk.DefaultRuleSet()
	v2.Version = risk.RuleVersion{Version: 2, Name: "cycle-focused-v2"}
	v2.FanInSources = 5
	v2.HotspotAmount = 0
	eng.UpdateRules(v2)
	newRep := eng.Analyze(l)
	t.Logf("==== v2 新结论：账户数=%d（v1 为 %d）====", len(newRep.Accounts), len(base.Accounts))

	// 5) 回放 v1，旧结论可追溯、逐字复现。
	old := eng.Replay(risk.DefaultRuleSet(), l)
	if fmt.Sprint(old) != fmt.Sprint(base) {
		t.Fatal("回放 v1 未复现旧结论")
	}
	t.Logf("回放 v1 成功复现旧结论；历史版本数=%d，输入指纹=%s",
		len(eng.History()), base.InputHash)

	// 6) 异常链路：构造 5000 跳无环链喂给图，必须快速安全、零环路。
	big := ledger.New()
	recs := make([]ledger.Record, 0, 5000)
	for i := 0; i < 5000; i++ {
		recs = append(recs, ledger.Record{
			ID: fmt.Sprintf("h%05d", i), Timestamp: int64(i),
			From: fmt.Sprintf("n%05d", i), To: fmt.Sprintf("n%05d", i+1), Amount: "1",
		})
	}
	r := big.Ingest(recs)
	if len(r.Accepted) != 5000 {
		t.Fatalf("超大输入接受数错误：%d", len(r.Accepted))
	}
	sg := sim.New(big).Graph()
	cycles := sg.DetectCycles(16)
	t.Logf("5000 跳无环链：环路数=%d（应为 0），检测安全返回", len(cycles))
	if len(cycles) != 0 {
		t.Fatal("超深无环链被误判")
	}
}
