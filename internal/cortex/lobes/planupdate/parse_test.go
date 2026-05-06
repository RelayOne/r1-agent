package planupdate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/RelayOne/r1/internal/conversation"
	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/cortex/lobes/llm"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/plan"
	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/stream"
)

// fakeProvider is a minimal provider.Provider stub for the parse-and-
// apply tests. ChatStream returns a fixed text payload (replyText) on
// every call; counts the number of calls in callCount for assertions.
type fakeProvider struct {
	mu        sync.Mutex
	replyText string
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
	return &provider.ChatResponse{
		Model:      req.Model,
		StopReason: "end_turn",
		Content: []provider.ResponseContent{
			{Type: "text", Text: f.replyText},
		},
	}, nil
}

// seedPlan writes a minimal stoke-plan.json to dir and returns the
// path. Used as the planPath constructor arg in the apply tests.
func seedPlan(t *testing.T, dir string, p *plan.Plan) string {
	t.Helper()
	planPath := filepath.Join(dir, "stoke-plan.json")
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		t.Fatalf("marshal seed plan: %v", err)
	}
	if err := os.WriteFile(planPath, data, 0644); err != nil {
		t.Fatalf("write seed plan: %v", err)
	}
	return planPath
}

// loadCurrent reads planPath back and returns the parsed plan. Used to
// assert the on-disk state after apply.
func loadCurrent(t *testing.T, planPath string) *plan.Plan {
	t.Helper()
	p, err := plan.LoadFile(planPath)
	if err != nil {
		t.Fatalf("LoadFile(%s): %v", planPath, err)
	}
	return p
}

// TestPlanUpdateLobe_AutoAppliesEditsButQueuesAddsRemoves covers
// TASK-19's happy path: the model returns a JSON object with all three
// lists populated. Edits auto-apply (plan.Save runs), adds+removes are
// queued as a single user-confirm Note.
func TestPlanUpdateLobe_AutoAppliesEditsButQueuesAddsRemoves(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	planPath := seedPlan(t, dir, &plan.Plan{
		ID: "p1",
		Tasks: []plan.Task{
			{ID: "t1", Description: "old title"},
			{ID: "t2", Description: "another"},
		},
	})

	output := modelOutput{
		Edits: []proposedEdit{
			{ID: "t1", Field: "title", New: "new title"},
		},
		Additions: []proposedAddition{
			{ID: "t3", Title: "new task", Deps: []string{"t1"}},
		},
		Removals: []proposedRemoval{
			{ID: "t2", Reason: "obsolete"},
		},
		Confidence: 0.9,
		Rationale:  "user explicitly described t3",
	}
	raw, _ := json.Marshal(output)

	fp := &fakeProvider{replyText: string(raw)}
	runtime := conversation.NewRuntime("planner", 8000)
	escalate := llm.NewEscalator(false)
	hubBus := hub.New()
	ws := cortex.NewWorkspace(hubBus, nil)

	l := NewPlanUpdateLobe(planPath, runtime, fp, escalate, ws, nil)

	// Force a triggered tick directly (turn 3 satisfies turn%3==0).
	for i := 0; i < 3; i++ {
		_ = l.Run(context.Background(), cortex.LobeInput{})
	}

	if got := fp.callCount.Load(); got != 1 {
		t.Errorf("ChatStream called %d times, want 1", got)
	}

	// Edits auto-applied: t1 description should now be "new title".
	current := loadCurrent(t, planPath)
	var t1Idx int = -1
	for i := range current.Tasks {
		if current.Tasks[i].ID == "t1" {
			t1Idx = i
			break
		}
	}
	if t1Idx < 0 {
		t.Fatal("t1 not present after edit apply")
	}
	if got, want := current.Tasks[t1Idx].Description, "new title"; got != want {
		t.Errorf("t1.Description = %q, want %q", got, want)
	}

	// Addition (t3) and removal (t2) were NOT auto-applied.
	for _, task := range current.Tasks {
		if task.ID == "t3" {
			t.Error("t3 should not be auto-applied — adds require user-confirm")
		}
	}
	t2Found := false
	for _, task := range current.Tasks {
		if task.ID == "t2" {
			t2Found = true
		}
	}
	if !t2Found {
		t.Error("t2 should still be present — removes require user-confirm")
	}

	// One user-confirm Note must be in the workspace.
	notes := ws.Snapshot()
	var confirmNote *cortex.Note
	for i := range notes {
		if notes[i].Meta == nil {
			continue
		}
		if notes[i].Meta[llm.MetaActionKind] == "user-confirm" {
			confirmNote = &notes[i]
			break
		}
	}
	if confirmNote == nil {
		t.Fatalf("expected one user-confirm Note, snapshot=%+v", notes)
	}
	if got, want := confirmNote.LobeID, "plan-update"; got != want {
		t.Errorf("Note.LobeID = %q, want %q", got, want)
	}
	if got, want := confirmNote.Severity, cortex.SevInfo; got != want {
		t.Errorf("Note.Severity = %v, want SevInfo", got)
	}
	hasPlanConfirmTag := false
	for _, tg := range confirmNote.Tags {
		if tg == "plan-confirm" {
			hasPlanConfirmTag = true
			break
		}
	}
	if !hasPlanConfirmTag {
		t.Errorf("Note.Tags missing plan-confirm: %v", confirmNote.Tags)
	}
	payload, ok := confirmNote.Meta[llm.MetaActionPayload].(map[string]any)
	if !ok {
		t.Fatalf("Note.Meta[%s] not a map[string]any: %T",
			llm.MetaActionPayload, confirmNote.Meta[llm.MetaActionPayload])
	}
	if _, ok := payload["adds"]; !ok {
		t.Error("payload missing adds")
	}
	if _, ok := payload["removes"]; !ok {
		t.Error("payload missing removes")
	}
	if _, ok := confirmNote.Meta["queue_id"].(string); !ok {
		t.Errorf("Meta[queue_id] missing or not a string: %v", confirmNote.Meta["queue_id"])
	}
}

