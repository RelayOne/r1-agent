// Package sessionhub: daemon-restart replay (spec §11.27).
//
// =============================================================================
// What this is
// =============================================================================
//
// On daemon startup, after the Phase A audit gate clears (spec §10) and
// before the WS listener accepts new connections, the daemon calls
// SessionHub.Reload to reconstruct every previously-known session
// from `~/.r1/sessions-index.json` and the per-session journal files.
// The reloaded sessions land in state SessionStatePausedReattachable
// — their in-memory representation exists but no Run goroutine is
// running, so a reconnecting client can attach and pick up where the
// crash left off.
//
// Once Reload returns, the daemon brings up the WS listener and emits
// `daemon.reloaded` to all subscribers. The event's Custom["sessions"]
// slice carries the reloaded ids so subscribers can build a "welcome
// back" preamble without re-walking the hub.
//
// =============================================================================
// Replay semantics
// =============================================================================
//
// Each session's journal is opened via journal.OpenReader and
// Replay'd into a per-session ReplayHandler. The Phase D handler is
// minimal — it counts records and tracks the last seq so the
// reattach logic can include `since_seq` parameters in WS frames.
// Future commits may pass a richer handler (e.g. one that
// reconstructs cortex Workspace state); the Reload signature accepts
// a ReplayHandler to keep that extension ergonomic.
//
// Corrupt journals abort the per-session replay but DO NOT abort the
// whole Reload — one bad session shouldn't take down all sessions.
// The error is recorded on the per-session result so the daemon can
// surface it via metrics.
package sessionhub

import (
	"context"
	"fmt"

	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/journal"
)

// ReloadResult is the outcome of a single session's replay.
type ReloadResult struct {
	// ID is the session's id (matches the IndexEntry's ID).
	ID string

	// Workdir is the session's recorded workdir.
	Workdir string

	// JournalPath is the journal file that was replayed.
	JournalPath string

	// LastSeq is the highest seq seen during replay. 0 if the
	// journal was empty or absent.
	LastSeq uint64

	// RecordCount is the number of records replayed.
	RecordCount int

	// Err, when non-nil, is the replay error for this session. The
	// session is still added to the hub (in paused-reattachable
	// state) so an operator can inspect it; the error is surfaced so
	// the daemon can choose to skip a reattach.
	Err error
}

// ReplayHandler is the per-record callback during Reload. The
// canonical Phase D handler is nil (count + track last seq); future
// commits may pass a richer callback (e.g. cortex Workspace
// reconstruction).
type ReplayHandler func(sessionID string, rec journal.Record) error

// Reload reconstructs sessions from `sessions-index.json` and the
// per-session journals. The hub MUST have been configured with
// SetSessionsIndex (and ideally SetJournalDir) before Reload is
// called. Returns one ReloadResult per non-deleted IndexEntry in
// index order.
//
// Reloaded sessions are registered in the hub's sync.Map with state =
// SessionStatePausedReattachable. The caller (the daemon's startup
// glue) is responsible for calling EmitDaemonReloaded once the WS
// listener is up.
//
// `handler` may be nil — Phase D ships without cortex-replay logic.
func (h *SessionHub) Reload(ctx context.Context, handler ReplayHandler) ([]ReloadResult, error) {
	if h.index == nil {
		// No index configured -> nothing to reload. This is the
		// expected first-run case; not an error.
		return nil, nil
	}
	idx, err := h.index.Load()
	if err != nil {
		return nil, fmt.Errorf("sessionhub: reload: load index: %w", err)
	}
	var results []ReloadResult
	for _, entry := range idx.Sessions {
		if entry.Deleted {
			continue
		}
		// Honor cancellation between sessions so a slow journal
		// replay doesn't pin the daemon forever during shutdown.
		if err := ctx.Err(); err != nil {
			return results, err
		}
		results = append(results, h.replayOne(entry, handler))
	}
	return results, nil
}

// replayOne reconstructs one session from its IndexEntry. Always
// registers the session in the hub (even on replay error) so an
// operator can inspect it; the error is reported on the
// ReloadResult and the session remains in paused-reattachable state.
func (h *SessionHub) replayOne(entry IndexEntry, handler ReplayHandler) ReloadResult {
	res := ReloadResult{
		ID:          entry.ID,
		Workdir:     entry.Workdir,
		JournalPath: entry.JournalPath,
	}
	// Fresh Session from the IndexEntry's metadata.
	s := &Session{
		ID:          entry.ID,
		SessionRoot: entry.Workdir,
		Model:       entry.Model,
		State:       SessionStatePausedReattachable,
	}
	// Register BEFORE replay so the operator can see the
	// paused-reattachable session even if replay fails halfway.
	if _, loaded := h.sessions.LoadOrStore(entry.ID, s); loaded {
		// Already registered (someone called Create with this id between
		// our Load and Store). Surface as an error but don't overwrite.
		res.Err = fmt.Errorf("sessionhub: reload: id %q already registered", entry.ID)
		return res
	}
	// Bump the hub's id counter past any reloaded id of the form
	// `s-N` so freshly-minted ids don't collide with reloaded ones.
	h.bumpIDPast(entry.ID)
	// Replay the journal.
	if entry.JournalPath == "" {
		// No journal recorded — nothing to replay; the session is
		// resumed at seq 0. Spec doesn't forbid this; some test
		// harness paths may install bare entries.
		return res
	}
	r := journal.OpenReader(entry.JournalPath)
	err := r.Replay(func(rec journal.Record) error {
		res.RecordCount++
		if rec.Seq > res.LastSeq {
			res.LastSeq = rec.Seq
		}
		if handler != nil {
			return handler(entry.ID, rec)
		}
		return nil
	})
	if err != nil {
		res.Err = err
	}
	return res
}

// bumpIDPast advances the hub's id counter past `id` if `id` is of
// the form `s-N` and N > the current counter. Reload-registered ids
// are sourced from the index; freshly-minted ids must not collide.
func (h *SessionHub) bumpIDPast(id string) {
	const prefix = "s-"
	if len(id) <= len(prefix) || id[:len(prefix)] != prefix {
		return
	}
	var n uint64
	for i := len(prefix); i < len(id); i++ {
		c := id[i]
		if c < '0' || c > '9' {
			return
		}
		n = n*10 + uint64(c-'0')
	}
	h.idMu.Lock()
	if n > h.idSeq {
		h.idSeq = n
	}
	h.idMu.Unlock()
}

// EmitDaemonReloaded broadcasts the daemon.reloaded event with the
// list of reloaded session ids. The daemon's startup glue calls this
// AFTER the WS listener is up so reconnecting clients see the event.
//
// The bus may be nil (in which case the call is a no-op), keeping the
// helper testable without a hub.Bus instance.
func EmitDaemonReloaded(bus *hub.Bus, sessionIDs []string) {
	if bus == nil {
		return
	}
	custom := map[string]any{
		"sessions": sessionIDs,
		"count":    len(sessionIDs),
	}
	bus.EmitAsync(&hub.Event{
		Type:   hub.EventDaemonReloaded,
		Custom: custom,
	})
}
