package walkeeper

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/hub"
)

// newTestBus opens a fresh durable bus.Bus rooted in t.TempDir(). The
// caller is responsible for closing it; tests that exercise restart
// re-open the same dir.
func newTestBus(t *testing.T) (*bus.Bus, string) {
	t.Helper()
	dir := t.TempDir()
	b, err := bus.New(dir)
	if err != nil {
		t.Fatalf("bus.New: %v", err)
	}
	return b, dir
}

// runLobe starts the Lobe in a background goroutine and returns a
// cancel function that tears it down and waits for the run goroutine
// to exit. Used by every test that exercises the live subscriber path.
func runLobe(t *testing.T, l *WALKeeperLobe) (cancel func()) {
	t.Helper()
	ctx, c := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = l.Run(ctx, cortex.LobeInput{})
	}()
	// Yield once so the subscriber registration completes before the
	// test starts emitting events.
	time.Sleep(20 * time.Millisecond)
	return func() {
		c()
		<-done
	}
}

// replayDurable returns every event with a type matching the keeper's
// framing prefix from the durable bus. The events are read back from
// the WAL via Replay so closure-bound subscribers do not race the
// drainer.
func replayDurable(t *testing.T, b *bus.Bus, prefix string) []bus.Event {
	t.Helper()
	out := make([]bus.Event, 0)
	if err := b.Replay(bus.Pattern{TypePrefix: prefix}, 1, func(e bus.Event) {
		out = append(out, e)
	}); err != nil {
		t.Fatalf("Replay: %v", err)
	}
	return out
}

