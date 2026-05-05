package rulecheck

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/hub"
)

// newTestBus opens a fresh durable bus.Bus rooted in t.TempDir(). The
// caller is responsible for closing it; tests that exercise restart
// re-open the same dir. Mirrors the helper in walkeeper/lobe_test.go.
func newTestBus(t *testing.T) *bus.Bus {
	t.Helper()
	dir := t.TempDir()
	b, err := bus.New(dir)
	if err != nil {
		t.Fatalf("bus.New: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

// runLobe starts the Lobe in a background goroutine and returns a
// cancel function that tears it down and waits for the Run goroutine to
// exit. Used by every test that exercises the live subscriber path.
func runLobe(t *testing.T, l *RuleCheckLobe) (cancel func()) {
	t.Helper()
	ctx, c := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = l.Run(ctx, cortex.LobeInput{})
	}()
	// Yield once so the bus.Subscribe call inside Run finishes
	// registering before the test starts publishing events.
	time.Sleep(20 * time.Millisecond)
	return func() {
		c()
		<-done
	}
}

// publishRuleFired publishes a synthetic supervisor.rule.fired event on
// the durable bus with the supplied rule name and rationale. Returns
// the event id so callers can correlate downstream Notes.
func publishRuleFired(t *testing.T, b *bus.Bus, ruleName, rationale string) string {
	t.Helper()
	pl, err := json.Marshal(ruleFiredPayload{
		SupervisorID:   "test-supervisor",
		SupervisorType: "branch",
		RuleName:       ruleName,
		RulePriority:   100,
		TriggerEventID: "trig-1",
		TriggerType:    "worker.action.completed",
		Rationale:      rationale,
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	evt := bus.Event{
		Type:    bus.EvtSupervisorRuleFired,
		Payload: pl,
	}
	if err := b.Publish(evt); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	return evt.ID
}

// waitForNote polls ws.Snapshot up to the deadline and returns the
// first Note matching pred, or fails the test.
func waitForNote(t *testing.T, ws *cortex.Workspace, pred func(cortex.Note) bool) cortex.Note {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, n := range ws.Snapshot() {
			if pred(n) {
				return n
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("note matching predicate did not arrive within 2s")
	return cortex.Note{}
}

// TestRuleCheckLobe_SubscribesToFiredPattern asserts that Run registers
// a subscriber for bus.EvtSupervisorRuleFired by exercising the
// behaviour: emit a matching event, observe a Note land in the
// Workspace. This is a stronger assertion than introspecting bus
// internals (which the bus does not expose).
func TestRuleCheckLobe_SubscribesToFiredPattern(t *testing.T) {
	t.Parallel()
	durable := newTestBus(t)
	ws := cortex.NewWorkspace(hub.New(), nil)

	l := NewRuleCheckLobe(durable, ws)
	if l.ID() != "rule-check" {
		t.Errorf("ID() = %q, want %q", l.ID(), "rule-check")
	}
	if l.Kind() != cortex.KindDeterministic {
		t.Errorf("Kind() = %v, want KindDeterministic", l.Kind())
	}

	stop := runLobe(t, l)
	defer stop()

	publishRuleFired(t, durable, "trust.completion_requires_second_opinion", "second opinion missing")

	got := waitForNote(t, ws, func(n cortex.Note) bool {
		return n.LobeID == "rule-check"
	})
	if got.Severity != cortex.SevCritical {
		t.Errorf("expected SevCritical for trust.* rule, got %q", got.Severity)
	}
	if got.Body != "second opinion missing" {
		t.Errorf("Body = %q, want rationale", got.Body)
	}
}

// TestRuleCheckLobe_NilDurableIsNoop verifies that a Lobe constructed
// with a nil bus still observes context cancellation and returns
// without registering any subscription — the LobeRunner contract.
func TestRuleCheckLobe_NilDurableIsNoop(t *testing.T) {
	t.Parallel()
	l := NewRuleCheckLobe(nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx, cortex.LobeInput{}) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run with nil bus returned %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run with nil bus did not exit on ctx.Done")
	}
}
