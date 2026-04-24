package hub

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// ChainedAuditEntry is a single entry in the tamper-evident audit log.
type ChainedAuditEntry struct {
	Sequence  uint64    `json:"seq"`
	Timestamp time.Time `json:"ts"`
	EventType EventType `json:"event"`
	Action    string    `json:"action"`            // "allow", "deny", "error"
	RuleID    string    `json:"rule_id"`           // which subscriber decided
	Hash      string    `json:"hash"`              // SHA-256 of this entry
	PrevHash  string    `json:"prev"`              // hash of previous entry (chain)
	Details   string    `json:"details,omitempty"` // optional details
}

// computeHash calculates the SHA-256 hash for this entry based on its fields
// and the previous entry's hash, forming the chain.
func (e *ChainedAuditEntry) computeHash() string {
	h := sha256.New()
	fmt.Fprintf(h, "%d|%s|%s|%s|%s|%s|%s",
		e.Sequence,
		e.Timestamp.UTC().Format(time.RFC3339Nano),
		e.EventType,
		e.Action,
		e.RuleID,
		e.PrevHash,
		e.Details,
	)
	return hex.EncodeToString(h.Sum(nil))
}

// ChainedAuditLog maintains a hash-chained append-only audit trail.
type ChainedAuditLog struct {
	mu       sync.Mutex
	entries  []ChainedAuditEntry
	nextSeq  uint64
	lastHash string
}

// NewChainedAuditLog creates a hash-chained audit log.
func NewChainedAuditLog() *ChainedAuditLog {
	return &ChainedAuditLog{}
}

// Append adds an entry, computing the hash chain. The Sequence, Hash, and
// PrevHash fields are set automatically; callers should populate EventType,
// Action, RuleID, Timestamp, and Details.
func (a *ChainedAuditLog) Append(entry ChainedAuditEntry) {
	a.mu.Lock()
	defer a.mu.Unlock()

	entry.Sequence = a.nextSeq
	entry.PrevHash = a.lastHash
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now()
	}
	entry.Hash = entry.computeHash()

	a.entries = append(a.entries, entry)
	a.lastHash = entry.Hash
	a.nextSeq++
}

// Verify checks the entire chain for tampering. It recomputes every hash
// and validates the prev-hash linkage. Returns nil if the chain is intact.
func (a *ChainedAuditLog) Verify() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	prevHash := ""
	var seqIdx uint64
	for i, entry := range a.entries {
		if entry.Sequence != seqIdx {
			return fmt.Errorf("audit chain broken at index %d: expected seq %d, got %d", i, i, entry.Sequence)
		}
		seqIdx++
		if entry.PrevHash != prevHash {
			return fmt.Errorf("audit chain broken at seq %d: prev hash mismatch", entry.Sequence)
		}
		expected := entry.computeHash()
		if entry.Hash != expected {
			return fmt.Errorf("audit chain tampered at seq %d: hash mismatch", entry.Sequence)
		}
		prevHash = entry.Hash
	}
	return nil
}

// Entries returns all entries as a read-only copy.
func (a *ChainedAuditLog) Entries() []ChainedAuditEntry {
	a.mu.Lock()
	defer a.mu.Unlock()

	out := make([]ChainedAuditEntry, len(a.entries))
	copy(out, a.entries)
	return out
}

// Since returns entries after the given sequence number.
func (a *ChainedAuditLog) Since(seq uint64) []ChainedAuditEntry {
	a.mu.Lock()
	defer a.mu.Unlock()

	var out []ChainedAuditEntry
	for _, entry := range a.entries {
		if entry.Sequence > seq {
			out = append(out, entry)
		}
	}
	return out
}
