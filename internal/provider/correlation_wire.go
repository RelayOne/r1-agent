// correlation_wire.go — glue between ChatRequest.Metadata and the
// portfolio-alignment outbound headers (AL-1) + per-call cost event
// (CS-1). Consolidating both helpers here keeps anthropic.go /
// ember.go / gemini.go response paths mechanical.

package provider

import (
	"net/http"

	"github.com/RelayOne/r1/internal/costtrack"
	"github.com/RelayOne/r1/internal/stream"
	"github.com/RelayOne/r1/internal/verityrename"
)

// Metadata keys recognized by applyStokeCorrelationHeaders. Callers
// populate ChatRequest.Metadata from correlation.IDs at the dispatch
// layer (internal/agentloop or chat session). The metadata key names
// remain `stoke-*` for now (internal-only wire; renaming them is an
// orthogonal cleanup outside the S1-2 header-dual-send scope).
const (
	MetaSessionID = "stoke-session-id"
	MetaAgentID   = "stoke-agent-id"
	MetaTaskID    = "stoke-task-id"
)

// clientAttributionValue is the frozen value emitted as X-Veritize-Client
// (and legacy X-Verity-Client) on every outbound inference request.
// RelayGate records this for attribution in the receipt-hook audit trail.
const clientAttributionValue = "r1"

// applyStokeCorrelationHeaders copies the three recognized metadata
// keys onto outbound request headers — setting BOTH the canonical
// X-R1-* header family AND the legacy X-Stoke-* family with identical
// values (AL-1 / SEAM-22 / S1-2 dual-send, 30-day window through
// 2026-05-23). RelayGate accepts either family and prefers canonical
// when both present (router-core commit a1ca514). After 2026-05-23
// the legacy X-Stoke-* emission is dropped per S6-1.
//
// In addition it stamps the V1-2 Veritize dual-send attribution headers
// (X-Veritize-Client + legacy X-Verity-Client) so RelayGate can record
// R1 provenance in its receipt-hook audit events. Both are emitted
// through 2026-05-23; after that the legacy emission is dropped per V6-1.
//
// Empty keys are skipped on BOTH families rather than emitted as
// empty strings — RelayGate's audit ingress checks for non-empty values.
func applyStokeCorrelationHeaders(req *http.Request, metadata map[string]string) {
	if req == nil {
		return
	}
	// V1-2: dual-send Veritize client attribution on every outbound call.
	verityrename.DualSend(req.Header, verityrename.ClientHeaderPair, clientAttributionValue)

	if len(metadata) == 0 {
		return
	}
	if v := metadata[MetaSessionID]; v != "" {
		req.Header.Set("X-R1-Session-ID", v)
		req.Header.Set("X-Stoke-Session-ID", v)
	}
	if v := metadata[MetaAgentID]; v != "" {
		req.Header.Set("X-R1-Agent-ID", v)
		req.Header.Set("X-Stoke-Agent-ID", v)
	}
	if v := metadata[MetaTaskID]; v != "" {
		req.Header.Set("X-R1-Task-ID", v)
		req.Header.Set("X-Stoke-Task-ID", v)
	}
}

// emitAnthropicCostEvent writes the CloudSwarm-compatible per-LLM-call
// cost event (CS-1) after a successful Anthropic response. The usd
// value is derived from the existing costtrack baselines so the event
// mirrors whatever the session total will reconcile to. When the
// model isn't in the baseline table the event still fires with usd=0
// so the CloudSwarm parser sees the canonical shape on every call.
func emitAnthropicCostEvent(model string, usage stream.TokenUsage) {
	usd := costtrack.ComputeCost(model, usage.Input, usage.Output, 0, 0)
	costtrack.EmitCostEventToStdout(model, usage.Input, usage.Output, usd)
}
