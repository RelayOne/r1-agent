package mcp

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/RelayOne/r1/internal/throttle"
)

// r1ThrottleStatusTool is the diagnostic surface mandated by spec
// task T24. Read-only — never mutates state. Designed so an
// operator can answer "why was I throttled" without grepping logs
// or recomputing buckets by hand.
//
// The tool returns the full live bucket snapshot when the optional
// `session_id` / `tool` filters are absent; otherwise it narrows to
// matching entries. Exposed under the `r1.` prefix so it dispatches
// through the same HandleToolCall switch as the other r1.* tools.
func r1ThrottleStatusToolDef() ToolDefinition {
	return ToolDefinition{
		Name:        "r1.throttle.status",
		Description: "Diagnostic snapshot of live throttle buckets (tool, scope, principal, available_tokens, burst, rate). Read-only; safe to poll. Optional filters: session_id, tool.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"session_id": {"type": "string", "description": "If set, only return buckets whose principal equals this session id."},
				"tool":       {"type": "string", "description": "If set, only return buckets whose tool name matches."}
			}
		}`),
	}
}

// handleThrottleStatus serializes the live bucket snapshot for the
// operator. When the throttler is nil the response is an empty list
// (matches the open-local-dev posture used by the rest of the
// server).
func (s *StokeServer) handleThrottleStatus(args map[string]interface{}) (string, error) {
	s.mu.Lock()
	throttler := s.preDispatch.Throttler
	s.mu.Unlock()
	if throttler == nil {
		return `{"buckets":[]}`, nil
	}
	filterSession, _ := args["session_id"].(string)
	filterTool, _ := args["tool"].(string)
	all := throttle.Status(throttler)
	out := make([]throttle.StatusSnapshot, 0, len(all))
	for _, b := range all {
		if filterSession != "" && b.Principal != filterSession {
			continue
		}
		if filterTool != "" && !strings.EqualFold(b.Tool, filterTool) {
			continue
		}
		out = append(out, b)
	}
	resp := map[string]any{"buckets": out}
	buf, err := json.Marshal(resp)
	if err != nil {
		return "", fmt.Errorf("throttle.status: marshal: %w", err)
	}
	return string(buf), nil
}
