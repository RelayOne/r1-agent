package planupdate

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/RelayOne/r1/internal/agentloop"
	"github.com/RelayOne/r1/internal/conversation"
	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/cortex/lobes/llm"
	"github.com/RelayOne/r1/internal/hub"
)

// userTurnHistory builds a one-message history slice with one user-role
// agentloop.Message. Centralized so tests stay readable.
func userTurnHistory(text string) []agentloop.Message {
	return []agentloop.Message{
		{Role: "user", Content: []agentloop.ContentBlock{{Type: "text", Text: text}}},
	}
}

// assistantTurnHistory builds a one-message history slice with one
// assistant-role agentloop.Message. Used for ticks that should NOT
// fire the verb-scan path.
func assistantTurnHistory(text string) []agentloop.Message {
	return []agentloop.Message{
		{Role: "assistant", Content: []agentloop.ContentBlock{{Type: "text", Text: text}}},
	}
}

// TestPlanUpdateLobe_Constructs covers TASK-16: the scaffold must
// compile, expose ID/Description/Kind, and capture every constructor
// argument so downstream tasks have a stable handle.
func TestPlanUpdateLobe_Constructs(t *testing.T) {
	t.Parallel()

	planPath := filepath.Join(t.TempDir(), "stoke-plan.json")
	runtime := conversation.NewRuntime("you are a planner", 8000)
	escalate := llm.NewEscalator(false)
	ws := cortex.NewWorkspace(hub.New(), nil)

	l := NewPlanUpdateLobe(planPath, runtime, nil, escalate, ws, nil)
	if l == nil {
		t.Fatal("NewPlanUpdateLobe returned nil")
	}

	if got, want := l.ID(), "plan-update"; got != want {
		t.Errorf("ID() = %q, want %q", got, want)
	}
	if l.Description() == "" {
		t.Error("Description() is empty")
	}
	if got, want := l.Kind(), cortex.KindLLM; got != want {
		t.Errorf("Kind() = %v, want KindLLM", got)
	}
	if got, want := l.PlanPath(), planPath; got != want {
		t.Errorf("PlanPath() = %q, want %q", got, want)
	}
	if got := l.TurnCount(); got != 0 {
		t.Errorf("TurnCount() at construction = %d, want 0", got)
	}
}

// TestPlanUpdateLobe_TriggerCadence covers TASK-17. Spec contract:
//
//	"every 3rd assistant turn boundary, OR on user message containing
//	 any verb from cortex.ActionVerbs"
//
// The test drives 9 ticks with a benign assistant-only history (no
// user turn -> verb scan never fires) and asserts the trigger fires
// exactly 3 times — at turns 3, 6, 9. Then it drives one more tick
// with a user message containing "rename" (which is not on a 3-multiple
// turn) and asserts the trigger fires from the verb-scan path.
func TestPlanUpdateLobe_TriggerCadence(t *testing.T) {
	t.Parallel()

	planPath := filepath.Join(t.TempDir(), "stoke-plan.json")
	runtime := conversation.NewRuntime("planner", 8000)
	escalate := llm.NewEscalator(false)
	ws := cortex.NewWorkspace(hub.New(), nil)

	l := NewPlanUpdateLobe(planPath, runtime, nil, escalate, ws, nil)

	var fired atomic.Uint64
	l.SetOnTrigger(func(ctx context.Context, in cortex.LobeInput) {
		fired.Add(1)
	})

	ctx := context.Background()
	benign := assistantTurnHistory("ok") // no user msg -> verb scan stays silent

	for i := 0; i < 9; i++ {
		_ = l.Run(ctx, cortex.LobeInput{History: benign})
	}

	if got, want := fired.Load(), uint64(3); got != want {
		t.Errorf("after 9 ticks fired=%d, want %d (turns 3,6,9)", got, want)
	}
	if got, want := l.TurnCount(), uint64(9); got != want {
		t.Errorf("TurnCount=%d, want %d", got, want)
	}
	if got, want := l.TriggerCount(), uint64(3); got != want {
		t.Errorf("TriggerCount=%d, want %d", got, want)
	}

	// Tick 10: NOT a multiple of 3, but the user message contains
	// "rename" — verb scan must fire the trigger.
	verbHistory := userTurnHistory("please rename task t-7 to clarify scope")
	_ = l.Run(ctx, cortex.LobeInput{History: verbHistory})

	if got, want := fired.Load(), uint64(4); got != want {
		t.Errorf("after verb tick fired=%d, want %d (verb-scan should fire on tick 10)", got, want)
	}
	if got, want := l.TurnCount(), uint64(10); got != want {
		t.Errorf("TurnCount=%d, want %d", got, want)
	}
}

// TestPlanUpdateLobe_TriggerSilentOnChitChat verifies that a non-multiple
// turn with a benign user message (no action verb) does NOT fire.
func TestPlanUpdateLobe_TriggerSilentOnChitChat(t *testing.T) {
	t.Parallel()

	planPath := filepath.Join(t.TempDir(), "stoke-plan.json")
	runtime := conversation.NewRuntime("planner", 8000)
	escalate := llm.NewEscalator(false)
	ws := cortex.NewWorkspace(hub.New(), nil)

	l := NewPlanUpdateLobe(planPath, runtime, nil, escalate, ws, nil)
	var fired atomic.Uint64
	l.SetOnTrigger(func(ctx context.Context, in cortex.LobeInput) { fired.Add(1) })

	ctx := context.Background()
	benign := userTurnHistory("hello, how is the weather?")
	_ = l.Run(ctx, cortex.LobeInput{History: benign})

	if got := fired.Load(); got != 0 {
		t.Errorf("chit-chat tick fired=%d, want 0", got)
	}
}

// TestScanVerbs_WholeWordMatch protects the verb-scan helper from the
// classic substring-bug regression: "padding" must NOT match "add" and
// "merger" must NOT match "merge".
func TestScanVerbs_WholeWordMatch(t *testing.T) {
	t.Parallel()
	cases := []struct {
		s    string
		want bool
	}{
		{"please add a task", true},
		{"padding the buffer", false},
		{"PLEASE REMOVE T7", true},
		{"this is a merger of teams", false},
		{"split the build", true},
		{"hello", false},
		{"", false},
	}
	for _, tc := range cases {
		got := scanVerbs(tc.s, actionVerbs)
		if got != tc.want {
			t.Errorf("scanVerbs(%q) = %v, want %v", tc.s, got, tc.want)
		}
	}
}
