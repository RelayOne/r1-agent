package sessionhub

// migrate.go — SessionHub helpers for cross-machine session
// migration (spec C1 specs/cross-machine-session-migration.md
// §5.3 + T4 + T17 + T18).
//
// The hub owns the quiet-point latch: BeginMigrateOut flips the
// source session's state to "migrating-out" so the agent loop's
// MidturnCheckFn (when consulted) yields after the current
// assistant/tool_result pair completes. EndMigrateOut either flips
// back to "idle" (after a successful export with no --park) or to
// "migrated-out" (after --park, leaving the session read-only and
// observable but not accepting new turns).
//
// Active-session safety (spec §9 + T17): only sessions in {paused,
// idle, completed, paused-reattachable} are migratable without
// --force. The "running" state is rejected unless force=true.

import (
	"errors"
	"fmt"
)

// SessionStateMigratingOut is the state set by BeginMigrateOut. The
// agent loop's midturn observer yields once it observes this state,
// preventing new tool dispatches from starting. The session remains
// reachable via Get / List but accepts no new turns.
const SessionStateMigratingOut = "migrating-out"

// SessionStateMigratedOut is the state set by EndMigrateOut when the
// --park flag was passed. The session is now read-only on the
// source — its journal + ledger remain available for forensic
// queries, but new user input is refused. After a successful
// migrate-in on the destination the source session SHOULD be parked
// (the destination owns the live state going forward).
const SessionStateMigratedOut = "migrated-out"

// SessionStateMigratingIn is the state the destination hub assigns
// to a freshly-allocated session during migrate-in. The state flips
// to "idle" only after the migration package's chain-root verify
// passes (spec §6.3 step 4).
const SessionStateMigratingIn = "migrating-in"

// SessionStateMigratedFailed is the state the destination hub
// assigns to a destination session when import fails post-allocation
// but pre-verify. Operators can inspect the partially-hydrated state
// for debugging; retrying the import is safe (idempotency table
// covers the re-attempt).
const SessionStateMigratedFailed = "migrated-failed"

// ErrSessionBusy fires from BeginMigrateOut when the session is in a
// non-migratable state and force=false. Distinct from
// ErrSessionNotFound so callers can map to a 409 vs a 404.
var ErrSessionBusy = errors.New("sessionhub: session busy; not migratable")

// IsMigratableState reports whether a session state is migratable
// without --force. The whitelist is intentionally tight: only states
// where no model call is in flight qualify. Running + tool-in-flight
// + migrating-* are all rejected.
func IsMigratableState(state string) bool {
	switch state {
	case SessionStateActive,
		SessionStatePaused,
		SessionStatePausedReattachable,
		SessionStateStopped:
		return true
	default:
		return false
	}
}

// BeginMigrateOut latches the source session for export. Behavior:
//
//   - id unknown → ErrSessionNotFound.
//   - session in a non-migratable state and force=false → ErrSessionBusy
//     wrapping the offending state.
//   - otherwise → flip state to "migrating-out" under the session's
//     runMu and return nil.
//
// Callers should pair every successful BeginMigrateOut with an
// EndMigrateOut so the source isn't left in a half-migrated state if
// the export aborts.
func (h *SessionHub) BeginMigrateOut(id string, force bool) error {
	s, err := h.Get(id)
	if err != nil {
		return err
	}
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if !IsMigratableState(s.State) && !force {
		return fmt.Errorf("%w: state=%s", ErrSessionBusy, s.State)
	}
	s.State = SessionStateMigratingOut
	return nil
}

// EndMigrateOut flips the source session out of the migrating-out
// state. When parked=true the session lands in "migrated-out" (read-
// only); otherwise the previous live state is restored to "idle"
// (so further user turns are accepted). Returns ErrSessionNotFound
// if id is unknown.
func (h *SessionHub) EndMigrateOut(id string, parked bool) error {
	s, err := h.Get(id)
	if err != nil {
		return err
	}
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if parked {
		s.State = SessionStateMigratedOut
		return nil
	}
	s.State = SessionStateActive
	return nil
}

// IsMigrating reports whether a session is currently in the
// migrating-out latched state. The agent loop's MidturnCheckFn calls
// this each turn boundary; on true the loop yields after the
// current assistant/tool_result pair completes (no orphaned
// tool_use, per spec §9 + RT-CANCEL-INTERRUPT.md).
//
// Returns false (without error) if the session is unknown — callers
// in the hot path shouldn't have to thread a 404 through the loop's
// observer chain; a stale session id simply means "don't yield."
func (h *SessionHub) IsMigrating(id string) bool {
	s, err := h.Get(id)
	if err != nil {
		return false
	}
	s.runMu.Lock()
	defer s.runMu.Unlock()
	return s.State == SessionStateMigratingOut
}

// AllocateMigrationDestination creates a new session in the
// migrating-in state. Used by the destination-side import path
// (cmd/r1-server/migrate_in.go). The caller provides a workdir +
// model; the hub mints a fresh ID and returns the Session pointer.
//
// The state stays "migrating-in" until the importer calls
// SetState("idle") after the chain-root verify passes. On import
// failure the importer SHOULD call SetState("migrated-failed") so
// operators can observe the half-hydrated session for debugging.
func (h *SessionHub) AllocateMigrationDestination(workdir, model string) (*Session, error) {
	s, err := h.Create(CreateOptions{Workdir: workdir, Model: model})
	if err != nil {
		return nil, fmt.Errorf("sessionhub: allocate migration dest: %w", err)
	}
	s.SetState(SessionStateMigratingIn)
	return s, nil
}
