package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestAllTaskStatuses_Seven(t *testing.T) {
	if got := AllTaskStatuses(); len(got) != 7 {
		t.Fatalf("AllTaskStatuses=%d want 7", len(got))
	}
}

func TestIsTerminal(t *testing.T) {
	for _, s := range []TaskStatus{TaskCompleted, TaskFailed, TaskCanceled, TaskRejected} {
		if !s.IsTerminal() {
			t.Errorf("%q should be terminal", s)
		}
	}
	for _, s := range []TaskStatus{TaskSubmitted, TaskWorking, TaskInputRequired} {
		if s.IsTerminal() {
			t.Errorf("%q should NOT be terminal", s)
		}
	}
}

func TestSubmit_StartsInSubmittedState(t *testing.T) {
	store := NewInMemoryTaskStore()
	task, err := store.Submit(context.Background(), json.RawMessage(`{"text":"hi"}`))
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if task.Status != TaskSubmitted {
		t.Errorf("status=%q want submitted", task.Status)
	}
	if task.ID == "" {
		t.Error("ID should be populated")
	}
	if len(task.History) != 1 {
		t.Errorf("history len=%d want 1", len(task.History))
	}
}

func TestTransition_ValidEdges(t *testing.T) {
	cases := []struct {
		name string
		from TaskStatus
		to   TaskStatus
	}{
		{"submitted→working", TaskSubmitted, TaskWorking},
		{"working→completed", TaskWorking, TaskCompleted},
		{"working→failed", TaskWorking, TaskFailed},
		{"working→input-required", TaskWorking, TaskInputRequired},
		{"input-required→working", TaskInputRequired, TaskWorking},
		{"submitted→rejected", TaskSubmitted, TaskRejected},
		{"submitted→canceled", TaskSubmitted, TaskCanceled},
		{"working→canceled", TaskWorking, TaskCanceled},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := NewInMemoryTaskStore()
			task, _ := store.Submit(context.Background(), nil)
			// Prime to `from` by finding a valid path.
			if c.from != TaskSubmitted {
				// Path to from: submitted → ... → c.from
				switch c.from {
				case TaskWorking:
					_, _ = store.Transition(context.Background(), task.ID, TaskWorking, "")
				case TaskInputRequired:
					_, _ = store.Transition(context.Background(), task.ID, TaskWorking, "")
					_, _ = store.Transition(context.Background(), task.ID, TaskInputRequired, "")
				}
			}
			_, err := store.Transition(context.Background(), task.ID, c.to, "msg")
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestTransition_ForbiddenEdges(t *testing.T) {
	cases := []struct {
		name string
		from TaskStatus
		to   TaskStatus
	}{
		{"submitted→completed (skips working)", TaskSubmitted, TaskCompleted},
		{"completed→anywhere", TaskCompleted, TaskWorking},
		{"failed→anywhere", TaskFailed, TaskWorking},
		{"canceled→anywhere", TaskCanceled, TaskWorking},
		{"rejected→anywhere", TaskRejected, TaskWorking},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := NewInMemoryTaskStore()
			task, _ := store.Submit(context.Background(), nil)
			// Navigate to the forbidden from-state.
			switch c.from {
			case TaskCompleted:
				_, _ = store.Transition(context.Background(), task.ID, TaskWorking, "")
				_, _ = store.Transition(context.Background(), task.ID, TaskCompleted, "")
			case TaskFailed:
				_, _ = store.Transition(context.Background(), task.ID, TaskWorking, "")
				_, _ = store.Transition(context.Background(), task.ID, TaskFailed, "")
			case TaskCanceled:
				_, _ = store.Transition(context.Background(), task.ID, TaskCanceled, "")
			case TaskRejected:
				_, _ = store.Transition(context.Background(), task.ID, TaskRejected, "")
			}
			_, err := store.Transition(context.Background(), task.ID, c.to, "")
			if !errors.Is(err, ErrInvalidTaskTransition) {
				t.Errorf("want ErrInvalidTaskTransition, got %v", err)
			}
		})
	}
}

func TestTransition_TaskNotFound(t *testing.T) {
	store := NewInMemoryTaskStore()
	_, err := store.Transition(context.Background(), "ghost", TaskWorking, "")
	if !errors.Is(err, ErrTaskNotFound) {
		t.Errorf("want ErrTaskNotFound, got %v", err)
	}
}

