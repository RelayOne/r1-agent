package clarifyq

import (
	"context"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/cortex/lobes/llm"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/provider"
)

// TestClarifyingQLobe_ResolvesOnUserAnswer covers TASK-25.
//
// Drives one user message that produces one queue_clarifying_question
// tool_use, captures the assigned question_id, then emits a
// cortex.user.answered_question event with that question_id and asserts
// the Lobe publishes a resolving Note (Resolves==originalNoteID).
//
// The test pins newQuestionID to a deterministic value so the test
// does not have to scrape Note.Meta to learn the question_id; the
// helper restores the original factory on cleanup.
func TestClarifyingQLobe_ResolvesOnUserAnswer(t *testing.T) {
	// NOT t.Parallel(): this test pins the package-level newQuestionID
	// factory for deterministic question IDs. Other tests in this
	// package read newQuestionID concurrently, so swapping it under
	// t.Parallel() races. The serialization cost is < 100ms.

	// Pin question_id for deterministic assertion. Restore on cleanup.
	origFactory := newQuestionID
	t.Cleanup(func() { newQuestionID = origFactory })
	const fixedQID = "Q1-test"
	newQuestionID = func() string { return fixedQID }

	bus := hub.New()
	ws := cortex.NewWorkspace(hub.New(), nil)
	fp := &fakeProvider{
		content: []provider.ResponseContent{
			makeToolUse("which env should we deploy to?", "scope", "ambiguous deploy target", true),
		},
	}

	l := NewClarifyingQLobe(fp, llm.NewEscalator(false), ws, bus)
	if err := l.Run(context.Background(), cortex.LobeInput{}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Step 1: drive user-message → 1 question Note.
	emitUserMessageForTest(bus, "deploy the thing")
	notes := waitForLobeNotes(t, ws, l.ID(), 1, 2*time.Second)
	if len(notes) != 1 {
		t.Fatalf("expected exactly 1 question Note before resolution, got %d", len(notes))
	}
	originalNoteID := notes[0].ID
	if originalNoteID == "" {
		t.Fatal("original Note has empty ID")
	}
	// Sanity check: question_id stamped in Meta.
	if got, _ := notes[0].Meta[metaQuestionID].(string); got != fixedQID {
		t.Errorf("Meta[question_id] = %q, want %q", got, fixedQID)
	}
	if got := l.OutstandingCount(); got != 1 {
		t.Errorf("OutstandingCount before resolve = %d, want 1", got)
	}

	// Step 2: emit cortex.user.answered_question with the question_id.
	emitAnsweredQuestionForTest(bus, fixedQID)

	// Step 3: wait for the resolving Note to land. We expect 2 Notes
	// total: the original question + the resolution.
	resolved := waitForLobeNotes(t, ws, l.ID(), 2, 2*time.Second)
	if len(resolved) != 2 {
		t.Fatalf("expected 2 Notes (question + resolution), got %d", len(resolved))
	}

	// The second Note must Resolve the first.
	resolutionNote := resolved[1]
	if resolutionNote.Resolves != originalNoteID {
		t.Errorf("resolution Note Resolves = %q, want %q",
			resolutionNote.Resolves, originalNoteID)
	}
	if resolutionNote.Severity != cortex.SevInfo {
		t.Errorf("resolution Note Severity = %v, want SevInfo", resolutionNote.Severity)
	}
	if got := l.OutstandingCount(); got != 0 {
		t.Errorf("OutstandingCount after resolve = %d, want 0", got)
	}

	// Step 4: confirm the original Note is now considered resolved by
	// the Workspace (UnresolvedCritical filters out resolved IDs;
	// here we exercise the same predicate by walking the snapshot).
	resolvedIDs := map[string]bool{}
	for _, n := range ws.Snapshot() {
		if n.Resolves != "" {
			resolvedIDs[n.Resolves] = true
		}
	}
	if !resolvedIDs[originalNoteID] {
		t.Errorf("original Note %q should be in resolved set, got %v",
			originalNoteID, resolvedIDs)
	}
}

// TestClarifyingQLobe_ResolveUnknownQuestionID covers the negative path
// of TASK-25: an answered_question event whose question_id is not
// outstanding is logged + dropped without publishing a resolution Note.
func TestClarifyingQLobe_ResolveUnknownQuestionID(t *testing.T) {
	t.Parallel()

	bus := hub.New()
	ws := cortex.NewWorkspace(hub.New(), nil)

	l := NewClarifyingQLobe(nil, llm.NewEscalator(false), ws, bus)
	if err := l.Run(context.Background(), cortex.LobeInput{}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	emitAnsweredQuestionForTest(bus, "does-not-exist")

	// Brief wait so the subscriber goroutine settles.
	time.Sleep(50 * time.Millisecond)

	if notes := filterByLobe(ws.Snapshot(), l.ID()); len(notes) != 0 {
		t.Errorf("expected 0 Notes for unknown question_id, got %d", len(notes))
	}
}
