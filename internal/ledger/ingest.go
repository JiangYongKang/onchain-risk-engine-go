package ledger

import (
	"sort"
	"strconv"
	"sync"
)

// Ledger 是线程安全的本地账本：负责增量摄取、规范化与去重。
//
// 一致性约定：
//   - 记录的最终顺序只由 (Timestamp, ID) 决定，与到达顺序无关，因此乱序投递
//     会被透明地重排后接受，不会改变最终状态。
//   - 同一 ID 重复投递：第一次有效记录被接受，之后一律以 duplicate_id 拒绝，
//     重复处理不改变账本状态（幂等）。
//   - 字段缺失 / 类型非法的记录按具体原因拒绝，绝不静默丢弃。
type Ledger struct {
	mu       sync.RWMutex
	accepted map[string]Tx
	order    []Tx
}

// New 创建空账本。
func New() *Ledger {
	return &Ledger{accepted: make(map[string]Tx)}
}

// validate 对单条记录做规范化校验，返回规范化 Tx 与拒绝原因。
// 原因判定具有固定优先级，保证同一条输入永远得到同一个结论。
func validate(r Record) (Tx, RejectReason, string) {
	if r.ID == "" {
		return Tx{}, ReasonMissingID, "record id is empty"
	}
	if r.From == "" {
		return Tx{}, ReasonMissingFrom, "from account is empty: id=" + r.ID
	}
	if r.To == "" {
		return Tx{}, ReasonMissingTo, "to account is empty: id=" + r.ID
	}
	if r.From == r.To {
		return Tx{}, ReasonSelfTransfer, "from and to are identical: " + r.From
	}
	if r.Timestamp < 0 {
		return Tx{}, ReasonBadTimestamp, "timestamp must be non-negative"
	}
	if r.Amount == "" {
		return Tx{}, ReasonBadAmount, "amount is empty: id=" + r.ID
	}
	amount, err := strconv.ParseInt(r.Amount, 10, 64)
	if err != nil {
		return Tx{}, ReasonBadAmount, "amount is not a valid int64: " + r.Amount
	}
	if amount <= 0 {
		return Tx{}, ReasonBadAmount, "amount must be positive: " + r.Amount
	}
	return Tx{ID: r.ID, Timestamp: r.Timestamp, From: r.From, To: r.To, Amount: amount}, ReasonNone, ""
}

// Ingest 增量摄取一批记录。重复投递不改变最终状态。
// 返回值是本次调用新接受 / 被拒绝记录的副本，可安全长期持有。
//
// 拒绝原因稳定性：先做单条记录的内容校验，再做幂等去重。因此一条记录
// 的拒绝原因只由它自身的内容决定（校验失败），或仅在内容合法时由 ID
// 唯一性决定（duplicate_id）——与批次大小、到达顺序、同批混入了什么
// 其他记录无关。同一条记录无论怎么分批、按什么顺序、和什么数据一起
// 投递，拒绝原因永远相同。
func (l *Ledger) Ingest(records []Record) IngestResult {
	l.mu.Lock()
	defer l.mu.Unlock()

	res := IngestResult{}
	// 本批次内已见过的合法记录 ID，保证批内重复也有确定结论。
	batchSeen := make(map[string]struct{}, len(records))

	for _, r := range records {
		// 1) 内容校验优先：非法记录永远得到自己的校验原因，
		//    不会因为 ID 撞车而被误报成 duplicate_id。
		tx, reason, detail := validate(r)
		if reason != ReasonNone {
			res.Rejected = append(res.Rejected, RejectedRecord{Record: r, Reason: reason, Detail: detail})
			continue
		}
		// 2) 合法记录才参与幂等去重。
		if _, ok := l.accepted[r.ID]; ok {
			res.Rejected = append(res.Rejected, RejectedRecord{
				Record: r, Reason: ReasonDuplicate, Detail: "already ingested: " + r.ID,
			})
			continue
		}
		if _, ok := batchSeen[r.ID]; ok {
			res.Rejected = append(res.Rejected, RejectedRecord{
				Record: r, Reason: ReasonDuplicate, Detail: "duplicate within same batch: " + r.ID,
			})
			continue
		}
		batchSeen[r.ID] = struct{}{}

		l.accepted[tx.ID] = tx
		l.order = append(l.order, tx)
		res.Accepted = append(res.Accepted, tx)
	}
	return res
}

// sorted 返回按 (Timestamp, ID) 排序的副本。调用方必须持锁。
func (l *Ledger) sorted() []Tx {
	out := make([]Tx, len(l.order))
	copy(out, l.order)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Timestamp != out[j].Timestamp {
			return out[i].Timestamp < out[j].Timestamp
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Txs 返回已提交转账的确定性快照（按时间、ID 排序）。
func (l *Ledger) Txs() []Tx {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.sorted()
}

// Has 报告某记录 ID 是否已经被接受。
func (l *Ledger) Has(id string) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	_, ok := l.accepted[id]
	return ok
}
