package server

// admin_antitrunc_buffer.go — A5 admin panel anti-truncation event
// ring buffer. Process-local Phase-1 surface; documented limitation
// in docs/operations/admin-panel.md.
//
// Spec: specs/admin-panel.md §10 task 27.

import (
	"sort"
	"sync"
	"time"
)

// AntiTruncEvent is the shape the admin panel renders. Mirrors the
// fields in specs/admin-panel.md §4.3 with one rename: TriggerPhrase
// stays a string because the upstream bus carries it as a string
// (the antitrunc package owns the phrase taxonomy itself).
type AntiTruncEvent struct {
	EventID         string    `json:"event_id"`
	SessionID       string    `json:"session_id"`
	TriggerPhrase   string    `json:"trigger_phrase"`
	OutcomeNodeCID  string    `json:"outcome_node_cid"`
	Timestamp       time.Time `json:"timestamp"`
}

// AntiTruncBuffer is a fixed-capacity ring of the most recent
// AntiTruncEvents. Safe for concurrent Publish / Snapshot calls; the
// underlying lock is a sync.RWMutex so paginated snapshots don't block
// each other.
type AntiTruncBuffer struct {
	mu   sync.RWMutex
	cap  int
	buf  []AntiTruncEvent
	head int  // next write index
	full bool // true once the ring has wrapped at least once
}

// DefaultAntiTruncBufferSize is the spec-mandated capacity (1000 most
// recent events). Tests construct smaller buffers via NewAntiTruncBuffer.
const DefaultAntiTruncBufferSize = 1000

// NewAntiTruncBuffer returns a buffer with the given capacity. Capacity
// < 1 is treated as the default; this keeps callers safe from a zero
// value misconfiguration.
func NewAntiTruncBuffer(capacity int) *AntiTruncBuffer {
	if capacity < 1 {
		capacity = DefaultAntiTruncBufferSize
	}
	return &AntiTruncBuffer{
		cap: capacity,
		buf: make([]AntiTruncEvent, capacity),
	}
}

// Publish records a new event. The oldest event is dropped when the
// ring is full. Safe to call from any goroutine.
func (b *AntiTruncBuffer) Publish(ev AntiTruncEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf[b.head] = ev
	b.head++
	if b.head >= b.cap {
		b.head = 0
		b.full = true
	}
}

// Len returns the number of events currently held (<= capacity).
func (b *AntiTruncBuffer) Len() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.full {
		return b.cap
	}
	return b.head
}

// Cap returns the configured capacity.
func (b *AntiTruncBuffer) Cap() int { return b.cap }

// Snapshot returns a copy of every event in chronological order
// (oldest first). The returned slice is owned by the caller — callers
// may sort, filter, or mutate it without affecting the buffer.
func (b *AntiTruncBuffer) Snapshot() []AntiTruncEvent {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if !b.full {
		out := make([]AntiTruncEvent, b.head)
		copy(out, b.buf[:b.head])
		return out
	}
	out := make([]AntiTruncEvent, b.cap)
	// Older half lives at [head : cap]; newer half at [0 : head].
	n := copy(out, b.buf[b.head:b.cap])
	copy(out[n:], b.buf[:b.head])
	return out
}

// SnapshotRecent returns the most recent N events (newest first).
// When n exceeds the buffer's current length, all events are returned.
func (b *AntiTruncBuffer) SnapshotRecent(n int) []AntiTruncEvent {
	all := b.Snapshot()
	// Reverse to put newest first.
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}
	if n > 0 && n < len(all) {
		all = all[:n]
	}
	return all
}

// SortedByTimestamp returns the events sorted descending by timestamp.
// Used by the admin handler so paginated rows are stable even when
// the bus delivers events out of order under load.
func (b *AntiTruncBuffer) SortedByTimestamp() []AntiTruncEvent {
	all := b.Snapshot()
	sort.Slice(all, func(i, j int) bool {
		return all[i].Timestamp.After(all[j].Timestamp)
	})
	return all
}
