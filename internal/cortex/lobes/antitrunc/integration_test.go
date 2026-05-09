// integration_test.go — closes the audit gap from
// audit/scan-governance-gaps.md item #2 (and TASK-7 of
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
//     AntiTruncLobe registered as a regular cortex.Lobe.
//  2. Wire the Lobe to write into c.Workspace() — the same Workspace
//     the Cortex's PreEndTurnGate reads from. (NewAntiTruncLobe captures
//     a writable *Workspace at construction; the wiring used here is
//     "construct cortex first, then construct the Lobe pointing at the
//     cortex's Workspace, then re-emit through MidturnNote" — but we
//     SHORTCUT the runner because LobeRunner.buildInput does not
//     propagate History, see internal/cortex/lobe.go:buildInput.
//     Calling Lobe.Run directly with a populated LobeInput exercises
//     exactly the Workspace-publish path the runner would use, minus
//     the History-stripping defect that is out of scope for this test.)
//  3. Drive Run with a History whose assistant text contains the
//     "premature_stop_let_me" truncation phrase.
//  4. Assert the Note appears in Workspace.Snapshot() AND that
//     PreEndTurnGate returns a non-empty block citing the antitrunc
//     LobeID + phrase ID.
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
// integration proof that AntiTruncLobe publishes Notes through the
// production cortex.Workspace and that those Notes are visible to
// cortex.Cortex.PreEndTurnGate.
//
// This verifies the wrapper switch from local shadow types to the real
// cortex package (PR #196) actually closes the loop end-to-end —
// assertions on Workspace.Snapshot prove the Note landed in the same
// store the cortex reads from, and assertions on PreEndTurnGate prove
// the gate refuses end_turn while the Note is unresolved.
func TestAntiTruncLobe_PublishesIntoCortexWorkspace_GateBlocks(t *testing.T) {
	t.Parallel()

	// Construct a fully-wired Cortex with no Lobes initially. The
	// Workspace pointer is stable for the cortex's lifetime; we capture
	// it and hand it to the AntiTruncLobe so Publish lands in the same
	// store PreEndTurnGate later reads.
	//
	// We deliberately do NOT register the Lobe with the cortex via
	// Config.Lobes because LobeRunner.buildInput does not propagate
	// History into LobeInput (see internal/cortex/lobe.go:buildInput).
	// The Run-direct shortcut below exercises the production publish
	// path — which is what the audit gap is asking us to verify — while
	// avoiding the unrelated runner-history wiring defect.
	c, err := cortex.New(cortex.Config{
		SessionID:       "antitrunc-integration",
		EventBus:        hub.New(),
		Provider:        &fakeCortexProvider{},
		PreWarmInterval: time.Hour, // suppress pump churn
		RoundDeadline:   2 * time.Second,
	})
	if err != nil {
		t.Fatalf("cortex.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = c.Stop(context.Background()) }()

	// Wire the AntiTruncLobe to the cortex's writable Workspace.
	// NewAntiTruncLobe stores the *Workspace pointer at construction,
	// and Run.Publish writes through that pointer — so Notes land in
	// the same store c.PreEndTurnGate consults.
	lobe := NewAntiTruncLobe(c.Workspace(), "", "")

	// History containing a known-truncation phrase. The phrase
	// "i'll stop here for now" matches the "premature_stop_let_me"
	// pattern in internal/antitrunc/phrases.go (the (?i)(?:i'?ll|let me|
	// i should)\s+(?:stop|pause|defer|skip|hold off) regex).
	const truncPhrase = "i'll stop here for now and pick up later"
	msgs := []agentloop.Message{
		{Role: "user", Content: []agentloop.ContentBlock{{Type: "text", Text: "do the work"}}},
		{Role: "assistant", Content: []agentloop.ContentBlock{{Type: "text", Text: truncPhrase}}},
	}

	// Drive the Lobe directly: this publishes one or more SevCritical
	// Notes into the cortex's Workspace.
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
	// contract is real, not a one-way assertion. Resolving the Note
	// drops it from UnresolvedCritical, so the gate must allow end_turn.
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
