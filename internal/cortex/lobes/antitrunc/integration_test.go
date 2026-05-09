// integration_test.go — closes the audit gap from
// audit/scan-governance-gaps.md item #2 (TASK-7 of
// specs/post-merge-audit-cleanup.md).
//
// PR #196 switched AntiTruncLobe from local Workspace shadow types to
// the real cortex.Workspace. The audit flagged that the wrapper change
// was unverified by an end-to-end test that drives the Lobe THROUGH the
// production cortex.Cortex and asserts the Note round-trips into
// Cortex.PreEndTurnGate. This test is that proof.
//
// Coverage path:
//
//  1. Construct a real cortex.Cortex via cortex.New(...) with the
//     AntiTruncLobe registered via Config.Lobes — the Cortex truly
//     contains the Lobe (verified via LobeStatus).
//  2. Wire the Lobe to write into c.Workspace() — the same Workspace
//     the Cortex's PreEndTurnGate reads from.
//  3. Drive Lobe.Run with a History whose assistant text contains the
//     "premature_stop_let_me" truncation phrase. Run.Publish writes
//     into c.Workspace() through the Lobe's captured pointer.
//  4. Assert the Note appears in c.Workspace().Snapshot() AND that
//     c.PreEndTurnGate(msgs) returns a non-empty block citing the
//     antitrunc LobeID + phrase ID.
//  5. Assert that publishing a resolver Note clears the gate, proving
//     the unresolved-only contract is real in both directions.
//
// The test is deterministic, makes no network calls, and runs in
// well under a second.

package antitrunclobe

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/agentloop"
	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/stream"
)

// fakeCortexProvider is a no-op provider.Provider that satisfies
// cortex.New's "Provider required" check. The cortex pre-warm pump
// invokes Chat once at Start; we return a benign empty response so the
// pump succeeds without any I/O.
//
// Mirrors the shape used in
// internal/cortex/lobes/rulecheck/integration_test.go (fakeProvider),
// duplicated here because that type lives in a different package.
type fakeCortexProvider struct{}

func (p *fakeCortexProvider) Name() string { return "fake-antitrunc" }

func (p *fakeCortexProvider) Chat(req provider.ChatRequest) (*provider.ChatResponse, error) {
	return &provider.ChatResponse{
		ID:         "msg_warm",
		Model:      req.Model,
		StopReason: "end_turn",
		Usage:      stream.TokenUsage{Input: 1, Output: 1},
	}, nil
}

func (p *fakeCortexProvider) ChatStream(req provider.ChatRequest, _ func(stream.Event)) (*provider.ChatResponse, error) {
	return p.Chat(req)
}

