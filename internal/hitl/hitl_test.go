package hitl

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1-agent/internal/streamjson"
)

// newTestSvc builds a Service with a TwoLane emitter over a shared
// buffer so tests can assert emitted lines.
func newTestSvc(t *testing.T, stdin io.Reader, timeout time.Duration) (*Service, *bytes.Buffer, *streamjson.TwoLane) {
	t.Helper()
	buf := &bytes.Buffer{}
	tl := streamjson.NewTwoLane(buf, true)
	return New(tl, stdin, timeout), buf, tl
}

// TestHITLDecisionHappy verifies the happy path: stdin line parses
// to an approved decision; RequestApproval returns Approved=true.
func TestHITLDecisionHappy(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	svc, buf, tl := newTestSvc(t, stdinR, 2*time.Second)
	defer tl.Drain(time.Second)

	var wg sync.WaitGroup
	wg.Add(1)
	var got Decision
	go func() {
		defer wg.Done()
		got = svc.RequestApproval(context.Background(), Request{
			Reason:       "soft pass",
			ApprovalType: "soft_pass",
		})
	}()
	// Give the emitter a beat to write hitl_required, then deliver
	// the decision.
	time.Sleep(50 * time.Millisecond)
	_, _ = stdinW.Write([]byte(`{"decision":true,"reason":"ok","decided_by":"test@example.com"}` + "\n"))
	wg.Wait()

	if !got.Approved {
		t.Errorf("expected Approved=true, got %+v", got)
	}
	if got.DecidedBy != "test@example.com" {
		t.Errorf("decided_by=%q", got.DecidedBy)
	}
	// Verify hitl_required line was emitted.
	tl.Drain(time.Second)
	if !strings.Contains(buf.String(), `"type":"hitl_required"`) {
		t.Errorf("expected hitl_required in emitter output, got %q", buf.String())
	}
}

// TestHITLTimeout verifies timeout with no stdin input returns
// Approved=false, Reason="timeout", and emits hitl.timeout.
func TestHITLTimeout(t *testing.T) {
	stdinR, _ := io.Pipe()
	svc, buf, tl := newTestSvc(t, stdinR, 150*time.Millisecond)
	defer tl.Drain(time.Second)

	d := svc.RequestApproval(context.Background(), Request{Reason: "x"})
	if d.Approved {
		t.Errorf("expected rejection on timeout")
	}
	if d.Reason != "timeout" {
		t.Errorf("reason=%q", d.Reason)
	}
	tl.Drain(time.Second)
	if !strings.Contains(buf.String(), `"subtype":"hitl.timeout"`) {
		t.Errorf("expected hitl.timeout observability event, got %q", buf.String())
	}
}

// TestHITLMalformedLine verifies a bad-JSON line is discarded and the
// next valid line still resolves the pending request.
func TestHITLMalformedLine(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	svc, _, tl := newTestSvc(t, stdinR, 2*time.Second)
	defer tl.Drain(time.Second)

	var wg sync.WaitGroup
	var got Decision
	wg.Add(1)
	go func() {
		defer wg.Done()
		got = svc.RequestApproval(context.Background(), Request{Reason: "x"})
	}()
	time.Sleep(30 * time.Millisecond)
	_, _ = stdinW.Write([]byte("not-json\n"))
	time.Sleep(30 * time.Millisecond)
	_, _ = stdinW.Write([]byte(`{"decision":true,"reason":"finally","decided_by":"user"}` + "\n"))
	wg.Wait()

	if !got.Approved {
		t.Errorf("expected approval after malformed+valid, got %+v", got)
	}
}

// TestHITLStdinClosed verifies EOF on stdin auto-rejects the pending
// request with Reason="stdin_closed".
func TestHITLStdinClosed(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	svc, _, tl := newTestSvc(t, stdinR, 5*time.Second)
	defer tl.Drain(time.Second)

	var wg sync.WaitGroup
	var got Decision
	wg.Add(1)
	go func() {
		defer wg.Done()
		got = svc.RequestApproval(context.Background(), Request{Reason: "x"})
	}()
	time.Sleep(30 * time.Millisecond)
	_ = stdinW.Close()
	wg.Wait()

	if got.Approved {
		t.Errorf("expected rejection on stdin close")
	}
	if got.Reason != "stdin_closed" {
		t.Errorf("reason=%q, want stdin_closed", got.Reason)
	}
}

// TestHITLConcurrentRequest verifies a second request sees
// "concurrent_request" while the first is in flight.
func TestHITLConcurrentRequest(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	svc, _, tl := newTestSvc(t, stdinR, 2*time.Second)
	defer tl.Drain(time.Second)

	first := make(chan Decision, 1)
	go func() {
		first <- svc.RequestApproval(context.Background(), Request{Reason: "first"})
	}()
	time.Sleep(50 * time.Millisecond)
	second := svc.RequestApproval(context.Background(), Request{Reason: "second"})
	if second.Reason != "concurrent_request" {
		t.Errorf("second.Reason=%q, want concurrent_request", second.Reason)
	}
	// Resolve first so goroutine exits.
	_, _ = stdinW.Write([]byte(`{"decision":true,"reason":"ok","decided_by":"u"}` + "\n"))
	<-first
}

// TestHITLContextCanceled verifies cancelling the context during the
// wait returns Reason="context_canceled".
func TestHITLContextCanceled(t *testing.T) {
	stdinR, _ := io.Pipe()
	svc, _, tl := newTestSvc(t, stdinR, 5*time.Second)
	defer tl.Drain(time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	d := svc.RequestApproval(ctx, Request{Reason: "x"})
	if d.Approved {
		t.Errorf("expected rejection on ctx cancel")
	}
	if d.Reason != "context_canceled" {
		t.Errorf("reason=%q, want context_canceled", d.Reason)
	}
}

// TestTimeoutOrDefault exercises the defaulting helper.
func TestTimeoutOrDefault(t *testing.T) {
	if got := TimeoutOrDefault(5*time.Second, time.Minute); got != 5*time.Second {
		t.Errorf("configured value should win: got %v", got)
	}
	if got := TimeoutOrDefault(0, time.Minute); got != time.Minute {
		t.Errorf("fallback should apply: got %v", got)
	}
}
