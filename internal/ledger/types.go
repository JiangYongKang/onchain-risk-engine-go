package ledger

// Record 是一条待摄取的原始账本记录（来自本地账本导出）。
type Record struct {
	ID        string
	Timestamp int64
	From      string
	To        string
	Amount    string // 以字符串承载，摄取时做严格解析
}

// RejectReason 拒绝原因。
type RejectReason string

const (
	ReasonNone         RejectReason = ""
	ReasonDuplicate    RejectReason = "duplicate_id"
	ReasonMissingID    RejectReason = "missing_id"
	ReasonMissingFrom  RejectReason = "missing_from"
	ReasonMissingTo    RejectReason = "missing_to"
	ReasonSelfTransfer RejectReason = "self_transfer"
	ReasonBadAmount    RejectReason = "invalid_amount"
	ReasonBadTimestamp RejectReason = "invalid_timestamp"
)

// RejectedRecord 描述一条被拒绝的记录及原因。
type RejectedRecord struct {
	Record Record
	Reason RejectReason
	Detail string
}

// Tx 是规范化后的不可变转账。
type Tx struct {
	ID        string
	Timestamp int64
	From      string
	To        string
	Amount    int64 // 最小货币单位，必须为正
}

// IngestResult 是一次（或一批）摄取的确定性结果。
type IngestResult struct {
	Accepted []Tx
	Rejected []RejectedRecord
	Pending  []Record // 乱序暂存、尚不能提交的记录
}
