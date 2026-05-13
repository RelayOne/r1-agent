// Package promptguard — phase_action.go
//
// Per-phase Action resolver. The daemon initializes this once at
// startup from the operator's PromptGuard policy block (see
// internal/config/policy.go). Wire-sites (plan/feasibility,
// workflow execute reads, verify reviewer assembly, convergence
// judge) consult it instead of threading the policy through every
// function signature.
//
// Spec defaults (specs/promptguard-hardening.md §T1):
//   - plan        → ActionStrip
//   - execute     → ActionWarn
//   - verify      → ActionStrip
//   - convergence → ActionStrip
//
// Operators override per-phase via the PromptGuard block in
// r1.policy.yaml.
package promptguard

import (
	"strings"
	"sync"
)

var (
	phaseActionMu sync.RWMutex
	phaseActions  = defaultPhaseActions()
)

// defaultPhaseActions returns the spec-mandated default action for each
// governance gate.
func defaultPhaseActions() map[string]Action {
	return map[string]Action{
		"plan":        ActionStrip,
		"execute":     ActionWarn,
		"verify":      ActionStrip,
		"convergence": ActionStrip,
	}
}

// PhaseAction returns the configured Action for the named phase, or
// the spec default if the phase is not explicitly configured. Unknown
// phase names fall through to ActionWarn (the safest non-mutating
// disposition) so an unrecognized phase name does not silently drop
// content.
func PhaseAction(phase string) Action {
	phaseActionMu.RLock()
	defer phaseActionMu.RUnlock()
	if a, ok := phaseActions[strings.ToLower(phase)]; ok {
		return a
	}
	return ActionWarn
}

// SetPhaseAction installs an explicit Action for one phase. Pass an
// empty phase to reset all phases to defaults.
func SetPhaseAction(phase string, action Action) {
	phaseActionMu.Lock()
	defer phaseActionMu.Unlock()
	if phase == "" {
		phaseActions = defaultPhaseActions()
		return
	}
	phaseActions[strings.ToLower(phase)] = action
}

// ResetPhaseActions restores every phase to its spec-default action.
// Exposed for tests so they can clean up after manipulating
// SetPhaseAction.
func ResetPhaseActions() {
	phaseActionMu.Lock()
	defer phaseActionMu.Unlock()
	phaseActions = defaultPhaseActions()
}

// String implements fmt.Stringer so log/JSON callers see a
// human-readable name instead of the raw int constant. Added in T1
// (spec §T1) for operator-facing policy diagnostics.
func (a Action) String() string {
	return actionName(a)
}

// ParseAction maps a human-readable action string ("warn"|"strip"|
// "reject") to an Action. Returns the second-return as false when the
// string is not one of the three accepted values — callers should fall
// back to the per-phase default in that case.
func ParseAction(s string) (Action, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "warn":
		return ActionWarn, true
	case "strip":
		return ActionStrip, true
	case "reject":
		return ActionReject, true
	}
	return ActionWarn, false
}
