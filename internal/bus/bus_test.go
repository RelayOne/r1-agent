package bus

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

func tempBus(t *testing.T) *Bus {
	t.Helper()
	dir := t.TempDir()
	b, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { b.Close() })
	return b
}

func makeEvent(typ EventType, emitter string) Event {
	return Event{
		Type:      typ,
		EmitterID: emitter,
		Scope:     Scope{MissionID: "m1"},
	}
}

func TestPublishDurablyWritesToWAL(t *testing.T) {
	dir := t.TempDir()
	b, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	evt := makeEvent(EvtWorkerSpawned, "w1")
	if err := b.Publish(evt); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	b.Close()

	// Re-open and verify the event survived.
	b2, err := New(dir)
	if err != nil {
		t.Fatalf("New (reopen): %v", err)
	}
	defer b2.Close()

	var replayed []Event
	err = b2.Replay(Pattern{}, 1, func(e Event) {
		replayed = append(replayed, e)
	})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(replayed) != 1 {
		t.Fatalf("expected 1 replayed event, got %d", len(replayed))
	}
	if replayed[0].Type != EvtWorkerSpawned {
		t.Errorf("expected type %s, got %s", EvtWorkerSpawned, replayed[0].Type)
	}
}

func TestSubscribeReceivesMatchingEvents(t *testing.T) {
	b := tempBus(t)

	var received []Event
	var mu sync.Mutex
	b.Subscribe(Pattern{TypePrefix: "worker."}, func(e Event) {
		mu.Lock()
		received = append(received, e)
		mu.Unlock()
	})

	// Publish a matching event.
	if err := b.Publish(makeEvent(EvtWorkerSpawned, "w1")); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	// Publish a non-matching event.
	if err := b.Publish(makeEvent(EvtMissionStarted, "m1")); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 {
		t.Fatalf("expected 1 event, got %d", len(received))
	}
	if received[0].Type != EvtWorkerSpawned {
		t.Errorf("expected %s, got %s", EvtWorkerSpawned, received[0].Type)
	}
}

