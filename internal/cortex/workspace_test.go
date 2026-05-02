package cortex

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/hub"
)

func validPublishNote() Note {
	return Note{
		LobeID:   "memory-recall",
		Severity: SevInfo,
		Title:    "hello",
		Body:     "body",
	}
}

func TestPublish(t *testing.T) {
	w := NewWorkspace(nil, nil)
	n := validPublishNote()

	if err := w.Publish(n); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	w.mu.RLock()
	defer w.mu.RUnlock()

	if got := len(w.notes); got != 1 {
		t.Fatalf("len(notes)=%d, want 1", got)
	}
	if got := w.seq; got != 1 {
		t.Fatalf("seq=%d, want 1", got)
	}
	stored := w.notes[0]
	if stored.ID != "note-0" {
		t.Fatalf("ID=%q, want %q", stored.ID, "note-0")
	}
	if stored.EmittedAt.IsZero() {
		t.Fatalf("EmittedAt is zero")
	}
	if stored.Round != 0 {
		t.Fatalf("Round=%d, want 0", stored.Round)
	}
}

func TestPublishValidates(t *testing.T) {
	w := NewWorkspace(nil, nil)
	bad := validPublishNote()
	bad.LobeID = "" // invalid

	err := w.Publish(bad)
	if err == nil {
		t.Fatalf("expected error from invalid Note, got nil")
	}

	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.seq != 0 {
		t.Fatalf("seq=%d after rejected Publish, want 0", w.seq)
	}
	if len(w.notes) != 0 {
		t.Fatalf("len(notes)=%d after rejected Publish, want 0", len(w.notes))
	}
}

func TestPublishConcurrent(t *testing.T) {
	const N = 100
	w := NewWorkspace(nil, nil)

	done := make(chan struct{}, N)
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			if err := w.Publish(validPublishNote()); err != nil {
				errs <- err
			}
		}()
	}
	for i := 0; i < N; i++ {
		<-done
	}
	if got := len(errs); got != 0 {
		close(errs)
		for err := range errs {
			t.Errorf("Publish: %v", err)
		}
		t.Fatalf("got %d Publish errors, want 0", got)
	}

	w.mu.RLock()
	defer w.mu.RUnlock()

	if got := len(w.notes); got != N {
		t.Fatalf("len(notes)=%d, want %d", got, N)
	}
	if got := w.seq; got != N {
		t.Fatalf("seq=%d, want %d", got, N)
	}

	seen := make(map[string]bool, N)
	for _, n := range w.notes {
		if n.ID == "" {
			t.Fatalf("empty ID in notes")
		}
		if seen[n.ID] {
			t.Fatalf("duplicate ID %q", n.ID)
		}
		seen[n.ID] = true
	}
	if len(seen) != N {
		t.Fatalf("unique IDs=%d, want %d", len(seen), N)
	}
}

func TestSnapshotDeepCopy(t *testing.T) {
	w := NewWorkspace(nil, nil)

	n1 := validPublishNote()
	n1.Title = "first"
	if err := w.Publish(n1); err != nil {
		t.Fatalf("Publish n1: %v", err)
	}
	n2 := validPublishNote()
	n2.Title = "second"
	if err := w.Publish(n2); err != nil {
		t.Fatalf("Publish n2: %v", err)
	}

	out := w.Snapshot()
	if got := len(out); got != 2 {
		t.Fatalf("len(Snapshot)=%d, want 2", got)
	}
	if out[0].Title != "first" {
		t.Fatalf("out[0].Title=%q, want %q", out[0].Title, "first")
	}

	// Mutate the returned slice; assert internal state untouched.
	out[0].Title = "MUTATED"
	out = append(out, validPublishNote())

	out2 := w.Snapshot()
	if got := len(out2); got != 2 {
		t.Fatalf("second Snapshot len=%d, want 2 (append must not leak)", got)
	}
	if out2[0].Title != "first" {
		t.Fatalf("out2[0].Title=%q, want %q (mutation leaked back)", out2[0].Title, "first")
	}
	if out2[1].Title != "second" {
		t.Fatalf("out2[1].Title=%q, want %q", out2[1].Title, "second")
	}
}

