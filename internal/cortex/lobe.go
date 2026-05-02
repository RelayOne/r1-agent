package cortex

import (
	"context"

	"github.com/RelayOne/r1/internal/agentloop"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/provider"
)

// Lobe is the parallel-cognition specialist. Implementations run in a
// dedicated goroutine; they read message history (read-only) and write
// Notes via Workspace.Publish.
//
// Lobe contract:
//   - Run MUST observe ctx.Done(); return nil on graceful shutdown.
//   - Run MAY be called multiple times across daemon restarts; state is
//     externalized to Workspace + bus.WAL.
//   - Run MUST be panic-safe; a Lobe panic is logged + recovered + emits
//     hub.Event{Type:"cortex.lobe.panic"} but does NOT bring down the loop.
//
// Lobes do NOT implement persistence themselves — the runner handles it.
type Lobe interface {
	ID() string          // stable; used as LobeID on Notes
	Description() string // human-readable, for /status
	Kind() LobeKind      // Deterministic | LLM
	Run(ctx context.Context, in LobeInput) error
}

// LobeKind drives semaphore acquisition: LLM Lobes bind against
// LobeSemaphore; Deterministic Lobes run free.
type LobeKind int

const (
	KindDeterministic LobeKind = iota
	KindLLM
)

// LobeInput is the read-only context handed to each Lobe per Round.
type LobeInput struct {
	Round     uint64
	History   []agentloop.Message // current conversation; deep-copied
	Workspace WorkspaceReader     // read-only Workspace handle
	Provider  provider.Provider   // model client (Lobes use as needed)
	Bus       *hub.Bus            // for emitting status events
}

// WorkspaceReader is the read-only subset Lobes get. Forces the contract
// "Lobes WRITE only via Publish; everything else is read-only".
type WorkspaceReader interface {
	Snapshot() []Note
	UnresolvedCritical() []Note
}

// workspaceReader is the private adapter that wraps a *Workspace and
// exposes only the read-only subset declared by WorkspaceReader. Keeping
// this type unexported enforces the spec invariant that Lobes cannot
// reach Workspace.Publish through type assertions.
type workspaceReader struct {
	w *Workspace
}

// Snapshot delegates to (*Workspace).Snapshot.
func (r workspaceReader) Snapshot() []Note { return r.w.Snapshot() }

// UnresolvedCritical delegates to (*Workspace).UnresolvedCritical.
func (r workspaceReader) UnresolvedCritical() []Note { return r.w.UnresolvedCritical() }

// WorkspaceReaderFor wraps a *Workspace in the read-only adapter so
// callers (Cortex, LobeRunner) can hand a WorkspaceReader to Lobes
// without exposing Publish or any other write-side method.
func WorkspaceReaderFor(w *Workspace) WorkspaceReader {
	return workspaceReader{w: w}
}