func TestHooksFireBeforeSubscribers(t *testing.T) {
	b := tempBus(t)

	var order []string
	var mu sync.Mutex

	err := b.RegisterHook(Hook{
		ID:        "hook1",
		Pattern:   Pattern{TypePrefix: "worker."},
		Priority:  10,
		Authority: "supervisor",
		Handler: func(ctx context.Context, evt Event) (*HookAction, error) {
			mu.Lock()
			order = append(order, "hook")
			mu.Unlock()
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("RegisterHook: %v", err)
	}

	b.Subscribe(Pattern{TypePrefix: "worker."}, func(e Event) {
		mu.Lock()
		order = append(order, "subscriber")
		mu.Unlock()
	})

	if err := b.Publish(makeEvent(EvtWorkerSpawned, "w1")); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 {
		t.Fatalf("expected 2 entries, got %d: %v", len(order), order)
	}
	if order[0] != "hook" || order[1] != "subscriber" {
		t.Errorf("expected [hook, subscriber], got %v", order)
	}
}

func TestNonSupervisorCannotRegisterHook(t *testing.T) {
	b := tempBus(t)

	err := b.RegisterHook(Hook{
		ID:        "bad-hook",
		Pattern:   Pattern{TypePrefix: "worker."},
		Priority:  1,
		Authority: "worker",
		Handler: func(ctx context.Context, evt Event) (*HookAction, error) {
			return nil, nil
		},
	})
	if err == nil {
		t.Fatal("expected error for non-supervisor hook registration")
	}
}

func TestReplayDeliversHistoricalEvents(t *testing.T) {
	b := tempBus(t)

	events := []EventType{EvtWorkerSpawned, EvtWorkerActionStarted, EvtMissionStarted}
	for _, typ := range events {
		if err := b.Publish(makeEvent(typ, "e1")); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}

	// Replay from seq 2 with worker pattern.
	var replayed []Event
	err := b.Replay(Pattern{TypePrefix: "worker."}, 2, func(e Event) {
		replayed = append(replayed, e)
	})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(replayed) != 1 {
		t.Fatalf("expected 1 event, got %d", len(replayed))
	}
	if replayed[0].Type != EvtWorkerActionStarted {
		t.Errorf("expected %s, got %s", EvtWorkerActionStarted, replayed[0].Type)
	}
}

func TestPublishDelayedFiresAfterDelay(t *testing.T) {
	b := tempBus(t)

	var received []Event
	var mu sync.Mutex
	b.Subscribe(Pattern{TypePrefix: "worker."}, func(e Event) {
		mu.Lock()
		received = append(received, e)
		mu.Unlock()
	})

	evt := makeEvent(EvtWorkerSpawned, "w1")
	_, err := b.PublishDelayed(evt, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("PublishDelayed: %v", err)
	}

	// Should not be delivered yet.
	mu.Lock()
	count := len(received)
	mu.Unlock()
	if count != 0 {
		t.Fatalf("expected 0 events immediately, got %d", count)
	}

	// Wait for delivery.
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 {
		t.Fatalf("expected 1 event after delay, got %d", len(received))
	}
}

func TestCancelDelayedPreventsDelivery(t *testing.T) {
	b := tempBus(t)

	var received []Event
	var mu sync.Mutex
	b.Subscribe(Pattern{TypePrefix: "worker."}, func(e Event) {
		mu.Lock()
		received = append(received, e)
		mu.Unlock()
	})

	evt := makeEvent(EvtWorkerSpawned, "w1")
	cancelID, err := b.PublishDelayed(evt, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("PublishDelayed: %v", err)
	}

	if err := b.CancelDelayed(cancelID); err != nil {
		t.Fatalf("CancelDelayed: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 0 {
		t.Fatalf("expected 0 events after cancel, got %d", len(received))
	}
}

func TestCursorSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	b, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Subscribe and publish some events.
	sub := b.Subscribe(Pattern{TypePrefix: "worker."}, func(e Event) {})
	for i := 0; i < 5; i++ {
		if err := b.Publish(makeEvent(EvtWorkerSpawned, "w1")); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}

	cursor := b.Cursor(sub.ID)
	if cursor != 5 {
		t.Fatalf("expected cursor 5, got %d", cursor)
	}

	// Close and reopen — the WAL should have all 5 events.
	b.Close()

	b2, err := New(dir)
	if err != nil {
		t.Fatalf("New (reopen): %v", err)
	}
	defer b2.Close()

	// Verify the WAL's last seq survived.
	if b2.CurrentSeq() != 5 {
		t.Errorf("expected seq 5 after restart, got %d", b2.CurrentSeq())
	}

	// A new subscriber replaying from cursor can catch up.
	var replayed []Event
	err = b2.Replay(Pattern{TypePrefix: "worker."}, cursor+1, func(e Event) {
		replayed = append(replayed, e)
	})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(replayed) != 0 {
		t.Errorf("expected 0 events after cursor, got %d", len(replayed))
	}

	// Replay from seq 3 should yield events 3, 4, 5.
	err = b2.Replay(Pattern{TypePrefix: "worker."}, 3, func(e Event) {
		replayed = append(replayed, e)
	})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(replayed) != 3 {
		t.Errorf("expected 3 events from seq 3, got %d", len(replayed))
	}
}

func TestHooksPriorityOrder(t *testing.T) {
	b := tempBus(t)

	var order []string
	var mu sync.Mutex

	for _, prio := range []HookPriority{1, 10, 5} {
		p := prio
		err := b.RegisterHook(Hook{
			Pattern:   Pattern{TypePrefix: "worker."},
			Priority:  p,
			Authority: "supervisor",
			Handler: func(ctx context.Context, evt Event) (*HookAction, error) {
				mu.Lock()
				order = append(order, fmt.Sprintf("p%d", p))
				mu.Unlock()
				return nil, nil
			},
		})
		if err != nil {
			t.Fatalf("RegisterHook: %v", err)
		}
	}

	if err := b.Publish(makeEvent(EvtWorkerSpawned, "w1")); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	expected := []string{"p10", "p5", "p1"}
	if len(order) != len(expected) {
		t.Fatalf("expected %d hooks fired, got %d", len(expected), len(order))
	}
	for i, v := range expected {
		if order[i] != v {
			t.Errorf("position %d: expected %s, got %s", i, v, order[i])
		}
	}
}

func TestHookInjectsEvents(t *testing.T) {
	b := tempBus(t)

	var received []EventType
	var mu sync.Mutex
	b.Subscribe(Pattern{}, func(e Event) {
		mu.Lock()
		received = append(received, e.Type)
		mu.Unlock()
	})

	err := b.RegisterHook(Hook{
		Pattern:   Pattern{TypePrefix: "worker.spawned"},
		Priority:  10,
		Authority: "supervisor",
		Handler: func(ctx context.Context, evt Event) (*HookAction, error) {
			return &HookAction{
				InjectEvents: []Event{
					{Type: EvtSupervisorHookInjected, EmitterID: "supervisor"},
				},
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("RegisterHook: %v", err)
	}

	if err := b.Publish(makeEvent(EvtWorkerSpawned, "w1")); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	// Should see both original and injected events.
	if len(received) < 2 {
		t.Fatalf("expected at least 2 events, got %d: %v", len(received), received)
	}
}

func TestPatternMatchesScope(t *testing.T) {
	b := tempBus(t)

	var received []Event
	var mu sync.Mutex
	b.Subscribe(Pattern{
		Scope: &Scope{MissionID: "m1", BranchID: "b1"},
	}, func(e Event) {
		mu.Lock()
		received = append(received, e)
		mu.Unlock()
	})

	// Matching scope.
	evt1 := Event{
		Type:      EvtWorkerSpawned,
		EmitterID: "w1",
		Scope:     Scope{MissionID: "m1", BranchID: "b1"},
	}
	if err := b.Publish(evt1); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// Non-matching scope.
	evt2 := Event{
		Type:      EvtWorkerSpawned,
		EmitterID: "w2",
		Scope:     Scope{MissionID: "m1", BranchID: "b2"},
	}
	if err := b.Publish(evt2); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 {
		t.Fatalf("expected 1 event, got %d", len(received))
	}
}

func TestRemoveHook(t *testing.T) {
	b := tempBus(t)

	var fired bool
	err := b.RegisterHook(Hook{
		ID:        "removable",
		Pattern:   Pattern{TypePrefix: "worker."},
		Priority:  10,
		Authority: "supervisor",
		Handler: func(ctx context.Context, evt Event) (*HookAction, error) {
			fired = true
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("RegisterHook: %v", err)
	}

	b.RemoveHook("removable")

	if err := b.Publish(makeEvent(EvtWorkerSpawned, "w1")); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if fired {
		t.Error("hook should not have fired after removal")
	}
}

func TestCausalRefMustPointToPast(t *testing.T) {
	b := tempBus(t)

	// Publish a base event.
	base := makeEvent(EvtWorkerSpawned, "w1")
	base.ID = "base-evt"
	if err := b.Publish(base); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// A valid causal ref should work.
	child := makeEvent(EvtWorkerActionStarted, "w1")
	child.CausalRef = "base-evt"
	if err := b.Publish(child); err != nil {
		t.Fatalf("Publish with valid causal ref: %v", err)
	}
}

func TestPayloadRoundTrip(t *testing.T) {
	b := tempBus(t)

	type payload struct {
		Message string `json:"message"`
	}
	data, _ := json.Marshal(payload{Message: "hello"})

	evt := makeEvent(EvtWorkerDeclarationDone, "w1")
	evt.Payload = data
	if err := b.Publish(evt); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	var replayed []Event
	err := b.Replay(Pattern{}, 1, func(e Event) {
		replayed = append(replayed, e)
	})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(replayed) != 1 {
		t.Fatalf("expected 1 event, got %d", len(replayed))
	}

	var p payload
	if err := json.Unmarshal(replayed[0].Payload, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if p.Message != "hello" {
		t.Errorf("expected 'hello', got %q", p.Message)
	}
}

func TestDelayedEventSurvivesRestart(t *testing.T) {
	dir := t.TempDir()

	b, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	evt := makeEvent(EvtWorkerSpawned, "w1")
	_, err = b.PublishDelayed(evt, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("PublishDelayed: %v", err)
	}

	// Close immediately (simulating crash before timer fires).
	b.Close()

	// Reopen — delayed event should be restored and fire.
	b2, err := New(dir)
	if err != nil {
		t.Fatalf("New (reopen): %v", err)
	}
	defer b2.Close()

	var received []Event
	var mu sync.Mutex
	b2.Subscribe(Pattern{TypePrefix: "worker."}, func(e Event) {
		mu.Lock()
		received = append(received, e)
		mu.Unlock()
	})

	// Wait for the restored delayed event to fire.
	time.Sleep(400 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 {
		t.Fatalf("expected 1 event after restart, got %d", len(received))
	}
}

func TestCancellationSurvivesRestart(t *testing.T) {
	dir := t.TempDir()

	b, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	evt := makeEvent(EvtWorkerSpawned, "w1")
	cancelID, err := b.PublishDelayed(evt, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("PublishDelayed: %v", err)
	}

	if err := b.CancelDelayed(cancelID); err != nil {
		t.Fatalf("CancelDelayed: %v", err)
	}

	b.Close()

	// Reopen — cancelled event should NOT fire.
	b2, err := New(dir)
	if err != nil {
		t.Fatalf("New (reopen): %v", err)
	}
	defer b2.Close()

	var received []Event
	var mu sync.Mutex
	b2.Subscribe(Pattern{TypePrefix: "worker."}, func(e Event) {
		mu.Lock()
		received = append(received, e)
		mu.Unlock()
	})

	time.Sleep(400 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 0 {
		t.Fatalf("expected 0 events after cancelled restart, got %d", len(received))
	}
}

func TestTwoSubscribersSamePattern(t *testing.T) {
	b := tempBus(t)

	var order1, order2 []uint64
	var mu sync.Mutex

	b.Subscribe(Pattern{TypePrefix: "worker."}, func(e Event) {
		mu.Lock()
		order1 = append(order1, e.Sequence)
		mu.Unlock()
	})
	b.Subscribe(Pattern{TypePrefix: "worker."}, func(e Event) {
		mu.Lock()
		order2 = append(order2, e.Sequence)
		mu.Unlock()
	})

	for i := 0; i < 5; i++ {
		if err := b.Publish(makeEvent(EvtWorkerSpawned, "w1")); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(order1) != 5 || len(order2) != 5 {
		t.Fatalf("expected both subscribers to get 5 events, got %d and %d", len(order1), len(order2))
	}
	for i := range order1 {
		if order1[i] != order2[i] {
			t.Errorf("position %d: subscriber1 got seq %d, subscriber2 got seq %d", i, order1[i], order2[i])
		}
	}
}

func TestSubscriptionCancel(t *testing.T) {
	b := tempBus(t)

	var count int
	sub := b.Subscribe(Pattern{TypePrefix: "worker."}, func(e Event) {
		count++
	})

	if err := b.Publish(makeEvent(EvtWorkerSpawned, "w1")); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 event before cancel, got %d", count)
	}

	sub.Cancel()

	if err := b.Publish(makeEvent(EvtWorkerSpawned, "w1")); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 event after cancel, got %d", count)
	}
}

func TestClosedBusRejectsPublish(t *testing.T) {
	dir := t.TempDir()
	b, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	b.Close()

	if err := b.Publish(makeEvent(EvtWorkerSpawned, "w1")); err == nil {
		t.Fatal("expected error publishing to closed bus")
	}
}

func TestEventLogFileExists(t *testing.T) {
	dir := t.TempDir()
	b, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer b.Close()

	if err := b.Publish(makeEvent(EvtWorkerSpawned, "w1")); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// Verify the events.log file exists and has content.
	info, err := os.Stat(dir + "/events.log")
	if err != nil {
		t.Fatalf("events.log not found: %v", err)
	}
	if info.Size() == 0 {
		t.Error("events.log is empty after publish")
	}
}