func TestUnresolvedCriticalFilter(t *testing.T) {
	w := NewWorkspace(nil, nil)

	c1 := validPublishNote()
	c1.Severity = SevCritical
	c1.Title = "the sky is falling"
	if err := w.Publish(c1); err != nil {
		t.Fatalf("Publish c1: %v", err)
	}

	// Capture the assigned ID via Snapshot.
	snap := w.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("expected 1 Note after publishing critical, got %d", len(snap))
	}
	c1ID := snap[0].ID

	got := w.UnresolvedCritical()
	if len(got) != 1 {
		t.Fatalf("UnresolvedCritical len=%d, want 1", len(got))
	}
	if got[0].ID != c1ID {
		t.Fatalf("UnresolvedCritical[0].ID=%q, want %q", got[0].ID, c1ID)
	}

	// Publish a resolving Note targeting c1.
	resolver := validPublishNote()
	resolver.Title = "fixed it"
	resolver.Resolves = c1ID
	if err := w.Publish(resolver); err != nil {
		t.Fatalf("Publish resolver: %v", err)
	}

	got2 := w.UnresolvedCritical()
	if len(got2) != 0 {
		t.Fatalf("UnresolvedCritical len=%d after resolution, want 0", len(got2))
	}
}

func TestDrainAdvancesCursor(t *testing.T) {
	w := NewWorkspace(nil, nil)

	// Round 0 Note (will not be drained at sinceRound=1).
	n0 := validPublishNote()
	n0.Title = "round-0 note"
	if err := w.Publish(n0); err != nil {
		t.Fatalf("Publish n0: %v", err)
	}

	// Advance to round 1 and publish.
	w.mu.Lock()
	w.currentRound = 1
	w.mu.Unlock()
	n1 := validPublishNote()
	n1.Title = "round-1 note"
	if err := w.Publish(n1); err != nil {
		t.Fatalf("Publish n1: %v", err)
	}

	// Advance to round 2 and publish.
	w.mu.Lock()
	w.currentRound = 2
	w.mu.Unlock()
	n2 := validPublishNote()
	n2.Title = "round-2 note"
	if err := w.Publish(n2); err != nil {
		t.Fatalf("Publish n2: %v", err)
	}

	drained, cursor := w.Drain(1)
	if len(drained) != 2 {
		t.Fatalf("Drain(1) len=%d, want 2", len(drained))
	}
	for _, n := range drained {
		if n.Round < 1 {
			t.Fatalf("Drain(1) returned Note with Round=%d, want >=1", n.Round)
		}
	}
	if cursor != 2 {
		t.Fatalf("Drain(1) cursor=%d, want 2", cursor)
	}

	// drainedUpTo must have advanced internally.
	w.mu.RLock()
	if w.drainedUpTo != 2 {
		w.mu.RUnlock()
		t.Fatalf("w.drainedUpTo=%d after Drain(1), want 2", w.drainedUpTo)
	}
	w.mu.RUnlock()

	// Calling Drain with a smaller sinceRound must NOT regress drainedUpTo.
	_, cursor2 := w.Drain(0)
	if cursor2 != 2 {
		t.Fatalf("Drain(0) cursor=%d, want 2 (must not regress)", cursor2)
	}
}

func TestSubscribeUnsubscribe(t *testing.T) {
	w := NewWorkspace(nil, nil)

	var got1, got2, got3 []Note
	cancel1 := w.Subscribe(func(n Note) { got1 = append(got1, n) })
	cancel2 := w.Subscribe(func(n Note) { got2 = append(got2, n) })
	cancel3 := w.Subscribe(func(n Note) { got3 = append(got3, n) })
	_ = cancel1
	_ = cancel3

	first := validPublishNote()
	first.Title = "first"
	if err := w.Publish(first); err != nil {
		t.Fatalf("Publish first: %v", err)
	}
	if len(got1) != 1 {
		t.Fatalf("sub#1 len=%d after first publish, want 1", len(got1))
	}
	if len(got2) != 1 {
		t.Fatalf("sub#2 len=%d after first publish, want 1", len(got2))
	}
	if len(got3) != 1 {
		t.Fatalf("sub#3 len=%d after first publish, want 1", len(got3))
	}

	// Cancel subscriber #2; subsequent Publish must skip it.
	cancel2()

	second := validPublishNote()
	second.Title = "second"
	if err := w.Publish(second); err != nil {
		t.Fatalf("Publish second: %v", err)
	}
	if len(got1) != 2 {
		t.Fatalf("sub#1 len=%d after second publish, want 2", len(got1))
	}
	if len(got2) != 1 {
		t.Fatalf("sub#2 len=%d after cancel+publish, want 1 (must not fire)", len(got2))
	}
	if len(got3) != 2 {
		t.Fatalf("sub#3 len=%d after second publish, want 2", len(got3))
	}

	// Calling cancel a second time must be a no-op.
	cancel2()

	// Sanity: the workspace map should now hold only two live entries.
	w.mu.RLock()
	liveSubs := len(w.subs)
	w.mu.RUnlock()
	if liveSubs != 2 {
		t.Fatalf("len(w.subs)=%d after cancel, want 2", liveSubs)
	}
}

