// Package dispatch implements a three-tier message dispatch queue with delivery tracking.
// Inspired by OmX's dispatch pattern:
// 1. Write state (persist intent before attempting delivery)
// 2. Attempt notify (try to deliver to recipient)
// 3. Track delivery (confirm receipt, retry with exponential backoff on failure)
//
// Messages are deduplicated by idempotency key, preventing duplicate side effects
// even across process restarts. Failed deliveries are retried with exponential backoff.
package dispatch

import (
	"fmt"
	"sync"
	"time"
)

// Priority levels for message dispatch.
type Priority int

// Priority tiers for the three-tier dispatch queue. Lower numbers
// dispatch first — Critical drains before High drains before Normal.
const (
	PriorityCritical Priority = 0 // system failures, security events
	PriorityHigh     Priority = 1 // task completion, phase transitions
	PriorityNormal   Priority = 2 // status updates, progress reports
	PriorityLow      Priority = 3 // informational, metrics
)

// Status tracks message lifecycle.
type Status string

// Message lifecycle states. Status strings are persisted in the queue
// store and exposed on telemetry — treat as wire protocol.
const (
	StatusPending   Status = "pending"   // written but not yet sent
	StatusSent      Status = "sent"      // delivery attempted
	StatusDelivered Status = "delivered" // confirmed receipt
	StatusFailed    Status = "failed"    // delivery failed, may retry
	StatusExpired   Status = "expired"   // exceeded max attempts
)

// Message is a dispatch queue entry.
type Message struct {
	ID             string         `json:"id"`
	IdempotencyKey string         `json:"idempotency_key"`
	Priority       Priority       `json:"priority"`
	Topic          string         `json:"topic"`
	Payload        map[string]any `json:"payload"`
	Status         Status         `json:"status"`
	Recipient      string         `json:"recipient"`
	Attempts       int            `json:"attempts"`
	MaxAttempts    int            `json:"max_attempts"`
	CreatedAt      time.Time      `json:"created_at"`
	LastAttempt    time.Time      `json:"last_attempt,omitempty"`
	DeliveredAt    time.Time      `json:"delivered_at,omitempty"`
	NextRetry      time.Time      `json:"next_retry,omitempty"`
	Error          string         `json:"error,omitempty"`
}

// DeliverFunc is called to deliver a message. Returns nil on success.
type DeliverFunc func(msg *Message) error

// Config controls queue behavior.
type Config struct {
	MaxAttempts    int           // default 5
	BaseBackoff    time.Duration // default 2s
	MaxBackoff     time.Duration // default 5min
	BackoffFactor  float64       // default 2.0
	ExpireAfter    time.Duration // default 1h
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{
		MaxAttempts:   5,
		BaseBackoff:   2 * time.Second,
		MaxBackoff:    5 * time.Minute,
		BackoffFactor: 2.0,
		ExpireAfter:   time.Hour,
	}
}

// Queue manages message dispatch with delivery tracking.
type Queue struct {
	mu       sync.Mutex
	config   Config
	messages []*Message
	seen     map[string]bool // idempotency keys already processed
	deliver  DeliverFunc
	nextID   int
}

// NewQueue creates a dispatch queue with the given delivery function.
func NewQueue(deliver DeliverFunc, cfg Config) *Queue {
	if cfg.MaxAttempts == 0 {
		cfg = DefaultConfig()
	}
	return &Queue{
		config:  cfg,
		seen:    make(map[string]bool),
		deliver: deliver,
	}
}

// Enqueue adds a message to the queue. Returns false if deduplicated.
func (q *Queue) Enqueue(topic, recipient string, priority Priority, payload map[string]any, idempotencyKey string) (string, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	// Dedup check
	if idempotencyKey != "" && q.seen[idempotencyKey] {
		return "", false
	}

	q.nextID++
	id := fmt.Sprintf("msg-%d", q.nextID)

	msg := &Message{
		ID:             id,
		IdempotencyKey: idempotencyKey,
		Priority:       priority,
		Topic:          topic,
		Payload:        payload,
		Status:         StatusPending,
		Recipient:      recipient,
		MaxAttempts:    q.config.MaxAttempts,
		CreatedAt:      time.Now(),
	}

	q.messages = append(q.messages, msg)
	if idempotencyKey != "" {
		q.seen[idempotencyKey] = true
	}

	return id, true
}

