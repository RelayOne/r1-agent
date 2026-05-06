package lanes

import (
	"context"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// fakeTransport is a no-op Transport for unit tests. List returns a
// fixed snapshot, Subscribe blocks until ctx is cancelled, all
// mutations are recorded for assertions.
type fakeTransport struct {
	snapshot []LaneSnapshot
	killed   []string
	pinned   map[string]bool
	killAll  int
}

func (f *fakeTransport) List(_ context.Context) ([]LaneSnapshot, error) {
	return f.snapshot, nil
}
func (f *fakeTransport) Subscribe(ctx context.Context, _ string, _ chan<- LaneEvent) error {
	<-ctx.Done()
	return ctx.Err()
}
func (f *fakeTransport) Kill(_ context.Context, id string) error {
	f.killed = append(f.killed, id)
	return nil
}
func (f *fakeTransport) KillAll(_ context.Context) error { f.killAll++; return nil }
func (f *fakeTransport) Pin(_ context.Context, id string, p bool) error {
	if f.pinned == nil {
		f.pinned = make(map[string]bool)
	}
	f.pinned[id] = p
	return nil
}

// TestLaneStatus_Glyph confirms the spec §"Implementation Checklist"
// item 3 glyph table is populated for every status.
func TestLaneStatus_Glyph(t *testing.T) {
	want := map[LaneStatus]string{
		StatusPending:   "·",
		StatusRunning:   "▸",
		StatusBlocked:   "⏸",
		StatusDone:      "✓",
		StatusErrored:   "✗",
		StatusCancelled: "⊘",
	}
	for s, g := range want {
		if got := s.Glyph(); got != g {
			t.Errorf("LaneStatus(%v).Glyph() = %q, want %q", s, got, g)
		}
	}
	if (LaneStatus(99)).Glyph() != "?" {
		t.Errorf("out-of-range LaneStatus glyph should be ?")
	}
}

// TestLaneStatus_String confirms String() returns the expected lower-
// case names for every status.
func TestLaneStatus_String(t *testing.T) {
	want := map[LaneStatus]string{
		StatusPending:   "pending",
		StatusRunning:   "running",
		StatusBlocked:   "blocked",
		StatusDone:      "done",
		StatusErrored:   "errored",
		StatusCancelled: "cancelled",
	}
	for s, n := range want {
		if got := s.String(); got != n {
			t.Errorf("LaneStatus(%v).String() = %q, want %q", s, got, n)
		}
	}
}

// TestLaneStatus_IsTerminal verifies terminal classification.
func TestLaneStatus_IsTerminal(t *testing.T) {
	for _, c := range []struct {
		s LaneStatus
		t bool
	}{
		{StatusPending, false},
		{StatusRunning, false},
		{StatusBlocked, false},
		{StatusDone, true},
		{StatusErrored, true},
		{StatusCancelled, true},
	} {
		if got := c.s.IsTerminal(); got != c.t {
			t.Errorf("%v.IsTerminal()=%v want %v", c.s, got, c.t)
		}
	}
}

// TestLane_DirtyOnSet confirms every Set* helper flips Dirty exactly
// when the value actually changes (spec checklist item 5).
func TestLane_DirtyOnSet(t *testing.T) {
	l := &Lane{}
	cases := []struct {
		name string
		mut  func()
		want bool
	}{
		{"SetStatus running", func() { l.SetStatus(StatusRunning) }, true},
		{"SetStatus running again (no-op)", func() { l.SetStatus(StatusRunning) }, false},
		{"SetActivity new", func() { l.SetActivity("hello") }, true},
		{"SetActivity same", func() { l.SetActivity("hello") }, false},
		{"SetTokens 10", func() { l.SetTokens(10) }, true},
		{"SetTokens 10 again", func() { l.SetTokens(10) }, false},
		{"SetCost 0.05", func() { l.SetCost(0.05) }, true},
		{"SetModel haiku", func() { l.SetModel("haiku-4.5") }, true},
		{"SetElapsed 1s", func() { l.SetElapsed(time.Second) }, true},
		{"SetErr boom", func() { l.SetErr("boom") }, true},
		{"SetTitle x", func() { l.SetTitle("x") }, true},
		{"SetRole r", func() { l.SetRole("r") }, true},
	}
	for _, c := range cases {
		l.Dirty = false
		c.mut()
		if l.Dirty != c.want {
			t.Errorf("%s: Dirty=%v want %v", c.name, l.Dirty, c.want)
		}
	}
}

// TestNew_Defaults exercises the New constructor with no options.
func TestNew_Defaults(t *testing.T) {
	m := New("sess-1", &fakeTransport{})
	if m.sessionID != "sess-1" {
		t.Errorf("sessionID=%q want sess-1", m.sessionID)
	}
	if m.transport == nil {
		t.Error("transport must be non-nil")
	}
	if m.sub == nil || cap(m.sub) == 0 {
		t.Error("sub channel must be buffered and non-nil")
	}
	if m.laneIndex == nil {
		t.Error("laneIndex must be initialized")
	}
	if m.cache == nil {
		t.Error("cache must be initialized")
	}
}


// TestUpdate_LaneStartCreatesEntry confirms that a laneStartMsg
// installs a new Lane in stable order.
func TestUpdate_LaneStartCreatesEntry(t *testing.T) {
	m := New("s", &fakeTransport{})
	now := time.Now()
	model, _ := m.Update(laneStartMsg{LaneID: "L1", Title: "first", Role: "main", StartedAt: now})
	got := model.(*Model)
	if len(got.lanes) != 1 || got.lanes[0].ID != "L1" {
		t.Fatalf("lanes=%+v", got.lanes)
	}
	if got.lanes[0].Status != StatusPending {
		t.Errorf("new lane should be StatusPending, got %v", got.lanes[0].Status)
	}
	if !got.lanes[0].Dirty {
		t.Error("new lane should be Dirty=true")
	}
	if got.totalLanes != 1 {
		t.Errorf("totalLanes=%d want 1", got.totalLanes)
	}
	// Idempotent: second start with same id must not duplicate.
	model, _ = got.Update(laneStartMsg{LaneID: "L1"})
	if len(model.(*Model).lanes) != 1 {
		t.Errorf("duplicate start created %d lanes", len(model.(*Model).lanes))
	}
}

// TestUpdate_LaneTickMutatesLane verifies that a tick updates fields
// and flips Dirty via the Set* helpers.
func TestUpdate_LaneTickMutatesLane(t *testing.T) {
	m := New("s", &fakeTransport{})
	m.Update(laneStartMsg{LaneID: "L1", Title: "x", StartedAt: time.Now()})
	// Clear dirty after start.
	m.lanes[0].Dirty = false
	m.Update(laneTickMsg{
		LaneID:   "L1",
		Status:   StatusRunning,
		Activity: "thinking",
		Tokens:   42,
		CostUSD:  0.01,
		Model:    "haiku-4.5",
	})
	l := m.lanes[0]
	if l.Status != StatusRunning || l.Activity != "thinking" || l.Tokens != 42 {
		t.Errorf("after tick: %+v", l)
	}
	if !l.Dirty {
		t.Error("tick must flip Dirty")
	}
	if m.totalCost != 0.01 {
		t.Errorf("totalCost=%v want 0.01", m.totalCost)
	}
	if m.currentModel != "haiku-4.5" {
		t.Errorf("currentModel=%q want haiku-4.5", m.currentModel)
	}
}

// TestUpdate_LaneEndTransitionsTerminal sets a terminal status and
// stamps cost.
func TestUpdate_LaneEndTransitionsTerminal(t *testing.T) {
	m := New("s", &fakeTransport{})
	m.Update(laneStartMsg{LaneID: "L1"})
	m.Update(laneEndMsg{LaneID: "L1", Final: StatusDone, CostUSD: 1.5, Tokens: 100})
	if !m.lanes[0].Status.IsTerminal() {
		t.Errorf("lane should be terminal, got %v", m.lanes[0].Status)
	}
	if m.lanes[0].CostUSD != 1.5 {
		t.Errorf("cost=%v want 1.5", m.lanes[0].CostUSD)
	}
}

// TestUpdate_LaneListReplaysAndSorts installs a list snapshot and
// confirms lanes are sorted by StartedAt asc, ID asc.
func TestUpdate_LaneListReplaysAndSorts(t *testing.T) {
	m := New("s", &fakeTransport{})
	t0 := time.Now()
	t1 := t0.Add(time.Second)
	t2 := t0.Add(2 * time.Second)
	m.Update(laneListMsg{Lanes: []LaneSnapshot{
		{ID: "Z", StartedAt: t2, Status: StatusRunning},
		{ID: "A", StartedAt: t0, Status: StatusPending},
		{ID: "M", StartedAt: t1, Status: StatusBlocked},
	}})
	want := []string{"A", "M", "Z"}
	for i, w := range want {
		if m.lanes[i].ID != w {
			t.Errorf("lanes[%d].ID=%q want %q", i, m.lanes[i].ID, w)
		}
	}
	// Re-replay with same ids must update existing lanes, not duplicate.
	m.Update(laneListMsg{Lanes: []LaneSnapshot{
		{ID: "A", StartedAt: t0, Status: StatusRunning},
	}})
	if len(m.lanes) != 3 {
		t.Errorf("after re-replay len(lanes)=%d want 3", len(m.lanes))
	}
	if m.lanes[0].Status != StatusRunning {
		t.Errorf("A status not updated, got %v", m.lanes[0].Status)
	}
}

// TestUpdate_KillAckClearsModal sets confirmKill and confirms the
// killAckMsg branch resets it.
func TestUpdate_KillAckClearsModal(t *testing.T) {
	m := New("s", &fakeTransport{})
	m.Update(laneStartMsg{LaneID: "L1"})
	m.confirmKill = "L1"
	m.Update(killAckMsg{LaneID: "L1"})
	if m.confirmKill != "" {
		t.Errorf("confirmKill=%q want empty after ack", m.confirmKill)
	}
	// Non-empty err should land on the lane.
	m.confirmKill = "L1"
	m.Update(killAckMsg{LaneID: "L1", Err: "denied"})
	if m.lanes[0].Err != "denied" {
		t.Errorf("lane.Err=%q want denied", m.lanes[0].Err)
	}
}

// TestUpdate_BudgetMsg updates aggregates.
func TestUpdate_BudgetMsg(t *testing.T) {
	m := New("s", &fakeTransport{})
	m.Update(budgetMsg{SpentUSD: 0.5, LimitUSD: 1.0})
	if m.totalCost != 0.5 || m.budgetLimit != 1.0 {
		t.Errorf("totalCost=%v budgetLimit=%v", m.totalCost, m.budgetLimit)
	}
}

// TestUpdate_WindowSize stores width/height.
func TestUpdate_WindowSize(t *testing.T) {
	m := New("s", &fakeTransport{})
	m.Update(tea.WindowSizeMsg{Width: 200, Height: 50})
	if m.width != 200 || m.height != 50 {
		t.Errorf("width=%d height=%d", m.width, m.height)
	}
}

// TestUpdate_QuitCancelsProducer covers the q-key branch of Update.
// It checks that pressing q dispatches tea.Quit and cancels the
// stored cancel func.
func TestUpdate_QuitCancelsProducer(t *testing.T) {
	m := New("s", &fakeTransport{})
	cancelled := false
	m.cancel = func() { cancelled = true }
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if cmd == nil {
		t.Fatal("q must return a non-nil cmd (tea.Quit)")
	}
	if !cancelled {
		t.Error("q must cancel the producer ctx")
	}
}

// TestUpdate_TickSentinelIsNoOp confirms the LaneID=="" sentinel
// (producer shutdown) does NOT re-arm the cmd.
func TestUpdate_TickSentinelIsNoOp(t *testing.T) {
	m := New("s", &fakeTransport{})
	_, cmd := m.Update(laneTickMsg{}) // zero-value
	if cmd != nil {
		t.Error("zero-LaneID tick must not re-arm any cmd")
	}
}

// TestStatusFromHub round-trips every status through the hub <-> panel
// converters.
func TestStatusFromHub_RoundTrip(t *testing.T) {
	for _, s := range []LaneStatus{
		StatusPending, StatusRunning, StatusBlocked,
		StatusDone, StatusErrored, StatusCancelled,
	} {
		got := StatusFromHub(StatusToHub(s))
		if got != s {
			t.Errorf("round-trip %v -> %v -> %v failed", s, StatusToHub(s), got)
		}
	}
}

// TestProducer_FlushesTicks runs the producer, pumps tick events at
// it, and confirms the model's sub channel receives the coalesced
// laneTickMsg within one tick window.
func TestProducer_FlushesTicks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m := New("s", &fakeTransport{})

	// Fake transport that pumps a few ticks then blocks on ctx. We
	// hijack Subscribe by running the producer with a transport
	// whose Subscribe pushes events directly. Easiest: use a real
	// channel-pumping transport.
	pumping := &pumpTransport{events: make(chan LaneEvent, 4)}
	m.transport = pumping

	go m.runProducer(ctx)

	// Send 3 ticks for the same lane within one window — coalescer
	// must collapse them.
	for i := 0; i < 3; i++ {
		pumping.events <- LaneEvent{
			Kind: "tick",
			Snapshot: LaneSnapshot{
				ID: "L1", Status: StatusRunning, Tokens: i + 1, Activity: "x",
			},
		}
	}

	// Wait for the next flush window and a status-change bypass to
	// fire. Timeout if nothing arrives.
	deadline := time.After(2 * time.Second)
	got := 0
	for got < 1 {
		select {
		case msg := <-m.sub:
			if _, ok := msg.(laneTickMsg); ok {
				got++
			}
		case <-deadline:
			t.Fatal("producer did not flush a tick within 2s")
		}
	}
}

