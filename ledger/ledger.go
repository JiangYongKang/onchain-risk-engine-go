package ledger

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
)

// Ledger 是线程安全的幂等账本存储。
type Ledger struct {
	mu    sync.RWMutex
	byID  map[string]Transfer
	order []string // 首次写入顺序，仅用于调试
}

// New 创建空账本。
func New() *Ledger {
	return &Ledger{byID: make(map[string]Transfer)}
}

// getString 提取字符串字段，区分缺失与类型非法。
func getString(rec RawRecord, key string) (string, RejectReason, bool) {
	v, ok := rec[key]
	if !ok || v == nil {
		return "", RejectMissingField, false
	}
	s, ok := v.(string)
	if !ok {
		return "", RejectInvalidType, false
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "", RejectMissingField, false
	}
	return s, "", true
}

// getInt 提取整数字段，容忍 JSON 数字（float64）但要求整数值。
func getInt(rec RawRecord, key string) (int64, RejectReason, bool) {
	v, ok := rec[key]
	if !ok || v == nil {
		return 0, RejectMissingField, false
	}
	switch n := v.(type) {
	case int64:
		return n, "", true
	case int:
		return int64(n), "", true
	case float64:
		if n != math.Trunc(n) || math.Abs(n) > math.MaxInt64 {
			return 0, RejectInvalidType, false
		}
		return int64(n), "", true
	default:
		return 0, RejectInvalidType, false
	}
}

// Normalize 把原始记录规范化为 Transfer，失败时给出可区分的拒绝原因。
func Normalize(rec RawRecord) (Transfer, Result) {
	var t Transfer
	fail := func(reason RejectReason, detail string) (Transfer, Result) {
		return Transfer{}, Result{TxID: t.TxID, Outcome: OutcomeRejected, Reason: reason, Detail: detail}
	}

	id, reason, ok := getString(rec, "tx_id")
	if !ok {
		return fail(reason, "field tx_id")
	}
	t.TxID = id
	if len(id) > 128 || strings.ContainsAny(id, " \t\n") {
		return fail(RejectInvalidID, "tx_id contains whitespace or exceeds 128 chars")
	}

	from, reason, ok := getString(rec, "from")
	if !ok {
		return fail(reason, "field from")
	}
	t.From = from

	to, reason, ok := getString(rec, "to")
	if !ok {
		return fail(reason, "field to")
	}
	t.To = to

	amount, reason, ok := getInt(rec, "amount")
	if !ok {
		return fail(reason, "field amount")
	}
	if amount <= 0 {
		return fail(RejectInvalidAmount, fmt.Sprintf("amount must be positive, got %d", amount))
	}
	t.Amount = amount

	ts, reason, ok := getInt(rec, "tx_time")
	if !ok {
		return fail(reason, "field tx_time")
	}
	if ts < 0 {
		return fail(RejectInvalidType, "tx_time must be non-negative")
	}
	t.TxTime = ts

	return t, Result{}
}

// Ingest 批量摄取原始记录，逐条返回结果。重复与乱序安全。
func (l *Ledger) Ingest(records ...RawRecord) []Result {
	results := make([]Result, 0, len(records))
	for _, rec := range records {
		t, res := Normalize(rec)
		if res.Outcome == OutcomeRejected {
			results = append(results, res)
			continue
		}
		results = append(results, l.IngestOne(t))
	}
	return results
}

// IngestOne 摄取单条规范化记录。重复 TxID 时校验内容一致性：
// 内容相同记为 duplicate（状态不变），内容冲突记为 rejected。
func (l *Ledger) IngestOne(t Transfer) Result {
	l.mu.Lock()
	defer l.mu.Unlock()
	if existing, ok := l.byID[t.TxID]; ok {
		if existing == t {
			return Result{TxID: t.TxID, Outcome: OutcomeDuplicate, Detail: "identical record already stored"}
		}
		return Result{TxID: t.TxID, Outcome: OutcomeRejected, Reason: RejectInvalidID,
			Detail: "conflicting record for existing tx_id"}
	}
	l.byID[t.TxID] = t
	l.order = append(l.order, t.TxID)
	return Result{TxID: t.TxID, Outcome: OutcomeApplied}
}

// Snapshot 返回按 TxID 排序的确定性快照，供分析使用。
func (l *Ledger) Snapshot() []Transfer {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]Transfer, 0, len(l.byID))
	for _, t := range l.byID {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TxID < out[j].TxID })
	return out
}

// Len 返回当前账本中的交易数。
func (l *Ledger) Len() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.byID)
}