func TestSubscribeMultiplePublishes(t *testing.T) {
	w := NewWorkspace(nil, nil)

	var got []Note
	w.Subscribe(func(n Note) { got = append(got, n) })

	titles := []string{"one", "two", "three", "four", "five"}
	for _, title := range titles {
		n := validPublishNote()
		n.Title = title
		if err := w.Publish(n); err != nil {
			t.Fatalf("Publish %q: %v", title, err)
		}
	}

	if len(got) != len(titles) {
		t.Fatalf("subscriber received %d Notes, want %d", len(got), len(titles))
	}
	for i, want := range titles {
		if got[i].Title != want {
			t.Fatalf("got[%d].Title=%q, want %q (order violated)", i, got[i].Title, want)
		}
	}
}

func TestNoteValidate(t *testing.T) {
	validNote := func() Note {
		return Note{
			ID:        "01H000000000000000000000",
			LobeID:    "memory-recall",
			Severity:  SevInfo,
			Title:     "all good",
			Body:      "body text",
			Tags:      []string{"a", "b"},
			EmittedAt: time.Unix(0, 0).UTC(),
			Round:     1,
		}
	}

	cases := []struct {
		name      string
		mutate    func(*Note)
		wantErr   bool
		substring string
	}{
		{
			name:      "empty LobeID",
			mutate:    func(n *Note) { n.LobeID = "" },
			wantErr:   true,
			substring: "empty LobeID",
		},
		{
			name:      "empty Title",
			mutate:    func(n *Note) { n.Title = "" },
			wantErr:   true,
			substring: "empty Title",
		},
		{
			name:      "title exceeds 80 runes",
			mutate:    func(n *Note) { n.Title = strings.Repeat("a", 81) },
			wantErr:   true,
			substring: "Title >80 runes",
		},
		{
			name:      "unknown Severity",
			mutate:    func(n *Note) { n.Severity = Severity("xyz") },
			wantErr:   true,
			substring: "unknown Severity",
		},
		{
			name:    "happy path",
			mutate:  func(n *Note) {},
			wantErr: false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			n := validNote()
			tc.mutate(&n)
			err := n.Validate()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tc.substring) {
					t.Fatalf("error %q does not contain %q", err.Error(), tc.substring)
				}
			} else {
				if err != nil {
					t.Fatalf("expected nil error, got %v", err)
				}
			}
		})
	}
}

// TestSpotlightUpgrade publishes four Notes of strictly increasing
// severity and asserts Workspace.Spotlight().Current() tracks the highest
// rank at every step.
func TestSpotlightUpgrade(t *testing.T) {
	w := NewWorkspace(hub.New(), nil)

	// Step 1: SevInfo -> spotlight = info Note.
	n1 := validPublishNote()
	n1.Severity = SevInfo
	n1.Title = "info"
	if err := w.Publish(n1); err != nil {
		t.Fatalf("Publish n1: %v", err)
	}
	if got := w.Spotlight().Current().Severity; got != SevInfo {
		t.Fatalf("after info publish: spotlight severity=%q, want %q", got, SevInfo)
	}

	// Step 2: SevAdvice -> spotlight upgrades.
	n2 := validPublishNote()
	n2.Severity = SevAdvice
	n2.Title = "advice"
	if err := w.Publish(n2); err != nil {
		t.Fatalf("Publish n2: %v", err)
	}
	if got := w.Spotlight().Current().Severity; got != SevAdvice {
		t.Fatalf("after advice publish: spotlight severity=%q, want %q", got, SevAdvice)
	}

	// Step 3: SevWarning -> upgrade.
	n3 := validPublishNote()
	n3.Severity = SevWarning
	n3.Title = "warning"
	if err := w.Publish(n3); err != nil {
		t.Fatalf("Publish n3: %v", err)
	}
	if got := w.Spotlight().Current().Severity; got != SevWarning {
		t.Fatalf("after warning publish: spotlight severity=%q, want %q", got, SevWarning)
	}

	// Step 4: SevCritical -> upgrade.
	n4 := validPublishNote()
	n4.Severity = SevCritical
	n4.Title = "critical"
	if err := w.Publish(n4); err != nil {
		t.Fatalf("Publish n4: %v", err)
	}
	if got := w.Spotlight().Current().Severity; got != SevCritical {
		t.Fatalf("after critical publish: spotlight severity=%q, want %q", got, SevCritical)
	}
	if got := w.Spotlight().Current().Title; got != "critical" {
		t.Fatalf("after critical publish: spotlight title=%q, want %q", got, "critical")
	}
}

