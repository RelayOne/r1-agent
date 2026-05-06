package antitrunc

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/ledger"
)

func TestSubagentSummaryTruncation_Evaluate_FiresWhenTaskIncomplete(t *testing.T) {
	dir := t.TempDir()
	l, _ := ledger.New(dir)
	defer l.Close()

	r := NewSubagentSummaryTruncation()
	payload, _ := json.Marshal(map[string]string{"summary": "i'll defer the rest"})
	evt := bus.Event{
		Type:    bus.EvtWorkerActionCompleted,
		Scope:   bus.Scope{TaskID: "task-7"},
		Payload: payload,
	}
	fired, err := r.Evaluate(context.Background(), evt, l)
	if err != nil {
		t.Fatal(err)
	}
	if !fired {
		t.Error("expected fire when task incomplete + truncation phrase")
	}
}

func TestSubagentSummaryTruncation_Evaluate_NoFireWhenTaskComplete(t *testing.T) {
	dir := t.TempDir()
	l, _ := ledger.New(dir)
	defer l.Close()

	// Mark task complete.
	content, _ := json.Marshal(taskCompleteContent{TaskID: "task-7"})
	_, err := l.AddNode(context.Background(), ledger.Node{
		Type:          "task.complete",
		SchemaVersion: 1,
		CreatedBy:     "system",
		Content:       content,
	})
	if err != nil {
		t.Fatal(err)
	}

	r := NewSubagentSummaryTruncation()
	payload, _ := json.Marshal(map[string]string{"summary": "i'll stop here"})
	evt := bus.Event{
		Type:    bus.EvtWorkerActionCompleted,
		Scope:   bus.Scope{TaskID: "task-7"},
		Payload: payload,
	}
	fired, err := r.Evaluate(context.Background(), evt, l)
	if err != nil {
		t.Fatal(err)
	}
	if fired {
		t.Error("expected NO fire when task is complete")
	}
}

func TestSubagentSummaryTruncation_Evaluate_NoPhraseNoFire(t *testing.T) {
	dir := t.TempDir()
	l, _ := ledger.New(dir)
	defer l.Close()
	r := NewSubagentSummaryTruncation()
	payload, _ := json.Marshal(map[string]string{"summary": "build green"})
	evt := bus.Event{
		Type:    bus.EvtWorkerActionCompleted,
		Scope:   bus.Scope{TaskID: "task-1"},
		Payload: payload,
	}
	fired, err := r.Evaluate(context.Background(), evt, l)
	if err != nil {
		t.Fatal(err)
	}
	if fired {
		t.Error("expected no fire on clean summary")
	}
}

func TestSubagentSummaryTruncation_Evaluate_NoTaskID_FiresConservatively(t *testing.T) {
	dir := t.TempDir()
	l, _ := ledger.New(dir)
	defer l.Close()
	r := NewSubagentSummaryTruncation()
	payload, _ := json.Marshal(map[string]string{"summary": "we should stop here"})
	evt := bus.Event{
		Type:    bus.EvtWorkerActionCompleted,
		Payload: payload,
	} // no TaskID
	fired, err := r.Evaluate(context.Background(), evt, l)
	if err != nil {
		t.Fatal(err)
	}
	if !fired {
		t.Error("expected conservative fire when TaskID missing")
	}
}

func TestSubagentSummaryTruncation_Action_Publishes(t *testing.T) {
	b, err := bus.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	r := NewSubagentSummaryTruncation()
	payload, _ := json.Marshal(map[string]string{"summary": "i'll skip the rest"})
	evt := bus.Event{
		ID:      "evt-3",
		Type:    bus.EvtWorkerActionCompleted,
		Scope:   bus.Scope{TaskID: "t-9"},
		Payload: payload,
	}

	var captured []bus.Event
	var mu sync.Mutex
	b.Subscribe(bus.Pattern{TypePrefix: string(bus.EvtSupervisorRuleFired)}, func(e bus.Event) {
		mu.Lock()
		captured = append(captured, e)
		mu.Unlock()
	})

	if err := r.Action(context.Background(), evt, b); err != nil {
		t.Fatalf("Action: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(captured)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(captured) == 0 {
		t.Fatal("expected published event")
	}
	var pl map[string]any
	if err := json.Unmarshal(captured[0].Payload, &pl); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if pl["task_id"] != "t-9" {
		t.Errorf("task_id = %v, want t-9", pl["task_id"])
	}
}
