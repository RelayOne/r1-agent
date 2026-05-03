package cortex

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/agentloop"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/stokerr"
)

// laneEventRecorder is a tiny hub subscriber that captures every lane
// event into an ordered slice under its own mutex. The hub fires
// EmitAsync on a goroutine, so tests use waitForEvents to synchronize.
type laneEventRecorder struct {
	mu     sync.Mutex
	events []recordedLaneEvent
}

type recordedLaneEvent struct {
	Type hub.EventType
	Lane *hub.LaneEvent
}

func (r *laneEventRecorder) handle(_ context.Context, ev *hub.Event) *hub.HookResponse {
	r.mu.Lock()
	r.events = append(r.events, recordedLaneEvent{Type: ev.Type, Lane: ev.Lane})
	r.mu.Unlock()
	return &hub.HookResponse{Decision: hub.Allow}
}

func (r *laneEventRecorder) snapshot() []recordedLaneEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recordedLaneEvent, len(r.events))
	copy(out, r.events)
	return out
}

// waitForEvents polls the recorder until at least n events have arrived
// or the deadline passes. Returns the snapshot SORTED by Lane.Seq so the
// EmitAsync goroutine fan-out cannot reorder assertions; the per-session
// seq is the canonical ordering on the wire (spec §5.5).
func (r *laneEventRecorder) waitForEvents(t *testing.T, n int, timeout time.Duration) []recordedLaneEvent {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if got := r.snapshot(); len(got) >= n {
			sortBySeq(got)
			return got
		}
		time.Sleep(2 * time.Millisecond)
	}
	got := r.snapshot()
	sortBySeq(got)
	t.Fatalf("waitForEvents: wanted %d events, got %d after %v: %+v", n, len(got), timeout, got)
	return got
}

func sortBySeq(events []recordedLaneEvent) {
	sort.SliceStable(events, func(i, j int) bool {
		var si, sj uint64
		if events[i].Lane != nil {
			si = events[i].Lane.Seq
		}
		if events[j].Lane != nil {
			sj = events[j].Lane.Seq
		}
		return si < sj
	})
}

func newTestWorkspace(t *testing.T) (*Workspace, *laneEventRecorder) {
	t.Helper()
	bus := hub.New()
	rec := &laneEventRecorder{}
	bus.Register(hub.Subscriber{
		ID:     "lane-test-recorder",
		Mode:   hub.ModeObserve,
		Events: []hub.EventType{hub.EventLaneCreated, hub.EventLaneStatus, hub.EventLaneDelta, hub.EventLaneCost, hub.EventLaneNote, hub.EventLaneKilled},
		Handler: rec.handle,
	})
	w := NewWorkspace(bus, nil)
	w.SetSessionID("sess_test")
	return w, rec
}

// TestLaneTransitionTable exercises the §3.3 transition table. Six valid
// transitions are accepted; four illegal transitions are rejected with
// *stokerr.Error{Code: ErrInternal}.
func TestLaneTransitionTable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		from    hub.LaneStatus
		to      hub.LaneStatus
		ok      bool
	}{
		// Valid transitions per §3.3.
		{"pending_to_running", hub.LaneStatusPending, hub.LaneStatusRunning, true},
		{"pending_to_cancelled", hub.LaneStatusPending, hub.LaneStatusCancelled, true},
		{"running_to_blocked", hub.LaneStatusRunning, hub.LaneStatusBlocked, true},
		{"running_to_done", hub.LaneStatusRunning, hub.LaneStatusDone, true},
		{"running_to_errored", hub.LaneStatusRunning, hub.LaneStatusErrored, true},
		{"running_to_cancelled", hub.LaneStatusRunning, hub.LaneStatusCancelled, true},
		{"blocked_to_running", hub.LaneStatusBlocked, hub.LaneStatusRunning, true},
		{"blocked_to_cancelled", hub.LaneStatusBlocked, hub.LaneStatusCancelled, true},
		{"blocked_to_errored", hub.LaneStatusBlocked, hub.LaneStatusErrored, true},

		// Invalid transitions: terminal → anything.
		{"done_to_running", hub.LaneStatusDone, hub.LaneStatusRunning, false},
		{"errored_to_running", hub.LaneStatusErrored, hub.LaneStatusRunning, false},
		{"cancelled_to_running", hub.LaneStatusCancelled, hub.LaneStatusRunning, false},
		// Invalid: pending → blocked / done / errored (must go through running).
		{"pending_to_blocked", hub.LaneStatusPending, hub.LaneStatusBlocked, false},
		{"pending_to_done", hub.LaneStatusPending, hub.LaneStatusDone, false},
		{"pending_to_errored", hub.LaneStatusPending, hub.LaneStatusErrored, false},
		// Invalid: running → pending (no rewind).
		{"running_to_pending", hub.LaneStatusRunning, hub.LaneStatusPending, false},
		// Invalid: blocked → done (must unblock through running).
		{"blocked_to_done", hub.LaneStatusBlocked, hub.LaneStatusDone, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := allowedTransition(tc.from, tc.to)
			if got != tc.ok {
				t.Errorf("allowedTransition(%q,%q) = %v, want %v", string(tc.from), string(tc.to), got, tc.ok)
			}
		})
	}
}

