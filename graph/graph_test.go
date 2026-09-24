package graph

import (
	"fmt"
	"sync"
	"testing"

	"github.com/highcumontoa/onchain-risk-engine-go/ledger"
)

func tx(id, from, to string, amount, ts int64) ledger.Transfer {
	return ledger.Transfer{TxID: id, From: from, To: to, Amount: amount, TxTime: ts}
}

func testThresholds() Thresholds {
	return Thresholds{FanInMinSources: 3, FanOutMinTargets: 3, ConcentrationShare: 50, MinCycleVolume: 1, MaxNodes: 100000}
}

func logFindings(t *testing.T, findings []Finding) {
	t.Helper()
	for _, f := range findings {
		t.Logf("finding kind=%s accounts=%v volume=%d detail=%s", f.Kind, f.Accounts, f.Volume, f.Detail)
	}
}

func TestFanIn(t *testing.T) {
	txs := []ledger.Transfer{
		tx("t1", "s1", "hub", 10, 1), tx("t2", "s2", "hub", 20, 2), tx("t3", "s3", "hub", 30, 3),
	}
	findings := Analyze(Build(txs), testThresholds())
	t.Logf("input=%v", txs)
	logFindings(t, findings)
	var fanIn *Finding
	for i, f := range findings {
		if f.Kind == PatternFanIn {
			fanIn = &findings[i]
		}
	}
	if fanIn == nil {
		t.Fatal("expected fan_in finding")
	}
	if fanIn.Accounts[0] != "hub" || fanIn.Volume != 60 {
		t.Fatalf("unexpected fan_in finding: %+v", fanIn)
	}
}

func TestFanOut(t *testing.T) {
	txs := []ledger.Transfer{
		tx("t1", "src", "d1", 10, 1), tx("t2", "src", "d2", 10, 2), tx("t3", "src", "d3", 10, 3),
	}
	findings := Analyze(Build(txs), testThresholds())
	t.Logf("input=%v", txs)
	logFindings(t, findings)
	found := false
	for _, f := range findings {
		if f.Kind == PatternFanOut && f.Accounts[0] == "src" && f.Volume == 30 {
			found = true
		}
	}
	if !found {
		t.Fatal("expected fan_out finding for src")
	}
}

func TestCycle(t *testing.T) {
	txs := []ledger.Transfer{
		tx("t1", "a", "b", 10, 1), tx("t2", "b", "c", 10, 2), tx("t3", "c", "a", 10, 3),
		tx("t4", "x", "y", 5, 4), // 非环，不应误报
	}
	findings := Analyze(Build(txs), testThresholds())
	t.Logf("input=%v", txs)
	logFindings(t, findings)
	cycles := 0
	for _, f := range findings {
		if f.Kind == PatternCycle {
			cycles++
			if len(f.Accounts) != 3 || f.Volume != 30 {
				t.Fatalf("unexpected cycle finding: %+v", f)
			}
		}
	}
	if cycles != 1 {
		t.Fatalf("expected exactly 1 cycle, got %d", cycles)
	}
}

func TestSelfLoop(t *testing.T) {
	txs := []ledger.Transfer{tx("t1", "a", "a", 10, 1)}
	findings := Analyze(Build(txs), testThresholds())
	t.Logf("input=%v", txs)
	logFindings(t, findings)
	found := false
	for _, f := range findings {
		if f.Kind == PatternCycle {
			found = true
		}
	}
	if !found {
		t.Fatal("expected self-loop to be detected as cycle")
	}
}

func TestConcentration(t *testing.T) {
	txs := []ledger.Transfer{
		tx("t1", "a", "whale", 90, 1), tx("t2", "b", "c", 10, 2),
	}
	findings := Analyze(Build(txs), testThresholds())
	t.Logf("input=%v", txs)
	logFindings(t, findings)
	found := false
	for _, f := range findings {
		if f.Kind == PatternConcentration && f.Accounts[0] == "whale" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected concentration finding for whale")
	}
}

