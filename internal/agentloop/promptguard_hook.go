// internal/agentloop/promptguard_hook.go
//
// In-process tool-dispatch wiring for promptguard's per-tool input
// validator (specs/promptguard-hardening.md §T2 item 9).
//
// executeTools calls validateAgentloopToolInput AFTER the hub policy
// gate (EventToolPreUse) and BEFORE the model-supplied ToolHandler
// runs. On rejection the per-tool tool_result fed back into the model
// is is_error=true with the structured rejection text so the model
// can see and revise; the raw payload is NEVER replayed to the model.
package agentloop

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/RelayOne/r1/internal/promptguard"
)

// validateAgentloopToolInput returns nil to allow the tool to dispatch,
// non-nil with the structured "promptguard: tool_input_rejected: ..."
// message otherwise. The error string is what the model sees as the
// tool_result content (is_error=true), which it can read on the next
// turn to revise its approach.
//
// Action gating: if the configured promptguard action is ActionWarn or
// ActionStrip, violations are logged via the threat-event emitter but
// the call is allowed to proceed. Only ActionReject blocks dispatch.
// This mirrors the MCP-side hook in internal/mcp/promptguard_hook.go.
func validateAgentloopToolInput(ctx context.Context, toolName string, rawInput json.RawMessage) error {
	raw := rawInput
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	rep, vErr := promptguard.ValidateToolInput(ctx, toolName, raw)
	if vErr == nil {
		return nil
	}
	if rep.Action != promptguard.ActionReject {
		return nil
	}
	return fmt.Errorf("promptguard: tool_input_rejected: %s", vErr.Error())
}