// TestLaneTransition exercises the live FSM through a Lane's Transition
// method. Walks a lane through pending → running → done and asserts the
// emitted lane.status events carry the right prev/new pairs.
func TestLaneTransition(t *testing.T) {
	t.Parallel()
	w, rec := newTestWorkspace(t)
	l := w.NewMainLane(context.Background())

	if err := l.Transition(hub.LaneStatusRunning, "started", "started"); err != nil {
		t.Fatalf("pending→running: %v", err)
	}
	if err := l.Transition(hub.LaneStatusDone, "ok", "ok"); err != nil {
		t.Fatalf("running→done: %v", err)
	}
	if !l.IsTerminal() {
		t.Errorf("lane should be terminal after Transition→done")
	}
	if l.EndedAt.IsZero() {
		t.Errorf("lane EndedAt should be set after terminal transition")
	}

	// Wait for: lane.created + 2× lane.status.
	got := rec.waitForEvents(t, 3, 2*time.Second)
	if got[0].Type != hub.EventLaneCreated {
		t.Errorf("event[0] = %q, want lane.created", got[0].Type)
	}
	if got[1].Type != hub.EventLaneStatus || got[1].Lane.Status != hub.LaneStatusRunning {
		t.Errorf("event[1] mismatch: %+v", got[1])
	}
	if got[1].Lane.PrevStatus != hub.LaneStatusPending {
		t.Errorf("event[1] prev_status = %q, want pending", got[1].Lane.PrevStatus)
	}
	if got[2].Type != hub.EventLaneStatus || got[2].Lane.Status != hub.LaneStatusDone {
		t.Errorf("event[2] mismatch: %+v", got[2])
	}
}

// TestLaneTransitionRejectsIllegal validates that an illegal FSM
// transition returns *stokerr.Error{Code: ErrInternal} and emits no
// lane.status event.
func TestLaneTransitionRejectsIllegal(t *testing.T) {
	t.Parallel()
	w, _ := newTestWorkspace(t)
	l := w.NewMainLane(context.Background())

	// pending → done is illegal (must go through running).
	err := l.Transition(hub.LaneStatusDone, "ok", "ok")
	if err == nil {
		t.Fatalf("expected error for pending→done, got nil")
	}
	var se *stokerr.Error
	if !errors.As(err, &se) {
		t.Fatalf("expected *stokerr.Error, got %T: %v", err, err)
	}
	if se.Code != stokerr.ErrInternal {
		t.Errorf("expected ErrInternal, got %q", string(se.Code))
	}

	// Lane should be unchanged.
	if l.Status != hub.LaneStatusPending {
		t.Errorf("rejected transition mutated state: status=%q", l.Status)
	}

	// Invalid newStatus also rejected.
	err = l.Transition(hub.LaneStatus("garbage"), "x", "x")
	if !errors.As(err, &se) || se.Code != stokerr.ErrInternal {
		t.Errorf("expected ErrInternal for invalid newStatus, got: %v", err)
	}
}

