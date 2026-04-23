package chat

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeGate is a synthetic descentGate for tests. It avoids the real
// `git status` + AC runner so tests don't need a worktree or a Go
// toolchain — both `ShouldFire` and `Run` return canned values.
type fakeGate struct {
	fire        bool
	changed     []string
	shouldErr   error
	verdict     ChatVerdict
	runErr      error
	ranWith     []string
	shouldFired int
	runs        int
}

func (f *fakeGate) ShouldFire(ctx context.Context) (bool, []string, error) {
	f.shouldFired++
	if f.shouldErr != nil {
		return false, nil, f.shouldErr
	}
	return f.fire, f.changed, nil
}

func (f *fakeGate) Run(ctx context.Context, changed []string) (ChatVerdict, error) {
	f.runs++
	f.ranWith = append([]string(nil), changed...)
	return f.verdict, f.runErr
}

// newSessionWithFakeGate returns a Session whose post-turn descent gate
// is the supplied fake. The mockProvider replays a single end-of-turn
// reply so Send completes in one round-trip.
func newSessionWithFakeGate(t *testing.T, reply string, gate descentGate) (*Session, *mockProvider) {
	t.Helper()
	mp := newMockProvider(mockResponse{deltas: []string{reply}})
	s, err := NewSession(mp, Config{Model: "m"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if gate != nil {
		s.setGateForTest(gate, "/tmp/fake-repo-does-not-need-to-exist")
	}
	return s, mp
}

// TestSession_GateNil_NoOp verifies that a Session with no gate behaves
// exactly like the legacy chat path: Send returns the model's reply
// unchanged, no descent lines are appended, history commits cleanly.
func TestSession_GateNil_NoOp(t *testing.T) {
	s, _ := newSessionWithFakeGate(t, "hello world", nil)

	var streamed strings.Builder
	result, err := s.Send(context.Background(), "hi", func(d string) { streamed.WriteString(d) }, nil)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if result.Text != "hello world" {
		t.Errorf("Text = %q, want %q", result.Text, "hello world")
	}
	if streamed.String() != "hello world" {
		t.Errorf("streamed = %q, want %q", streamed.String(), "hello world")
	}
	if len(result.DescentLines) != 0 {
		t.Errorf("DescentLines = %v, want empty when gate is nil", result.DescentLines)
	}
	if result.Discarded {
		t.Error("Discarded = true with nil gate, want false")
	}
	if s.TurnCount() != 2 {
		t.Errorf("TurnCount = %d, want 2 (user+assistant)", s.TurnCount())
	}
}

// TestSession_GateFires_AllPass verifies that when the gate fires and
// every AC passes, the reply gets one "✓ <AC.ID> passed" line per
// outcome and history commits normally.
func TestSession_GateFires_AllPass(t *testing.T) {
	gate := &fakeGate{
		fire:    true,
		changed: []string{"main.go"},
		verdict: ChatVerdict{
			Outcomes: []ACOutcome{
				{AC: AcceptanceCriterion{ID: "chat.build"}, Passed: true},
				{AC: AcceptanceCriterion{ID: "chat.test"}, Passed: true},
			},
		},
	}
	s, _ := newSessionWithFakeGate(t, "edited main.go", gate)

	var streamed strings.Builder
	result, err := s.Send(context.Background(), "make a change", func(d string) { streamed.WriteString(d) }, nil)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if result.Discarded {
		t.Error("Discarded = true on all-pass verdict, want false")
	}
	if gate.shouldFired != 1 {
		t.Errorf("ShouldFire calls = %d, want 1", gate.shouldFired)
	}
	if gate.runs != 1 {
		t.Errorf("Run calls = %d, want 1", gate.runs)
	}
	if len(gate.ranWith) != 1 || gate.ranWith[0] != "main.go" {
		t.Errorf("Run got changed=%v, want [main.go]", gate.ranWith)
	}
	if len(result.DescentLines) != 2 {
		t.Fatalf("DescentLines = %v, want 2 entries", result.DescentLines)
	}
	for _, want := range []string{"✓ chat.build passed", "✓ chat.test passed"} {
		found := false
		for _, line := range result.DescentLines {
			if strings.Contains(line, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("DescentLines %v missing %q", result.DescentLines, want)
		}
	}
	// onDelta should also have received the descent lines (so the live
	// REPL/TUI surfaces them inline).
	if !strings.Contains(streamed.String(), "✓ chat.build passed") {
		t.Errorf("streamed text missing pass line: %q", streamed.String())
	}
	if s.TurnCount() != 2 {
		t.Errorf("TurnCount = %d, want 2 (clean turn commits)", s.TurnCount())
	}
}

// TestSession_GateFails_AppendsVerdict verifies that a failing AC
// outcome lands a "✗ ... failed: <stderr-snippet>" line in the reply.
// The session should still commit history (no discard) so the user can
// react to the failure on their next turn.
func TestSession_GateFails_AppendsVerdict(t *testing.T) {
	gate := &fakeGate{
		fire:    true,
		changed: []string{"broken.go"},
		verdict: ChatVerdict{
			Outcomes: []ACOutcome{
				{
					AC:     AcceptanceCriterion{ID: "chat.build"},
					Passed: false,
					Stderr: "broken.go:3:1: syntax error",
				},
			},
			FatalErr: errors.New("AC chat.build failed (no operator available)"),
		},
		runErr: errors.New("AC chat.build failed (no operator available)"),
	}
	s, _ := newSessionWithFakeGate(t, "tried to edit", gate)

	result, err := s.Send(context.Background(), "edit broken.go", nil, nil)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if result.Discarded {
		t.Error("Discarded = true on a non-EditPrompt failure, want false")
	}
	wantSnip := "✗ chat.build failed: broken.go:3:1: syntax error"
	matched := false
	for _, line := range result.DescentLines {
		if strings.Contains(line, wantSnip) {
			matched = true
			break
		}
	}
	if !matched {
		t.Errorf("DescentLines %v missing %q", result.DescentLines, wantSnip)
	}
	// The fatal-err tail line ("⚠ ...") should also be present because
	// the verdict was neither soft-passed nor edit-prompted.
	tailFound := false
	for _, line := range result.DescentLines {
		if strings.Contains(line, "⚠") && strings.Contains(line, "AC chat.build failed") {
			tailFound = true
			break
		}
	}
	if !tailFound {
		t.Errorf("DescentLines %v missing fatal-err tail", result.DescentLines)
	}
	// History should still commit on a non-discard outcome — the
	// failure is reported back to the user, not silently swallowed.
	if s.TurnCount() != 2 {
		t.Errorf("TurnCount = %d, want 2 (failure does not discard)", s.TurnCount())
	}
}

// TestSession_EditPrompt_DiscardsReply verifies that when the gate's
// verdict has EditPrompt=true, the Result is marked Discarded and the
// session does NOT commit history for that turn (the user will restate).
func TestSession_EditPrompt_DiscardsReply(t *testing.T) {
	gate := &fakeGate{
		fire:    true,
		changed: []string{"file.go"},
		verdict: ChatVerdict{
			Outcomes: []ACOutcome{
				{
					AC:     AcceptanceCriterion{ID: "chat.build"},
					Passed: false,
					Stderr: "boom",
				},
			},
			EditPrompt: true,
		},
	}
	s, _ := newSessionWithFakeGate(t, "did some thing", gate)

	before := s.TurnCount()
	result, err := s.Send(context.Background(), "make change", nil, nil)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !result.Discarded {
		t.Error("Discarded = false, want true on EditPrompt verdict")
	}
	// Session must NOT have committed the user/assistant turn pair —
	// the user is going to restate, the prior history is the canonical
	// state to continue from.
	if s.TurnCount() != before {
		t.Errorf("TurnCount = %d, want %d (EditPrompt must not commit)", s.TurnCount(), before)
	}
	// The "✗ failed" line should still be visible so the user
	// understands why the gate kicked them back to restate.
	failFound := false
	for _, line := range result.DescentLines {
		if strings.Contains(line, "✗ chat.build failed") {
			failFound = true
			break
		}
	}
	if !failFound {
		t.Errorf("DescentLines %v missing failure context", result.DescentLines)
	}
}
