package rulecheck

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/agentloop"
	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/cortex/lobes/llm"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/stream"
)

// fakeProvider is a minimal provider.Provider used by the integration
// test that constructs a real cortex.Cortex (which requires a non-nil
// Provider for pre-warm). The fake never touches the network — it
// returns a canned empty ChatResponse so cortex.Start's synchronous
// pre-warm pump succeeds without I/O.
//
// Mirrors the startStopProvider pattern in internal/cortex/cortex_test.go,
// duplicated here because that type lives in the cortex_test package
// and is not importable.
type fakeProvider struct{}

func (p *fakeProvider) Name() string { return "fake-rulecheck" }

func (p *fakeProvider) Chat(req provider.ChatRequest) (*provider.ChatResponse, error) {
	return &provider.ChatResponse{
		ID:         "msg_warm",
		Model:      req.Model,
		StopReason: "end_turn",
		Usage:      stream.TokenUsage{Input: 1, Output: 1},
	}, nil
}

func (p *fakeProvider) ChatStream(req provider.ChatRequest, _ func(stream.Event)) (*provider.ChatResponse, error) {
	return p.Chat(req)
}

// TestRuleCheckLobe_CriticalNotesAreSticky asserts that a Note produced
// from a trust.* rule firing carries the spec item-15 sticky marker
// (MetaExpiresAfterRound = 0) and remains in UnresolvedCritical across
// several Workspace rounds. Ticking the round counter forward must not
// auto-expire the Note — only an explicit resolution Note removes it.
func TestRuleCheckLobe_CriticalNotesAreSticky(t *testing.T) {
	t.Parallel()
	durable := newTestBus(t)
	ws := cortex.NewWorkspace(hub.New(), nil)

	l := NewRuleCheckLobe(durable, ws)
	stop := runLobe(t, l)
	defer stop()

	publishRuleFired(t, durable, "trust.fix_requires_second_opinion", "fix declared without independent review")

	got := waitForNote(t, ws, func(n cortex.Note) bool {
		return n.Severity == cortex.SevCritical
	})

	// Sticky marker: ExpiresAfterRound is stored as uint64(0) in Meta
	// per llm.MetaExpiresAfterRound. The exact zero value is what makes
	// the Note sticky — there is no future round number that would
	// auto-resolve it.
	rawExp, ok := got.Meta[llm.MetaExpiresAfterRound]
	if !ok {
		t.Fatalf("Note.Meta missing %q key; got keys: %v", llm.MetaExpiresAfterRound, metaKeys(got.Meta))
	}
	exp, ok := rawExp.(uint64)
	if !ok {
		t.Fatalf("Note.Meta[%q] is %T, want uint64", llm.MetaExpiresAfterRound, rawExp)
	}
	if exp != 0 {
		t.Errorf("Note.Meta[%q] = %d, want 0 (sticky)", llm.MetaExpiresAfterRound, exp)
	}

	// Action kind must mark the Note as a rule violation so the UI /
	// router can dispatch it through the right confirm pipeline.
	if got.Meta[llm.MetaActionKind] != "rule-violation" {
		t.Errorf("Note.Meta[%q] = %v, want %q", llm.MetaActionKind, got.Meta[llm.MetaActionKind], "rule-violation")
	}

	// Tag must encode the rule name per spec item 15.
	wantTag := "rule:trust.fix_requires_second_opinion"
	if !containsString(got.Tags, wantTag) {
		t.Errorf("Note.Tags = %v, want to contain %q", got.Tags, wantTag)
	}

	// Advance the workspace round counter several times; the Note must
	// remain in UnresolvedCritical because a sticky Note has no
	// expiration round.
	for r := uint64(2); r < 8; r++ {
		ws.SetRound(r)
		uc := ws.UnresolvedCritical()
		if !containsCritical(uc, got.ID) {
			t.Fatalf("after round=%d, UnresolvedCritical lost the sticky Note %q", r, got.ID)
		}
	}

	// Now publish an explicit resolver Note: the critical Note must
	// drop out of UnresolvedCritical (proving the only way to clear a
	// sticky Note is an explicit resolution, not a round bump).
	if err := ws.Publish(cortex.Note{
		LobeID:   "test-resolver",
		Severity: cortex.SevInfo,
		Title:    "second opinion provided",
		Resolves: got.ID,
	}); err != nil {
		t.Fatalf("Publish resolver: %v", err)
	}
	uc := ws.UnresolvedCritical()
	if containsCritical(uc, got.ID) {
		t.Errorf("UnresolvedCritical still contains %q after explicit resolution", got.ID)
	}
}

