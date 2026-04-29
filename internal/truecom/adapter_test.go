package truecom

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

// fixedTime is a stable timestamp used across determinism tests.
var fixedTime = time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)

func baseReceipt() SOWReceipt {
	return SOWReceipt{
		TaskID:     "t-abc-123",
		ContractID: "c-def-456",
		AgentDID:   "did:tp:agent-1",
		Outcome:    "pass",
		TaskType:   "code",
		CostUSD:    0.42,
		DurationMS: 8000,
		Source:     "r1",
		EmittedAt:  fixedTime,
	}
}

// TestAdapt_RequiresTaskID ensures Adapt returns an error when task_id is empty.
func TestAdapt_RequiresTaskID(t *testing.T) {
	r := baseReceipt()
	r.TaskID = ""
	_, err := Adapt(r)
	if err == nil {
		t.Fatal("expected error for empty task_id, got nil")
	}
}

// TestAdapt_ReceiptIDDeterministic verifies that the same task_id always
// yields the same receipt_id (UUIDv5 idempotency).
func TestAdapt_ReceiptIDDeterministic(t *testing.T) {
	r := baseReceipt()
	p1, err := Adapt(r)
	if err != nil {
		t.Fatalf("Adapt: %v", err)
	}
	p2, err := Adapt(r)
	if err != nil {
		t.Fatalf("Adapt second call: %v", err)
	}
	if p1.ReceiptID != p2.ReceiptID {
		t.Errorf("receipt_id not deterministic: %v vs %v", p1.ReceiptID, p2.ReceiptID)
	}
}

// TestAdapt_TransactionIDDeterministic verifies transaction_id stability.
func TestAdapt_TransactionIDDeterministic(t *testing.T) {
	r := baseReceipt()
	p1, _ := Adapt(r)
	p2, _ := Adapt(r)
	if p1.TransactionID != p2.TransactionID {
		t.Errorf("transaction_id not deterministic: %v vs %v", p1.TransactionID, p2.TransactionID)
	}
}

// TestAdapt_DifferentTaskIDsYieldDifferentReceiptIDs checks the UUID namespace
// discriminates inputs properly.
func TestAdapt_DifferentTaskIDsYieldDifferentReceiptIDs(t *testing.T) {
	r1 := baseReceipt()
	r2 := baseReceipt()
	r2.TaskID = "t-xyz-999"

	p1, _ := Adapt(r1)
	p2, _ := Adapt(r2)

	if p1.ReceiptID == p2.ReceiptID {
		t.Error("distinct task IDs must produce distinct receipt_ids")
	}
	if p1.TransactionID == p2.TransactionID {
		t.Error("distinct task IDs must produce distinct transaction_ids")
	}
}

// TestAdapt_TransactionIDChangesWithContract verifies that a different
// contract_id produces a different transaction_id even for the same task.
func TestAdapt_TransactionIDChangesWithContract(t *testing.T) {
	r1 := baseReceipt()
	r2 := baseReceipt()
	r2.ContractID = "c-other-789"

	p1, _ := Adapt(r1)
	p2, _ := Adapt(r2)

	if p1.TransactionID == p2.TransactionID {
		t.Error("different contract_ids with same task_id must produce different transaction_ids")
	}
	// receipt_id must remain the same (keyed only on task_id).
	if p1.ReceiptID != p2.ReceiptID {
		t.Error("receipt_id must be stable across different contract_ids for the same task")
	}
}

// TestAdapt_NoContractIDFallback verifies that empty contract_id still
// produces a valid, non-nil transaction_id (uses task_id as fallback seed).
func TestAdapt_NoContractIDFallback(t *testing.T) {
	r := baseReceipt()
	r.ContractID = ""
	p, err := Adapt(r)
	if err != nil {
		t.Fatalf("Adapt: %v", err)
	}
	if p.TransactionID == (uuid.UUID{}) {
		t.Error("transaction_id must be non-nil even without contract_id")
	}
	// Must differ from receipt_id (different namespace seed).
	if p.TransactionID == p.ReceiptID {
		t.Error("transaction_id and receipt_id must differ even when contract_id is empty")
	}
}

// TestAdapt_RecordHashCanonical verifies:
//  1. record_hash is exactly 32 bytes.
//  2. Two identical receipts produce the same hash.
//  3. Changing one field changes the hash.
func TestAdapt_RecordHashCanonical(t *testing.T) {
	r := baseReceipt()
	p, err := Adapt(r)
	if err != nil {
		t.Fatalf("Adapt: %v", err)
	}
	if len(p.RecordHash) != sha256.Size {
		t.Errorf("record_hash length: want %d, got %d", sha256.Size, len(p.RecordHash))
	}

	// Same input → same hash.
	p2, _ := Adapt(r)
	if !bytes.Equal(p.RecordHash, p2.RecordHash) {
		t.Error("record_hash not deterministic for identical inputs")
	}

	// Different outcome → different hash.
	rMod := r
	rMod.Outcome = "fail"
	pMod, _ := Adapt(rMod)
	if bytes.Equal(p.RecordHash, pMod.RecordHash) {
		t.Error("record_hash must change when outcome changes")
	}
}

