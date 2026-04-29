// emitter.go — R1 SOW-completion receipt emitter (F-W1-3).
//
// When a SOW (Statement of Work) task reaches a terminal state the agent-serve
// path calls EmitSOWReceipt to push a structured receipt to TrustPlane.
// TrustPlane stores it under source="r1" so it can be queried independently
// of router-core inference receipts.
//
// Design rules (inherit from real.go):
//
//   - HTTP POST to /v1/r1/receipt with DPoP proof (same key as identity).
//   - Fail-soft: callers wrap this in a goroutine; a gateway outage must
//     never stop the agent from serving the next task.
//   - Idempotency: callers SHOULD set TaskID to a stable, unique identifier
//     so a retry on network failure doesn't double-count. The gateway dedupes
//     on task_id within a 24-hour window.
//   - No retry here: the caller owns retry policy (e.g. bounded exponential
//     back-off before giving up).
//
// Degradation: if TrustPlane is unreachable or returns 5xx the error is
// returned to the caller for logging. The caller MUST NOT surface this to
// end users — it is an observability / billing gap, not a user-visible error.
package truecom

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// SOWReceipt is the R1-side payload posted to TrustPlane after a SOW
// task completes (pass or fail). The source field is always "r1" so
// TrustPlane can filter by origin in queries.
type SOWReceipt struct {
	// TaskID is the stable task identifier emitted by agentserve
	// (e.g. "t-<uuid>"). Used by TrustPlane for 24h dedup.
	TaskID string `json:"task_id"`

	// ContractID ties this receipt to a TrustPlane hire contract.
	// May be empty for locally-dispatched tasks that have no contract.
	ContractID string `json:"contract_id,omitempty"`

	// AgentDID is the registered DID of the agent that executed the task.
	AgentDID string `json:"agent_did,omitempty"`

	// Outcome is "pass" | "fail" | "partial".
	Outcome string `json:"outcome"`

	// TaskType mirrors agentserve.TaskState.TaskType ("code", "research", …).
	TaskType string `json:"task_type,omitempty"`

	// CostUSD is the realized cost in US dollars (0 when unknown).
	CostUSD float64 `json:"cost_usd,omitempty"`

	// DurationMS is wall-clock milliseconds from task start to terminal state.
	DurationMS int64 `json:"duration_ms,omitempty"`

	// EvidenceDigest is an optional SHA-256 hex digest of the evidence
	// bundle the agent produced. Lets TrustPlane verify the evidence
	// submitted in a Dispute without holding the raw bytes.
	EvidenceDigest string `json:"evidence_digest,omitempty"`

	// Source is always "r1". Set by EmitSOWReceipt so callers cannot
	// accidentally override it.
	Source string `json:"source"`

	// EmittedAt is the RFC3339 timestamp when the receipt was produced.
	EmittedAt time.Time `json:"emitted_at"`
}

// SOWReceiptResponse is the TrustPlane acknowledgement for a stored receipt.
type SOWReceiptResponse struct {
	ReceiptID string    `json:"receipt_id"`
	TaskID    string    `json:"task_id"`
	Status    string    `json:"status"`
	StoredAt  time.Time `json:"stored_at"`
}

// EmitSOWReceipt POST /v1/r1/receipt.
//
// Sends a SOW completion receipt to TrustPlane. The Source field is
// forced to "r1" regardless of what the caller sets. Returns the
// gateway's acknowledgement on success or an error the caller should
// log (but never surface to end users — this is fail-soft telemetry).
//
// Caller pattern (from buildSettlementCallback):
//
//	go func() {
//	    ctx2, cancel := context.WithTimeout(context.Background(), 10*time.Second)
//	    defer cancel()
//	    if _, err := tp.EmitSOWReceipt(ctx2, receipt); err != nil {
//	        log.Printf("truecom: emit receipt %s: %v", receipt.TaskID, err)
//	    }
//	}()
func (c *RealClient) EmitSOWReceipt(ctx context.Context, r SOWReceipt) (SOWReceiptResponse, error) {
	if strings.TrimSpace(r.TaskID) == "" {
		return SOWReceiptResponse{}, fmt.Errorf("truecom: EmitSOWReceipt requires task_id")
	}
	// Force source so the gateway filter always works.
	r.Source = "r1"
	if r.EmittedAt.IsZero() {
		r.EmittedAt = time.Now().UTC()
	}
	outcome := strings.TrimSpace(r.Outcome)
	if outcome == "" {
		return SOWReceiptResponse{}, fmt.Errorf("truecom: EmitSOWReceipt requires outcome")
	}

	body, status, err := c.do(ctx, http.MethodPost, "/v1/r1/receipt", r)
	if err != nil {
		return SOWReceiptResponse{}, fmt.Errorf("truecom: emit SOW receipt: %w", err)
	}
	if status/100 != 2 {
		return SOWReceiptResponse{}, wrapHTTPErr("POST", "/v1/r1/receipt", status, body, nil)
	}
	var out SOWReceiptResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return SOWReceiptResponse{}, fmt.Errorf("truecom: decode SOW receipt response: %w", err)
	}
	return out, nil
}

