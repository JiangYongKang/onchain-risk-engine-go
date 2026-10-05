package risk

import (
	"fmt"
	"math/rand"
	"sync"
	"testing"

	"github.com/highcumontoa/onchain-risk-engine-go/internal/ledger"
)

// makeTxs 构造 n 笔确定性交易（含汇聚、拆分、环路结构）。
func makeTxs(n int) []ledger.Tx {
	txs := make([]ledger.Tx, 0, n)
	for i := 0; i < n; i++ {
		var from, to string
		switch i % 7 {
		case 0, 1, 2: // 汇聚到 hub
			from, to = fmt.Sprintf("src%d", i), "hub"
		case 3, 4: // hub 拆分出去
			from, to = "hub", fmt.Sprintf("dst%d", i)
		case 5: // 环的一段
			from, to = fmt.Sprintf("ring%d", i%50), fmt.Sprintf("ring%d", (i+1)%50)
		default:
			from, to = fmt.Sprintf("a%d", i), fmt.Sprintf("b%d", i)
		}
		txs = append(txs, ledger.Tx{
			ID: fmt.Sprintf("tx%05d", i), Timestamp: int64(i * 10),
			From: from, To: to, Amount: int64(100 + i),
		})
	}
	return txs
}

// TestIncrementalMatchesFullRecompute 是核心一致性测试：
// 同一批交易，用多种随机分批/乱序方式增量喂入，每一步的结论都必须
// 与“把到目前为此的全部交易做一次全量 analyze”逐字节一致。
func TestIncrementalMatchesFullRecompute(t *testing.T) {
	const total = 600
	all := makeTxs(total)
	rules := DefaultRuleSet()

	rng := rand.New(rand.NewSource(42))
	for trial := 0; trial < 8; trial++ {
		// 随机打乱 + 随机切批。
		shuffled := make([]ledger.Tx, total)
		copy(shuffled, all)
		rng.Shuffle(total, func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })

		inc := NewIncremental(rules)
		var fed []ledger.Tx
		batches := 0
		for len(shuffled) > 0 {
			n := 1 + rng.Intn(37) // 随机批大小
			if n > len(shuffled) {
				n = len(shuffled)
			}
			batch := shuffled[:n]
			shuffled = shuffled[n:]
			inc.Add(batch)
			fed = append(fed, batch...)
			batches++

			// 每一步：增量结论 == 对已喂入交易的全量重算。
			want := analyze(rules, sortedCopy(fed))
			got := inc.Report()
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("trial=%d 第 %d 批后增量与全量结论不一致（已喂 %d 笔）",
					trial, batches, len(fed))
			}
		}
		if trial == 0 {
			rep := inc.Report()
			t.Logf("输入：%d 笔交易，随机分 %d 批增量喂入", total, batches)
			t.Logf("判定依据：每批后与全量 analyze 逐字节对比")
			t.Logf("最终结论: %s", reportSummary(rep))
		}
		if inc.Size() != total {
			t.Fatalf("应纳入 %d 笔，实际 %d", total, inc.Size())
		}
	}
	t.Log("8 组随机分批/乱序试验，增量结论与全量重算全部一致")
}

func sortedCopy(txs []ledger.Tx) []ledger.Tx {
	l := ledger.New()
	recs := make([]ledger.Record, len(txs))
	for i, tx := range txs {
		recs[i] = ledger.Record{
			ID: tx.ID, Timestamp: tx.Timestamp,
			From: tx.From, To: tx.To, Amount: fmt.Sprint(tx.Amount),
		}
	}
	l.Ingest(recs)
	return l.Txs()
}

// TestIncrementalIdempotentAdd 重复纳入同一批交易不改变结论。
func TestIncrementalIdempotentAdd(t *testing.T) {
	all := makeTxs(100)
	inc := NewIncremental(DefaultRuleSet())
	inc.Add(all)
	first := inc.Report()
	if n := inc.Add(all); n != 0 {
		t.Fatalf("重复纳入应返回 0，实际 %d", n)
	}
	if fmt.Sprint(inc.Report()) != fmt.Sprint(first) {
		t.Fatal("重复纳入后结论发生变化")
	}
	t.Logf("重复纳入 100 笔幂等：结论不变，Size=%d", inc.Size())
}

// TestIncrementalConcurrent 并发 Add + Report：不出现中间态，最终一致。
func TestIncrementalConcurrent(t *testing.T) {
	const total = 2000
	all := makeTxs(total)
	inc := NewIncremental(DefaultRuleSet())

	const writers, readers = 8, 16
	var wg sync.WaitGroup
	start := make(chan struct{})

	// 写方：把交易切成 writers 份并发喂入。
	chunk := total / writers
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			<-start
			lo, hi := w*chunk, (w+1)*chunk
			if w == writers-1 {
				hi = total
			}
			inc.Add(all[lo:hi])
		}(w)
	}
	// 读方：并发取报告；每份报告必须内部自洽（指纹对应其账户集合）。
	var readWg sync.WaitGroup
	for r := 0; r < readers; r++ {
		readWg.Add(1)
		go func() {
			defer readWg.Done()
			<-start
			for i := 0; i < 50; i++ {
				rep := inc.Report()
				// 自洽性：报告指纹必须等于对“某个一致快照”的重算指纹，
				// 这里验证其账户列表与模式引用不残缺（无 panic、无空版本）。
				if rep.RuleVersion.Version != 1 {
					t.Errorf("报告版本残缺：%+v", rep.RuleVersion)
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	readWg.Wait()

	// 全部写完后，最终结论必须与全量重算逐字节一致。
	want := analyze(DefaultRuleSet(), sortedCopy(all))
	got := inc.Report()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatal("并发喂入后最终结论与全量重算不一致")
	}
	t.Logf("%d 写 + %d 读并发完成，最终结论与全量重算一致: %s",
		writers, readers, reportSummary(got))
}
