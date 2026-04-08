package dispatch

import (
	"fmt"
	"testing"
)

func TestEnqueueAndProcess(t *testing.T) {
	delivered := make(map[string]bool)
	q := NewQueue(func(msg *Message) error {
		delivered[msg.ID] = true
		return nil
	}, DefaultConfig())

	id, ok := q.Enqueue("task.complete", "worker-1", PriorityNormal, map[string]any{"task": "t1"}, "key-1")
	if !ok || id == "" {
		t.Fatal("expected enqueue success")
	}

	count := q.Process()
	if count != 1 {
		t.Errorf("expected 1 delivered, got %d", count)
	}
	if !delivered[id] {
		t.Error("message should have been delivered")
	}

	msg := q.Get(id)
	if msg.Status != StatusDelivered {
		t.Errorf("expected delivered, got %s", msg.Status)
	}
}

func TestDeduplication(t *testing.T) {
	q := NewQueue(func(msg *Message) error { return nil }, DefaultConfig())

	_, ok1 := q.Enqueue("topic", "r", PriorityNormal, nil, "same-key")
	_, ok2 := q.Enqueue("topic", "r", PriorityNormal, nil, "same-key")

	if !ok1 {
		t.Error("first enqueue should succeed")
	}
	if ok2 {
		t.Error("duplicate should be rejected")
	}
}

func TestRetryOnFailure(t *testing.T) {
	attempts := 0
	q := NewQueue(func(msg *Message) error {
		attempts++
		if attempts < 3 {
			return fmt.Errorf("transient error")
		}
		return nil
	}, Config{
		MaxAttempts:   5,
		BaseBackoff:   0, // no delay for test
		MaxBackoff:    0,
		BackoffFactor: 1.0,
	})

	q.Enqueue("topic", "r", PriorityNormal, nil, "retry-key")

	// First attempt fails
	q.Process()
	msg := q.Get("msg-1")
	if msg.Status != StatusFailed {
		t.Errorf("expected failed, got %s", msg.Status)
	}

	// Second attempt fails
	q.Process()

	// Third attempt succeeds
	q.Process()
	msg = q.Get("msg-1")
	if msg.Status != StatusDelivered {
		t.Errorf("expected delivered after retry, got %s", msg.Status)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

func TestExpiredAfterMaxAttempts(t *testing.T) {
	q := NewQueue(func(msg *Message) error {
		return fmt.Errorf("always fails")
	}, Config{
		MaxAttempts:   2,
		BaseBackoff:   0,
		MaxBackoff:    0,
		BackoffFactor: 1.0,
	})

	q.Enqueue("topic", "r", PriorityNormal, nil, "expire-key")
	q.Process()
	q.Process()

	msg := q.Get("msg-1")
	if msg.Status != StatusExpired {
		t.Errorf("expected expired, got %s", msg.Status)
	}
}

func TestPriorityOrder(t *testing.T) {
	var order []Priority
	q := NewQueue(func(msg *Message) error {
		order = append(order, msg.Priority)
		return nil
	}, DefaultConfig())

	q.Enqueue("topic", "r", PriorityLow, nil, "k1")
	q.Enqueue("topic", "r", PriorityCritical, nil, "k2")
	q.Enqueue("topic", "r", PriorityHigh, nil, "k3")

	q.Process()

	if len(order) != 3 {
		t.Fatalf("expected 3, got %d", len(order))
	}
	if order[0] != PriorityCritical || order[1] != PriorityHigh || order[2] != PriorityLow {
		t.Errorf("expected critical,high,low order, got %v", order)
	}
}

func TestStats(t *testing.T) {
	q := NewQueue(func(msg *Message) error { return nil }, DefaultConfig())
	q.Enqueue("topic", "r", PriorityNormal, nil, "s1")
	q.Enqueue("topic", "r", PriorityNormal, nil, "s2")

	stats := q.Stats()
	if stats.Pending != 2 {
		t.Errorf("expected 2 pending, got %d", stats.Pending)
	}

	q.Process()
	stats = q.Stats()
	if stats.Delivered != 2 {
		t.Errorf("expected 2 delivered, got %d", stats.Delivered)
	}
}

func TestPurge(t *testing.T) {
	q := NewQueue(func(msg *Message) error { return nil }, DefaultConfig())
	q.Enqueue("topic", "r", PriorityNormal, nil, "p1")
	q.Process()

	purged := q.Purge(0)
	if purged != 1 {
		t.Errorf("expected 1 purged, got %d", purged)
	}
	if len(q.messages) != 0 {
		t.Error("messages should be empty after purge")
	}
}

func TestEmptyKeyNoDedupe(t *testing.T) {
	q := NewQueue(func(msg *Message) error { return nil }, DefaultConfig())
	_, ok1 := q.Enqueue("topic", "r", PriorityNormal, nil, "")
	_, ok2 := q.Enqueue("topic", "r", PriorityNormal, nil, "")

	if !ok1 || !ok2 {
		t.Error("empty keys should not deduplicate")
	}
}
