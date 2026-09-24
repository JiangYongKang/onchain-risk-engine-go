// Package ledger 负责本地账本数据的摄取、规范化与幂等存储。
package ledger

// RejectReason 区分记录被拒绝的原因。
type RejectReason string

const (
	RejectMissingField  RejectReason = "missing_field"  // 必填字段缺失
	RejectInvalidType   RejectReason = "invalid_type"   // 字段类型非法
	RejectInvalidAmount RejectReason = "invalid_amount" // 金额非法（<=0 或溢出）
	RejectInvalidID     RejectReason = "invalid_id"     // 交易 ID 非法
)

// Outcome 表示单条记录摄取的结果类别。
type Outcome string

const (
	OutcomeApplied   Outcome = "applied"   // 新记录，已写入
	OutcomeDuplicate Outcome = "duplicate" // 重复投递，状态不变
	OutcomeRejected  Outcome = "rejected"  // 被拒绝，附原因
)

// RawRecord 是未规范化的输入记录，字段类型未知（模拟外部投递）。
type RawRecord map[string]any

// Transfer 是规范化后的转账记录。金额为整数最小单位，避免浮点不确定性。
type Transfer struct {
	TxID   string
	From   string
	To     string
	Amount int64
	TxTime int64
}

// Result 是单条记录的摄取结果。
type Result struct {
	TxID    string
	Outcome Outcome
	Reason  RejectReason // 仅 OutcomeRejected 时有意义
	Detail  string
}