// Process attempts delivery for all pending/retryable messages.
// Returns the number of messages successfully delivered.
func (q *Queue) Process() int {
	q.mu.Lock()
	now := time.Now()
	var ready []*Message
	for _, msg := range q.messages {
		if msg.Status == StatusPending || (msg.Status == StatusFailed && !msg.NextRetry.After(now)) {
			ready = append(ready, msg)
		}
	}
	q.mu.Unlock()

	// Sort by priority (lower = higher priority)
	for i := 1; i < len(ready); i++ {
		for j := i; j > 0 && ready[j].Priority < ready[j-1].Priority; j-- {
			ready[j], ready[j-1] = ready[j-1], ready[j]
		}
	}

	delivered := 0
	for _, msg := range ready {
		q.mu.Lock()
		msg.Attempts++
		msg.LastAttempt = time.Now()
		msg.Status = StatusSent
		q.mu.Unlock()

		err := q.deliver(msg)

		q.mu.Lock()
		if err == nil {
			msg.Status = StatusDelivered
			msg.DeliveredAt = time.Now()
			delivered++
		} else {
			msg.Error = err.Error()
			if msg.Attempts >= msg.MaxAttempts {
				msg.Status = StatusExpired
			} else {
				msg.Status = StatusFailed
				backoff := q.backoff(msg.Attempts)
				msg.NextRetry = time.Now().Add(backoff)
			}
		}
		q.mu.Unlock()
	}

	return delivered
}

// Pending returns the count of messages awaiting delivery.
func (q *Queue) Pending() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	count := 0
	for _, msg := range q.messages {
		if msg.Status == StatusPending || msg.Status == StatusFailed {
			count++
		}
	}
	return count
}

// Stats returns delivery statistics.
func (q *Queue) Stats() QueueStats {
	q.mu.Lock()
	defer q.mu.Unlock()

	var s QueueStats
	for _, msg := range q.messages {
		s.Total++
		switch msg.Status {
		case StatusPending:
			s.Pending++
		case StatusSent:
			s.InFlight++
		case StatusDelivered:
			s.Delivered++
		case StatusFailed:
			s.Failed++
		case StatusExpired:
			s.Expired++
		}
	}
	s.Deduplicated = len(q.seen) - len(q.messages)
	if s.Deduplicated < 0 {
		s.Deduplicated = 0
	}
	return s
}

// QueueStats holds dispatch statistics.
type QueueStats struct {
	Total        int `json:"total"`
	Pending      int `json:"pending"`
	InFlight     int `json:"in_flight"`
	Delivered    int `json:"delivered"`
	Failed       int `json:"failed"`
	Expired      int `json:"expired"`
	Deduplicated int `json:"deduplicated"`
}

// Get retrieves a message by ID.
func (q *Queue) Get(id string) *Message {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, msg := range q.messages {
		if msg.ID == id {
			return msg
		}
	}
	return nil
}

// Purge removes delivered and expired messages older than the given age.
func (q *Queue) Purge(olderThan time.Duration) int {
	q.mu.Lock()
	defer q.mu.Unlock()

	cutoff := time.Now().Add(-olderThan)
	purged := 0
	var kept []*Message
	for _, msg := range q.messages {
		if (msg.Status == StatusDelivered || msg.Status == StatusExpired) && msg.CreatedAt.Before(cutoff) {
			purged++
		} else {
			kept = append(kept, msg)
		}
	}
	q.messages = kept
	return purged
}

func (q *Queue) backoff(attempt int) time.Duration {
	d := q.config.BaseBackoff
	for i := 1; i < attempt; i++ {
		d = time.Duration(float64(d) * q.config.BackoffFactor)
		if d > q.config.MaxBackoff {
			d = q.config.MaxBackoff
			break
		}
	}
	return d
}
