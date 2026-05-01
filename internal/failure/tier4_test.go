package failure

import (
	"fmt"
	"testing"
	"time"
)

func TestDeriveIdempotencyKeyStableAcrossMetaOrder(t *testing.T) {
	key1 := DeriveIdempotencyKey("session-1", "build", "ship it", "/repo", "hybrid", map[string]string{
		"parent_task_id": "t-1",
		"agent_id":       "claude",
	})
	key2 := DeriveIdempotencyKey("session-1", "build", "ship it", "/repo", "hybrid", map[string]string{
		"agent_id":       "claude",
		"parent_task_id": "t-1",
	})
	if key1 != key2 {
		t.Fatalf("keys differ across stable meta ordering: %q vs %q", key1, key2)
	}
}

func TestDeriveIdempotencyKeyIgnoresVolatileMeta(t *testing.T) {
	key1 := DeriveIdempotencyKey("session-1", "build", "ship it", "/repo", "hybrid", map[string]string{
		"resume_checkpoint": "attempt 1",
		"agent_id":          "claude",
	})
	key2 := DeriveIdempotencyKey("session-1", "build", "ship it", "/repo", "hybrid", map[string]string{
		"resume_checkpoint": "attempt 2",
		"agent_id":          "claude",
	})
	if key1 != key2 {
		t.Fatalf("volatile meta changed key: %q vs %q", key1, key2)
	}
}

func TestStableTaskIDAndNextStableSequence(t *testing.T) {
	first := StableTaskID("agent-123", 1)
	if first != "agent-123-000001" {
		t.Fatalf("first id = %q", first)
	}
	next := NextStableSequence("agent-123", []string{"agent-123-000001", "agent-123-000002"})
	if next != 3 {
		t.Fatalf("next sequence = %d want 3", next)
	}
}

func TestClassifyAPIFailure(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		attempt   int
		wantClass APIFailureClass
		retryable bool
		backoff   time.Duration
	}{
		{
			name:      "transient timeout",
			err:       fmt.Errorf("openai request timed out after 30s"),
			attempt:   2,
			wantClass: APIFailureTransientTimeout,
			retryable: true,
			backoff:   4 * time.Second,
		},
		{
			name:      "rate limit",
			err:       fmt.Errorf("429 rate limit exceeded"),
			attempt:   3,
			wantClass: APIFailureTransientRate,
			retryable: true,
			backoff:   20 * time.Second,
		},
		{
			name:      "permanent auth",
			err:       fmt.Errorf("unauthorized: invalid api key"),
			attempt:   1,
			wantClass: APIFailurePermanentAuth,
			retryable: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyAPIFailure(tc.err, tc.attempt)
			if got.Class != tc.wantClass {
				t.Fatalf("class = %s want %s", got.Class, tc.wantClass)
			}
			if got.Retryable != tc.retryable {
				t.Fatalf("retryable = %v want %v", got.Retryable, tc.retryable)
			}
			if got.Backoff != tc.backoff {
				t.Fatalf("backoff = %s want %s", got.Backoff, tc.backoff)
			}
		})
	}
}

func TestComputeBackpressure(t *testing.T) {
	tests := []struct {
		name      string
		snapshot  BackpressureSnapshot
		saturated bool
		delay     time.Duration
	}{
		{
			name:      "idle queue",
			snapshot:  BackpressureSnapshot{Active: 0, Capacity: 4, Queued: 0},
			saturated: false,
			delay:     0,
		},
		{
			name:      "near saturation",
			snapshot:  BackpressureSnapshot{Active: 3, Capacity: 4, Queued: 5},
			saturated: true,
			delay:     75 * time.Millisecond,
		},
		{
			name:      "hard saturation",
			snapshot:  BackpressureSnapshot{Active: 4, Capacity: 4, Queued: 12},
			saturated: true,
			delay:     150 * time.Millisecond,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputeBackpressure(tc.snapshot)
			if got.Saturated != tc.saturated {
				t.Fatalf("saturated = %v want %v", got.Saturated, tc.saturated)
			}
			if got.Delay != tc.delay {
				t.Fatalf("delay = %s want %s", got.Delay, tc.delay)
			}
		})
	}
}

func TestDetectPartialState(t *testing.T) {
	events := []RecoveryEvent{
		{TaskID: "t-1", Type: "start", WorkerID: "w-1", Message: "attempt 1", Evidence: map[string]string{"attempt": "1"}},
		{TaskID: "t-1", Type: "retry", WorkerID: "w-1", Message: "resume from write proofs", Evidence: map[string]string{"attempt": "1", "resume_checkpoint": "write proofs"}},
		{TaskID: "t-2", Type: "start", WorkerID: "w-2", Message: "attempt 1"},
		{TaskID: "t-2", Type: "done", WorkerID: "w-2", Message: "completed"},
	}
	got := DetectPartialState(events)
	if len(got) != 1 {
		t.Fatalf("checkpoints len = %d want 1", len(got))
	}
	cp := got["t-1"]
	if cp.ResumeFrom != "write proofs" {
		t.Fatalf("resume checkpoint = %q", cp.ResumeFrom)
	}
	if cp.Attempt != 1 {
		t.Fatalf("attempt = %d want 1", cp.Attempt)
	}
}