func TestAppendArtifact(t *testing.T) {
	store := NewInMemoryTaskStore()
	task, _ := store.Submit(context.Background(), nil)
	got, err := store.AppendArtifact(context.Background(), task.ID, json.RawMessage(`{"file":"x.txt"}`))
	if err != nil {
		t.Fatalf("AppendArtifact: %v", err)
	}
	if len(got.Artifacts) != 1 {
		t.Errorf("artifacts=%d want 1", len(got.Artifacts))
	}
}

func TestSetResult_AndSetError(t *testing.T) {
	store := NewInMemoryTaskStore()
	task, _ := store.Submit(context.Background(), nil)
	_, _ = store.Transition(context.Background(), task.ID, TaskWorking, "")
	_, _ = store.Transition(context.Background(), task.ID, TaskCompleted, "")
	got, err := store.SetResult(context.Background(), task.ID, json.RawMessage(`"done"`))
	if err != nil {
		t.Fatalf("SetResult: %v", err)
	}
	if string(got.Result) != `"done"` {
		t.Errorf("result=%s want \"done\"", got.Result)
	}

	store2 := NewInMemoryTaskStore()
	task2, _ := store2.Submit(context.Background(), nil)
	_, _ = store2.Transition(context.Background(), task2.ID, TaskWorking, "")
	_, _ = store2.Transition(context.Background(), task2.ID, TaskFailed, "")
	got2, _ := store2.SetError(context.Background(), task2.ID, "boom")
	if got2.Error != "boom" {
		t.Errorf("error=%q want boom", got2.Error)
	}
}

func TestList_SortedByCreatedAt(t *testing.T) {
	store := NewInMemoryTaskStore()
	// Stub clock to produce monotonic timestamps in submit order.
	t1 := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { t1 = t1.Add(time.Second); return t1 })
	for i := 0; i < 3; i++ {
		_, _ = store.Submit(context.Background(), nil)
	}
	got, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("len=%d want 3", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].CreatedAt.Before(got[i-1].CreatedAt) {
			t.Errorf("out of order at %d", i)
		}
	}
}

func TestHistoryCap(t *testing.T) {
	store := NewInMemoryTaskStore()
	task, _ := store.Submit(context.Background(), nil)
	// Force a bunch of transitions.
	_, _ = store.Transition(context.Background(), task.ID, TaskWorking, "")
	for i := 0; i < MaxHistoryEntries+10; i++ {
		_, _ = store.Transition(context.Background(), task.ID, TaskInputRequired, "wait")
		_, _ = store.Transition(context.Background(), task.ID, TaskWorking, "resume")
	}
	got, _ := store.Get(context.Background(), task.ID)
	if len(got.History) > MaxHistoryEntries {
		t.Errorf("history len=%d exceeds cap %d", len(got.History), MaxHistoryEntries)
	}
}

func TestHandleSubmit_JSONRPC(t *testing.T) {
	store := NewInMemoryTaskStore()
	raw := json.RawMessage(`{"prompt":{"text":"hello"}}`)
	task, err := HandleSubmit(context.Background(), store, raw)
	if err != nil {
		t.Fatalf("HandleSubmit: %v", err)
	}
	if task.Status != TaskSubmitted {
		t.Errorf("status=%q", task.Status)
	}
}

func TestHandleStatus_JSONRPC(t *testing.T) {
	store := NewInMemoryTaskStore()
	task, _ := store.Submit(context.Background(), nil)
	raw := json.RawMessage(`{"taskId":"` + task.ID + `"}`)
	got, err := HandleStatus(context.Background(), store, raw)
	if err != nil {
		t.Fatalf("HandleStatus: %v", err)
	}
	if got.ID != task.ID {
		t.Errorf("id=%q want %q", got.ID, task.ID)
	}
}

func TestHandleStatus_MissingTaskID(t *testing.T) {
	store := NewInMemoryTaskStore()
	_, err := HandleStatus(context.Background(), store, json.RawMessage(`{}`))
	if err == nil {
		t.Error("expected error on empty taskId")
	}
}

func TestHandleCancel_JSONRPC(t *testing.T) {
	store := NewInMemoryTaskStore()
	task, _ := store.Submit(context.Background(), nil)
	raw := json.RawMessage(`{"taskId":"` + task.ID + `","reason":"user aborted"}`)
	got, err := HandleCancel(context.Background(), store, raw)
	if err != nil {
		t.Fatalf("HandleCancel: %v", err)
	}
	if got.Status != TaskCanceled {
		t.Errorf("status=%q want canceled", got.Status)
	}
}
