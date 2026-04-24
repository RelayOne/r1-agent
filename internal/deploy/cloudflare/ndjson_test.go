package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// tightenPolling shortens the package's polling knobs for tests so the
// suite finishes in milliseconds rather than multiple seconds. Must be
// called from every test that exercises TailNDJSON.
func tightenPolling(t *testing.T) {
	t.Helper()
	origPoll := pollInterval
	origCreate := createWaitInterval
	pollInterval = 10 * time.Millisecond
	createWaitInterval = 5 * time.Millisecond
	t.Cleanup(func() {
		pollInterval = origPoll
		createWaitInterval = origCreate
	})
}

// collector is a thread-safe onEvent sink for assertions. onEvent is
// documented to run on the tailer goroutine, so the test must not touch
// the slice from the main goroutine without holding the mutex.
type collector struct {
	mu     sync.Mutex
	events []Event
}

func (c *collector) onEvent(e Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
}

func (c *collector) snapshot() []Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Event, len(c.events))
	copy(out, c.events)
	return out
}

// waitFor polls fn every 5ms up to 2s, returning true when fn reports
// satisfied. The long upper bound is intentional: under `-race` and a
// loaded CI host the tailer's 10ms tick can slip.
func waitFor(t *testing.T, fn func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// startTail spawns TailNDJSON on a goroutine and returns a cancel func
// + an error channel that is written exactly once when the tailer
// exits. Callers should defer cancel() and <-errCh in that order.
func startTail(t *testing.T, path string, c *collector) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- TailNDJSON(ctx, path, c.onEvent)
	}()
	return cancel, errCh
}

// newTempNDJSONPath picks a path inside t.TempDir() but does NOT create
// the file — some tests rely on the "file does not yet exist" branch of
// openWhenReady.
func newTempNDJSONPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "wrangler.ndjson")
}

