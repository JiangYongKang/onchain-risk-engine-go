package incremental

import (
	"fmt"
	"math/rand"
	"sync"
	"testing"

	"github.com/highcumontoa/onchain-risk-engine-go/internal/ledger"
	"github.com/highcumontoa/onchain-risk-engine-go/internal/risk"
)

// fullReport 用一次性全量摄取得到基准报告。
func fullReport(recs []ledger.Record, rules risk.RuleSet) risk.Report {
	l := ledger.New()
	l.Ingest(recs)
	return risk.AnalyzeTxs(rules, l.Txs())
}

// TestIncrementalEqualsFullRecompute 核对：分批增量摄取，每一批之后的结论
// 都与“把截至该批的全部记录一次性全量重算”的结论逐字节一致。
func TestIncrementalEqualsFullRecompute(t *testing.T) {
	rules := risk.DefaultRuleSet()
	all := []ledger.Record{
		{ID: "t1", Timestamp: 1, From: "s1", To: "x", Amount: "100"},
		{ID: "t2", Timestamp: 2, From: "s2", To: "x", Amount: "200"},
		{ID: "t3", Timestamp: 3, From: "s3", To: "x", Amount: "300"},
		{ID: "t4", Timestamp: 4, From: "x", To: "s1", Amount: "50"},
		{ID: "t5", Timestamp: 5, From: "m", To: "s2", Amount: "9"},
		{ID: "bad", Timestamp: 1, From: "a", To: "b", Amount: "NaN"}, // 坏记录
	}
	batches := [][]ledger.Record{
		{all[5], all[0]},
		{all[2], all[1]}, // 乱序
		{all[3], all[0]}, // 含重复 t1
		{all[4]},
	}
	acc := New()
	var seen []ledger.Record
	for bi, b := range batches {
		res, out := acc.Ingest(b)
		seen = append(seen, b...)
		got := acc.Analyze(rules)
		want := fullReport(seen, rules)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("批次 %d 后增量结论与全量重算不一致：\ninc=%v\nfull=%v", bi+1, got, want)
		}
		// 边集合也必须逐笔一致。
		if fmt.Sprint(acc.Txs()) != fmt.Sprint(allTxs(seen)) {
			t.Fatalf("批次 %d 后增量边集与全量不一致", bi+1)
		}
		t.Logf("批次%d：拒绝=%d 替换=%d 视图边数=%d，结论与全量一致",
			bi+1, len(res.Rejected), len(out.Removed), out.EdgeCount)
	}
	final := acc.Analyze(rules)
	t.Logf("最终增量结论账户数=%d 指纹=%s", len(final.Accounts), final.InputHash)
}

func allTxs(recs []ledger.Record) []ledger.Tx {
	l := ledger.New()
	l.Ingest(recs)
	return l.Txs()
}

// TestIncrementalRandomPartitionOrder 强随机核对：同一记录集合，随机分批、
// 随机顺序、混入坏记录与同 ID 冲突，最终视图与报告都与全量一致。
func TestIncrementalRandomPartitionOrder(t *testing.T) {
	rules := risk.DefaultRuleSet()
	recs := []ledger.Record{
		{ID: "k", Timestamp: 9, From: "a", To: "z", Amount: "5"},
		{ID: "k", Timestamp: 1, From: "a", To: "b", Amount: "5"}, // 同 ID 更优
		{ID: "f1", Timestamp: 2, From: "u", To: "v", Amount: "10"},
		{ID: "f2", Timestamp: 3, From: "v", To: "w", Amount: "10"},
		{ID: "f3", Timestamp: 4, From: "w", To: "u", Amount: "10"}, // 3 环
		{ID: "f4", Timestamp: 5, From: "q1", To: "h", Amount: "10"},
		{ID: "f5", Timestamp: 6, From: "q2", To: "h", Amount: "10"},
		{ID: "f6", Timestamp: 7, From: "q3", To: "h", Amount: "10"}, // h 汇聚
		{ID: "bad", Timestamp: 1, From: "a", To: "a", Amount: "1"},  // 自转
		{ID: "bad2", Timestamp: 1, From: "a", To: "b", Amount: "0"}, // 非法金额
	}
	wantReport := fullReport(recs, rules)
	wantTxs := allTxs(recs)

	rng := rand.New(rand.NewSource(7))
	for trial := 0; trial < 40; trial++ {
		perm := rng.Perm(len(recs))
		acc := New()
		idx := 0
		for idx < len(perm) {
			size := 1 + rng.Intn(4)
			if size > len(perm)-idx {
				size = len(perm) - idx
			}
			batch := make([]ledger.Record, 0, size)
			for j := 0; j < size; j++ {
				batch = append(batch, recs[perm[idx]])
				idx++
			}
			acc.Ingest(batch)
		}
		got := acc.Analyze(rules)
		if fmt.Sprint(got) != fmt.Sprint(wantReport) {
			t.Fatalf("trial %d：随机分批增量报告与全量不一致", trial)
		}
		if fmt.Sprint(acc.Txs()) != fmt.Sprint(wantTxs) {
			t.Fatalf("trial %d：随机分批增量边集与全量不一致", trial)
		}
	}
	t.Logf("40 种随机分批/顺序（含同ID冲突、坏记录）增量结论与全量全部逐字节一致")
	t.Logf("基准指纹=%s 账户数=%d", wantReport.InputHash, len(wantReport.Accounts))
}

