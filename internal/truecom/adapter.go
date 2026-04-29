// adapter.go — RelayGate↔TrueCom receipt contract adapter (F-W1-2).
//
// Translates R1's SOWReceipt into the field set TrueCom's CustomerReceipt
// requires.  Zero fields are shared between the two schemas; every field
// is derived deterministically so the adapter is idempotent across retries.
//
// # Field derivation
//
//   - receipt_id      UUIDv5(relayGateNS, task_id)
//                     Deterministic per task, safe to retry.
//   - transaction_id  UUIDv5(relayGateNS, contract_id+":"+task_id)
//                     When contract_id is empty, falls back to task_id.
//   - record_hash     SHA-256(canonical JSON of SOWReceipt).
//                     Canonical = keys alphabetically sorted, no extraneous
//                     whitespace, RFC3339Nano timestamps.  Matches the wire
//                     shape TrueCom expects for hash-chain continuity.
//   - rfc3161_timestamp_token  RFC3339Nano bytes of emitted_at (gateway
//                     clock stub).  A real RFC 3161 TSA call is gated on a
//                     future SOW; callers that need the real token MUST
//                     override rfc3161_timestamp_token after calling Adapt.
//   - tenant_id       Passed by the caller; RelayGate does not carry a
//                     TrueCom tenant_id internally.
//   - audit_record_id UUIDv5(relayGateNS, "audit:"+task_id) — stable
//                     cross-reference the TrueCom audit table can join on.
//
// # Namespace
//
// UUIDv5 namespace: SHA-1 of "relaygate.relayone.ai" per RFC 4122 §4.3.
// Constant computed once at package init; never changes.
package truecom

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// relayGateNS is the UUIDv5 namespace for all RelayGate-derived UUIDs.
// It is the RFC 4122 §4.3 UUID derived from the DNS name "relaygate.relayone.ai".
var relayGateNS = uuid.NewSHA1(uuid.NameSpaceDNS, []byte("relaygate.relayone.ai"))

// TrueComReceiptPayload is the adapter output — the field set that maps onto
// TrueCom's CustomerReceipt wire schema.  It contains only the fields that
// R1 can derive from a SOWReceipt; fields that require TrueCom-internal data
// (batch_promise timing, receipt_signature, tenant auth) must be populated by
// the emitter after calling Adapt.
//
// JSON tags match TrueCom's snake_case wire format exactly, so the struct can
// be marshalled directly into the POST /v1/r1/receipt body.
type TrueComReceiptPayload struct {
	// ReceiptID is a UUIDv5 derived from task_id.  Idempotent.
	ReceiptID uuid.UUID `json:"receipt_id"`

	// TransactionID is a UUIDv5 derived from contract_id + task_id.
	TransactionID uuid.UUID `json:"transaction_id"`

	// AuditRecordID is a UUIDv5 stable cross-reference for the TrueCom
	// audit table.
	AuditRecordID uuid.UUID `json:"audit_record_id"`

	// RecordHash is SHA-256(canonical JSON of the source SOWReceipt).
	// Canonical JSON: keys alphabetically sorted, no whitespace.
	RecordHash []byte `json:"record_hash"`

	// RFC3161TimestampToken is the raw timestamp authority token.
	// In this stub implementation it is the RFC3339Nano bytes of
	// SOWReceipt.EmittedAt.  Replace with a real TSA token before
	// submitting to a production TrueCom endpoint.
	RFC3161TimestampToken []byte `json:"rfc3161_timestamp_token"`

	// IssuedAt mirrors SOWReceipt.EmittedAt.
	IssuedAt time.Time `json:"issued_at"`

	// Source is always "r1" — preserved for TrueCom filter queries.
	Source string `json:"source"`
}

// canonicalSOWReceipt is a minimal, key-stable representation of SOWReceipt
// used only for hashing.  Fields are listed alphabetically so json.Marshal
// always produces the same byte sequence regardless of Go struct layout.
type canonicalSOWReceipt struct {
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

// canonicalHash returns SHA-256(sorted-key canonical JSON of r).
// The JSON is deterministic because canonicalSOWReceipt declares fields
// in alphabetical order and uses no maps.
func canonicalHash(r SOWReceipt) ([]byte, error) {
	c := canonicalSOWReceipt{
		AgentDID:       r.AgentDID,
		ContractID:     r.ContractID,
		CostUSD:        r.CostUSD,
		DurationMS:     r.DurationMS,
		EmittedAt:      r.EmittedAt.UTC(),
		EvidenceDigest: r.EvidenceDigest,
		Outcome:        r.Outcome,
		Source:         r.Source,
		TaskID:         r.TaskID,
		TaskType:       r.TaskType,
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("truecom adapter: canonical hash: %w", err)
	}
	sum := sha256.Sum256(raw)
	return sum[:], nil
}

// Adapt converts a SOWReceipt into a TrueComReceiptPayload.
//
// Rules:
//
//   - r.TaskID must be non-empty; returns error otherwise.
//   - r.EmittedAt is normalised to UTC; if zero it is set to now.
//   - The rfc3161_timestamp_token is a gateway-clock stub (RFC3339Nano bytes).
//     Production callers MUST replace it with a real TSA token.
func Adapt(r SOWReceipt) (TrueComReceiptPayload, error) {
	if r.TaskID == "" {
		return TrueComReceiptPayload{}, fmt.Errorf("truecom adapter: task_id required")
	}
	if r.EmittedAt.IsZero() {
		r.EmittedAt = time.Now().UTC()
	} else {
		r.EmittedAt = r.EmittedAt.UTC()
	}
	if r.Source == "" {
		r.Source = "r1"
	}

	receiptID := uuid.NewSHA1(relayGateNS, []byte(r.TaskID))

	transactionSeed := r.ContractID + ":" + r.TaskID
	if r.ContractID == "" {
		// Use a distinct prefix so the fallback seed never collides with the
		// receipt_id seed (which is task_id alone).
		transactionSeed = "tx:" + r.TaskID
	}
	transactionID := uuid.NewSHA1(relayGateNS, []byte(transactionSeed))

	auditRecordID := uuid.NewSHA1(relayGateNS, []byte("audit:"+r.TaskID))

	hash, err := canonicalHash(r)
	if err != nil {
		return TrueComReceiptPayload{}, err
	}

	// Gateway-clock timestamp stub: RFC3339Nano bytes.
	tsToken := []byte(r.EmittedAt.Format(time.RFC3339Nano))

	return TrueComReceiptPayload{
		ReceiptID:             receiptID,
		TransactionID:         transactionID,
		AuditRecordID:         auditRecordID,
		RecordHash:            hash,
		RFC3161TimestampToken: tsToken,
		IssuedAt:              r.EmittedAt,
		Source:                r.Source,
	}, nil
}
