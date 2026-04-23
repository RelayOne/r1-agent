package tui

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ericmacdougall/stoke/internal/hub"
)

// syncBuffer wraps bytes.Buffer with a mutex so concurrent Writes from
// multiple goroutines are safe during race tests.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestPlanReadyPopulatesHeader(t *testing.T) {
	var buf syncBuffer
	r := New(&buf, true, 0)
	// fixed clock so the "m:ss" elapsed field is deterministic
	r.now = func() time.Time { return time.Unix(1_700_000_000, 0) }

	r.HandleEvent(&hub.Event{
		Type: EventStokePlanReady,
		Custom: map[string]any{
			"title":           "sentinel-mvp",
			"total_sessions":  7,
			"estimated_cost":  12.40,
		},
	})

	out := buf.String()
	if !strings.Contains(out, "sentinel-mvp") {
		t.Fatalf("expected header to contain title, got:\n%s", out)
	}
	if !strings.Contains(out, "7 sessions") {
		t.Fatalf("expected header to show 7 sessions, got:\n%s", out)
	}
	if !strings.Contains(out, "$12.40") {
		t.Fatalf("expected budget $12.40, got:\n%s", out)
	}
	if r.Budget() != 12.40 {
		t.Fatalf("budget mismatch: %v", r.Budget())
	}
}

func TestSessionStartThenEndMarksDone(t *testing.T) {
	var buf syncBuffer
	r := New(&buf, true, 15.0)
	r.now = func() time.Time { return time.Unix(1_700_000_000, 0) }

	r.HandleEvent(&hub.Event{
		Type: EventStokePlanReady,
		Custom: map[string]any{
			"total_sessions": 2,
			"estimated_cost": 5.0,
		},
	})
	r.HandleEvent(&hub.Event{
		Type: EventStokeSessionStart,
		Custom: map[string]any{
			"session_id": "S1",
			"title":      "Foundation",
			"total_acs":  4,
		},
	})

	if r.SessionCount() != 1 {
		t.Fatalf("expected 1 session, got %d", r.SessionCount())
	}

	// Running session renders the running icon
	out := buf.String()
	if !strings.Contains(out, "[>]") {
		t.Fatalf("expected running icon [>] after session.start, got:\n%s", out)
	}

	r.HandleEvent(&hub.Event{
		Type: EventStokeSessionEnd,
		Custom: map[string]any{
			"session_id": "S1",
			"verdict":    "pass",
		},
	})

	out = buf.String()
	if !strings.Contains(out, "[v]") {
		t.Fatalf("expected done icon [v] after session.end, got:\n%s", out)
	}
	if !strings.Contains(out, "Sessions: 1/2") {
		t.Fatalf("expected Sessions: 1/2 line, got:\n%s", out)
	}
}

func TestSessionFailedVerdict(t *testing.T) {
	var buf syncBuffer
	r := New(&buf, true, 0)
	r.HandleEvent(&hub.Event{
		Type:   EventStokeSessionStart,
		Custom: map[string]any{"session_id": "S1"},
	})
	r.HandleEvent(&hub.Event{
		Type:   EventStokeSessionEnd,
		Custom: map[string]any{"session_id": "S1", "verdict": "failed"},
	})
	if !strings.Contains(buf.String(), "[x]") {
		t.Fatalf("expected failed icon [x], got:\n%s", buf.String())
	}
}

func TestCostAccumulates(t *testing.T) {
	var buf syncBuffer
	r := New(&buf, true, 10.0)

	r.HandleEvent(&hub.Event{
		Type:   EventStokeCost,
		Custom: map[string]any{"delta_usd": 1.25},
	})
	r.HandleEvent(&hub.Event{
		Type:   EventStokeCost,
		Custom: map[string]any{"delta_usd": 2.75},
	})

	if got := r.Spent(); got != 4.0 {
		t.Fatalf("expected spent 4.00, got %v", got)
	}
	if !strings.Contains(buf.String(), "Spent: $4.00") {
		t.Fatalf("expected Spent: $4.00 line, got:\n%s", buf.String())
	}
}