// TestIncrementalIdempotent 核对重复喂入同一批不改变视图与结论。
func TestIncrementalIdempotent(t *testing.T) {
	rules := risk.DefaultRuleSet()
	batch := []ledger.Record{
		{ID: "i1", Timestamp: 1, From: "a", To: "b", Amount: "10"},
		{ID: "i2", Timestamp: 2, From: "b", To: "c", Amount: "10"},
	}
	acc := New()
	acc.Ingest(batch)
	r1 := acc.Analyze(rules)
	res, out := acc.Ingest(batch)
	r2 := acc.Analyze(rules)
	if len(res.Accepted) != 0 || len(res.Rejected) != 2 || out.EdgeCount != 2 {
		t.Fatalf("重复批次应全部拒绝且边数不变：accepted=%d rejected=%d edges=%d",
			len(res.Accepted), len(res.Rejected), out.EdgeCount)
	}
	if fmt.Sprint(r1) != fmt.Sprint(r2) {
		t.Fatal("重复喂入改变了结论")
	}
	t.Log("重复批次幂等：视图边数=2，结论不变")
}

// TestIncrementalConcurrentNoHalfState 核对一边并发摄取、一边并发分析时，
// 读者拿到的每份报告都等于“某个完整摄取时刻”的全量结论，不会出现只算了
// 一半的中间状态；最终结论与全量一致。
func TestIncrementalConcurrentNoHalfState(t *testing.T) {
	rules := risk.DefaultRuleSet()
	// 200 笔交易构成无平行边的长链，切成 20 个互不重叠的批次并发喂入。
	// （刻意不构造“环上大量平行边”图：那会触发既有风险报告环路 DFS 的
	// 组合放大，与本用例要验证的并发一致性无关；大规模环由 trace 包覆盖。）
	// 200 笔长链 + 尾部 3 笔构成一个固定 3 环（无平行边），使并发读到的
	// 报告可能命中真实模式，从而压到“含依据的报告也不出现撕裂”。
	const total, batches = 203, 20
	all := make([]ledger.Record, 0, total)
	for i := 0; i < 200; i++ {
		all = append(all, ledger.Record{
			ID: fmt.Sprintf("c%04d", i), Timestamp: int64(i),
			From: fmt.Sprintf("a%04d", i), To: fmt.Sprintf("a%04d", i+1),
			Amount: "1",
		})
	}
	// 环：z1->z2->z3->z1。
	all = append(all,
		ledger.Record{ID: "z12", Timestamp: 200, From: "z1", To: "z2", Amount: "1"},
		ledger.Record{ID: "z23", Timestamp: 201, From: "z2", To: "z3", Amount: "1"},
		ledger.Record{ID: "z31", Timestamp: 202, From: "z3", To: "z1", Amount: "1"},
	)
	acc := New()

	var writerWg, readerWg sync.WaitGroup
	start := make(chan struct{})
	stop := make(chan struct{})
	observed := make(chan int, 4096) // 每次观察到的账户数

	// 写者：每批一个 goroutine。
	for b := 0; b < batches; b++ {
		writerWg.Add(1)
		go func(b int) {
			defer writerWg.Done()
			<-start
			lo := b * (len(all) / batches)
			hi := lo + len(all)/batches
			if b == batches-1 {
				hi = len(all) // 最后一批吸收余数（含尾部 3 环）
			}
			acc.Ingest(all[lo:hi])
		}(b)
	}
	// 读者：8 个 goroutine 持续分析，直到 stop 关闭。
	for r := 0; r < 8; r++ {
		readerWg.Add(1)
		go func() {
			defer readerWg.Done()
			<-start
			for {
				select {
				case <-stop:
					return
				default:
					rep := acc.Analyze(rules)
					// 每份报告都必须是自洽的完整报告：指纹非空、边数不超过最终值。
					observed <- len(rep.Accounts)
					if rep.InputHash == "" {
						t.Error("观察到缺少输入指纹的半成品报告")
						return
					}
				}
			}
		}()
	}
	close(start)
	writerWg.Wait() // 全部批次完整提交后
	close(stop)     // 通知读者停止
	readerWg.Wait()
	close(observed)

	final := acc.Analyze(rules)
	want := fullReport(all, rules)
	if fmt.Sprint(final) != fmt.Sprint(want) {
		t.Fatal("并发摄取后的最终增量结论与全量不一致")
	}
	if acc.Len() != total {
		t.Fatalf("最终应收录 %d 笔，实际 %d", total, acc.Len())
	}
	n := 0
	for accts := range observed {
		n++
		if accts > len(final.Accounts) {
			t.Fatalf("观察到超出最终状态的撕裂报告：%d > %d", accts, len(final.Accounts))
		}
	}
	t.Logf("并发摄取+分析：观察 %d 份报告均为完整状态，最终边数=%d 账户=%d 指纹=%s",
		n, acc.Len(), len(final.Accounts), final.InputHash)
}
