package sessionctl

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ericmacdougall/stoke/internal/operator"
)

// slowOp is a test operator.Operator whose Ask blocks until the
// supplied context is cancelled or done signal fires, then returns the
// preset label. Used to exercise the socket-wins and timeout paths.
type slowOp struct {
	label string
	done  chan struct{}
}

func (s *slowOp) Ask(ctx context.Context, _ string, _ []operator.Option) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-s.done:
		return s.label, nil
	}
}

func (s *slowOp) Notify(operator.NotifyKind, string) {}

// recordingOp is a test operator.Operator that records Ask calls and
// returns a fixed label immediately. Used for the happy-path test.
type recordingOp struct {
	mu     sync.Mutex
	label  string
	prompt string
	opts   []operator.Option
	calls  int
}

func (r *recordingOp) Ask(_ context.Context, prompt string, opts []operator.Option) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.prompt = prompt
	r.opts = opts
	return r.label, nil
}

func (r *recordingOp) Notify(operator.NotifyKind, string) {}

func TestAskThroughRouter_TerminalResolves(t *testing.T) {
	t.Parallel()
	r := NewApprovalRouter()
	op := &recordingOp{label: "retry"}

	ch, askID, err := r.AskThroughRouter(
		context.Background(),
		op,
		"AC go-build failed. What now?",
		[]operator.Option{{Label: "retry"}, {Label: "accept-as-is"}, {Label: "edit-prompt"}},
		time.Second,
	)
	if err != nil {
		t.Fatalf("AskThroughRouter: %v", err)
	}
	if askID == "" {
		t.Fatalf("AskThroughRouter returned empty askID")
	}
	if len(askID) != 12 {
		t.Errorf("askID length = %d, want 12", len(askID))
	}

	select {
	case got, ok := <-ch:
		if !ok {
			t.Fatalf("channel closed before decision")
		}
		if got.Choice != "retry" {
			t.Errorf("Choice = %q, want %q", got.Choice, "retry")
		}
		if got.Actor != "cli:term" {
			t.Errorf("Actor = %q, want %q", got.Actor, "cli:term")
		}
		if got.AskID != askID {
			t.Errorf("AskID = %q, want %q", got.AskID, askID)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for terminal decision")
	}
}

func TestAskThroughRouter_SocketResolvesFirst(t *testing.T) {
	t.Parallel()
	r := NewApprovalRouter()
	op := &slowOp{label: "retry", done: make(chan struct{})}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, askID, err := r.AskThroughRouter(
		ctx, op, "question?", nil, 5*time.Second,
	)
	if err != nil {
		t.Fatalf("AskThroughRouter: %v", err)
	}

	// Simulate an early socket-path Resolve — before the terminal Ask
	// returns. The router should deliver the socket decision to ch.
	if err := r.Resolve(askID, Decision{
		AskID:     askID,
		Choice:    "accept-as-is",
		Actor:     "cli:socket",
		Timestamp: time.Now(),
	}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	select {
	case got, ok := <-ch:
		if !ok {
			t.Fatalf("channel closed before decision")
		}
		if got.Actor != "cli:socket" {
			t.Errorf("Actor = %q, want %q (socket should win)", got.Actor, "cli:socket")
		}
		if got.Choice != "accept-as-is" {
			t.Errorf("Choice = %q, want %q", got.Choice, "accept-as-is")
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for socket decision")
	}

	// Now let the terminal goroutine finish; it should find askID already
	// resolved (ErrAskUnknown) and exit silently without panicking.
	close(op.done)
	// Small grace window to let the goroutine attempt its idempotent Resolve.
	time.Sleep(50 * time.Millisecond)
}

func TestAskThroughRouter_Timeout(t *testing.T) {
	t.Parallel()
	r := NewApprovalRouter()
	op := &slowOp{label: "retry", done: make(chan struct{})}
	defer close(op.done)

	// Context deadline is shorter than the router timeout to force the
	// slowOp to cancel on ctx first, BUT the router timeout is the one
	// that actually delivers the "timeout" decision, not the op.
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()

	ch, _, err := r.AskThroughRouter(
		ctx, op, "timeout?", nil, 50*time.Millisecond,
	)
	if err != nil {
		t.Fatalf("AskThroughRouter: %v", err)
	}

	select {
	case got, ok := <-ch:
		if !ok {
			t.Fatalf("channel closed before timeout decision")
		}
		if got.Choice != "timeout" {
			t.Errorf("Choice = %q, want %q", got.Choice, "timeout")
		}
		if got.Actor != "timer" {
			t.Errorf("Actor = %q, want %q", got.Actor, "timer")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timed out waiting for router timeout sentinel")
	}
}

// TestAskThroughRouter_NoOpHappyPath exercises the spec-suggested happy
// path: operator.NoOp{Default:"retry"} immediately answers "retry".
func TestAskThroughRouter_NoOpHappyPath(t *testing.T) {
	t.Parallel()
	r := NewApprovalRouter()
	op := &operator.NoOp{Default: "retry"}

	ch, _, err := r.AskThroughRouter(
		context.Background(), op, "prompt?", nil, time.Second,
	)
	if err != nil {
		t.Fatalf("AskThroughRouter: %v", err)
	}

	select {
	case got := <-ch:
		if got.Choice != "retry" {
			t.Errorf("Choice = %q, want %q", got.Choice, "retry")
		}
		if got.Actor != "cli:term" {
			t.Errorf("Actor = %q, want %q", got.Actor, "cli:term")
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for NoOp decision")
	}
}
