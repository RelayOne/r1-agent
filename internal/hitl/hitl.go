// Package hitl implements the human-in-the-loop approval flow used by
// the cloudswarm-protocol (spec-2 item 3). A worker invokes
// Service.RequestApproval when a gate needs human sign-off (soft-pass,
// destructive op, protected-file write). The service:
//
//   1. Emits an `hitl_required` line on the TwoLane's critical lane.
//   2. Starts a singleton stdin reader goroutine (if not already
//      running).
//   3. Blocks on an internal channel until the reader delivers a
//      Decision or a timeout/context cancellation fires.
//
// Wire format on stdin matches the CloudSwarm supervisor output
// (RT-CLOUDSWARM-MAP §3): one JSON object per line, decoded into
// Decision{Approved, Reason, DecidedBy}.
//
// Stdin constraints:
//   - os.Stdin.SetDeadline is broken on Linux pipes (golang/go#24842),
//     so timeouts use time.NewTimer + select.
//   - bufio.Reader with 1 MiB buffer handles long approval notes.
//   - EOF auto-rejects any pending request with Reason="stdin_closed".
//
// Only one HITL request is in-flight at a time per Service (the
// CloudSwarm workflow enforces this upstream anyway). Concurrent
// callers receive an immediate reject with Reason="concurrent_request".
package hitl

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/RelayOne/r1-agent/internal/streamjson"
)

// Request is the caller-facing input to RequestApproval.
type Request struct {
	Reason       string
	ApprovalType string // "soft_pass" | "file_write" | "destructive_op"
	File         string
	Context      map[string]any
}

// Decision is the operator's response from stdin. Field names match
// RT-CLOUDSWARM-MAP §3's supervisor output shape.
type Decision struct {
	Approved  bool   `json:"decision"`
	Reason    string `json:"reason"`
	DecidedBy string `json:"decided_by"`
}

// Service brokers hitl_required events + stdin decisions. Safe for
// concurrent RequestApproval calls; only one proceeds at a time.
type Service struct {
	emitter *streamjson.TwoLane
	stdin   io.Reader
	timeout time.Duration

	mu     sync.Mutex
	waiter chan Decision

	readerOnce sync.Once
	readerDone chan struct{} // closed on EOF
}

// New constructs a Service. timeout controls the default wait ceiling
// for every RequestApproval; the per-request timeout can override via
// context.WithTimeout.
func New(emitter *streamjson.TwoLane, stdin io.Reader, timeout time.Duration) *Service {
	return &Service{
		emitter:    emitter,
		stdin:      stdin,
		timeout:    timeout,
		readerDone: make(chan struct{}),
	}
}

// RequestApproval emits an hitl_required line and blocks until the
// operator replies via stdin, the timeout fires, the context cancels,
// or stdin closes. Returns Decision{Approved: false, Reason: <why>}
// on every failure path so callers can forward a consistent shape.
func (s *Service) RequestApproval(ctx context.Context, req Request) Decision {
	// Guard: one HITL at a time per service.
	s.mu.Lock()
	if s.waiter != nil {
		s.mu.Unlock()
		return Decision{Approved: false, Reason: "concurrent_request"}
	}
	ch := make(chan Decision, 1)
	s.waiter = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.waiter = nil
		s.mu.Unlock()
	}()

	// Emit the hitl_required line on the critical lane.
	if s.emitter != nil {
		extra := map[string]any{
			"reason":        req.Reason,
			"approval_type": req.ApprovalType,
		}
		if req.File != "" {
			extra["file"] = req.File
		}
		if req.Context != nil {
			extra["_stoke.dev/context"] = req.Context
		}
		s.emitter.EmitTopLevel(streamjson.TypeHITLRequired, extra)
	}

	s.startReaderOnce()

	// Determine wait ceiling.
	timeout := s.timeout
	if timeout <= 0 {
		timeout = time.Hour
	}
	t := time.NewTimer(timeout)
	defer t.Stop()

	select {
	case d := <-ch:
		return d
	case <-t.C:
		if s.emitter != nil {
			s.emitter.EmitSystem("hitl.timeout", map[string]any{"_stoke.dev/reason": req.Reason})
		}
		return Decision{Approved: false, Reason: "timeout"}
	case <-ctx.Done():
		return Decision{Approved: false, Reason: "context_canceled"}
	case <-s.readerDone:
		return Decision{Approved: false, Reason: "stdin_closed"}
	}
}

// startReaderOnce lazy-starts the stdin reader goroutine. Idempotent
// (sync.Once) so repeated RequestApproval calls share a single
// reader.
func (s *Service) startReaderOnce() {
	s.readerOnce.Do(func() {
		if s.stdin == nil {
			close(s.readerDone)
			return
		}
		go s.runReader()
	})
}

// runReader loops over stdin lines, decodes each into Decision, and
// delivers the result to the current waiter. Malformed lines emit a
// error event (observability lane) and are discarded. EOF signals
// readerDone so pending waiters auto-reject.
func (s *Service) runReader() {
	defer close(s.readerDone)
	r := bufio.NewReaderSize(s.stdin, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			s.deliver(line)
		}
		if err != nil {
			return
		}
	}
}

// deliver parses one stdin line. On success, hands the Decision to
// the current waiter; on malformed JSON emits an error subtype and
// continues.
func (s *Service) deliver(line []byte) {
	var d Decision
	if err := json.Unmarshal(line, &d); err != nil {
		if s.emitter != nil {
			// Observability lane — drop-oldest safe.
			s.emitter.EmitSystem("error", map[string]any{
				"_stoke.dev/kind":    "malformed_decision",
				"_stoke.dev/error":   err.Error(),
				"_stoke.dev/raw_len": len(line),
			})
		}
		return
	}
	s.mu.Lock()
	waiter := s.waiter
	s.mu.Unlock()
	if waiter == nil {
		// No one waiting — discard, as the operator spoke too late or
		// too early.
		return
	}
	select {
	case waiter <- d:
	default:
		// Channel full (shouldn't happen with cap 1); log + drop.
		if s.emitter != nil {
			s.emitter.EmitSystem("error", map[string]any{
				"_stoke.dev/kind": "waiter_channel_full",
			})
		}
	}
}

// Close signals the reader to stop if it hasn't already. Returns
// nil for future-proofing; today no error is possible.
func (s *Service) Close() error {
	// Nothing to close — the reader exits on stdin EOF or on process
	// shutdown. The struct stays reusable for the lifetime of the
	// process.
	return nil
}

// TimeoutOrDefault returns the per-service timeout, defaulting to the
// fallback when the configured value is <=0.
func TimeoutOrDefault(configured, fallback time.Duration) time.Duration {
	if configured > 0 {
		return configured
	}
	return fallback
}

// Supervisor expects plain JSON, not base64. Supervisor base64-decodes
// BEFORE writing to our stdin (per RT-CLOUDSWARM-MAP §3), so we never
// see base64 at this layer. This comment exists to document the
// invariant for anyone reading this file; no runtime code depends on it.
var _ = fmt.Sprintf