func TestDeepChainNoCrash(t *testing.T) {
	// 10 万级深链：递归实现会爆栈，迭代实现必须安全完成。
	const depth = 100000
	txs := make([]ledger.Transfer, depth)
	for i := 0; i < depth; i++ {
		txs[i] = tx(fmt.Sprintf("d%d", i), fmt.Sprintf("n%d", i), fmt.Sprintf("n%d", i+1), 1, int64(i))
	}
	th := testThresholds()
	th.MaxNodes = 0 // 不限制规模，验证迭代算法本身不爆栈
	findings := Analyze(Build(txs), th)
	t.Logf("deep chain depth=%d findings=%d", depth, len(findings))
	for _, f := range findings {
		if f.Kind == PatternCycle {
			t.Fatalf("deep chain falsely reported cycle: %+v", f)
		}
	}
}

func TestLargeCycleNoHang(t *testing.T) {
	// 大环：必须终止且恰好报一个环。
	const n = 50000
	txs := make([]ledger.Transfer, n)
	for i := 0; i < n; i++ {
		txs[i] = tx(fmt.Sprintf("c%d", i), fmt.Sprintf("m%d", i), fmt.Sprintf("m%d", (i+1)%n), 1, int64(i))
	}
	findings := Analyze(Build(txs), testThresholds())
	cycles := 0
	for _, f := range findings {
		if f.Kind == PatternCycle {
			cycles++
		}
	}
	t.Logf("large cycle n=%d cycles=%d", n, cycles)
	if cycles != 1 {
		t.Fatalf("expected 1 cycle, got %d", cycles)
	}
}

func TestMaxNodesSafeguard(t *testing.T) {
	txs := []ledger.Transfer{
		tx("t1", "a", "b", 10, 1), tx("t2", "b", "a", 10, 2),
	}
	th := testThresholds()
	th.MaxNodes = 1
	findings := Analyze(Build(txs), th)
	t.Logf("safeguard findings:")
	logFindings(t, findings)
	cycles := 0
	for _, f := range findings {
		if f.Kind == PatternCycle {
			cycles++
			if len(f.Accounts) != 0 {
				t.Fatalf("expected safeguard finding without accounts, got %+v", f)
			}
		}
	}
	if cycles != 1 {
		t.Fatalf("expected exactly 1 safeguard cycle finding, got %d", cycles)
	}
}

func TestConcurrentDeterministic(t *testing.T) {
	// 构造混合模式数据，乱序摄取后并发分析，结论必须逐字节一致。
	l := ledger.New()
	var raw []ledger.RawRecord
	for i := 0; i < 200; i++ {
		raw = append(raw, ledger.RawRecord{
			"tx_id": fmt.Sprintf("tx%03d", i), "from": fmt.Sprintf("src%d", i%7),
			"to": fmt.Sprintf("dst%d", i%5), "amount": int64(i + 1), "tx_time": int64(i),
		})
	}
	// 环路
	raw = append(raw,
		ledger.RawRecord{"tx_id": "c1", "from": "dst0", "to": "dst1", "amount": 5, "tx_time": 300},
		ledger.RawRecord{"tx_id": "c2", "from": "dst1", "to": "dst0", "amount": 5, "tx_time": 301},
	)
	// 乱序 + 重复投递
	for i := len(raw) - 1; i >= 0; i -= 3 {
		l.Ingest(raw[i])
	}
	l.Ingest(raw...)
	snap := l.Snapshot()

	w := Weights{FanIn: 20, FanOut: 20, Cycle: 40, Concentration: 30}
	reference := Score(Analyze(Build(snap), testThresholds()), w)
	t.Logf("reference score=%d level=%s findings=%d", reference.Score, reference.Level, len(reference.Findings))
	logFindings(t, reference.Findings)

	const workers = 32
	results := make([]Report, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = Score(Analyze(Build(snap), testThresholds()), w)
		}(i)
	}
	wg.Wait()
	for i, r := range results {
		if r.Score != reference.Score || r.Level != reference.Level ||
			fmt.Sprint(r.Findings) != fmt.Sprint(reference.Findings) ||
			fmt.Sprint(r.Accounts) != fmt.Sprint(reference.Accounts) {
			t.Fatalf("worker %d got divergent result: %+v vs reference %+v", i, r, reference)
		}
	}
	t.Logf("conclusion: %d concurrent analyses identical, score=%d level=%s", workers, reference.Score, reference.Level)
}
