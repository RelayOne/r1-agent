// workspace_methods_pending.go is the TASK-5 hand-off file. The three
// receiver methods declared here (Snapshot, UnresolvedCritical, Drain)
// are intentionally minimal: they return zero values so that TASK-8's
// WorkspaceReader adapter compiles, vets, and tests cleanly without
// pulling forward TASK-5's storage-layer changes.
//
// TASK-5 OWNS THIS FILE. When TASK-5 lands it deletes this file in the
// same commit that introduces the real Snapshot / UnresolvedCritical /
// Drain bodies on *Workspace inside workspace.go. Do not fold the bodies
// here -- the deletion is what marks TASK-5 complete.
package cortex

// Snapshot returns the unresolved-Note view of the Workspace. TASK-5
// will replace this body with the real RLock + copy-and-sort
// implementation backed by w.notes.
func (w *Workspace) Snapshot() []Note { return nil }

// UnresolvedCritical returns the SevCritical subset of Snapshot that has
// no resolving Note. TASK-5 will replace this body with the real filter
// over w.notes; the result drives PreEndTurnCheckFn in TASK-9.
func (w *Workspace) UnresolvedCritical() []Note { return nil }

// Drain returns every Note with Round >= sinceRound and advances the
// internal drained-up-to cursor. TASK-5 will replace this body with the
// real cursor-aware implementation; MidturnCheckFn calls it to format
// the supervisor injection block.
func (w *Workspace) Drain(sinceRound uint64) ([]Note, uint64) { return nil, 0 }
