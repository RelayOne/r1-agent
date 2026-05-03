package sessionhub

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/RelayOne/r1/internal/hub"
)

// stubJournal is a test-only Journal implementation that either
// succeeds or returns a configured error. We use it (rather than
// the real *journal.Writer) so the test can drive the failure path
// without a corrupted file on disk.
type stubJournal struct {
	calls atomic.Int64
	fail  error
}

func (s *stubJournal) Append(_ string, _ any) (uint64, error) {
	n := s.calls.Add(1)
	if s.fail != nil {
		return 0, s.fail
	}
	return uint64(n), nil
}

// TestOnEventJournalFirst_HappyPath asserts that a successful journal
// append is followed by a subscriber-fanout call in that order.
func TestOnEventJournalFirst_HappyPath(t *testing.T) {
	j := &stubJournal{}
	var fanoutCalls atomic.Int64
	var orderViolated atomic.Bool
	fanout := func(_ context.Context, _ *hub.Event) error {
		// At fanout time, journal MUST already have been called.
		if j.calls.Load() == 0 {
			orderViolated.Store(true)
		}
		fanoutCalls.Add(1)
		return nil
	}
	hook := JournalFirstHook(j, fanout)

	ev := &hub.Event{Type: hub.EventToolPostUse}
	if err := hook(context.Background(), ev); err != nil {
		t.Fatalf("hook: %v", err)
	}
	if j.calls.Load() != 1 {
		t.Fatalf("journal calls: got %d, want 1", j.calls.Load())
	}
	if fanoutCalls.Load() != 1 {
		t.Fatalf("fanout calls: got %d, want 1", fanoutCalls.Load())
	}
	if orderViolated.Load() {
		t.Fatalf("fanout fired before journal append")
	}
}

// TestOnEventJournalFirst_JournalFailureBlocksFanout is the load-bearing
// invariant from spec §11.24: when the journal Append fails, NO
// subscriber may observe the event. We model "kill journal write
// mid-stream" by configuring the stub to return an error and assert
// fanout is never reached.
func TestOnEventJournalFirst_JournalFailureBlocksFanout(t *testing.T) {
	wantErr := errors.New("disk full")
	j := &stubJournal{fail: wantErr}
	var fanoutCalls atomic.Int64
	fanout := func(_ context.Context, _ *hub.Event) error {
		fanoutCalls.Add(1)
		return nil
	}
	hook := JournalFirstHook(j, fanout)

	ev := &hub.Event{Type: hub.EventToolPostUse}
	err := hook(context.Background(), ev)
	if err == nil {
		t.Fatalf("expected error; got nil")
	}
	if !errors.Is(err, wantErr) {
		// errWrap strips Is-chain; assert via substring instead.
		if !contains(err.Error(), "disk full") {
			t.Fatalf("error: got %v, want chain containing %q", err, wantErr)
		}
	}
	if fanoutCalls.Load() != 0 {
		t.Fatalf("fanout fired despite journal failure: %d calls", fanoutCalls.Load())
	}
}

// TestOnEventJournalFirst_NilJournalSkipsAppend asserts the hook
// gracefully no-ops when no journal is wired (boot path before
// SetJournal fires).
func TestOnEventJournalFirst_NilJournalSkipsAppend(t *testing.T) {
	var fanoutCalls atomic.Int64
	hook := JournalFirstHook(nil, func(_ context.Context, _ *hub.Event) error {
		fanoutCalls.Add(1)
		return nil
	})
	if err := hook(context.Background(), &hub.Event{}); err != nil {
		t.Fatalf("hook: %v", err)
	}
	if fanoutCalls.Load() != 1 {
		t.Fatalf("fanout calls: got %d, want 1", fanoutCalls.Load())
	}
}

// TestOnEventJournalFirst_NilFanoutOK asserts the hook works as a
// journal-only sink when no fanout callback is supplied.
func TestOnEventJournalFirst_NilFanoutOK(t *testing.T) {
	j := &stubJournal{}
	hook := JournalFirstHook(j, nil)
	if err := hook(context.Background(), &hub.Event{}); err != nil {
		t.Fatalf("hook: %v", err)
	}
	if j.calls.Load() != 1 {
		t.Fatalf("journal calls: got %d, want 1", j.calls.Load())
	}
}

// TestSession_DispatchEvent asserts the Session.DispatchEvent entry
// point honours the OnEvent hook the daemon installed via SetOnEvent.
func TestSession_DispatchEvent(t *testing.T) {
	_, cleanup := withSandbox(t)
	defer cleanup()
	hub2, _ := NewHub()
	s, _ := hub2.Create(CreateOptions{Workdir: t.TempDir()})

	j := &stubJournal{}
	var fanoutSaw atomic.Int64
	hook := JournalFirstHook(j, func(_ context.Context, _ *hub.Event) error {
		fanoutSaw.Add(1)
		return nil
	})
	s.SetOnEvent(hook)

	if err := s.DispatchEvent(context.Background(), &hub.Event{Type: hub.EventToolPostUse}); err != nil {
		t.Fatalf("DispatchEvent: %v", err)
	}
	if j.calls.Load() != 1 || fanoutSaw.Load() != 1 {
		t.Fatalf("journal=%d fanout=%d, want 1/1", j.calls.Load(), fanoutSaw.Load())
	}

	// Now drop a journal failure in mid-stream and assert fanout
	// stops seeing events.
	j.fail = errors.New("write failure")
	if err := s.DispatchEvent(context.Background(), &hub.Event{Type: hub.EventToolPostUse}); err == nil {
		t.Fatalf("expected DispatchEvent error on journal failure")
	}
	if got := fanoutSaw.Load(); got != 1 {
		t.Fatalf("fanout invoked despite journal failure: got %d, want 1", got)
	}
}

// contains is a tiny strings.Contains substitute kept inline so this
// test file doesn't import the strings package just for one call.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
