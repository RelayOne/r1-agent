package supervisor

// hooks.go — Hook-driven veto path for the supervisor.
//
// R1-V1 audit Domain 9 P0 #1: prior to this change, the supervisor
// ONLY subscribed to the bus (Subscribe, see core.go:151). Subscribers
// observe events; they cannot veto them or inject side-effects in the
// publish path. The supervisor's "30 governance rules" were therefore
// advisory — every rule that decided "this should not have happened"
// could only emit a `supervisor.rule.fired` event after the fact.
//
// The bus already supports a privileged Hook surface:
//
//   bus.RegisterHook(Hook{ Authority: "supervisor", ... })
//
// Hooks fire INLINE on Publish before subscribers get the event, can
// inject follow-up events, pause/resume/spawn workers, and return
// errors that the bus surfaces via `bus.evt_hook_action_failed` /
// `bus.evt_hook_injection_failed`. (See bus/bus.go:565-585.)
//
// This file adds:
//
//  1. The HookRule optional interface — rules that want to gate (not
//     just observe) opt in by implementing it. Existing rules remain
//     pure observers.
//
//  2. RegisterHookRules — wires every rule that implements HookRule
//     into the bus as a privileged hook. Called from the supervisor
//     constructor / Start path; safe to call when no rules implement
//     the interface (no-op).
//
// The split is deliberate: not every rule should be a hook. Hooks are
// privileged AND synchronous — a hook that hangs blocks the publish
// path. Use the HookRule path only for governance rules where veto
// is the right authority model (snapshot protection, trust gate,
// consensus quorum, hierarchy enforcement). Advisory / observational
// rules stay on the Subscribe path (already wired in core.go:151).

import (
	"context"
	"fmt"
	"log"

	"github.com/RelayOne/r1/internal/bus"
)

// HookRule is the OPTIONAL interface a Rule may implement to be wired
// as a privileged bus hook (gate authority) rather than a passive
// subscriber (observe authority). Hooks fire on the publish path
// BEFORE subscribers and can return injectable HookActions or errors.
//
// Returning a nil HookAction with a nil error means "this hook has
// nothing to add for this event" — equivalent to a no-op subscriber
// match. Returning a non-nil HookAction may inject events, pause /
// resume / spawn workers, or otherwise gate the bus's downstream
// delivery.
//
// Hook rules MUST also satisfy the base Rule interface (Pattern,
// Priority, Evaluate, Action, Rationale, Name). The supervisor still
// runs Evaluate / Action on the subscribe path so hook rules emit the
// usual `supervisor.rule.fired` observability event AND get a chance
// to gate. Rules that want pure-gate semantics can no-op their
// subscribe-path Action(); rules that want pure-observe semantics
// simply don't implement HookRule.
type HookRule interface {
	Rule
	// HookAction is invoked synchronously by the bus on every event
	// matching the rule's Pattern, BEFORE subscriber delivery. Returning
	// a non-nil *bus.HookAction injects the requested side-effects into
	// the publish path. Returning an error surfaces as
	// `bus.evt_hook_action_failed` for observability.
	HookAction(ctx context.Context, evt bus.Event) (*bus.HookAction, error)
	// HookPriority controls firing order across hooks; higher priorities
	// fire first. When two hooks would inject conflicting actions, the
	// higher-priority hook wins.
	HookPriority() bus.HookPriority
}

// RegisterHookRules wires every registered rule that implements
// HookRule into the bus as a privileged supervisor hook. Idempotent
// in the sense that re-calling it after appending new rules registers
// only the new ones — but in practice it is invoked once per Start.
//
// Returns the number of hooks registered, plus a non-nil error if any
// individual hook registration failed (rare; only happens if the bus
// rejects the authority claim, which it should not when the caller is
// the supervisor itself).
func (s *Supervisor) RegisterHookRules(ctx context.Context) (int, error) {
	s.mu.Lock()
	rules := make([]Rule, len(s.rules))
	copy(rules, s.rules)
	s.mu.Unlock()

	registered := 0
	for _, r := range rules {
		hr, ok := r.(HookRule)
		if !ok {
			continue
		}
		// Capture loop variable for the closure.
		hookRule := hr
		hook := bus.Hook{
			Pattern:   hookRule.Pattern(),
			Priority:  hookRule.HookPriority(),
			Authority: "supervisor",
			Handler: func(handlerCtx context.Context, evt bus.Event) (*bus.HookAction, error) {
				// Run the rule's Evaluate first so hooks share the
				// same gate predicate as the subscribe path. A rule
				// whose Evaluate returns false is a no-op hook.
				fire, err := hookRule.Evaluate(handlerCtx, evt, s.ledger)
				if err != nil {
					return nil, fmt.Errorf("hook rule %s evaluate: %w", hookRule.Name(), err)
				}
				if !fire {
					return nil, nil
				}
				return hookRule.HookAction(handlerCtx, evt)
			},
		}
		if err := s.bus.RegisterHook(hook); err != nil {
			log.Printf("supervisor %s: register hook for rule %s: %v",
				s.config.ID, hookRule.Name(), err)
			return registered, fmt.Errorf("register hook %s: %w", hookRule.Name(), err)
		}
		registered++
	}
	return registered, nil
}
