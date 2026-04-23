// correlation_wire.go — glue between ChatRequest.Metadata and the
// portfolio-alignment outbound headers (AL-1) + per-call cost event
// (CS-1). Consolidating both helpers here keeps anthropic.go /
// ember.go / gemini.go response paths mechanical.

package provider

import (
	"net/http"

	"github.com/ericmacdougall/stoke/internal/costtrack"
	"github.com/ericmacdougall/stoke/internal/stream"
)

// Metadata keys recognized by applyStokeCorrelationHeaders. Callers
// populate ChatRequest.Metadata from correlation.IDs at the dispatch
// layer (internal/agentloop or chat session).
const (
	MetaSessionID = "stoke-session-id"
	MetaAgentID   = "stoke-agent-id"
	MetaTaskID    = "stoke-task-id"
)

// applyStokeCorrelationHeaders copies the three recognized metadata
// keys onto outbound X-Stoke-* headers (AL-1 / SEAM-22). Empty keys
// are skipped rather than emitted as empty strings — RelayGate's
// audit ingress checks for non-empty values.
func applyStokeCorrelationHeaders(req *http.Request, metadata map[string]string) {
	if req == nil || len(metadata) == 0 {
		return
	}
	if v := metadata[MetaSessionID]; v != "" {
		req.Header.Set("X-Stoke-Session-ID", v)
	}
	if v := metadata[MetaAgentID]; v != "" {
		req.Header.Set("X-Stoke-Agent-ID", v)
	}
	if v := metadata[MetaTaskID]; v != "" {
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
