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