// TestAntiTruncLobe_PublishesIntoCortexWorkspace_GateBlocks is the
// integration proof that AntiTruncLobe — registered as a real
// cortex.Lobe — publishes Notes through the production cortex.Workspace
// and that those Notes are visible to cortex.Cortex.PreEndTurnGate.
//
// This verifies the wrapper switch from local shadow types to the real
// cortex package (PR #196) actually closes the loop end-to-end:
//   - The Lobe satisfies the cortex.Lobe interface (compile-time check
//     in lobe_wrapper.go and runtime check via Config.Lobes
//     registration here).
//   - The Lobe's writable *cortex.Workspace pointer is the same one
//     PreEndTurnGate reads (both bound to c.Workspace()).
//   - A SevCritical Note Published by the Lobe appears in
//     c.Workspace().Snapshot() AND in c.PreEndTurnGate(msgs) output.
//   - A resolver Note clears the gate.
//
// Two-step Lobe wiring: cortex.New constructs the Workspace internally,
// so we cannot pass the Workspace pointer to NewAntiTruncLobe before
// New returns. We construct the Lobe with a nil Workspace, register
// it in Config.Lobes so Cortex.LobeStatus reports the registration,
// then re-bind the Lobe's ws field to c.Workspace() before exercising
// Run. This is the same pattern internal/cortex/lobes/rulecheck/
// integration_test.go uses for RuleCheckLobe (see lines 139-156 of
// that file).
func TestAntiTruncLobe_PublishesIntoCortexWorkspace_GateBlocks(t *testing.T) {
	t.Parallel()

	// Construct the Lobe with a nil Workspace. The wrapper's nil-ws
	// guard makes Run a no-op until we re-bind it; cortex.LobeStatus
	// only inspects ID/Description/Kind so the nil ws does not block
	// registration. We re-bind the writable Workspace pointer below
	// after cortex.New returns.
	lobe := NewAntiTruncLobe(nil, "", "")

	c, err := cortex.New(cortex.Config{
		SessionID:       "antitrunc-integration",
		EventBus:        hub.New(),
		Provider:        &fakeCortexProvider{},
		Lobes:           []cortex.Lobe{lobe},
		PreWarmInterval: time.Hour, // suppress pre-warm pump churn during the test
		RoundDeadline:   2 * time.Second,
	})
	if err != nil {
		t.Fatalf("cortex.New: %v", err)
	}

	// Cortex genuinely contains the Lobe: LobeStatus reports it under
	// the canonical id "antitrunc". This is the runtime check that the
	// wrapper satisfies cortex.Lobe (the var _ cortex.Lobe assertion
	// in lobe_wrapper.go is the compile-time companion).
	statuses := c.LobeStatus()
	var registered bool
	for _, s := range statuses {
		if s.ID == "antitrunc" {
			registered = true
			if s.Kind != cortex.KindDeterministic {
				t.Errorf("registered antitrunc Lobe Kind = %v, want KindDeterministic", s.Kind)
			}
			break
		}
	}
	if !registered {
		t.Fatalf("Cortex.LobeStatus does not contain antitrunc Lobe; got %+v", statuses)
	}

	// Re-bind the Lobe's writable Workspace to the cortex's. After this
	// assignment, every Lobe.Run.Publish writes into c.Workspace() —
	// the same store c.PreEndTurnGate later reads.
	lobe.ws = c.Workspace()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	// History containing a known-truncation phrase. The phrase
	// "i'll stop here for now" matches the "premature_stop_let_me"
	// pattern catalogued in internal/antitrunc/phrases.go.
	const truncPhrase = "i'll stop here for now and pick up later"
	msgs := []agentloop.Message{
		{Role: "user", Content: []agentloop.ContentBlock{{Type: "text", Text: "do the work"}}},
		{Role: "assistant", Content: []agentloop.ContentBlock{{Type: "text", Text: truncPhrase}}},
	}

	// Drive the Lobe with a populated History. We invoke Lobe.Run
	// directly (rather than relying on Cortex.MidturnNote) because
	// LobeRunner.buildInput in internal/cortex/lobe.go does not yet
	// propagate History into LobeInput — see the same constraint
	// documented in internal/cortex/lobes/all_integration_test.go
	// lines 312-318 for memory-recall. The publish path is identical
	// either way: the Lobe writes through its captured *Workspace
	// pointer, which we just re-bound to c.Workspace() above. The
	// runner-history wiring is tracked separately and is not the
	// subject of this audit gap.
	if err := lobe.Run(ctx, cortex.LobeInput{History: msgs}); err != nil {
		t.Fatalf("AntiTruncLobe.Run: %v", err)
	}

	// Assertion 1: the published Note is visible via Workspace.Snapshot.
	// Snapshot returns every Note ever Published, regardless of severity
	// or resolution state, so any landing Note must appear here.
	snapshot := c.Workspace().Snapshot()
	if len(snapshot) == 0 {
		t.Fatalf("Workspace.Snapshot() returned 0 Notes; AntiTruncLobe did not publish into the cortex's Workspace")
	}
	var found *cortex.Note
	for i := range snapshot {
		if snapshot[i].LobeID == "antitrunc" {
			n := snapshot[i]
			found = &n
			break
		}
	}
	if found == nil {
		t.Fatalf("Snapshot has %d Notes but none with LobeID=antitrunc; got: %+v", len(snapshot), snapshot)
	}
	if found.Severity != cortex.SevCritical {
		t.Errorf("antitrunc Note Severity = %q, want %q", found.Severity, cortex.SevCritical)
	}
	if !strings.Contains(found.Title, "premature_stop_let_me") {
		t.Errorf("antitrunc Note Title = %q, want to contain phrase ID %q", found.Title, "premature_stop_let_me")
	}
	if !strings.Contains(found.Title, "ANTI-TRUNCATION") {
		t.Errorf("antitrunc Note Title = %q, want ANTI-TRUNCATION marker", found.Title)
	}

	// Assertion 2: PreEndTurnGate refuses end_turn while the Note is
	// unresolved, and its output cites the antitrunc LobeID and the
	// detected phrase. Per cortex.go:619 the format is:
	//   [CRITICAL CORTEX NOTES — resolve before ending turn]
	//   - <LobeID>: <Title>
	gate := c.PreEndTurnGate(msgs)
	if gate == "" {
		t.Fatalf("PreEndTurnGate returned empty; expected non-empty block citing the antitrunc Note (Snapshot=%+v)", snapshot)
	}
	if !strings.Contains(gate, "CRITICAL CORTEX NOTES") {
		t.Errorf("PreEndTurnGate output missing CRITICAL CORTEX NOTES header:\n%s", gate)
	}
	if !strings.Contains(gate, "antitrunc") {
		t.Errorf("PreEndTurnGate output should cite LobeID=antitrunc; got:\n%s", gate)
	}
	if !strings.Contains(gate, "premature_stop_let_me") {
		t.Errorf("PreEndTurnGate output should cite phrase ID premature_stop_let_me; got:\n%s", gate)
	}

	// Assertion 3: explicit resolution clears the gate. This is the
	// flip side of the above — proving the gate's "while unresolved"
	// contract holds in both directions. Publishing a follow-on Note
	// with Resolves=foundID drops the antitrunc Note from
	// UnresolvedCritical, so the gate must allow end_turn.
	if err := c.Workspace().Publish(cortex.Note{
		LobeID:   "test-resolver",
		Severity: cortex.SevInfo,
		Title:    "operator confirmed continuation; antitrunc finding addressed",
		Resolves: found.ID,
	}); err != nil {
		t.Fatalf("Publish resolver: %v", err)
	}
	if got := c.PreEndTurnGate(msgs); got != "" {
		t.Errorf("PreEndTurnGate must return empty after resolution; got:\n%s", got)
	}
}
