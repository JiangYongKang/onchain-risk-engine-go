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
// 拒绝原因的稳定性约定（与分批、到达顺序、混批内容无关）：
//   - 结构性非法记录（缺字段 / 自转 / 负时间戳 / 非法金额）**先**做结构校验，
//     因此永远得到其结构原因；即使同一 ID 的有效记录已经先到，坏记录重投也
//     不会被顶成 duplicate_id，反过来亦然。
//   - 内容完全相同的记录重投：首份生效，其余恒为 duplicate_id。
//   - 同一 ID 但内容不同的有效记录冲突：按记录内容全序
//     (Timestamp, From, To, Amount, ID) 取最小者生效，其余为 duplicate_id。
//     因此最终账本状态只取决于记录集合，与谁先到无关；被淘汰者会先从已接受
//     集合移出并在 Replaced 中报告，保证“增量视图 == 全量重算视图”。
func (l *Ledger) Ingest(records []Record) IngestResult {
	l.mu.Lock()
	defer l.mu.Unlock()

	res := IngestResult{}

	for _, r := range records {
		// 先做不依赖账本状态的结构校验（ID 为空无法做幂等判断，直接报缺失）。
		// 结构性原因与“是否同批重复”无关：坏记录无论出现几次都得到结构原因，
		// 这是混批拒绝原因稳定的关键。
		tx, reason, detail := validate(r)
		if reason != ReasonNone {
			res.Rejected = append(res.Rejected, RejectedRecord{Record: r, Reason: reason, Detail: detail})
			continue
		}

		// 有效记录：批内此前的同 ID 记录已即时提交进 accepted，因此与跨批
		// 冲突走完全相同的内容定序判定，保证批内/批间语义一致。
		existing, ok := l.accepted[tx.ID]
		if !ok {
			l.accepted[tx.ID] = tx
			l.order = append(l.order, tx)
			res.Accepted = append(res.Accepted, tx)
			continue
		}

		// 同 ID 有效记录冲突：内容相同是幂等重投；内容不同按内容全序定胜负。
		if existing == tx {
			res.Rejected = append(res.Rejected, RejectedRecord{
				Record: r, Reason: ReasonDuplicate, Detail: "already ingested: " + tx.ID,
			})
			continue
		}
		if contentLess(tx, existing) {
			// 新记录内容全序更小：淘汰并移除旧记录，新记录生效。
			// 旧记录在本结果的 Replaced 中报告，便于增量视图纠正；其最终结论
			// 恒为 duplicate_id（被同 ID 更优记录取代），与投递顺序无关。
			delete(l.accepted, existing.ID)
			l.removeOrder(existing.ID)
			res.Replaced = append(res.Replaced, existing)
			l.accepted[tx.ID] = tx
			l.order = append(l.order, tx)
			res.Accepted = append(res.Accepted, tx)
			continue
		}
		res.Rejected = append(res.Rejected, RejectedRecord{
			Record: r, Reason: ReasonDuplicate,
			Detail: "conflicting record with same id loses content ordering: " + tx.ID,
		})
	}
	return res
}

// contentLess 定义同 ID 有效记录冲突时的内容全序：
// (Timestamp, From, To, Amount)；这些字段相同时视为同内容（ID 必然相同）。
func contentLess(a, b Tx) bool {
	if a.Timestamp != b.Timestamp {
		return a.Timestamp < b.Timestamp
	}
	if a.From != b.From {
		return a.From < b.From
	}
	if a.To != b.To {
		return a.To < b.To
	}
	return a.Amount < b.Amount
}

// removeOrder 从顺序切片中删除指定 ID（同 ID 在集合中至多一份）。调用方持锁。
func (l *Ledger) removeOrder(id string) {
	for i, t := range l.order {
		if t.ID == id {
			l.order = append(l.order[:i], l.order[i+1:]...)
			return
		}
	}
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

// Classify 是纯函数：给定一份完整记录集合（可乱序、可重复、可混批），
// 返回每条记录（按下标）在“全部记录一次性确定性归并”后的最终结论。
//
// 用途：核对拒绝原因稳定性——同一条记录无论实际怎么分批、什么顺序投递，
// 其最终原因都应与 Classify 给出的结论一致：
//   - 结构性非法：结构原因（missing_*/self_transfer/invalid_*）；
//   - 同 ID 内容全序最小的有效记录：ReasonNone（最终被接受）；
//   - 其余有效记录：duplicate_id。
//
// 结果只依赖输入记录内容，与输入顺序无关。
func Classify(records []Record) []RejectReason {
	type cand struct {
		tx  Tx
		idx int
	}
	best := map[string]cand{} // id -> 内容全序最小的有效记录
	for i, r := range records {
		tx, reason, _ := validate(r)
		if reason != ReasonNone {
			continue // 结构原因与 ID 无关，直接在结果中给结论
		}
		cur, ok := best[tx.ID]
		if !ok || contentLess(tx, cur.tx) {
			best[tx.ID] = cand{tx: tx, idx: i}
		}
	}

	out := make([]RejectReason, len(records))
	for i, r := range records {
		_, reason, _ := validate(r)
		if reason != ReasonNone {
			out[i] = reason
			continue
		}
		if best[r.ID].idx == i {
			out[i] = ReasonNone
		} else {
			out[i] = ReasonDuplicate
		}
	}
	return out
}
