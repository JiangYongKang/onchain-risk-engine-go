package ledger

import "testing"

func rec(id, from, to string, amount, ts int64) RawRecord {
	return RawRecord{"tx_id": id, "from": from, "to": to, "amount": amount, "tx_time": ts}
}

func TestIngestBasic(t *testing.T) {
	l := New()
	res := l.Ingest(rec("tx1", "a", "b", 100, 1))
	t.Logf("input=%v result=%+v", rec("tx1", "a", "b", 100, 1), res[0])
	if res[0].Outcome != OutcomeApplied {
		t.Fatalf("expected applied, got %+v", res[0])
	}
	if l.Len() != 1 {
		t.Fatalf("expected len 1, got %d", l.Len())
	}
}

func TestDuplicateIdempotent(t *testing.T) {
	l := New()
	l.Ingest(rec("tx1", "a", "b", 100, 1))
	res := l.Ingest(rec("tx1", "a", "b", 100, 1))
	t.Logf("duplicate input result=%+v (must be reported, not silently ignored)", res[0])
	if res[0].Outcome != OutcomeDuplicate {
		t.Fatalf("expected duplicate, got %+v", res[0])
	}
	if l.Len() != 1 {
		t.Fatalf("duplicate changed state: len=%d", l.Len())
	}
	// 冲突的重复（同 ID 不同内容）必须拒绝。
	res = l.Ingest(rec("tx1", "a", "c", 100, 1))
	t.Logf("conflicting duplicate result=%+v", res[0])
	if res[0].Outcome != OutcomeRejected || res[0].Reason != RejectInvalidID {
		t.Fatalf("expected rejected/invalid_id, got %+v", res[0])
	}
	if l.Len() != 1 {
		t.Fatalf("conflicting duplicate changed state: len=%d", l.Len())
	}
}

func TestOutOfOrderDeterministic(t *testing.T) {
	mk := func(order []RawRecord) []Transfer {
		l := New()
		l.Ingest(order...)
		return l.Snapshot()
	}
	a := []RawRecord{rec("tx1", "a", "b", 100, 3), rec("tx2", "b", "c", 50, 1), rec("tx3", "c", "d", 25, 2)}
	b := []RawRecord{rec("tx3", "c", "d", 25, 2), rec("tx1", "a", "b", 100, 3), rec("tx2", "b", "c", 50, 1)}
	sa, sb := mk(a), mk(b)
	t.Logf("snapshot A=%+v", sa)
	t.Logf("snapshot B=%+v", sb)
	if len(sa) != len(sb) {
		t.Fatalf("snapshot length mismatch")
	}
	for i := range sa {
		if sa[i] != sb[i] {
			t.Fatalf("out-of-order ingestion changed snapshot at %d: %v vs %v", i, sa[i], sb[i])
		}
	}
}

func TestRejectReasons(t *testing.T) {
	cases := []struct {
		name   string
		rec    RawRecord
		reason RejectReason
	}{
		{"missing from", RawRecord{"tx_id": "x", "to": "b", "amount": 1, "tx_time": 1}, RejectMissingField},
		{"bad type from", RawRecord{"tx_id": "x", "from": 42, "to": "b", "amount": 1, "tx_time": 1}, RejectInvalidType},
		{"bad type amount", RawRecord{"tx_id": "x", "from": "a", "to": "b", "amount": "lots", "tx_time": 1}, RejectInvalidType},
		{"zero amount", RawRecord{"tx_id": "x", "from": "a", "to": "b", "amount": 0, "tx_time": 1}, RejectInvalidAmount},
		{"negative amount", RawRecord{"tx_id": "x", "from": "a", "to": "b", "amount": -5, "tx_time": 1}, RejectInvalidAmount},
		{"bad id", RawRecord{"tx_id": "has space", "from": "a", "to": "b", "amount": 1, "tx_time": 1}, RejectInvalidID},
		{"missing time", RawRecord{"tx_id": "x", "from": "a", "to": "b", "amount": 1}, RejectMissingField},
	}
	for _, tc := range cases {
		l := New()
		res := l.Ingest(tc.rec)
		t.Logf("case=%s input=%v outcome=%s reason=%s detail=%s",
			tc.name, tc.rec, res[0].Outcome, res[0].Reason, res[0].Detail)
		if res[0].Outcome != OutcomeRejected {
			t.Fatalf("%s: expected rejected, got %+v", tc.name, res[0])
		}
		if res[0].Reason != tc.reason {
			t.Fatalf("%s: expected reason %s, got %s", tc.name, tc.reason, res[0].Reason)
		}
		if l.Len() != 0 {
			t.Fatalf("%s: rejected record changed state", tc.name)
		}
	}
}

func TestJSONNumberTolerance(t *testing.T) {
	l := New()
	// JSON 解码产生 float64，整数值应被接受。
	res := l.Ingest(RawRecord{"tx_id": "j1", "from": "a", "to": "b", "amount": float64(10), "tx_time": float64(7)})
	t.Logf("json-number input result=%+v", res[0])
	if res[0].Outcome != OutcomeApplied {
		t.Fatalf("expected applied, got %+v", res[0])
	}
	// 非整数 float 应拒绝。
	res = l.Ingest(RawRecord{"tx_id": "j2", "from": "a", "to": "b", "amount": 1.5, "tx_time": 1})
	t.Logf("fractional amount result=%+v", res[0])
	if res[0].Outcome != OutcomeRejected || res[0].Reason != RejectInvalidType {
		t.Fatalf("expected rejected/invalid_type, got %+v", res[0])
	}
}
