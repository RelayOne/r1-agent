package remote

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/hub"
)

// progressServer records every PUT /v1/sessions/<id> body so tests can
// assert that hub events actually reach the Ember API as Update calls.
type progressServer struct {
	mu      sync.Mutex
	updates []SessionUpdate
	srv     *httptest.Server
}

func newProgressServer(t *testing.T) *progressServer {
	t.Helper()
	ps := &progressServer{}
	ps.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sessions":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"session_id":"sess-1","url":"https://dash.example/s/sess-1"}`))
		case r.Method == http.MethodPut && r.URL.Path == "/v1/sessions/sess-1":
			var u SessionUpdate
			if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
				t.Errorf("decode update body: %v", err)
			}
			ps.mu.Lock()
			ps.updates = append(ps.updates, u)
			ps.mu.Unlock()
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(ps.srv.Close)
	return ps
}

// snapshotOf returns a copy of the recorded updates.
func (ps *progressServer) snapshotOf() []SessionUpdate {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	out := make([]SessionUpdate, len(ps.updates))
	copy(out, ps.updates)
	return out
}

// waitFor polls until cond(updates) is true or the deadline passes.
func (ps *progressServer) waitFor(t *testing.T, cond func([]SessionUpdate) bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond(ps.snapshotOf()) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitState polls the bridge's internal state until cond is true, so
// sequential bus emits (delivered on fire-and-forget observer
// goroutines) are folded in a deterministic order.
func waitState(t *testing.T, p *HubProgress, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		ok := cond()
		p.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("timed out waiting for bridge state")
}

// TestHubProgress_PushesSnapshotsThroughBus is the A038 regression test:
// task lifecycle + model-cost events emitted on a hub.Bus must reach the
// Ember API as PUT /v1/sessions/<id> bodies with non-empty Tasks.
func TestHubProgress_PushesSnapshotsThroughBus(t *testing.T) {
	ps := newProgressServer(t)

	rep := &SessionReporter{endpoint: ps.srv.URL, apiKey: "k", client: ps.srv.Client()}
	if _, err := rep.RegisterSession("plan-1"); err != nil {
		t.Fatalf("RegisterSession: %v", err)
	}

	hp := newHubProgress(rep, "plan-1", 5*time.Millisecond)
	bus := hub.New()
	hp.Register(bus)

	ctx := context.Background()

	// Observer delivery is fire-and-forget, so sequence the emits by
	// waiting for each one's effect before sending the next — mirroring
	// the real per-task ordering (workflow runs phases sequentially).
	bus.Emit(ctx, &hub.Event{Type: hub.EventTaskStarted, TaskID: "T1", Phase: "execute"})
	waitState(t, hp, func() bool { return hp.tasks["T1"] != nil })

	bus.Emit(ctx, &hub.Event{
		Type: hub.EventModelPostCall, TaskID: "T1", Phase: "execute",
		Model: &hub.ModelEvent{Provider: "claude", CostUSD: 0.25},
	})
	waitState(t, hp, func() bool { return hp.tasks["T1"].prog.CostUSD == 0.25 })

	bus.Emit(ctx, &hub.Event{
		Type: hub.EventTaskCompleted, TaskID: "T1",
		Model: &hub.ModelEvent{CostUSD: 0.40},
	})
	waitState(t, hp, func() bool { return hp.tasks["T1"].terminal })

	// Wait until the terminal snapshot lands on the server, then Stop
	// (which performs one final flush).
	ps.waitFor(t, func(us []SessionUpdate) bool {
		for _, u := range us {
			for _, tp := range u.Tasks {
				if tp.TaskID == "T1" && tp.Phase == "completed" {
					return true
				}
			}
		}
		return false
	})
	hp.Stop()

	updates := ps.snapshotOf()
	if len(updates) == 0 {
		t.Fatal("no PUT /v1/sessions/<id> progress updates reached the server")
	}
	last := updates[len(updates)-1]
	if last.SessionID != "sess-1" {
		t.Errorf("SessionID = %q, want sess-1 (Update must stamp the registered session)", last.SessionID)
	}
	if last.PlanID != "plan-1" {
		t.Errorf("PlanID = %q, want plan-1", last.PlanID)
	}
	if len(last.Tasks) != 1 || last.Tasks[0].TaskID != "T1" {
		t.Fatalf("last update tasks = %+v, want exactly task T1", last.Tasks)
	}
	if got := last.Tasks[0].Phase; got != "completed" {
		t.Errorf("final phase = %q, want completed", got)
	}
	// The terminal event's total (0.40) must REPLACE the per-call
	// accumulation (0.25), not add to it.
	if got := last.Tasks[0].CostUSD; got != 0.40 {
		t.Errorf("final task cost = %v, want 0.40 (authoritative total, no double count)", got)
	}
	if got := last.TotalCostUSD; got != 0.40 {
		t.Errorf("TotalCostUSD = %v, want 0.40", got)
	}
	if last.BurstWorkers != 0 {
		t.Errorf("BurstWorkers = %d, want 0 after terminal event", last.BurstWorkers)
	}
	if last.Tasks[0].Worker != "claude" {
		t.Errorf("Worker = %q, want claude (from model post-call)", last.Tasks[0].Worker)
	}
}

// TestHubProgress_ThrottlesBursts verifies that a burst of events
// produces far fewer HTTP pushes than events (bounded kick channel +
// one push per interval), and that Stop's final flush still lands.
func TestHubProgress_ThrottlesBursts(t *testing.T) {
	ps := newProgressServer(t)

	rep := &SessionReporter{endpoint: ps.srv.URL, apiKey: "k", client: ps.srv.Client()}
	if _, err := rep.RegisterSession("plan-2"); err != nil {
		t.Fatalf("RegisterSession: %v", err)
	}

	// Long interval: at most ONE timed push can happen during the burst.
	hp := newHubProgress(rep, "plan-2", time.Hour)

	for i := 0; i < 50; i++ {
		hp.handle(context.Background(), &hub.Event{
			Type: hub.EventModelPostCall, TaskID: "T1",
			Model: &hub.ModelEvent{CostUSD: 0.01},
		})
	}
	// One push may be in flight from the first kick; Stop flushes once more.
	hp.Stop()

	updates := ps.snapshotOf()
	if len(updates) == 0 {
		t.Fatal("expected at least the final flush to reach the server")
	}
	if len(updates) > 3 {
		t.Errorf("50 events produced %d pushes, want <=3 (throttling broken)", len(updates))
	}
	last := updates[len(updates)-1]
	if len(last.Tasks) != 1 {
		t.Fatalf("last update tasks = %+v, want one task", last.Tasks)
	}
	if got := last.Tasks[0].CostUSD; got < 0.499 || got > 0.501 {
		t.Errorf("accumulated cost = %v, want ~0.50", got)
	}
}

// TestHubProgress_NilSafety: a nil bridge (no EMBER_API_KEY) must be
// inert everywhere the caller touches it.
func TestHubProgress_NilSafety(t *testing.T) {
	var hp *HubProgress
	if got := NewHubProgress(nil, "plan"); got != nil {
		t.Fatalf("NewHubProgress(nil reporter) = %v, want nil", got)
	}
	hp.Register(hub.New()) // must not panic
	hp.Stop()              // must not panic
}