// TestSpotlightTieBrokenByEmittedAt publishes two SevWarning Notes whose
// EmittedAt timestamps are explicitly ordered (older first). The newer
// Note must take the spotlight per the tie-break rule.
func TestSpotlightTieBrokenByEmittedAt(t *testing.T) {
	w := NewWorkspace(nil, nil)

	older := validPublishNote()
	older.Severity = SevWarning
	older.Title = "older"
	older.EmittedAt = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if err := w.Publish(older); err != nil {
		t.Fatalf("Publish older: %v", err)
	}

	newer := validPublishNote()
	newer.Severity = SevWarning
	newer.Title = "newer"
	newer.EmittedAt = time.Date(2026, 1, 1, 12, 0, 1, 0, time.UTC)
	if err := w.Publish(newer); err != nil {
		t.Fatalf("Publish newer: %v", err)
	}

	cur := w.Spotlight().Current()
	if cur.Title != "newer" {
		t.Fatalf("spotlight title=%q, want %q (tie should break to newer EmittedAt)",
			cur.Title, "newer")
	}
}

// TestSpotlightResolvedDowngrades publishes a SevCritical Note, then a
// SevInfo Note that resolves it. The spotlight must downgrade to the
// info Note (the only unresolved Note remaining).
func TestSpotlightResolvedDowngrades(t *testing.T) {
	w := NewWorkspace(nil, nil)

	c1 := validPublishNote()
	c1.Severity = SevCritical
	c1.Title = "the sky is falling"
	if err := w.Publish(c1); err != nil {
		t.Fatalf("Publish c1: %v", err)
	}
	c1ID := w.Spotlight().Current().ID
	if c1ID == "" {
		t.Fatalf("c1 has empty ID after publish")
	}

	i1 := validPublishNote()
	i1.Severity = SevInfo
	i1.Title = "fixed it"
	i1.Resolves = c1ID
	if err := w.Publish(i1); err != nil {
		t.Fatalf("Publish i1: %v", err)
	}

	cur := w.Spotlight().Current()
	if cur.Title != "fixed it" {
		t.Fatalf("spotlight title=%q, want %q (resolved critical must yield to info resolver)",
			cur.Title, "fixed it")
	}
	if cur.Severity != SevInfo {
		t.Fatalf("spotlight severity=%q, want %q", cur.Severity, SevInfo)
	}
}

// TestSpotlightSubscribeFanout subscribes to spotlight upgrades, publishes
// a SevWarning Note that triggers an upgrade, and asserts the subscriber
// observed the new spotlight Note.
func TestSpotlightSubscribeFanout(t *testing.T) {
	w := NewWorkspace(nil, nil)

	var (
		mu       sync.Mutex
		received []Note
	)
	cancel := w.Spotlight().Subscribe(func(n Note) {
		mu.Lock()
		received = append(received, n)
		mu.Unlock()
	})
	defer cancel()

	n := validPublishNote()
	n.Severity = SevWarning
	n.Title = "loud"
	if err := w.Publish(n); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	mu.Lock()
	got := append([]Note(nil), received...)
	mu.Unlock()

	if len(got) != 1 {
		t.Fatalf("subscriber received %d Notes, want 1", len(got))
	}
	if got[0].Title != "loud" {
		t.Fatalf("subscriber Note title=%q, want %q", got[0].Title, "loud")
	}
	if got[0].Severity != SevWarning {
		t.Fatalf("subscriber Note severity=%q, want %q", got[0].Severity, SevWarning)
	}
}

// TestSpotlightEmitsHubEvent verifies that an upgrade emits a
// "cortex.spotlight.changed" event via the hub bus, with from/to IDs.
func TestSpotlightEmitsHubEvent(t *testing.T) {
	bus := hub.New()
	w := NewWorkspace(bus, nil)

	var (
		mu     sync.Mutex
		events []*hub.Event
	)
	bus.Register(hub.Subscriber{
		ID:     "spotlight-test-observer",
		Events: []hub.EventType{hub.EventCortexSpotlightChanged},
		Mode:   hub.ModeObserve,
		Handler: func(_ context.Context, ev *hub.Event) *hub.HookResponse {
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
			return nil
		},
	})

	n := validPublishNote()
	n.Severity = SevCritical
	n.Title = "boom"
	if err := w.Publish(n); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// EmitAsync schedules the dispatch on a goroutine; poll briefly.
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		count := len(events)
		mu.Unlock()
		if count >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for cortex.spotlight.changed event")
		}
		time.Sleep(5 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	ev := events[0]
	if ev.Type != hub.EventCortexSpotlightChanged {
		t.Fatalf("event type=%q, want %q", ev.Type, hub.EventCortexSpotlightChanged)
	}
	if ev.Custom == nil {
		t.Fatalf("event Custom is nil")
	}
	if from, ok := ev.Custom["from"].(string); !ok || from != "" {
		t.Fatalf("event Custom[from]=%v, want empty string (no prior spotlight)", ev.Custom["from"])
	}
	if to, ok := ev.Custom["to"].(string); !ok || to == "" {
		t.Fatalf("event Custom[to]=%v, want non-empty Note ID", ev.Custom["to"])
	}
}