// appendLine appends data to path, creating the file if missing. It
// opens + syncs + closes per call to mimic Wrangler's flush behavior.
func appendLine(t *testing.T, path string, data string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("append open %s: %v", path, err)
	}
	if _, err := f.WriteString(data); err != nil {
		_ = f.Close()
		t.Fatalf("append write: %v", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		t.Fatalf("append sync: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("append close: %v", err)
	}
}

func TestTailNDJSON_EveryDocumentedType(t *testing.T) {
	tightenPolling(t)
	path := newTempNDJSONPath(t)
	c := &collector{}

	cancel, errCh := startTail(t, path, c)
	defer func() {
		cancel()
		<-errCh
	}()

	// Three representative event types from the spec's Wrangler NDJSON
	// Contract table. We don't assert on the full documented set here
	// because the tailer treats them all identically — what matters is
	// that three distinct, well-formed lines all reach onEvent.
	appendLine(t, path, `{"type":"build-start","message":"building"}`+"\n")
	appendLine(t, path, `{"type":"upload-progress","bytes":1024,"total":2048}`+"\n")
	appendLine(t, path, `{"type":"deploy-complete","url":"https://x.workers.dev","version_id":"v1"}`+"\n")

	ok := waitFor(t, func() bool { return len(c.snapshot()) >= 3 })
	if !ok {
		t.Fatalf("expected 3 events, got %d: %+v", len(c.snapshot()), c.snapshot())
	}
	events := c.snapshot()
	gotTypes := map[string]bool{}
	for _, e := range events {
		gotTypes[e.Type] = true
		if len(e.Raw) == 0 {
			t.Errorf("event %q has empty Raw", e.Type)
		}
		if e.Timestamp.IsZero() {
			t.Errorf("event %q has zero Timestamp", e.Type)
		}
	}
	for _, want := range []string{"build-start", "upload-progress", "deploy-complete"} {
		if !gotTypes[want] {
			t.Errorf("missing event type %q; got %v", want, gotTypes)
		}
	}
}

func TestTailNDJSON_UnknownType(t *testing.T) {
	tightenPolling(t)
	path := newTempNDJSONPath(t)
	c := &collector{}

	cancel, errCh := startTail(t, path, c)
	defer func() {
		cancel()
		<-errCh
	}()

	appendLine(t, path, `{"type":"future-v2-event","data":"opaque"}`+"\n")

	ok := waitFor(t, func() bool { return len(c.snapshot()) >= 1 })
	if !ok {
		t.Fatal("unknown-type event did not arrive")
	}
	got := c.snapshot()[0]
	if got.Type != "future-v2-event" {
		t.Errorf("Type = %q, want %q", got.Type, "future-v2-event")
	}
	// Raw must round-trip the full object so the caller can decode the
	// "data" payload the tailer itself knows nothing about.
	var decoded map[string]any
	if err := json.Unmarshal(got.Raw, &decoded); err != nil {
		t.Fatalf("Raw is not valid JSON: %v", err)
	}
	if decoded["data"] != "opaque" {
		t.Errorf("Raw lost payload: got %+v", decoded)
	}
}

func TestTailNDJSON_MalformedLine(t *testing.T) {
	tightenPolling(t)
	path := newTempNDJSONPath(t)
	c := &collector{}

	cancel, errCh := startTail(t, path, c)
	defer func() {
		cancel()
		<-errCh
	}()

	// First line is garbage; second is valid. The tailer must skip the
	// garbage (logged at DEBUG) and still deliver the valid event.
	appendLine(t, path, "not-json\n")
	appendLine(t, path, `{"type":"build-start"}`+"\n")

	ok := waitFor(t, func() bool { return len(c.snapshot()) >= 1 })
	if !ok {
		t.Fatal("valid event after malformed line did not arrive")
	}
	events := c.snapshot()
	if len(events) != 1 {
		t.Fatalf("expected exactly 1 event (malformed skipped), got %d: %+v", len(events), events)
	}
	if events[0].Type != "build-start" {
		t.Errorf("Type = %q, want %q", events[0].Type, "build-start")
	}
}

func TestTailNDJSON_PartialLineBuffered(t *testing.T) {
	tightenPolling(t)
	path := newTempNDJSONPath(t)
	c := &collector{}

	cancel, errCh := startTail(t, path, c)
	defer func() {
		cancel()
		<-errCh
	}()

	// Write the line in two chunks. The first lacks a trailing '\n',
	// so the tailer must buffer it; only after the second chunk
	// completes the line with '\n' should onEvent fire.
	appendLine(t, path, `{"type":"upload`)

	// Give the tailer several poll intervals to observe the partial
	// write. No event should arrive.
	time.Sleep(50 * time.Millisecond)
	if n := len(c.snapshot()); n != 0 {
		t.Fatalf("event fired on partial line; got %d events: %+v", n, c.snapshot())
	}

	appendLine(t, path, `-progress"}`+"\n")

	ok := waitFor(t, func() bool { return len(c.snapshot()) >= 1 })
	if !ok {
		t.Fatal("completed event did not arrive after second chunk")
	}
	events := c.snapshot()
	if events[0].Type != "upload-progress" {
		t.Errorf("Type = %q, want %q (partial line reassembly broke)", events[0].Type, "upload-progress")
	}
}

func TestTailNDJSON_CtxCancel(t *testing.T) {
	tightenPolling(t)

	// Pre-create the file so the tailer is guaranteed to be past
	// openWhenReady and sitting in the select loop when cancel() fires.
	// Otherwise we'd be testing openWhenReady's ctx branch, not the main
	// loop's, and the two return different error values.
	path := newTempNDJSONPath(t)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- TailNDJSON(ctx, path, func(Event) {})
	}()

	// Let the tailer reach its select loop.
	time.Sleep(30 * time.Millisecond)

	start := time.Now()
	cancel()

	select {
	case tErr := <-errCh:
		elapsed := time.Since(start)
		if elapsed > 2*time.Second {
			t.Fatalf("tailer took %v to return after cancel; want <2s", elapsed)
		}
		// Contract (ndjson.go doc): TailNDJSON returns nil on ctx cancel
		// from the main loop. If a future refactor chooses to propagate
		// ctx.Err instead, accept that too — both are legitimate
		// "cancelled cleanly" signals.
		if tErr != nil && !errors.Is(tErr, context.Canceled) && !errors.Is(tErr, context.DeadlineExceeded) {
			t.Fatalf("unexpected err on cancel: %v", tErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("tailer did not return within 2s of ctx cancel")
	}
}