// TestLaneEmitDelta asserts EmitDelta publishes a lane.delta with the
// matching content_block and increments the per-lane delta_seq.
func TestLaneEmitDelta(t *testing.T) {
	t.Parallel()
	w, rec := newTestWorkspace(t)
	l := w.NewLobeLane(context.Background(), "MemoryRecallLobe", nil)
	if err := l.Transition(hub.LaneStatusRunning, "started", "started"); err != nil {
		t.Fatalf("transition: %v", err)
	}

	l.EmitDelta(agentloop.ContentBlock{Type: "text_delta", Text: "hello"})
	l.EmitDelta(agentloop.ContentBlock{Type: "text_delta", Text: "world"})

	// Wait: lane.created + lane.status + 2× lane.delta.
	got := rec.waitForEvents(t, 4, 2*time.Second)
	deltas := []recordedLaneEvent{}
	for _, e := range got {
		if e.Type == hub.EventLaneDelta {
			deltas = append(deltas, e)
		}
	}
	if len(deltas) != 2 {
		t.Fatalf("expected 2 lane.delta, got %d", len(deltas))
	}
	if deltas[0].Lane.DeltaSeq != 1 || deltas[1].Lane.DeltaSeq != 2 {
		t.Errorf("delta_seq not monotonic: %d, %d", deltas[0].Lane.DeltaSeq, deltas[1].Lane.DeltaSeq)
	}
	if deltas[0].Lane.Block == nil || deltas[0].Lane.Block.Text != "hello" {
		t.Errorf("delta[0] block mismatch: %+v", deltas[0].Lane.Block)
	}

	// Terminal: EmitDelta is a no-op.
	if err := l.Transition(hub.LaneStatusDone, "ok", "ok"); err != nil {
		t.Fatalf("transition done: %v", err)
	}
	// Wait for the lane.status(done) to land before sampling, so the
	// async emission does not race the no-op check below.
	rec.waitForEvents(t, 5, 2*time.Second)
	before := len(rec.snapshot())
	l.EmitDelta(agentloop.ContentBlock{Type: "text_delta", Text: "ignored"})
	time.Sleep(50 * time.Millisecond)
	after := len(rec.snapshot())
	if after != before {
		t.Errorf("EmitDelta on terminal lane should be a no-op; events grew %d→%d", before, after)
	}
}

// TestLaneEmitCost asserts EmitCost publishes a lane.cost with the
// supplied tokens and dollar amount.
func TestLaneEmitCost(t *testing.T) {
	t.Parallel()
	w, rec := newTestWorkspace(t)
	l := w.NewMainLane(context.Background())

	l.EmitCost(12480, 312, 0.00184)

	got := rec.waitForEvents(t, 2, 2*time.Second)
	var cost *hub.LaneEvent
	for _, e := range got {
		if e.Type == hub.EventLaneCost {
			cost = e.Lane
		}
	}
	if cost == nil {
		t.Fatalf("no lane.cost event emitted")
	}
	if cost.TokensIn != 12480 || cost.TokensOut != 312 {
		t.Errorf("cost tokens mismatch: in=%d out=%d", cost.TokensIn, cost.TokensOut)
	}
	if cost.USD != 0.00184 {
		t.Errorf("cost usd mismatch: %v", cost.USD)
	}
	if cost.LaneID != l.ID {
		t.Errorf("cost lane_id mismatch: got %q want %q", cost.LaneID, l.ID)
	}
}

// TestLaneEmitNote asserts EmitNote publishes a lane.note pointer event
// with the note id and severity.
func TestLaneEmitNote(t *testing.T) {
	t.Parallel()
	w, rec := newTestWorkspace(t)
	l := w.NewMainLane(context.Background())

	l.EmitNote("note_01J0K3M4PX", "critical")

	got := rec.waitForEvents(t, 2, 2*time.Second)
	var note *hub.LaneEvent
	for _, e := range got {
		if e.Type == hub.EventLaneNote {
			note = e.Lane
		}
	}
	if note == nil {
		t.Fatalf("no lane.note event emitted")
	}
	if note.NoteID != "note_01J0K3M4PX" || note.NoteSeverity != "critical" {
		t.Errorf("note pointer mismatch: %+v", note)
	}
}