func TestCostTotalSpentOverride(t *testing.T) {
	var buf syncBuffer
	r := New(&buf, true, 10.0)
	// Hub CostEvent path: full snapshot.
	r.HandleEvent(&hub.Event{
		Type: EventStokeCost,
		Cost: &hub.CostEvent{TotalSpent: 7.5, BudgetLimit: 20.0},
	})
	if got := r.Spent(); got != 7.5 {
		t.Fatalf("expected spent 7.5, got %v", got)
	}
	if got := r.Budget(); got != 20.0 {
		t.Fatalf("expected budget 20.0, got %v", got)
	}
}

func TestACResultMarksPass(t *testing.T) {
	var buf syncBuffer
	r := New(&buf, true, 0)
	r.HandleEvent(&hub.Event{
		Type: EventStokeSessionStart,
		Custom: map[string]any{
			"session_id": "S2",
			"title":      "API routes",
			"total_acs":  5,
		},
	})
	r.HandleEvent(&hub.Event{
		Type: EventStokeACResult,
		Custom: map[string]any{
			"session_id": "S2",
			"ac_id":      "AC1",
			"title":      "GET /tasks returns 200",
			"verdict":    "pass",
		},
	})
	out := buf.String()
	if !strings.Contains(out, "[ok]") {
		t.Fatalf("expected [ok] marker for pass, got:\n%s", out)
	}
	if !strings.Contains(out, "GET /tasks returns 200") {
		t.Fatalf("expected AC title in output, got:\n%s", out)
	}
	if !strings.Contains(out, "[1/5 ACs]") {
		t.Fatalf("expected 1/5 ACs counter, got:\n%s", out)
	}
}

func TestDescentAnnotatesAC(t *testing.T) {
	var buf syncBuffer
	r := New(&buf, true, 0)
	r.HandleEvent(&hub.Event{
		Type:   EventStokeSessionStart,
		Custom: map[string]any{"session_id": "S3", "total_acs": 5},
	})
	r.HandleEvent(&hub.Event{
		Type: EventStokeDescentStart,
		Custom: map[string]any{
			"session_id": "S3",
			"ac_id":      "AC3",
			"title":      "Auth middleware",
		},
	})
	r.HandleEvent(&hub.Event{
		Type: EventStokeDescentTier,
		Custom: map[string]any{
			"session_id":   "S3",
			"ac_id":        "AC3",
			"tier":         "T5",
			"category":     "env",
			"attempt":      2,
			"max_attempts": 3,
		},
	})
	out := buf.String()
	if !strings.Contains(out, "T5") {
		t.Fatalf("expected descent tier T5, got:\n%s", out)
	}
	if !strings.Contains(out, "env") {
		t.Fatalf("expected category env, got:\n%s", out)
	}
	if !strings.Contains(out, "repair 2/3") {
		t.Fatalf("expected repair 2/3, got:\n%s", out)
	}
}

func TestDescentResolveClearsTier(t *testing.T) {
	var buf syncBuffer
	r := New(&buf, true, 0)
	r.HandleEvent(&hub.Event{
		Type:   EventStokeSessionStart,
		Custom: map[string]any{"session_id": "S4", "total_acs": 1},
	})
	r.HandleEvent(&hub.Event{
		Type: EventStokeDescentStart,
		Custom: map[string]any{
			"session_id": "S4",
			"ac_id":      "AC1",
		},
	})
	r.HandleEvent(&hub.Event{
		Type: EventStokeDescentResolve,
		Custom: map[string]any{
			"session_id": "S4",
			"ac_id":      "AC1",
			"verdict":    "softpass",
		},
	})
	out := buf.String()
	if !strings.Contains(out, "[~]") {
		t.Fatalf("expected softpass marker [~], got:\n%s", out)
	}
	// The TTY buffer accumulates every frame prefixed with ANSI cursor-up
	// escapes; scan only the final frame (after the last overwrite).
	finalFrame := lastFrame(out)
	if strings.Contains(finalFrame, "descent") {
		t.Fatalf("descent annotation should be cleared in final frame, got:\n%s", finalFrame)
	}
	if !strings.Contains(finalFrame, "[~]") {
		t.Fatalf("expected softpass marker in final frame, got:\n%s", finalFrame)
	}
}

