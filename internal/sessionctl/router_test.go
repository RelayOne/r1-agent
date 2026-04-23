package sessionctl

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestRouter_RegisterResolve_Roundtrip(t *testing.T) {
	t.Parallel()
	r := NewApprovalRouter()

	ch, err := r.Register("a", 0)
	if err != nil {
		t.Fatalf("Register: unexpected error: %v", err)
	}

	want := Decision{
		AskID:     "a",
		Choice:    "yes",
		Reason:    "lgtm",
		Actor:     "cli:term",
		Timestamp: time.Now(),
	}
	if err := r.Resolve("a", want); err != nil {
		t.Fatalf("Resolve: unexpected error: %v", err)
	}

	select {
	case got, ok := <-ch:
		if !ok {
			t.Fatalf("channel closed before receiving decision")
		}
		if got.AskID != want.AskID || got.Choice != want.Choice || got.Reason != want.Reason || got.Actor != want.Actor {
			t.Fatalf("got decision %+v, want %+v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for decision on channel")
	}

	// After resolve, channel should be closed (receive returns zero value, ok=false).
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatalf("expected channel closed after resolve")
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for channel close")
	}
}

func TestRouter_ResolveUnknown_Errors(t *testing.T) {
	t.Parallel()
	r := NewApprovalRouter()

	err := r.Resolve("missing", Decision{AskID: "missing", Choice: "yes"})
	if !errors.Is(err, ErrAskUnknown) {
		t.Fatalf("Resolve(missing): got err=%v, want %v", err, ErrAskUnknown)
	}
}

func TestRouter_DoubleRegister_Errors(t *testing.T) {
	t.Parallel()
	r := NewApprovalRouter()

	if _, err := r.Register("a", 0); err != nil {
		t.Fatalf("first Register: unexpected error: %v", err)
	}
	_, err := r.Register("a", 0)
	if !errors.Is(err, ErrAskAlreadyRegistered) {
		t.Fatalf("second Register: got err=%v, want %v", err, ErrAskAlreadyRegistered)
	}
}

func TestRouter_DoubleResolve_Errors(t *testing.T) {
	t.Parallel()
	r := NewApprovalRouter()

	if _, err := r.Register("a", 0); err != nil {
		t.Fatalf("Register: unexpected error: %v", err)
	}
	if err := r.Resolve("a", Decision{AskID: "a", Choice: "yes"}); err != nil {
		t.Fatalf("first Resolve: unexpected error: %v", err)
	}
	err := r.Resolve("a", Decision{AskID: "a", Choice: "no"})
	if !errors.Is(err, ErrAskUnknown) {
		t.Fatalf("second Resolve: got err=%v, want %v", err, ErrAskUnknown)
	}
}

func TestRouter_Timeout_DeliversSentinel(t *testing.T) {
	t.Parallel()
	r := NewApprovalRouter()

	ch, err := r.Register("a", 50*time.Millisecond)
	if err != nil {
		t.Fatalf("Register: unexpected error: %v", err)
	}

	select {
	case got, ok := <-ch:
		if !ok {
			t.Fatalf("channel closed before timeout decision")
		}
		if got.Choice != "timeout" {
			t.Fatalf("Choice: got %q, want %q", got.Choice, "timeout")
		}
		if got.Actor != "timer" {
			t.Fatalf("Actor: got %q, want %q", got.Actor, "timer")
		}
		if got.AskID != "a" {
			t.Fatalf("AskID: got %q, want %q", got.AskID, "a")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timed out waiting for timeout sentinel")
	}
}

func TestRouter_Timeout_IdempotentAfterEarlyResolve(t *testing.T) {
	t.Parallel()
	r := NewApprovalRouter()

	ch, err := r.Register("a", 25*time.Millisecond)
	if err != nil {
		t.Fatalf("Register: unexpected error: %v", err)
	}

	// Resolve before the timer fires. The timer goroutine will call
	// Resolve again, which should be a no-op (ErrAskUnknown, ignored).
	if err := r.Resolve("a", Decision{AskID: "a", Choice: "yes", Actor: "cli:term"}); err != nil {
		t.Fatalf("Resolve: unexpected error: %v", err)
	}

	select {
	case got, ok := <-ch:
		if !ok {
			t.Fatalf("channel closed before decision")
		}
		if got.Choice != "yes" {
			t.Fatalf("Choice: got %q, want %q", got.Choice, "yes")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timed out waiting for decision")
	}

	// Give the timer time to fire; nothing should blow up.
	time.Sleep(75 * time.Millisecond)
	if got := r.OldestOpen(); got != "" {
		t.Fatalf("OldestOpen after resolve: got %q, want \"\"", got)
	}
}

func TestRouter_OldestOpen_ByOpenedAt(t *testing.T) {
	t.Parallel()
	r := NewApprovalRouter()

	if _, err := r.Register("a", 0); err != nil {
		t.Fatalf("Register a: %v", err)
	}
	// Sleep to ensure monotonic ordering of OpenedAt.
	time.Sleep(2 * time.Millisecond)
	if _, err := r.Register("b", 0); err != nil {
		t.Fatalf("Register b: %v", err)
	}

	if got := r.OldestOpen(); got != "a" {
		t.Fatalf("OldestOpen: got %q, want %q", got, "a")
	}

	if err := r.Resolve("a", Decision{AskID: "a", Choice: "yes"}); err != nil {
		t.Fatalf("Resolve a: %v", err)
	}

	if got := r.OldestOpen(); got != "b" {
		t.Fatalf("OldestOpen after Resolve a: got %q, want %q", got, "b")
	}

	if err := r.Resolve("b", Decision{AskID: "b", Choice: "yes"}); err != nil {
		t.Fatalf("Resolve b: %v", err)
	}
	if got := r.OldestOpen(); got != "" {
		t.Fatalf("OldestOpen when empty: got %q, want \"\"", got)
	}
}

func TestRouter_List_Ordered(t *testing.T) {
	t.Parallel()
	r := NewApprovalRouter()

	ids := []string{"alpha", "bravo", "charlie", "delta"}
	for _, id := range ids {
		if _, err := r.Register(id, 0); err != nil {
			t.Fatalf("Register %q: %v", id, err)
		}
		time.Sleep(1 * time.Millisecond)
	}

	got := r.List()
	if !reflect.DeepEqual(got, ids) {
		t.Fatalf("List: got %v, want %v", got, ids)
	}

	// Resolve the middle one and confirm it disappears while order is preserved.
	if err := r.Resolve("bravo", Decision{AskID: "bravo", Choice: "yes"}); err != nil {
		t.Fatalf("Resolve bravo: %v", err)
	}
	wantAfter := []string{"alpha", "charlie", "delta"}
	got = r.List()
	if !reflect.DeepEqual(got, wantAfter) {
		t.Fatalf("List after resolve: got %v, want %v", got, wantAfter)
	}
}

func TestRouter_List_EmptyWhenNone(t *testing.T) {
	t.Parallel()
	r := NewApprovalRouter()
	if got := r.List(); len(got) != 0 {
		t.Fatalf("List on empty router: got %v, want empty", got)
	}
}