// TestRuleCheckLobe_PreEndTurnGate is the spec item-15 integration test:
// build a real cortex.Cortex, register the Lobe, emit a trust.* rule
// event on the durable bus, and assert PreEndTurnGate returns a
// non-empty block (the gate refuses end_turn until the critical Note is
// addressed).
func TestRuleCheckLobe_PreEndTurnGate(t *testing.T) {
	t.Parallel()
	durable := newTestBus(t)
	eventBus := hub.New()

	// Construct the Lobe FIRST so we can hand its Workspace handle to
	// the Cortex (the Lobe captures a writable *Workspace at
	// construction time; the Cortex.New path then uses the same
	// Workspace because we wire it post-construction).
	//
	// Cortex.New constructs its own Workspace internally. To make the
	// Lobe write into it, we construct the Lobe with a stub workspace
	// FIRST, then swap to c.Workspace() before Start. The runLobe
	// helper is not used here because the Cortex.Start manages the
	// Lobe's Run goroutine via LobeRunner.
	l := &RuleCheckLobe{durable: durable}

	c, err := cortex.New(cortex.Config{
		SessionID:       "rulecheck-integ",
		EventBus:        eventBus,
		Durable:         durable,
		Provider:        &fakeProvider{},
		Lobes:           []cortex.Lobe{l},
		PreWarmInterval: time.Hour, // suppress pump churn
		RoundDeadline:   2 * time.Second,
	})
	if err != nil {
		t.Fatalf("cortex.New: %v", err)
	}

	// Inject the cortex's writable Workspace into the Lobe so the Note
	// publication path lands in the same Workspace PreEndTurnGate reads.
	l.ws = c.Workspace()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = c.Stop(context.Background()) }()

	// LobeRunner only invokes lobe.Run on tick; for subscribe-and-hold
	// Lobes (rule-check is one — Run blocks on ctx.Done after
	// registering its bus subscription) the first tick starts the
	// subscription and the Run goroutine then never returns until the
	// runner's context is cancelled. Drive one MidturnNote round to
	// fire that initial tick.
	msgs := []agentloop.Message{
		{Role: "user", Content: []agentloop.ContentBlock{{Type: "text", Text: "hi"}}},
	}
	_ = c.MidturnNote(msgs, 0)

	// Yield once so the LobeRunner's Run goroutine reaches the bus
	// Subscribe call before we publish. The MidturnNote round above
	// also waits on the round barrier, but RoundDeadline of 2s lets
	// the Lobe's Run block past Wait — we just need the Subscribe to
	// be live, not for Run to have returned.
	time.Sleep(50 * time.Millisecond)

	publishRuleFired(t, durable, "trust.problem_requires_second_opinion", "problem declared without independent review")

	// Wait for the Note to land — bus delivery is async, as is the
	// LobeRunner driver, so we poll PreEndTurnGate.
	deadline := time.Now().Add(3 * time.Second)
	var gate string
	for time.Now().Before(deadline) {
		gate = c.PreEndTurnGate(msgs)
		if gate != "" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if gate == "" {
		t.Fatalf("PreEndTurnGate did not block within deadline; expected non-empty block citing the rule-check Note")
	}
	if !strings.Contains(gate, "rule-check") {
		t.Errorf("PreEndTurnGate output should mention LobeID \"rule-check\":\n%s", gate)
	}
	if !strings.Contains(gate, "trust.problem_requires_second_opinion") {
		t.Errorf("PreEndTurnGate output should mention rule name; got:\n%s", gate)
	}
}

// metaKeys returns the keys of m for diagnostic test failures.
func metaKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// containsString reports whether s contains target.
func containsString(s []string, target string) bool {
	for _, v := range s {
		if v == target {
			return true
		}
	}
	return false
}

// containsCritical reports whether any Note in s has the supplied ID.
func containsCritical(s []cortex.Note, id string) bool {
	for _, n := range s {
		if n.ID == id {
			return true
		}
	}
	return false
}
