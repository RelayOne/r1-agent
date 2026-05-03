package sessionhub

import (
	"context"
	"sync"
	"time"
)

// Session is the per-session state owned by the SessionHub. Phase D
// item 22 fills this out (Run, journal, OnEvent, dispatchTool); item 21
// declares the minimal struct so SessionHub.Create has something to
// register. The fields below are stable across the whole Phase D
// rollout — later items only ADD fields, they do not rename or remove.
type Session struct {
	// ID is the per-daemon unique identifier (e.g. "s-1"). Never empty
	// for a session reachable via the hub.
	ID string

	// SessionRoot is the absolute, validated workdir the session
	// operates against. The sentinel (sentinel.go) guards every
	// goroutine-bound dispatch against drifting away from this path.
	SessionRoot string

	// Workspace is an opaque pointer to the per-session cortex
	// Workspace. Item 22 wires this; item 21 leaves it nil.
	Workspace any

	// Model is the provider model id (e.g. "claude-sonnet-4-5-...").
	Model string

	// StartedAt is the wall-clock time at Create. Used by sessions-index
	// for at-a-glance "how long has this been running" diagnostics.
	StartedAt time.Time

	// State is the lifecycle state. The hub itself only flips this when
	// reattaching after a daemon restart (item 27 sets it to
	// "paused-reattachable"). The agent loop owns the live transitions.
	State string

	// runMu serializes Run() against cancelRun() during shutdown.
	runMu sync.Mutex
	// cancel is the goroutine cancel func set when Run starts. Nil
	// before Run fires and after it returns.
	cancel context.CancelFunc
}

// SessionStateActive is the default state of a freshly Create'd session
// before Run has fired. After Run starts, the agent loop owns transitions.
const SessionStateActive = "active"

// SessionStatePausedReattachable indicates a session that was reloaded
// from `sessions-index.json` on daemon restart (item 27). The session
// has its journal replayed but no live agent goroutine.
const SessionStatePausedReattachable = "paused-reattachable"

// newSession builds a Session with all id/path/model fields populated.
// Used by SessionHub.Create after validation passes; never called
// directly by external code.
func newSession(id, sessionRoot, model string) *Session {
	return &Session{
		ID:          id,
		SessionRoot: sessionRoot,
		Model:       model,
		StartedAt:   time.Now(),
		State:       SessionStateActive,
	}
}

// cancelRun is the hub-internal hook called by SessionHub.Delete to
// wind down a Session. If Run has not started, the call is a no-op.
// Safe to call multiple times.
func (s *Session) cancelRun() {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
}
