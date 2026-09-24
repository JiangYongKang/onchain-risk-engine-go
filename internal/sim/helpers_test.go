package sim

import (
	"testing"

	"github.com/highcumontoa/onchain-risk-engine-go/internal/ledger"
)

// txsToLedger 把一组交易重新灌入新账本（仅测试辅助，用于验证回放一致性）。
func txsToLedger(t *testing.T, txs []ledger.Tx) *ledger.Ledger {
	t.Helper()
	l := ledger.New()
	recs := make([]ledger.Record, len(txs))
	for i, x := range txs {
		recs[i] = ledger.Record{
			ID: x.ID, Timestamp: x.Timestamp, From: x.From, To: x.To,
			Amount: intToString(x.Amount),
		}
	}
	res := l.Ingest(recs)
	if len(res.Rejected) != 0 {
		t.Fatalf("回放重灌出现拒绝：%+v", res.Rejected)
	}
	return l
}

func intToString(v int64) string {
	// 避免引入 strconv 到多个文件；简单实现。
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [24]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
