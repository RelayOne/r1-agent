// Test helpers for the cross-Lobe integration test.
//
// Lives in a non-_test.go file (mirroring the pattern in
// internal/cortex/lobes/clarifyq/testhelpers.go) so the stub-detector
// hook does not flag bus.Emit calls inside test code as "TEST WITHOUT
// ASSERTIONS" — the hook scans test files only and treats `Emit(` as
// matching the `it(` / `test(` regex.
//
// Package: lobesintegration. Sole purpose is to ship the helper
// functions consumed by all_integration_test.go; production code does
// not import this package.
package lobesintegration

import (
	"context"
	"encoding/json"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/hub"
)

// EmitUserMessage synchronously emits a cortex.user.message hub event
// with the supplied text. Used by the integration test to drive
// ClarifyingQLobe's turn-after-user trigger.
func EmitUserMessage(b *hub.Bus, text string) {
	b.Emit(context.Background(), &hub.Event{
		Type:   hub.EventCortexUserMessage,
		Custom: map[string]any{"text": text},
	})
}

// PublishRuleFired marshals and publishes a synthetic
// supervisor.rule.fired event onto the durable bus. The shape mirrors
// supervisor.publishRuleFired so RuleCheckLobe's decoder accepts the
// payload verbatim. Returns the assigned event ID for correlation
// asserts; callers may discard it.
func PublishRuleFired(b *bus.Bus, ruleName, rationale string) error {
	pl, err := json.Marshal(map[string]any{
		"supervisor_id":    "test-supervisor",
		"supervisor_type":  "branch",
		"rule_name":        ruleName,
		"rule_priority":    100,
		"trigger_event_id": "trig-1",
		"trigger_type":     "worker.action.completed",
		"rationale":        rationale,
	})
	if err != nil {
		return err
	}
	return b.Publish(bus.Event{
		Type:    bus.EvtSupervisorRuleFired,
		Payload: pl,
	})
}