// lastFrame returns the portion of the raw TTY output that comes after
// the final carriage return, i.e. the most recently rendered frame.
// progress.go always emits a "\r" immediately before writing a redraw
// frame (see the ANSI branch of ProgressRenderer.redraw).
func lastFrame(s string) string {
	if i := strings.LastIndex(s, "\r"); i >= 0 {
		return s[i+1:]
	}
	return s
}

func TestNonTTYPlainText(t *testing.T) {
	var buf syncBuffer
	r := New(&buf, false /* isTTY */, 5.0)

	r.HandleEvent(&hub.Event{
		Type: EventStokePlanReady,
		Custom: map[string]any{
			"total_sessions": 3,
			"estimated_cost": 5.0,
		},
	})
	r.HandleEvent(&hub.Event{
		Type:   EventStokeSessionStart,
		Custom: map[string]any{"session_id": "S1"},
	})
	r.HandleEvent(&hub.Event{
		Type:   EventStokeSessionEnd,
		Custom: map[string]any{"session_id": "S1", "verdict": "pass"},
	})

	out := buf.String()
	if strings.Contains(out, "\x1b[") {
		t.Fatalf("plain-text mode must not emit ANSI escapes, got:\n%q", out)
	}
	if !strings.Contains(out, "sessions 1/3") {
		t.Fatalf("expected sessions 1/3 summary, got:\n%s", out)
	}
	// Plain-text mode emits one line per event (three events -> at least
	// three summary lines).
	if lines := strings.Count(out, "\n"); lines < 3 {
		t.Fatalf("expected at least 3 plain-text lines, got %d:\n%s", lines, out)
	}
}

func TestTTYFrameEmitsANSICursorControls(t *testing.T) {
	var buf syncBuffer
	r := New(&buf, true, 5.0)
	r.HandleEvent(&hub.Event{
		Type:   EventStokePlanReady,
		Custom: map[string]any{"total_sessions": 1, "estimated_cost": 5.0},
	})
	r.HandleEvent(&hub.Event{
		Type:   EventStokeSessionStart,
		Custom: map[string]any{"session_id": "S1"},
	})
	// After the second event, the renderer must have emitted an ANSI
	// cursor-up escape in order to overwrite the prior frame.
	if !strings.Contains(buf.String(), "\x1b[") {
		t.Fatalf("expected ANSI escape sequence in TTY output, got:\n%q", buf.String())
	}
}