// ListSOWReceiptsResponse is returned by GET /v1/r1/receipts.
type ListSOWReceiptsResponse struct {
	Receipts []SOWReceiptEntry `json:"receipts"`
	Total    int               `json:"total"`
}

// SOWReceiptEntry is one row from the TrustPlane r1_receipts table.
type SOWReceiptEntry struct {
	ReceiptID      string    `json:"receipt_id"`
	TaskID         string    `json:"task_id"`
	ContractID     string    `json:"contract_id,omitempty"`
	AgentDID       string    `json:"agent_did,omitempty"`
	Outcome        string    `json:"outcome"`
	TaskType       string    `json:"task_type,omitempty"`
	CostUSD        float64   `json:"cost_usd,omitempty"`
	DurationMS     int64     `json:"duration_ms,omitempty"`
	EvidenceDigest string    `json:"evidence_digest,omitempty"`
	Source         string    `json:"source"`
	EmittedAt      time.Time `json:"emitted_at"`
	StoredAt       time.Time `json:"stored_at"`
}

// EmitCustomerReceipt translates r into TrueCom's CustomerReceipt field set
// via Adapt and then POSTs it to /v1/r1/customer-receipt.
//
// This is the concrete wiring point for F-W1-2: it calls Adapt internally so
// no caller needs to know the derivation rules.  The resulting
// TrueComReceiptPayload carries a gateway-clock rfc3161_timestamp_token stub;
// replace it with a real TSA token before enabling production enforcement.
//
// Fail-soft contract mirrors EmitSOWReceipt: log the error, never surface it
// to end users.
func (c *RealClient) EmitCustomerReceipt(ctx context.Context, r SOWReceipt) (SOWReceiptResponse, error) {
	payload, err := Adapt(r)
	if err != nil {
		return SOWReceiptResponse{}, fmt.Errorf("truecom: EmitCustomerReceipt adapt: %w", err)
	}
	body, status, err := c.do(ctx, http.MethodPost, "/v1/r1/customer-receipt", payload)
	if err != nil {
		return SOWReceiptResponse{}, fmt.Errorf("truecom: emit customer receipt: %w", err)
	}
	if status/100 != 2 {
		return SOWReceiptResponse{}, wrapHTTPErr("POST", "/v1/r1/customer-receipt", status, body, nil)
	}
	var out SOWReceiptResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return SOWReceiptResponse{}, fmt.Errorf("truecom: decode customer receipt response: %w", err)
	}
	return out, nil
}

// ListSOWReceipts GET /v1/r1/receipts.
//
// Fetches stored R1 SOW receipts. Pass source="r1" to filter only
// R1-originated receipts (the gateway default). agentDID and
// contractID are optional additional filters; pass empty string to omit.
func (c *RealClient) ListSOWReceipts(ctx context.Context, source, agentDID, contractID string) (ListSOWReceiptsResponse, error) {
	path := "/v1/r1/receipts"
	sep := "?"
	if source != "" {
		path += sep + "source=" + source
		sep = "&"
	}
	if agentDID != "" {
		path += sep + "agent_did=" + agentDID
		sep = "&"
	}
	if contractID != "" {
		path += sep + "contract_id=" + contractID
	}

	body, status, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return ListSOWReceiptsResponse{}, fmt.Errorf("truecom: list SOW receipts: %w", err)
	}
	if status/100 != 2 {
		return ListSOWReceiptsResponse{}, wrapHTTPErr("GET", path, status, body, nil)
	}
	var out ListSOWReceiptsResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return ListSOWReceiptsResponse{}, fmt.Errorf("truecom: decode list receipts response: %w", err)
	}
	return out, nil
}
