package planupdate

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/conversation"
	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/cortex/lobes/llm"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/plan"
)

// TestPlanUpdateLobe_AppliesOnConfirmation covers TASK-20.
//
// Flow:
//  1. Seed a plan with t1, t2.
//  2. Run the Lobe through one Haiku call producing additions+removals
//     (t3 add, t2 remove). Edits empty so plan stays untouched here.
//  3. Pull the queue_id off the published Note.
//  4. Emit EventCortexUserConfirmedPlanChange with that queue_id.
//  5. Assert plan now has t3 and no t2.
func TestPlanUpdateLobe_AppliesOnConfirmation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	planPath := seedPlan(t, dir, &plan.Plan{
		ID: "p1",
		Tasks: []plan.Task{
			{ID: "t1", Description: "task one"},
			{ID: "t2", Description: "task two"},
		},
	})

	output := modelOutput{
		Additions: []proposedAddition{
			{ID: "t3", Title: "task three", Deps: []string{"t1"}},
		},
		Removals: []proposedRemoval{
			{ID: "t2", Reason: "obsolete"},
		},
		Confidence: 0.9,
		Rationale:  "user described t3, declared t2 obsolete",
	}
	raw, _ := json.Marshal(output)

	fp := &fakeProvider{replyText: string(raw)}
	runtime := conversation.NewRuntime("planner", 8000)
	escalate := llm.NewEscalator(false)
	hubBus := hub.New()
	ws := cortex.NewWorkspace(hubBus, nil)

	l := NewPlanUpdateLobe(planPath, runtime, fp, escalate, ws, hubBus)

	for i := 0; i < 3; i++ {
		_ = l.Run(context.Background(), cortex.LobeInput{})
	}

	// Find the queued queue_id from the published Note.
	var queueID string
	for _, n := range ws.Snapshot() {
		if n.Meta == nil {
			continue
		}
		if id, ok := n.Meta["queue_id"].(string); ok && id != "" {
			queueID = id
			break
		}
	}
	if queueID == "" {
		t.Fatalf("expected user-confirm Note with queue_id, snapshot=%+v", ws.Snapshot())
	}

	if got := l.QueuedCount(); got != 1 {
		t.Errorf("QueuedCount before confirm = %d, want 1", got)
	}

	// Plan should still be in pre-confirmation state.
	pre := loadCurrent(t, planPath)
	if findTaskIdx(pre, "t3") >= 0 {
		t.Error("t3 should NOT be present before confirmation")
	}
	if findTaskIdx(pre, "t2") < 0 {
		t.Error("t2 should still be present before confirmation")
	}

	// Emit the user-confirmed event.
	hubBus.EmitAsync(&hub.Event{
		Type: hub.EventCortexUserConfirmedPlanChange,
		Custom: map[string]any{
			"queue_id": queueID,
		},
	})

	// Subscriber is fire-and-forget Observe; poll until QueuedCount==0
	// or timeout.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if l.QueuedCount() == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := l.QueuedCount(); got != 0 {
		t.Fatalf("QueuedCount after confirm = %d, want 0", got)
	}

	post := loadCurrent(t, planPath)
	if findTaskIdx(post, "t3") < 0 {
		t.Errorf("t3 not added after confirmation: %+v", post.Tasks)
	}
	if findTaskIdx(post, "t2") >= 0 {
		t.Errorf("t2 not removed after confirmation: %+v", post.Tasks)
	}
	// Sanity: t1 untouched.
	if idx := findTaskIdx(post, "t1"); idx < 0 || post.Tasks[idx].Description != "task one" {
		t.Errorf("t1 unexpectedly mutated: %+v", post.Tasks)
	}
	// And t3's Deps preserved from the proposal.
	if idx := findTaskIdx(post, "t3"); idx >= 0 {
		if got, want := post.Tasks[idx].Dependencies, []string{"t1"}; len(got) != len(want) || got[0] != want[0] {
			t.Errorf("t3.Dependencies = %v, want %v", got, want)
		}
	}
}

// TestPlanUpdateLobe_UnknownQueueIDIsNoop covers the defensive path:
// a confirm event with a queue_id we don't know about must NOT crash
// and must NOT mutate the plan.
func TestPlanUpdateLobe_UnknownQueueIDIsNoop(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	planPath := seedPlan(t, dir, &plan.Plan{
		ID:    "p1",
		Tasks: []plan.Task{{ID: "t1", Description: "stable"}},
	})
	hubBus := hub.New()
	ws := cortex.NewWorkspace(hubBus, nil)
	runtime := conversation.NewRuntime("planner", 8000)
	escalate := llm.NewEscalator(false)
	l := NewPlanUpdateLobe(planPath, runtime, nil, escalate, ws, hubBus)

	hubBus.EmitAsync(&hub.Event{
		Type:   hub.EventCortexUserConfirmedPlanChange,
		Custom: map[string]any{"queue_id": "no-such-queue-99999"},
	})

	// Settle.
	time.Sleep(100 * time.Millisecond)

	if got := l.QueuedCount(); got != 0 {
		t.Errorf("QueuedCount = %d, want 0", got)
	}
	post := loadCurrent(t, planPath)
	if len(post.Tasks) != 1 || post.Tasks[0].ID != "t1" {
		t.Errorf("plan mutated by unknown queue_id: %+v", post)
	}
}

// TestPlanUpdateLobe_MissingQueueIDIsNoop covers the case where the
// confirm event arrives without a queue_id at all.
func TestPlanUpdateLobe_MissingQueueIDIsNoop(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	planPath := seedPlan(t, dir, &plan.Plan{
		ID:    "p1",
		Tasks: []plan.Task{{ID: "t1", Description: "stable"}},
	})
	hubBus := hub.New()
	ws := cortex.NewWorkspace(hubBus, nil)
	runtime := conversation.NewRuntime("planner", 8000)
	escalate := llm.NewEscalator(false)
	l := NewPlanUpdateLobe(planPath, runtime, nil, escalate, ws, hubBus)

	hubBus.EmitAsync(&hub.Event{
		Type:   hub.EventCortexUserConfirmedPlanChange,
		Custom: map[string]any{},
	})
	time.Sleep(100 * time.Millisecond)

	if got := l.QueuedCount(); got != 0 {
		t.Errorf("QueuedCount = %d, want 0", got)
	}
}