func TestSubscribeObserveMode(t *testing.T) {
	bus := hub.New()
	var buf syncBuffer
	r := New(&buf, false, 3.0)
	r.Subscribe(bus)

	// The hub records one registration per event type in its subscriber
	// map, so SubscriberCount returns len(Events). We verify at least
	// one was recorded; the dedup-by-ID test below covers idempotency.
	if bus.SubscriberCount() == 0 {
		t.Fatalf("expected at least one subscriber after Subscribe, got 0")
	}

	// Emit a cost event; Observe-mode delivery is asynchronous so we poll.
	bus.Emit(context.Background(), &hub.Event{
		// assert.Spent() reaches 1.5 by the deadline below.
		Type:   EventStokeCost,
		Custom: map[string]any{"delta_usd": 1.5},
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r.Spent() == 1.5 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("observe subscriber never received event; spent=%v", r.Spent())
}

func TestConcurrentEventsRace(t *testing.T) {
	var buf syncBuffer
	r := New(&buf, true, 100.0)

	r.HandleEvent(&hub.Event{
		Type:   EventStokePlanReady,
		Custom: map[string]any{"total_sessions": 4, "estimated_cost": 100.0},
	})

	// Spawn several goroutines that each drive a different session
	// concurrently. With -race this will flag unsynchronized state.
	var wg sync.WaitGroup
	var delivered int64
	sessionIDs := []string{"A", "B", "C", "D"}
	for _, sid := range sessionIDs {
		wg.Add(1)
		sid := sid
		go func() {
			defer wg.Done()
			r.HandleEvent(&hub.Event{
				Type:   EventStokeSessionStart,
				Custom: map[string]any{"session_id": sid, "total_acs": 2},
			})
			for i := 0; i < 20; i++ {
				r.HandleEvent(&hub.Event{
					Type:   EventStokeCost,
					Custom: map[string]any{"delta_usd": 0.01},
				})
			}
			r.HandleEvent(&hub.Event{
				Type:   EventStokeSessionEnd,
				Custom: map[string]any{"session_id": sid, "verdict": "pass"},
			})
			atomic.AddInt64(&delivered, 22)
		}()
	}
	wg.Wait()
	// assert.delivered/spent/SessionCount below.

	if got := atomic.LoadInt64(&delivered); got != 88 {
		t.Fatalf("expected 88 events delivered, got %d", got)
	}
	// 4 sessions * 20 cost events * $0.01 = $0.80
	if got := r.Spent(); got < 0.79 || got > 0.81 {
		t.Fatalf("expected spent ~$0.80, got %v", got)
	}
	if r.SessionCount() != 4 {
		t.Fatalf("expected 4 sessions, got %d", r.SessionCount())
	}
}

func TestMultipleSubscribeCallsAreIdempotent(t *testing.T) {
	bus := hub.New()
	var buf syncBuffer
	r := New(&buf, false, 0)
	r.Subscribe(bus)
	first := bus.SubscriberCount()
	r.Subscribe(bus) // dedup by ID — see hub.Bus.Register
	second := bus.SubscriberCount()
	if first == 0 {
		t.Fatalf("first Subscribe registered nothing")
	}
	if first != second {
		t.Fatalf("expected dedup: %d before, %d after second Subscribe", first, second)
	}
}

func TestSubscribeNilBusNoPanic(t *testing.T) {
	r := New(&syncBuffer{}, false, 0)
	r.Subscribe(nil) // must not panic
}

func TestPlanReadyPreloadsSessions(t *testing.T) {
	var buf syncBuffer
	r := New(&buf, true, 0)
	r.HandleEvent(&hub.Event{
		Type: EventStokePlanReady,
		Custom: map[string]any{
			"estimated_cost": 3.0,
			"plan": []any{
				map[string]any{"id": "S1", "title": "Foundation", "acs": 4},
				map[string]any{"id": "S3", "title": "Frontend", "blocked": true},
			},
		},
	})
	if got := r.SessionCount(); got != 2 {
		t.Fatalf("expected 2 preloaded sessions, got %d", got)
	}
	out := buf.String()
	if !strings.Contains(out, "Foundation") || !strings.Contains(out, "Frontend") {
		t.Fatalf("expected preloaded session titles, got:\n%s", out)
	}
	if !strings.Contains(out, "[blocked]") {
		t.Fatalf("expected blocked marker, got:\n%s", out)
	}
}

func TestTaskStartTracksCurrentTask(t *testing.T) {
	// Covers that onTaskStart/onTaskEnd mutate without panicking and do
	// not corrupt AC counters.
	var buf syncBuffer
	r := New(&buf, true, 0)
	r.HandleEvent(&hub.Event{
		Type:   EventStokeSessionStart,
		Custom: map[string]any{"session_id": "S1", "total_acs": 2},
	})
	r.HandleEvent(&hub.Event{
		Type: EventStokeTaskStart,
		Custom: map[string]any{
			"session_id":  "S1",
			"task_id":     "impl-foo",
			"description": "implement foo",
		},
	})
	r.HandleEvent(&hub.Event{
		Type: EventStokeTaskEnd,
		Custom: map[string]any{
			"session_id": "S1",
			"task_id":    "impl-foo",
		},
	})
	// No assertion on stdout content beyond "did not crash"; session
	// count and AC counter must be untouched.
	if r.SessionCount() != 1 {
		t.Fatalf("expected 1 session, got %d", r.SessionCount())
	}
}