// TestLaneKill asserts Kill emits lane.killed followed by a terminal
// lane.status(cancelled), and that re-killing a terminal lane is a
// no-op (idempotent).
func TestLaneKill(t *testing.T) {
	t.Parallel()
	w, rec := newTestWorkspace(t)
	l := w.NewMainLane(context.Background())
	if err := l.Transition(hub.LaneStatusRunning, "started", "started"); err != nil {
		t.Fatalf("transition: %v", err)
	}

	if err := l.Kill("user pressed k"); err != nil {
		t.Fatalf("kill: %v", err)
	}
	if l.Status != hub.LaneStatusCancelled {
		t.Errorf("after Kill, status = %q, want cancelled", l.Status)
	}

	// Wait: lane.created + lane.status(running) + lane.killed + lane.status(cancelled).
	got := rec.waitForEvents(t, 4, 2*time.Second)

	// Find the kill+cancel pair.
	var killed *hub.LaneEvent
	var cancelStatus *hub.LaneEvent
	for _, e := range got {
		switch e.Type {
		case hub.EventLaneKilled:
			killed = e.Lane
		case hub.EventLaneStatus:
			if e.Lane.Status == hub.LaneStatusCancelled {
				cancelStatus = e.Lane
			}
		}
	}
	if killed == nil {
		t.Fatalf("no lane.killed event")
	}
	if killed.Reason != "user pressed k" || killed.Actor != "operator" {
		t.Errorf("lane.killed payload mismatch: %+v", killed)
	}
	if cancelStatus == nil {
		t.Fatalf("no terminal lane.status(cancelled) after Kill")
	}
	if cancelStatus.ReasonCode != "cancelled_by_operator" {
		t.Errorf("cancel reason_code = %q, want cancelled_by_operator", cancelStatus.ReasonCode)
	}

	// Idempotent re-kill. Wait for the prior emissions to settle first
	// so the async fan-out does not race this no-op check.
	rec.waitForEvents(t, 4, 2*time.Second)
	before := len(rec.snapshot())
	if err := l.Kill("again"); err != nil {
		t.Errorf("re-kill returned error: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	after := len(rec.snapshot())
	if after != before {
		t.Errorf("re-kill should be a no-op; events grew %d→%d", before, after)
	}
}

// TestLaneConstructorsKindAndParent asserts the three Workspace.NewLane*
// constructors emit lane.created with the right kind and parent_id.
func TestLaneConstructorsKindAndParent(t *testing.T) {
	t.Parallel()
	w, rec := newTestWorkspace(t)

	main := w.NewMainLane(context.Background())
	lobe := w.NewLobeLane(context.Background(), "PlanUpdateLobe", main)
	tool := w.NewToolLane(context.Background(), main, "Bash")

	got := rec.waitForEvents(t, 3, 2*time.Second)
	if got[0].Lane.Kind != hub.LaneKindMain || got[0].Lane.ParentID != "" {
		t.Errorf("main lane: %+v", got[0].Lane)
	}
	if got[1].Lane.Kind != hub.LaneKindLobe || got[1].Lane.ParentID != main.ID || got[1].Lane.LobeName != "PlanUpdateLobe" {
		t.Errorf("lobe lane: %+v", got[1].Lane)
	}
	if got[2].Lane.Kind != hub.LaneKindTool || got[2].Lane.ParentID != main.ID || got[2].Lane.Label != "Bash" {
		t.Errorf("tool lane: %+v", got[2].Lane)
	}

	// Each constructor should set Lane.ws so methods work.
	if main.ws == nil || lobe.ws == nil || tool.ws == nil {
		t.Errorf("constructor did not bind workspace back-pointer")
	}
	// SessionID stamped on every event.
	for i, e := range got {
		if e.Lane.SessionID != "sess_test" {
			t.Errorf("event[%d] session_id = %q, want sess_test", i, e.Lane.SessionID)
		}
	}
}
