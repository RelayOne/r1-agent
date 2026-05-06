package workflow

// skill_scope_closer.go — production caller for skilltracker.CloseScope.
// Spec: skill-aware-compactor.md.
//
// When a workflow phase exits — whether by normal completion or by
// abort — every skill that was loaded into that phase's task scope
// should be unloaded with Reason="scope_exit". This thin wrapper is
// the surface a phase machine calls; the tracker handles the actual
// SkillUnloaded ledger emission.
//
// Wiring path:
//   workflow.PhaseRunner.OnPhaseExit
//     └── workflow.SkillScopeCloser.OnPhaseExit
//           └── skilltracker.Tracker.CloseScope
//                 └── (n × builtin.EmitSkillUnloaded)

import (
	"context"

	"github.com/RelayOne/r1/internal/skilltracker"
)

// SkillScopeCloser fires Tracker.CloseScope when a phase / task DAG
// scope ends. Holds no state of its own — pure pass-through to the
// tracker.
type SkillScopeCloser struct {
	tracker *skilltracker.Tracker
}

// NewSkillScopeCloser binds a closer to the supplied tracker. nil
// tracker is allowed and turns every method into a no-op (matches
// the rest of the skilltracker surface so feature-flagged callers
// can keep the same call site).
func NewSkillScopeCloser(t *skilltracker.Tracker) *SkillScopeCloser {
	return &SkillScopeCloser{tracker: t}
}

// OnPhaseExit drops every skill loaded into (stanceID, taskScope)
// and emits SkillUnloaded(reason=scope_exit) per drop. Empty
// stanceID OR empty taskScope is treated as "no scope to close" and
// returns nil — the closer is best-effort, never fatal to the
// phase machine.
//
// Returns the count of skills dropped (0 if scope had nothing) +
// the first emission error (if any). Subsequent emissions still
// fire even when one errors so the audit trail stays complete.
//
// Idempotent: repeating the call on the same scope returns 0
// because the tracker already cleared the bucket.
func (c *SkillScopeCloser) OnPhaseExit(ctx context.Context, stanceID, taskScope string) (int, error) {
	if c == nil || c.tracker == nil {
		return 0, nil
	}
	if stanceID == "" || taskScope == "" {
		return 0, nil
	}
	return c.tracker.CloseScope(ctx, stanceID, taskScope)
}

// Active reports whether the closer has a real tracker bound.
// Useful for debug logging.
func (c *SkillScopeCloser) Active() bool {
	return c != nil && c.tracker != nil
}
