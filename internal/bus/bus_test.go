package bus

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
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

// waitFor polls a condition with a timeout. Use only in tests.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("waitFor: condition not met within timeout")
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

	if err := b.Publish(makeEvent(EvtWorkerSpawned, "w1")); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if err := b.Publish(makeEvent(EvtMissionStarted, "m1")); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	waitFor(t, time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) >= 1
	})

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

	waitFor(t, time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(order) >= 2
	})

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
	time.Sleep(10 * time.Millisecond)
	mu.Lock()
	count := len(received)
	mu.Unlock()
	if count != 0 {
		t.Fatalf("expected 0 events immediately, got %d", count)
	}

	// Wait for delivery.
	waitFor(t, 500*time.Millisecond, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) >= 1
	})
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

	sub := b.Subscribe(Pattern{TypePrefix: "worker."}, func(e Event) {})
	for i := 0; i < 5; i++ {
		if err := b.Publish(makeEvent(EvtWorkerSpawned, "w1")); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}

	// Wait for async delivery.
	time.Sleep(50 * time.Millisecond)

	cursor := b.Cursor(sub.ID)
	if cursor != 5 {
		t.Fatalf("expected cursor 5, got %d", cursor)
	}

	b.Close()

	b2, err := New(dir)
	if err != nil {
		t.Fatalf("New (reopen): %v", err)
	}
	defer b2.Close()

	if b2.CurrentSeq() != 5 {
		t.Errorf("expected seq 5 after restart, got %d", b2.CurrentSeq())
	}

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

	waitFor(t, time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) >= 2
	})
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

	waitFor(t, time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) >= 1
	})

	// Brief additional wait to confirm no extra events arrive.
	time.Sleep(50 * time.Millisecond)

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

	time.Sleep(50 * time.Millisecond)

	if fired {
		t.Error("hook should not have fired after removal")
	}
}

func TestCausalRefMustPointToPast(t *testing.T) {
	b := tempBus(t)

	base := makeEvent(EvtWorkerSpawned, "w1")
	base.ID = "base-evt"
	if err := b.Publish(base); err != nil {
		t.Fatalf("Publish: %v", err)
	}

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

	b.Close()

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

	waitFor(t, time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) >= 1
	})
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

	waitFor(t, time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(order1) >= 5 && len(order2) >= 5
	})

	mu.Lock()
	defer mu.Unlock()
	for i := range order1 {
		if order1[i] != order2[i] {
			t.Errorf("position %d: subscriber1 got seq %d, subscriber2 got seq %d", i, order1[i], order2[i])
		}
	}
}