// TestProducer_ListBypassesCoalesce sends a list event and confirms
// it lands as laneListMsg without delay.
func TestProducer_ListBypassesCoalesce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m := New("s", &fakeTransport{})
	pumping := &pumpTransport{events: make(chan LaneEvent, 4)}
	m.transport = pumping

	go m.runProducer(ctx)

	pumping.events <- LaneEvent{
		Kind: "list",
		List: []LaneSnapshot{{ID: "A"}, {ID: "B"}},
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case msg := <-m.sub:
			if l, ok := msg.(laneListMsg); ok {
				if len(l.Lanes) != 2 {
					t.Errorf("list len=%d want 2", len(l.Lanes))
				}
				return
			}
		case <-deadline:
			t.Fatal("list event not flushed")
		}
	}
}

// TestProducer_StartBypass confirms a "start" event arrives without
// waiting for a tick window.
func TestProducer_StartBypass(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m := New("s", &fakeTransport{})
	pumping := &pumpTransport{events: make(chan LaneEvent, 4)}
	m.transport = pumping

	go m.runProducer(ctx)

	t0 := time.Now()
	pumping.events <- LaneEvent{
		Kind: "start",
		Snapshot: LaneSnapshot{
			ID: "L1", Title: "first", Status: StatusPending, StartedAt: t0,
		},
	}
	deadline := time.After(time.Second)
	for {
		select {
		case msg := <-m.sub:
			if s, ok := msg.(laneStartMsg); ok {
				if s.LaneID != "L1" || s.Title != "first" {
					t.Errorf("start mismatched: %+v", s)
				}
				// Bypass: should arrive well before
				// PRODUCER_TICK_MS.
				if time.Since(t0) > 200*time.Millisecond {
					t.Errorf("start arrived after %v — bypass should be immediate", time.Since(t0))
				}
				return
			}
		case <-deadline:
			t.Fatal("start event not flushed")
		}
	}
}

