package sessionhub

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestSession_PauseResumeIdempotent confirms Pause/Resume are
// idempotent and observable via IsPaused / State.
func TestSession_PauseResumeIdempotent(t *testing.T) {
	s := newSession("s-pause", t.TempDir(), "model")

	if s.IsPaused() {
		t.Fatalf("freshly-created session should not be paused")
	}

	s.Pause()
	if !s.IsPaused() {
		t.Errorf("Pause should set IsPaused=true")
	}
	if s.State != SessionStatePaused {
		t.Errorf("State after Pause = %q, want %q", s.State, SessionStatePaused)
	}

	s.Pause()
	if !s.IsPaused() {
		t.Errorf("idempotent Pause should leave IsPaused=true")
	}

	s.Resume()
	if s.IsPaused() {
		t.Errorf("Resume should clear IsPaused")
	}

	s.Resume()
	if s.IsPaused() {
		t.Errorf("idempotent Resume should leave IsPaused=false")
	}
}

// TestSession_WaitWhilePaused_BlocksThenWakes asserts a goroutine
// waiting on WaitWhilePaused blocks while paused, then wakes when
// Resume fires. Race-detector-safe.
func TestSession_WaitWhilePaused_BlocksThenWakes(t *testing.T) {
	s := newSession("s-wait", t.TempDir(), "model")
	s.Pause()

	done := make(chan error, 1)
	go func() {
		done <- s.WaitWhilePaused(context.Background())
	}()

	select {
	case <-done:
		t.Fatalf("WaitWhilePaused returned before Resume")
	case <-time.After(20 * time.Millisecond):
	}

	s.Resume()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("WaitWhilePaused after Resume: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("WaitWhilePaused did not wake after Resume")
	}
}

// TestSession_WaitWhilePaused_NotPaused returns immediately.
func TestSession_WaitWhilePaused_NotPaused(t *testing.T) {
	s := newSession("s-not-paused", t.TempDir(), "model")
	if err := s.WaitWhilePaused(context.Background()); err != nil {
		t.Errorf("WaitWhilePaused on not-paused session: %v", err)
	}
}

// TestSession_WaitWhilePaused_CtxCancel returns ctx.Err() when ctx
// fires before Resume.
func TestSession_WaitWhilePaused_CtxCancel(t *testing.T) {
	s := newSession("s-ctx", t.TempDir(), "model")
	s.Pause()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	err := s.WaitWhilePaused(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("WaitWhilePaused with cancelled ctx: got %v, want context.Canceled", err)
	}
}

// TestSession_Send_PreRun returns ErrSessionInputClosed.
func TestSession_Send_PreRun(t *testing.T) {
	s := newSession("s-send-pre-run", t.TempDir(), "model")
	err := s.Send(InboundTurn{Text: "hi"})
	if !errors.Is(err, ErrSessionInputClosed) {
		t.Errorf("Send before Run: got %v, want ErrSessionInputClosed", err)
	}
}

// TestSession_Send_BufferAndDrain confirms Send pushes onto the
// inbox channel installed by Run, and Inbox() returns a channel
// from which the agent loop can drain.
func TestSession_Send_BufferAndDrain(t *testing.T) {
	s := newSession("s-send-drain", t.TempDir(), "model")
	s.installInbox()
	defer s.closeInbox()

	if err := s.Send(InboundTurn{Text: "first"}); err != nil {
		t.Fatalf("Send #1: %v", err)
	}
	if err := s.Send(InboundTurn{Role: "user", Text: "second"}); err != nil {
		t.Fatalf("Send #2: %v", err)
	}

	inbox := s.Inbox()
	if inbox == nil {
		t.Fatalf("Inbox returned nil after installInbox")
	}
	turn1 := <-inbox
	turn2 := <-inbox
	if turn1.Text != "first" || turn1.Role != "user" {
		t.Errorf("turn1 = %+v, want {user, first}", turn1)
	}
	if turn2.Text != "second" {
		t.Errorf("turn2 = %+v, want second", turn2)
	}
}

// TestSession_Send_Full returns ErrSessionInputFull when the inbox is
// at capacity.
func TestSession_Send_Full(t *testing.T) {
	s := newSession("s-send-full", t.TempDir(), "model")
	s.installInbox()
	defer s.closeInbox()

	for i := 0; i < sessionInboxCap; i++ {
		if err := s.Send(InboundTurn{Text: "x"}); err != nil {
			t.Fatalf("Send fill #%d: %v", i, err)
		}
	}
	err := s.Send(InboundTurn{Text: "overflow"})
	if !errors.Is(err, ErrSessionInputFull) {
		t.Errorf("Send when full: got %v, want ErrSessionInputFull", err)
	}
}

// TestSession_CloseInbox_ThenSend returns ErrSessionInputClosed.
func TestSession_CloseInbox_ThenSend(t *testing.T) {
	s := newSession("s-send-closed", t.TempDir(), "model")
	s.installInbox()
	s.closeInbox()
	err := s.Send(InboundTurn{Text: "hi"})
	if !errors.Is(err, ErrSessionInputClosed) {
		t.Errorf("Send after closeInbox: got %v, want ErrSessionInputClosed", err)
	}
}
