package clarifyq

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/cortex/lobes/llm"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/stream"
)

// fakeProvider is a minimal provider.Provider stub for the clarifyq
// tests. ChatStream returns a fixed Content slice on every call; counts
// the number of calls in callCount for assertions.
type fakeProvider struct {
	mu        sync.Mutex
	content   []provider.ResponseContent
	callCount atomic.Uint64
	failWith  error
}

func (f *fakeProvider) Name() string { return "fake-haiku" }

func (f *fakeProvider) Chat(req provider.ChatRequest) (*provider.ChatResponse, error) {
	return f.ChatStream(req, nil)
}

func (f *fakeProvider) ChatStream(req provider.ChatRequest, onEvent func(stream.Event)) (*provider.ChatResponse, error) {
	f.callCount.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return nil, f.failWith
	}
	out := make([]provider.ResponseContent, len(f.content))
	copy(out, f.content)
	return &provider.ChatResponse{
		Model:      req.Model,
		StopReason: "end_turn",
		Content:    out,
	}, nil
}

// makeToolUse builds a queue_clarifying_question tool_use ResponseContent
// block carrying the supplied question text and metadata.
func makeToolUse(question, category, rationale string, blocking bool) provider.ResponseContent {
	return provider.ResponseContent{
		Type: "tool_use",
		Name: clarifyToolName,
		ID:   "tu-" + question,
		Input: map[string]any{
			"question":  question,
			"category":  category,
			"blocking":  blocking,
			"rationale": rationale,
		},
	}
}

// waitForNotes blocks until the workspace contains at least n notes from
// the supplied LobeID, or until timeout fires. Returns the matching
// notes; the test asserts on the slice.
func waitForLobeNotes(t *testing.T, ws *cortex.Workspace, lobeID string, n int, timeout time.Duration) []cortex.Note {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var match []cortex.Note
		for _, note := range ws.Snapshot() {
			if note.LobeID == lobeID {
				match = append(match, note)
			}
		}
		if len(match) >= n {
			return match
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d notes from %s; have %d", n, lobeID, len(filterByLobe(ws.Snapshot(), lobeID)))
	return nil
}

func filterByLobe(notes []cortex.Note, lobeID string) []cortex.Note {
	var out []cortex.Note
	for _, n := range notes {
		if n.LobeID == lobeID {
			out = append(out, n)
		}
	}
	return out
}


// TestClarifyingQLobe_CapsAtThreeOutstanding covers the cap-at-3 half
// of TASK-24: when the model returns 5 tool_use blocks in a single
// response, only the first 3 become Notes. The remaining 2 are
// silently dropped per spec ("drop overflow tool calls silently").
func TestClarifyingQLobe_CapsAtThreeOutstanding(t *testing.T) {
	t.Parallel()

	bus := hub.New()
	ws := cortex.NewWorkspace(hub.New(), nil)
	fp := &fakeProvider{
		content: []provider.ResponseContent{
			makeToolUse("Q1: which env?", "scope", "ambiguous deploy target", true),
			makeToolUse("Q2: which file?", "scope", "ambiguous edit target", true),
			makeToolUse("Q3: which test?", "scope", "ambiguous test target", true),
			makeToolUse("Q4: which metric?", "constraint", "ambiguous perf goal", false),
			makeToolUse("Q5: which user?", "data", "ambiguous user identity", false),
		},
	}

	l := NewClarifyingQLobe(fp, llm.NewEscalator(false), ws, bus)
	// Run() once to install the subscribers.
	if err := l.Run(context.Background(), cortex.LobeInput{}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	emitUserMessageForTest(bus,"deploy and ship the thing")

	notes := waitForLobeNotes(t, ws, l.ID(), 3, 2*time.Second)
	if len(notes) != 3 {
		t.Fatalf("expected exactly 3 clarifying-q Notes (cap), got %d", len(notes))
	}
	if got := l.OutstandingCount(); got != 3 {
		t.Errorf("OutstandingCount = %d, want 3", got)
	}
	if got := fp.callCount.Load(); got != 1 {
		t.Errorf("provider call count = %d, want 1 (one call per user turn)", got)
	}

	// Verify the first 3 questions are the ones that landed (FIFO order
	// from the model's tool_use list).
	wantTitles := []string{
		"Q1: which env?",
		"Q2: which file?",
		"Q3: which test?",
	}
	for i, want := range wantTitles {
		if notes[i].Title != want {
			t.Errorf("Note[%d].Title = %q, want %q", i, notes[i].Title, want)
		}
	}
}

// TestClarifyingQLobe_NoQuestionsWhenNotAmbiguous covers the second
// half of TASK-24: if the model returns text-only content (no tool_use
// blocks), the Lobe publishes zero Notes. This is the
// "no_ambiguity" idle path from spec §5.
func TestClarifyingQLobe_NoQuestionsWhenNotAmbiguous(t *testing.T) {
	t.Parallel()

	bus := hub.New()
	ws := cortex.NewWorkspace(hub.New(), nil)
	fp := &fakeProvider{
		content: []provider.ResponseContent{
			{Type: "text", Text: "no_ambiguity"},
		},
	}

	l := NewClarifyingQLobe(fp, llm.NewEscalator(false), ws, bus)
	if err := l.Run(context.Background(), cortex.LobeInput{}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	emitUserMessageForTest(bus,"thanks for your help!")

	// Brief wait so the subscriber goroutine settles. We expect zero
	// notes; we sleep a small amount and then assert.
	time.Sleep(50 * time.Millisecond)

	if notes := filterByLobe(ws.Snapshot(), l.ID()); len(notes) != 0 {
		t.Errorf("expected 0 clarifying-q Notes, got %d: %+v", len(notes), notes)
	}
	if got := l.OutstandingCount(); got != 0 {
		t.Errorf("OutstandingCount = %d, want 0", got)
	}
	if got := fp.callCount.Load(); got != 1 {
		t.Errorf("provider call count = %d, want 1", got)
	}
}
