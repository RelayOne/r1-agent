// Package correlation carries per-request Stoke session/agent/task IDs
// on the request context so the outbound LLM-call layer can attach
// X-Stoke-Session-ID / X-Stoke-Agent-ID / X-Stoke-Task-ID headers.
//
// RelayGate's apiserver reads these headers and threads them into the
// audit pipeline; every LLM call routed through RelayGate ends up
// correlated back to the originating Stoke session / worker / task.
// When the IDs are unset (zero-value struct), the header-setting
// helper omits them entirely rather than emitting empty strings.
//
// This package is deliberately minimal — no imports beyond context —
// so it can be consumed from internal/apiclient, internal/provider,
// and internal/agentloop without creating import cycles.
package correlation

import (
	"context"
	"net/http"
)

type ctxKey int

const idsKey ctxKey = 0

// IDs carries the three Stoke correlation IDs. Any field may be empty;
// the header-setting helper skips empty fields rather than emitting
// them as empty-string headers.
type IDs struct {
	SessionID string
	AgentID   string
	TaskID    string
}

// WithIDs returns ctx annotated with ids. If all three fields are
// empty, ctx is returned unchanged.
func WithIDs(ctx context.Context, ids IDs) context.Context {
	if ids.SessionID == "" && ids.AgentID == "" && ids.TaskID == "" {
		return ctx
	}
	return context.WithValue(ctx, idsKey, ids)
}

// FromContext extracts IDs previously stored via WithIDs. Returns the
// zero IDs{} if none present — callers do not need to branch on
// "is it there?" separately.
func FromContext(ctx context.Context) IDs {
	if ctx == nil {
		return IDs{}
	}
	if v, ok := ctx.Value(idsKey).(IDs); ok {
		return v
	}
	return IDs{}
}

// ApplyHeaders sets X-Stoke-Session-ID / X-Stoke-Agent-ID / X-Stoke-Task-ID
// on req for any non-empty ID in ctx. Empty fields are skipped —
// standalone Stoke runs with no session/task identity emit zero
// correlation headers rather than empty-string headers.
func ApplyHeaders(ctx context.Context, req *http.Request) {
	if req == nil {
		return
	}
	ids := FromContext(ctx)
	if ids.SessionID != "" {
		req.Header.Set("X-Stoke-Session-ID", ids.SessionID)
	}
	if ids.AgentID != "" {
		req.Header.Set("X-Stoke-Agent-ID", ids.AgentID)
	}
	if ids.TaskID != "" {
		req.Header.Set("X-Stoke-Task-ID", ids.TaskID)
	}
}