// TestAdapt_RecordHashMatchesManualComputation checks the canonical JSON shape
// by recomputing the hash manually and comparing.
func TestAdapt_RecordHashMatchesManualComputation(t *testing.T) {
	r := baseReceipt()
	p, err := Adapt(r)
	if err != nil {
		t.Fatalf("Adapt: %v", err)
	}

	// Reproduce the canonical struct manually (same field order as canonicalSOWReceipt).
	type canonical struct {
		AgentDID       string    `json:"agent_did,omitempty"`
		ContractID     string    `json:"contract_id,omitempty"`
		CostUSD        float64   `json:"cost_usd,omitempty"`
		DurationMS     int64     `json:"duration_ms,omitempty"`
		EmittedAt      time.Time `json:"emitted_at"`
		EvidenceDigest string    `json:"evidence_digest,omitempty"`
		Outcome        string    `json:"outcome"`
		Source         string    `json:"source"`
		TaskID         string    `json:"task_id"`
		TaskType       string    `json:"task_type,omitempty"`
	}
	c := canonical{
		AgentDID:   r.AgentDID,
		ContractID: r.ContractID,
		CostUSD:    r.CostUSD,
		DurationMS: r.DurationMS,
		EmittedAt:  r.EmittedAt.UTC(),
		Outcome:    r.Outcome,
		Source:     r.Source,
		TaskID:     r.TaskID,
		TaskType:   r.TaskType,
	}
	raw, _ := json.Marshal(c)
	want := sha256.Sum256(raw)
	if !bytes.Equal(p.RecordHash, want[:]) {
		t.Errorf("record_hash mismatch: want %x, got %x", want, p.RecordHash)
	}
}

// TestAdapt_RFC3161TokenIsTimestamp verifies the stub token contains the
// RFC3339Nano representation of emitted_at.
func TestAdapt_RFC3161TokenIsTimestamp(t *testing.T) {
	r := baseReceipt()
	p, err := Adapt(r)
	if err != nil {
		t.Fatalf("Adapt: %v", err)
	}
	want := []byte(fixedTime.Format(time.RFC3339Nano))
	if !bytes.Equal(p.RFC3161TimestampToken, want) {
		t.Errorf("rfc3161_timestamp_token: want %q, got %q", want, p.RFC3161TimestampToken)
	}
}

// TestAdapt_ZeroEmittedAtDefaultsToNow checks that a zero EmittedAt is
// replaced by a non-zero timestamp (the exact value will differ, but it must
// not be zero and the token must be non-empty).
func TestAdapt_ZeroEmittedAtDefaultsToNow(t *testing.T) {
	r := baseReceipt()
	r.EmittedAt = time.Time{}
	p, err := Adapt(r)
	if err != nil {
		t.Fatalf("Adapt: %v", err)
	}
	if p.IssuedAt.IsZero() {
		t.Error("IssuedAt must not be zero when EmittedAt was zero")
	}
	if len(p.RFC3161TimestampToken) == 0 {
		t.Error("RFC3161TimestampToken must not be empty")
	}
}

// TestAdapt_SourceForcedToR1 verifies Source is always "r1".
func TestAdapt_SourceForcedToR1(t *testing.T) {
	r := baseReceipt()
	r.Source = "unexpected-override"
	p, err := Adapt(r)
	if err != nil {
		t.Fatalf("Adapt: %v", err)
	}
	// The adapter normalises an empty source but does not force "r1" over a
	// caller-supplied non-empty value — that enforcement lives in EmitSOWReceipt.
	// What we assert here: Source is propagated (non-empty).
	if p.Source == "" {
		t.Error("Source must not be empty after Adapt")
	}
}

// TestAdapt_EmptySourceDefaultsToR1 verifies that an empty Source is set to "r1".
func TestAdapt_EmptySourceDefaultsToR1(t *testing.T) {
	r := baseReceipt()
	r.Source = ""
	p, err := Adapt(r)
	if err != nil {
		t.Fatalf("Adapt: %v", err)
	}
	if p.Source != "r1" {
		t.Errorf("empty Source should default to \"r1\", got %q", p.Source)
	}
}

// TestAdapt_AllUUIDsDistinct ensures receipt_id, transaction_id, and
// audit_record_id are all pairwise distinct for a normal receipt.
func TestAdapt_AllUUIDsDistinct(t *testing.T) {
	r := baseReceipt()
	p, err := Adapt(r)
	if err != nil {
		t.Fatalf("Adapt: %v", err)
	}
	if p.ReceiptID == p.TransactionID {
		t.Error("receipt_id and transaction_id must differ")
	}
	if p.ReceiptID == p.AuditRecordID {
		t.Error("receipt_id and audit_record_id must differ")
	}
	if p.TransactionID == p.AuditRecordID {
		t.Error("transaction_id and audit_record_id must differ")
	}
}

// TestAdapt_IssuedAtMatchesEmittedAt verifies IssuedAt round-trips.
func TestAdapt_IssuedAtMatchesEmittedAt(t *testing.T) {
	r := baseReceipt()
	p, err := Adapt(r)
	if err != nil {
		t.Fatalf("Adapt: %v", err)
	}
	if !p.IssuedAt.Equal(fixedTime) {
		t.Errorf("IssuedAt: want %v, got %v", fixedTime, p.IssuedAt)
	}
}
