package cortex

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSemaphoreCapacity(t *testing.T) {
	s := NewLobeSemaphore(8)

	// 8 acquires should all succeed within their timeout.
	for i := 0; i < 8; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		if err := s.Acquire(ctx); err != nil {
			cancel()
			t.Fatalf("acquire %d: unexpected error: %v", i, err)
		}
		cancel()
	}

	// 9th acquire must block until the timeout expires.
	ctx9, cancel9 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	start := time.Now()
	err := s.Acquire(ctx9)
	elapsed := time.Since(start)
	cancel9()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("9th acquire: expected DeadlineExceeded, got %v", err)
	}
	if elapsed < 40*time.Millisecond {
		t.Fatalf("9th acquire returned too quickly (%v); expected to block until deadline", elapsed)
	}

	// Release one slot, then a fresh acquire should succeed.
	s.Release()
	ctxOK, cancelOK := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelOK()
	if err := s.Acquire(ctxOK); err != nil {
		t.Fatalf("acquire after release: unexpected error: %v", err)
	}
}

func TestSemaphoreCtxCancel(t *testing.T) {
	s := NewLobeSemaphore(1)

	// Take the only slot.
	ctxFirst, cancelFirst := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelFirst()
	if err := s.Acquire(ctxFirst); err != nil {
		t.Fatalf("initial acquire: unexpected error: %v", err)
	}

	// Spawn a goroutine that tries to acquire under a cancellable ctx.
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.Acquire(ctx)
	}()

	// Give the goroutine a moment to enter Acquire, then cancel.
	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("goroutine did not return within 100ms after cancel")
	}
}

func TestSemaphorePanicsOversize(t *testing.T) {
	cases := []int{0, 9}
	for _, c := range cases {
		c := c
		t.Run("", func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatalf("NewLobeSemaphore(%d): expected panic, got none", c)
				}
			}()
			_ = NewLobeSemaphore(c)
		})
	}
}
