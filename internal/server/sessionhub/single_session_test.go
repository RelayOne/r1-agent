package sessionhub

// single_session_test.go — TASK-40 SessionHub.SetSingleSession tests.
//
// Asserts that:
//   - default mode is single-session OFF (multiple Creates succeed).
//   - SetSingleSession(true) gates Create on a 1-session limit.
//   - Delete clears the active count so a fresh Create after the
//     prior Delete succeeds (single-session is a "1 at a time" guard,
//     not a "1 ever" hard cap).
//   - Concurrent Creates under single-session mode race-deterministically:
//     the first wins, the second(s) all hit ErrSingleSessionExceeded.

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestSessionHub_SingleSession_Off(t *testing.T) {
	_, cleanup := withSandbox(t)
	defer cleanup()
	hub, err := NewHub()
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}
	if hub.SingleSession() {
		t.Fatal("default SingleSession() must be false")
	}
	wd1, wd2 := t.TempDir(), t.TempDir()
	if _, err := hub.Create(CreateOptions{Workdir: wd1}); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := hub.Create(CreateOptions{Workdir: wd2}); err != nil {
		t.Fatalf("second Create (off mode): %v", err)
	}
}

func TestSessionHub_SingleSession_RejectsSecond(t *testing.T) {
	_, cleanup := withSandbox(t)
	defer cleanup()
	hub, err := NewHub()
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}
	hub.SetSingleSession(true)
	if !hub.SingleSession() {
		t.Fatal("SetSingleSession(true): SingleSession() returned false")
	}

	wd1 := t.TempDir()
	if _, err := hub.Create(CreateOptions{Workdir: wd1}); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	wd2 := t.TempDir()
	_, err = hub.Create(CreateOptions{Workdir: wd2})
	if !errors.Is(err, ErrSingleSessionExceeded) {
		t.Errorf("second Create: got %v, want ErrSingleSessionExceeded", err)
	}
}

func TestSessionHub_SingleSession_AllowsAfterDelete(t *testing.T) {
	_, cleanup := withSandbox(t)
	defer cleanup()
	hub, err := NewHub()
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}
	hub.SetSingleSession(true)

	wd1 := t.TempDir()
	s1, err := hub.Create(CreateOptions{Workdir: wd1})
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if err := hub.Delete(s1.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	wd2 := t.TempDir()
	if _, err := hub.Create(CreateOptions{Workdir: wd2}); err != nil {
		t.Errorf("post-Delete Create: %v (should succeed)", err)
	}
}

func TestSessionHub_SingleSession_ConcurrentCreates(t *testing.T) {
	// assert.Equal-style block-checks below for race-determinism.
	_, cleanup := withSandbox(t)
	defer cleanup()
	hub, err := NewHub()
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}
	hub.SetSingleSession(true)
	const N = 16
	var wg sync.WaitGroup
	var successes int32
	var rejections int32
	wg.Add(N)
	for i := 0; i < N; i++ {
		wd := t.TempDir()
		go func(workdir string) {
			defer wg.Done()
			_, cerr := hub.Create(CreateOptions{Workdir: workdir})
			if cerr == nil {
				atomic.AddInt32(&successes, 1)
				return
			}
			if errors.Is(cerr, ErrSingleSessionExceeded) {
				atomic.AddInt32(&rejections, 1)
				return
			}
			t.Errorf("unexpected Create error: %v", cerr)
		}(wd)
	}
	wg.Wait()
	// assert.Equal-style explicit checks below confirm exactly one
	// goroutine acquired the single-session slot and the rest were
	// rejected via ErrSingleSessionExceeded.
	gotSuccesses := atomic.LoadInt32(&successes)
	gotRejections := atomic.LoadInt32(&rejections)
	if gotSuccesses != 1 {
		t.Errorf("successes: got %d, want 1", gotSuccesses)
	}
	if gotRejections != N-1 {
		t.Errorf("rejections: got %d, want %d", gotRejections, N-1)
	}
}
