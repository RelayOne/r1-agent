package hub

import (
	"sync"
	"time"
)

// AuditEntry records the processing of a single event through the bus.
type AuditEntry struct {
	EventID     string          `json:"event_id"`
	EventType   EventType       `json:"event_type"`
	Timestamp   time.Time       `json:"timestamp"`
	Subscribers []string        `json:"subscribers"`
	Decisions   []AuditDecision `json:"decisions,omitempty"`
	FinalResult Decision        `json:"final_result"`
	Injections  int             `json:"injections"`
	LatencyMs   int64           `json:"latency_ms"`
}

// AuditDecision records one subscriber's decision for an event.
type AuditDecision struct {
	SubscriberID string   `json:"subscriber_id"`
	Decision     Decision `json:"decision"`
	Reason       string   `json:"reason,omitempty"`
}

// AuditLog is a bounded ring buffer of audit entries.
type AuditLog struct {
	mu       sync.Mutex
	entries  []AuditEntry
	capacity int
	pos      int // next write position
	full     bool
}

// NewAuditLog creates a ring buffer audit log with the given capacity.
func NewAuditLog(capacity int) *AuditLog {
	if capacity < 1 {
		capacity = 1000
	}
	return &AuditLog{
		entries:  make([]AuditEntry, capacity),
		capacity: capacity,
	}
}

// Record adds an entry to the audit log.
func (a *AuditLog) Record(entry AuditEntry) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.entries[a.pos] = entry
	a.pos++
	if a.pos >= a.capacity {
		a.pos = 0
		a.full = true
	}
}

// Recent returns the most recent entries, up to limit.
func (a *AuditLog) Recent(limit int) []AuditEntry {
	a.mu.Lock()
	defer a.mu.Unlock()

	count := a.pos
	if a.full {
		count = a.capacity
	}
	if limit > count {
		limit = count
	}
	if limit <= 0 {
		return nil
	}

	result := make([]AuditEntry, limit)
	for i := 0; i < limit; i++ {
		idx := a.pos - 1 - i
		if idx < 0 {
			idx += a.capacity
		}
		result[i] = a.entries[idx]
	}
	return result
}

// Len returns the number of entries currently stored.
func (a *AuditLog) Len() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.full {
		return a.capacity
	}
	return a.pos
}