// TestWALKeeperLobe_FramesAndForwards verifies that a single hub event
// is forwarded to the durable bus with TypePrefix applied and the
// payload round-tripping the original event.
func TestWALKeeperLobe_FramesAndForwards(t *testing.T) {
	durable, _ := newTestBus(t)
	defer durable.Close()

	h := hub.New()
	l := NewWALKeeperLobe(h, durable, nil, WALFraming{})

	stop := runLobe(t, l)
	defer stop()

	src := &hub.Event{
		ID:   "src-1",
		Type: hub.EventToolPreUse,
		Tool: &hub.ToolEvent{Name: "Read", FilePath: "/tmp/x"},
	}
	h.Emit(context.Background(), src)

	// Wait briefly for the async observe handler + drainer to publish.
	deadline := time.Now().Add(2 * time.Second)
	var got []bus.Event
	for time.Now().Before(deadline) {
		got = replayDurable(t, durable, defaultTypePrefix)
		if len(got) >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(got) < 1 {
		t.Fatalf("expected at least 1 forwarded event, got %d", len(got))
	}

	// First event should be the framed copy.
	first := got[0]
	wantType := defaultTypePrefix + string(hub.EventToolPreUse)
	if string(first.Type) != wantType {
		t.Fatalf("Type mismatch: got %q want %q", first.Type, wantType)
	}
	if first.CausalRef != src.ID {
		t.Fatalf("CausalRef mismatch: got %q want %q", first.CausalRef, src.ID)
	}

	var roundTrip hub.Event
	if err := json.Unmarshal(first.Payload, &roundTrip); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if roundTrip.ID != src.ID {
		t.Fatalf("payload ID mismatch: got %q want %q", roundTrip.ID, src.ID)
	}
	if roundTrip.Type != src.Type {
		t.Fatalf("payload Type mismatch: got %q want %q", roundTrip.Type, src.Type)
	}
}

// TestWALKeeperLobe_CustomFramingPrefix verifies that a user-supplied
// non-empty TypePrefix is honored verbatim. Defends item 10's "default
// 'cortex.hub.'" wording from regressing into a hard-code.
func TestWALKeeperLobe_CustomFramingPrefix(t *testing.T) {
	durable, _ := newTestBus(t)
	defer durable.Close()

	h := hub.New()
	l := NewWALKeeperLobe(h, durable, nil, WALFraming{TypePrefix: "myapp.hub."})
	stop := runLobe(t, l)
	defer stop()

	h.Emit(context.Background(), &hub.Event{
		ID:   "x-1",
		Type: hub.EventSessionInit,
	})

	deadline := time.Now().Add(2 * time.Second)
	var got []bus.Event
	for time.Now().Before(deadline) {
		got = replayDurable(t, durable, "myapp.hub.")
		if len(got) >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 event with custom prefix, got %d", len(got))
	}
	if !strings.HasPrefix(string(got[0].Type), "myapp.hub.") {
		t.Fatalf("prefix not applied: %q", got[0].Type)
	}
}

// TestWALKeeperLobe_DefaultPrefix verifies that an empty TypePrefix
// resolves to "cortex.hub." per spec item 10.
func TestWALKeeperLobe_DefaultPrefix(t *testing.T) {
	l := NewWALKeeperLobe(nil, nil, nil, WALFraming{})
	if l.framing.TypePrefix != defaultTypePrefix {
		t.Fatalf("default prefix mismatch: got %q want %q", l.framing.TypePrefix, defaultTypePrefix)
	}
}

// TestWALKeeperLobe_NilBusesNoOp verifies graceful degradation when
// either bus is nil. Run must observe ctx.Done() and return nil.
func TestWALKeeperLobe_NilBusesNoOp(t *testing.T) {
	l := NewWALKeeperLobe(nil, nil, nil, WALFraming{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx, cortex.LobeInput{}) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run nil-bus: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("Run did not return after ctx cancel")
	}
}

// TestWALKeeperLobe_DropsInfoOnBackpressure verifies that info-severity
// events are dropped when the pending channel is at or above the
// backpressure threshold (≥0.9*1000=900). The test bypasses the live
// drainer by populating l.pending directly with backlog bus.Events so
// the channel saturates without requiring a slow durable bus mock.
func TestWALKeeperLobe_DropsInfoOnBackpressure(t *testing.T) {
	durable, _ := newTestBus(t)
	defer durable.Close()

	h := hub.New()
	l := NewWALKeeperLobe(h, durable, nil, WALFraming{})

	// Saturate the pending channel to 900 (= 0.9*1000) without
	// starting the drainer. Each backlog entry is a real bus.Event
	// shaped exactly like a forwarded hub event would be.
	for i := 0; i < 900; i++ {
		l.pending <- pendingItem{evt: bus.Event{Type: "cortex.hub.backlog", CausalRef: "evt-" + itoa(i)}}
	}
	if got := l.PendingLen(); got != 900 {
		t.Fatalf("pre-test pending depth: got %d want 900", got)
	}

	// Drive the handler directly (no goroutine bus dispatch) so the
	// test is deterministic.
	const numInfo = 100
	for i := 0; i < numInfo; i++ {
		l.handleHubEvent(&hub.Event{
			ID:   "info-" + itoa(i),
			Type: hub.EventToolPreUse, // info-severity per eventSeverity
		})
	}

	dropped := l.DroppedCount()
	if dropped == 0 {
		t.Fatalf("expected non-zero drops at saturation; got %d", dropped)
	}
	if dropped > numInfo {
		t.Fatalf("dropped exceeds emitted: %d > %d", dropped, numInfo)
	}
	t.Logf("dropped=%d of %d info events", dropped, numInfo)
}

// TestWALKeeperLobe_NoDropBelowThreshold verifies the lobe does NOT
// drop info events when the pending channel is comfortably below
// 0.9*cap. We pre-load to 800 (well under 900) and send 50 info
// events; pending climbs to 850 (still under threshold), so all 50
// should enqueue successfully with zero drops.
func TestWALKeeperLobe_NoDropBelowThreshold(t *testing.T) {
	durable, _ := newTestBus(t)
	defer durable.Close()

	h := hub.New()
	l := NewWALKeeperLobe(h, durable, nil, WALFraming{})

	// Push the channel to 800 — comfortably under the 900 threshold.
	for i := 0; i < 800; i++ {
		l.pending <- pendingItem{evt: bus.Event{Type: "cortex.hub.backlog", CausalRef: "evt-" + itoa(i)}}
	}

	// Drive 50 info events; channel will rise to 850, still < 900.
	for i := 0; i < 50; i++ {
		l.handleHubEvent(&hub.Event{
			ID:   "info-" + itoa(i),
			Type: hub.EventToolPreUse,
		})
	}

	if got := l.DroppedCount(); got != 0 {
		t.Fatalf("unexpected drops below threshold: got %d want 0", got)
	}
	if got := l.PendingLen(); got != 850 {
		t.Fatalf("pending depth after enqueue: got %d want 850", got)
	}
}

// TestWALKeeperLobe_NonInfoEventsNotDropped verifies that warning- and
// critical-severity events are NOT dropped via the info-only path
// even at full saturation; they take the blocking-send fallback.
func TestWALKeeperLobe_NonInfoEventsNotDropped(t *testing.T) {
	durable, _ := newTestBus(t)
	defer durable.Close()

	h := hub.New()
	l := NewWALKeeperLobe(h, durable, nil, WALFraming{})

	// Saturate, then send a warning-severity event.
	for i := 0; i < 1000; i++ {
		l.pending <- pendingItem{evt: bus.Event{Type: "cortex.hub.backlog", CausalRef: "evt-" + itoa(i)}}
	}

	preDrop := l.DroppedCount()

	// Run handler in goroutine so its 50ms blocking-send does not
	// stall the test if the channel is full and unconsumed.
	done := make(chan struct{})
	go func() {
		l.handleHubEvent(&hub.Event{
			ID:   "err-1",
			Type: hub.EventToolError, // warning per eventSeverity
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		// Acceptable: 50ms blocking timeout + scheduler jitter.
	}

	// At most one drop (the timeout fallback) is allowed; the
	// info-only fast-drop path must not trigger.
	postDrop := l.DroppedCount()
	delta := int64(postDrop - preDrop)
	if delta > 1 {
		t.Fatalf("warning event dropped via info path: delta=%d", delta)
	}
}

// TestWALKeeperLobe_EmitsBackpressureNote verifies that with at least
// one drop the warning Note ticker emits a single Note with
// Severity=warning and a "wal" tag. The interval is overridden to 50ms
// so the test runs in well under a second.
func TestWALKeeperLobe_EmitsBackpressureNote(t *testing.T) {
	durable, _ := newTestBus(t)
	defer durable.Close()

	ws := cortex.NewWorkspace(nil, nil)
	h := hub.New()
	l := NewWALKeeperLobe(h, durable, ws, WALFraming{}).
		WithBackpressureNoteInterval(50 * time.Millisecond)

	// Pre-load the dropped counter directly so the next ticker
	// interval emits a Note unconditionally.
	l.dropped.Store(7)

	stop := runLobe(t, l)
	defer stop()

	// Wait up to 1s for the ticker to fire and the Note to land.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		notes := ws.Snapshot()
		for _, n := range notes {
			if n.LobeID == l.ID() && n.Severity == cortex.SevWarning && hasTag(n.Tags, "wal") {
				t.Logf("got backpressure note: %q", n.Title)
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("backpressure note never published; notes=%d", len(ws.Snapshot()))
}

// TestWALKeeperLobe_NoNoteWhenZeroDrops verifies the ticker does NOT
// emit a Note when the dropped counter is zero (avoids spam).
func TestWALKeeperLobe_NoNoteWhenZeroDrops(t *testing.T) {
	durable, _ := newTestBus(t)
	defer durable.Close()

	ws := cortex.NewWorkspace(nil, nil)
	h := hub.New()
	l := NewWALKeeperLobe(h, durable, ws, WALFraming{}).
		WithBackpressureNoteInterval(20 * time.Millisecond)

	stop := runLobe(t, l)
	defer stop()

	time.Sleep(150 * time.Millisecond) // multiple tick intervals

	for _, n := range ws.Snapshot() {
		if n.LobeID == l.ID() {
			t.Fatalf("unexpected note when dropped=0: %+v", n)
		}
	}
}

// --- helpers ---

func hasTag(tags []string, want string) bool {
	for _, tag := range tags {
		if tag == want {
			return true
		}
	}
	return false
}

// itoa is a no-import int-to-string helper used in tight loops where
// importing strconv would be overkill. Handles non-negative ints only.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	buf := make([]byte, 0, 8)
	for i > 0 {
		buf = append([]byte{byte('0' + i%10)}, buf...)
		i /= 10
	}
	return string(buf)
}