// TestPlanUpdateLobe_MalformedJSONNoOp covers TASK-19's failure path:
// the model returns plain prose. parsePlanUpdate fails -> Lobe logs a
// warning and emits NO Note. plan.json remains untouched.
func TestPlanUpdateLobe_MalformedJSONNoOp(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	planPath := seedPlan(t, dir, &plan.Plan{
		ID:    "p1",
		Tasks: []plan.Task{{ID: "t1", Description: "original"}},
	})

	fp := &fakeProvider{replyText: "I am not JSON. I am prose."}
	runtime := conversation.NewRuntime("planner", 8000)
	escalate := llm.NewEscalator(false)
	ws := cortex.NewWorkspace(hub.New(), nil)

	l := NewPlanUpdateLobe(planPath, runtime, fp, escalate, ws, nil)

	for i := 0; i < 3; i++ {
		_ = l.Run(context.Background(), cortex.LobeInput{})
	}

	if got := fp.callCount.Load(); got != 1 {
		t.Errorf("ChatStream called %d times, want 1", got)
	}

	if notes := ws.Snapshot(); len(notes) != 0 {
		t.Errorf("expected zero Notes on malformed output, got %d: %+v", len(notes), notes)
	}

	current := loadCurrent(t, planPath)
	if len(current.Tasks) != 1 || current.Tasks[0].Description != "original" {
		t.Errorf("plan was mutated despite malformed output: %+v", current)
	}
}

// TestPlanUpdateLobe_LowConfidenceNoOp covers the spec rule
// "if confidence < 0.6, return empty arrays". A 0.5-confidence response
// must auto-apply nothing and queue no Note.
func TestPlanUpdateLobe_LowConfidenceNoOp(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	planPath := seedPlan(t, dir, &plan.Plan{
		ID:    "p1",
		Tasks: []plan.Task{{ID: "t1", Description: "stable"}},
	})
	output := modelOutput{
		Edits:      []proposedEdit{{ID: "t1", Field: "title", New: "drift"}},
		Confidence: 0.5,
	}
	raw, _ := json.Marshal(output)

	fp := &fakeProvider{replyText: string(raw)}
	runtime := conversation.NewRuntime("planner", 8000)
	escalate := llm.NewEscalator(false)
	ws := cortex.NewWorkspace(hub.New(), nil)

	l := NewPlanUpdateLobe(planPath, runtime, fp, escalate, ws, nil)
	for i := 0; i < 3; i++ {
		_ = l.Run(context.Background(), cortex.LobeInput{})
	}

	current := loadCurrent(t, planPath)
	if current.Tasks[0].Description != "stable" {
		t.Errorf("plan mutated despite low confidence: %+v", current)
	}
	if notes := ws.Snapshot(); len(notes) != 0 {
		t.Errorf("expected zero Notes on low-confidence, got %d", len(notes))
	}
}

// TestPlanUpdateLobe_ProviderErrorIsNoop covers the soft-fail path:
// when ChatStream returns an error, the Lobe must NOT crash, NOT
// publish a Note, and NOT mutate the plan.
func TestPlanUpdateLobe_ProviderErrorIsNoop(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	planPath := seedPlan(t, dir, &plan.Plan{
		ID:    "p1",
		Tasks: []plan.Task{{ID: "t1", Description: "a"}},
	})
	fp := &fakeProvider{failWith: errors.New("upstream 500")}
	runtime := conversation.NewRuntime("planner", 8000)
	escalate := llm.NewEscalator(false)
	ws := cortex.NewWorkspace(hub.New(), nil)
	l := NewPlanUpdateLobe(planPath, runtime, fp, escalate, ws, nil)
	for i := 0; i < 3; i++ {
		_ = l.Run(context.Background(), cortex.LobeInput{})
	}
	if notes := ws.Snapshot(); len(notes) != 0 {
		t.Errorf("expected zero Notes on provider error, got %d", len(notes))
	}
}

// TestParsePlanUpdate_StripsCodeFences covers the defensive fence-strip
// path: a model that wraps output in ```json fences (despite the system
// prompt saying not to) is still parsed cleanly.
func TestParsePlanUpdate_StripsCodeFences(t *testing.T) {
	t.Parallel()
	cases := []string{
		"```json\n{\"confidence\":1.0,\"additions\":[]}\n```",
		"```\n{\"confidence\":1.0}\n```",
		"{\"confidence\":1.0}",
	}
	for _, c := range cases {
		out, err := parsePlanUpdate(c)
		if err != nil {
			t.Errorf("parsePlanUpdate(%q) returned err=%v", c, err)
			continue
		}
		if out.Confidence != 1.0 {
			t.Errorf("Confidence=%v on input %q", out.Confidence, c)
		}
	}
}