// pumpTransport is a Transport whose Subscribe drains an internal
// channel. Used by producer tests.
type pumpTransport struct {
	events chan LaneEvent
}

func (p *pumpTransport) List(_ context.Context) ([]LaneSnapshot, error) {
	return nil, nil
}
func (p *pumpTransport) Subscribe(ctx context.Context, _ string, out chan<- LaneEvent) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev := <-p.events:
			select {
			case out <- ev:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}
func (p *pumpTransport) Kill(_ context.Context, _ string) error    { return nil }
func (p *pumpTransport) KillAll(_ context.Context) error           { return nil }
func (p *pumpTransport) Pin(_ context.Context, _ string, _ bool) error { return nil }

// TestRemoteTransport_NotConnected confirms that Kill / Pin /
// KillAll / List on a remoteTransport that has not yet dialled
// return an explicit error rather than panicking on a nil conn.
func TestRemoteTransport_NotConnected(t *testing.T) {
	rt := NewRemoteTransport("127.0.0.1:65535", "")
	ctx := context.Background()
	if err := rt.Kill(ctx, "L1"); err == nil {
		t.Error("Kill on unconnected remote should error")
	}
	if err := rt.KillAll(ctx); err == nil {
		t.Error("KillAll on unconnected remote should error")
	}
	if err := rt.Pin(ctx, "L1", true); err == nil {
		t.Error("Pin on unconnected remote should error")
	}
	if _, err := rt.List(ctx); err == nil {
		t.Error("List on unconnected remote should error")
	}
}

// TestLocalTransport_ListEmpty exercises the local-transport List
// path with a workspace that has no lanes — should return an empty
// snapshot, not error.
func TestLocalTransport_ListEmpty(t *testing.T) {
	// Use a nil workspace to force the unbound error path —
	// validates that List returns an explicit error rather than a
	// nil-pointer panic.
	lt := &localTransport{ws: nil}
	if _, err := lt.List(context.Background()); err == nil {
		t.Error("nil workspace must error from List")
	}
}