func TestSubscriptionCancel(t *testing.T) {
	b := tempBus(t)

	var count atomic.Int32
	sub := b.Subscribe(Pattern{TypePrefix: "worker."}, func(e Event) {
		count.Add(1)
	})

	if err := b.Publish(makeEvent(EvtWorkerSpawned, "w1")); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	waitFor(t, time.Second, func() bool {
		return count.Load() >= 1
	})

	sub.Cancel()

	if err := b.Publish(makeEvent(EvtWorkerSpawned, "w1")); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	if count.Load() != 1 {
		t.Errorf("expected 1 event after cancel, got %d", count.Load())
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

	info, err := os.Stat(dir + "/events.log")
	if err != nil {
		t.Fatalf("events.log not found: %v", err)
	}
	if info.Size() == 0 {
		t.Error("events.log is empty after publish")
	}
}

// --- Fix #2 acceptance tests ---

func TestSubscriberDeliveryIsAsync(t *testing.T) {
	b := tempBus(t)

	// The handler blocks on a channel — Publish must return before it completes.
	blockCh := make(chan struct{})
	var delivered atomic.Bool

	b.Subscribe(Pattern{TypePrefix: "worker."}, func(e Event) {
		<-blockCh // block until unblocked
		delivered.Store(true)
	})

	if err := b.Publish(makeEvent(EvtWorkerSpawned, "w1")); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// Publish returned. The handler should NOT have completed yet.
	if delivered.Load() {
		t.Fatal("handler should not have completed — delivery should be async")
	}

	// Unblock the handler.
	close(blockCh)

	waitFor(t, time.Second, func() bool {
		return delivered.Load()
	})
}

func TestSubscriberPanicDoesNotCrashBus(t *testing.T) {
	b := tempBus(t)

	var panicSubReceived atomic.Int32
	var goodSubReceived atomic.Int32

	// Panicking subscriber.
	b.Subscribe(Pattern{TypePrefix: "worker."}, func(e Event) {
		panicSubReceived.Add(1)
		panic("intentional test panic")
	})

	// Good subscriber.
	b.Subscribe(Pattern{TypePrefix: "worker."}, func(e Event) {
		goodSubReceived.Add(1)
	})

	// Publish two events. The bus should survive the panics.
	for i := 0; i < 2; i++ {
		if err := b.Publish(makeEvent(EvtWorkerSpawned, "w1")); err != nil {
			t.Fatalf("Publish %d: %v", i, err)
		}
	}

	waitFor(t, time.Second, func() bool {
		return goodSubReceived.Load() >= 2
	})

	// The panicking subscriber should have been called twice (recovered each time).
	waitFor(t, time.Second, func() bool {
		return panicSubReceived.Load() >= 2
	})
}

func TestSlowSubscriberDoesNotBlockOthers(t *testing.T) {
	b := tempBus(t)

	var fastReceived atomic.Int32

	// Slow subscriber.
	b.Subscribe(Pattern{TypePrefix: "worker."}, func(e Event) {
		time.Sleep(200 * time.Millisecond)
	})

	// Fast subscriber.
	b.Subscribe(Pattern{TypePrefix: "worker."}, func(e Event) {
		fastReceived.Add(1)
	})

	start := time.Now()
	for i := 0; i < 10; i++ {
		if err := b.Publish(makeEvent(EvtWorkerSpawned, "w1")); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}

	// Fast subscriber should get all 10 events well before the slow one finishes.
	waitFor(t, time.Second, func() bool {
		return fastReceived.Load() >= 10
	})

	elapsed := time.Since(start)
	// If delivery were synchronous, publishing 10 events with a 200ms handler
	// would take 2+ seconds. Async should be well under 1s.
	if elapsed > time.Second {
		t.Fatalf("fast subscriber took too long (%s) — delivery may not be async", elapsed)
	}
}

func TestHookActionErrorPublishesFailureEvent(t *testing.T) {
	b := tempBus(t)

	// Register a hook that returns an error.
	err := b.RegisterHook(Hook{
		ID:        "failing-hook",
		Pattern:   Pattern{TypePrefix: "worker."},
		Priority:  10,
		Authority: "supervisor",
		Handler: func(ctx context.Context, evt Event) (*HookAction, error) {
			return nil, fmt.Errorf("intentional hook failure")
		},
	})
	if err != nil {
		t.Fatalf("RegisterHook: %v", err)
	}

	if err := b.Publish(makeEvent(EvtWorkerSpawned, "w1")); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// The failure event should be in the WAL.
	var found bool
	err = b.Replay(Pattern{TypePrefix: "bus.hook.action_failed"}, 1, func(e Event) {
		found = true
		var payload map[string]any
		if jsonErr := json.Unmarshal(e.Payload, &payload); jsonErr != nil {
			t.Fatalf("unmarshal: %v", jsonErr)
		}
		if payload["hook_id"] != "failing-hook" {
			t.Errorf("expected hook_id=failing-hook, got %v", payload["hook_id"])
		}
		errStr, _ := payload["error"].(string)
		if errStr != "intentional hook failure" {
			t.Errorf("expected error message, got %q", errStr)
		}
	})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if !found {
		t.Fatal("expected bus.hook.action_failed event in WAL")
	}
}

func TestHookPanicIsRecovered(t *testing.T) {
	b := tempBus(t)

	var goodHookFired atomic.Bool

	// Panicking hook (higher priority — fires first).
	err := b.RegisterHook(Hook{
		Pattern:   Pattern{TypePrefix: "worker."},
		Priority:  100,
		Authority: "supervisor",
		Handler: func(ctx context.Context, evt Event) (*HookAction, error) {
			panic("hook panic")
		},
	})
	if err != nil {
		t.Fatalf("RegisterHook: %v", err)
	}

	// Lower priority hook should still fire.
	err = b.RegisterHook(Hook{
		Pattern:   Pattern{TypePrefix: "worker."},
		Priority:  1,
		Authority: "supervisor",
		Handler: func(ctx context.Context, evt Event) (*HookAction, error) {
			goodHookFired.Store(true)
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("RegisterHook: %v", err)
	}

	if err := b.Publish(makeEvent(EvtWorkerSpawned, "w1")); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if !goodHookFired.Load() {
		t.Fatal("lower-priority hook should have fired despite higher-priority hook panicking")
	}
}

func TestInjectedEventDeliveredAfterOriginalSubscribers(t *testing.T) {
	b := tempBus(t)
	defer b.Close()

	// Track the order in which a subscriber sees events.
	var mu sync.Mutex
	var seen []EventType

	b.Subscribe(Pattern{}, func(evt Event) {
		// Wildcard subscriber: sees everything.
		mu.Lock()
		seen = append(seen, evt.Type)
		mu.Unlock()
	})

	// Hook that injects a second event when it sees the original.
	if err := b.RegisterHook(Hook{
		ID:        "injector",
		Pattern:   Pattern{TypePrefix: "original."},
		Priority:  100,
		Authority: "supervisor",
		Handler: func(_ context.Context, evt Event) (*HookAction, error) {
			return &HookAction{
				InjectEvents: []Event{{Type: "injected.consequence"}},
			}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Publish the original event.
	if err := b.Publish(Event{Type: "original.trigger"}); err != nil {
		t.Fatal(err)
	}

	// Wait for async delivery.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(seen)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()

	if len(seen) < 2 {
		t.Fatalf("expected at least 2 events, got %d: %v", len(seen), seen)
	}

	// The original event MUST be seen before the injected event.
	if seen[0] != "original.trigger" {
		t.Errorf("first event should be original.trigger, got %s", seen[0])
	}
	if seen[1] != "injected.consequence" {
		t.Errorf("second event should be injected.consequence, got %s", seen[1])
	}
}

func TestPrefixIndexNarrowsLookup(t *testing.T) {
	b := tempBus(t)
	defer b.Close()

	var workerCount, ledgerCount int32

	// Subscribe to worker events only.
	b.Subscribe(Pattern{TypePrefix: "worker."}, func(evt Event) {
		atomic.AddInt32(&workerCount, 1)
	})

	// Subscribe to ledger events only.
	b.Subscribe(Pattern{TypePrefix: "ledger."}, func(evt Event) {
		atomic.AddInt32(&ledgerCount, 1)
	})

	// Publish a worker event — should only reach the worker subscriber.
	if err := b.Publish(Event{Type: EvtWorkerSpawned}); err != nil {
		t.Fatal(err)
	}

	// Publish a ledger event — should only reach the ledger subscriber.
	if err := b.Publish(Event{Type: "ledger.node.added"}); err != nil {
		t.Fatal(err)
	}

	// Wait for delivery.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&workerCount) >= 1 && atomic.LoadInt32(&ledgerCount) >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if wc := atomic.LoadInt32(&workerCount); wc != 1 {
		t.Errorf("worker subscriber got %d events, want 1", wc)
	}
	if lc := atomic.LoadInt32(&ledgerCount); lc != 1 {
		t.Errorf("ledger subscriber got %d events, want 1", lc)
	}
}

func TestPrefixIndexWildcardSubscriber(t *testing.T) {
	b := tempBus(t)
	defer b.Close()

	var wildcardCount int32
	var specificCount int32

	// Wildcard subscriber (empty prefix) sees all events.
	b.Subscribe(Pattern{}, func(evt Event) {
		atomic.AddInt32(&wildcardCount, 1)
	})

	// Specific subscriber sees only worker events.
	b.Subscribe(Pattern{TypePrefix: "worker."}, func(evt Event) {
		atomic.AddInt32(&specificCount, 1)
	})

	// Publish events of different types.
	b.Publish(Event{Type: "worker.spawned"})
	b.Publish(Event{Type: "ledger.node.added"})
	b.Publish(Event{Type: "bus.internal"})

	// Wait for delivery.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&wildcardCount) >= 3 && atomic.LoadInt32(&specificCount) >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if wc := atomic.LoadInt32(&wildcardCount); wc != 3 {
		t.Errorf("wildcard subscriber got %d events, want 3", wc)
	}
	if sc := atomic.LoadInt32(&specificCount); sc != 1 {
		t.Errorf("specific subscriber got %d events, want 1", sc)
	}
}
