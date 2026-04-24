// Package correlation carries per-request R1 (formerly Stoke) session/
// agent/task IDs on the request context so the outbound LLM-call layer
// can attach X-R1-Session-ID / X-R1-Agent-ID / X-R1-Task-ID headers
// (canonical) plus the legacy X-Stoke-* header pair during the S1-2
// 30-day dual-send window (through 2026-05-23).
//
// RelayGate's apiserver reads either header family (canonical wins
// when both present — see router-core commit a1ca514) and threads the
// ID into the audit pipeline; every LLM call routed through RelayGate
// ends up correlated back to the originating R1 session / worker /
// task. When the IDs are unset (zero-value struct), the header-setting
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

// IDs carries the three R1 correlation IDs. Any field may be empty;
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

// ApplyHeaders sets both the canonical X-R1-* header family AND the
// legacy X-Stoke-* family on req for any non-empty ID in ctx. Both
// families carry IDENTICAL values. This is the S1-2 30-day dual-send
// window (through 2026-05-23) — RelayGate accepts either family and
// prefers canonical when both are present (router-core commit
// a1ca514). After 2026-05-23 the legacy X-Stoke-* emission is dropped
// per S6-1.
//
// Empty fields are skipped on BOTH families — standalone R1 runs with
// no session/task identity emit zero correlation headers rather than
// empty-string headers.
func ApplyHeaders(ctx context.Context, req *http.Request) {
	if req == nil {
		return
	}
	ids := FromContext(ctx)
	if ids.SessionID != "" {
		req.Header.Set("X-R1-Session-ID", ids.SessionID)
		req.Header.Set("X-Stoke-Session-ID", ids.SessionID)
	}
	if ids.AgentID != "" {
		req.Header.Set("X-R1-Agent-ID", ids.AgentID)
		req.Header.Set("X-Stoke-Agent-ID", ids.AgentID)
	}
	if ids.TaskID != "" {
		req.Header.Set("X-R1-Task-ID", ids.TaskID)
		req.Header.Set("X-Stoke-Task-ID", ids.TaskID)
	}
}
