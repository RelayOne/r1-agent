package sessionctl

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/RelayOne/r1-agent/internal/operator"
)

// Decision captures an operator's answer to an Ask.
type Decision struct {
	AskID     string    // must match the Register call
	Choice    string    // "yes" / "no" / option Label / free-text
	Reason    string
	Actor     string    // "cli:socket" | "cli:term" | "cloudswarm:stdin"
	Timestamp time.Time
}

// AskEntry is one open Ask.
type AskEntry struct {
	AskID    string
	OpenedAt time.Time
	Ch       chan Decision
}

// ApprovalRouter owns all open asks.
type ApprovalRouter struct {
	mu   sync.Mutex
	open map[string]*AskEntry
}

// NewApprovalRouter constructs an empty router.
func NewApprovalRouter() *ApprovalRouter {
	return &ApprovalRouter{open: make(map[string]*AskEntry)}
}

// Errors.
var (
	ErrAskAlreadyRegistered = errors.New("sessionctl: ask_id already registered")
	ErrAskUnknown           = errors.New("sessionctl: ask_id no longer open")
)

// Register adds a new ask_id. Returns a channel that receives at most
// one Decision. If timeout is non-zero, the channel is closed after
// that duration and a typed "timeout" decision is delivered.
// Re-registering an existing ask_id returns an error.
func (r *ApprovalRouter) Register(askID string, timeout time.Duration) (<-chan Decision, error) {
	r.mu.Lock()
	if _, exists := r.open[askID]; exists {
		r.mu.Unlock()
		return nil, ErrAskAlreadyRegistered
	}
	ch := make(chan Decision, 1)
	entry := &AskEntry{
		AskID:    askID,
		OpenedAt: time.Now(),
		Ch:       ch,
	}
	r.open[askID] = entry
	r.mu.Unlock()

	if timeout > 0 {
		go func() {
			time.Sleep(timeout)
			// Idempotent: if already resolved, Resolve returns
			// ErrAskUnknown and we ignore it.
			_ = r.Resolve(askID, Decision{
				AskID:     askID,
				Choice:    "timeout",
				Actor:     "timer",
				Timestamp: time.Now(),
			})
		}()
	}

	return ch, nil
}

// Resolve delivers the decision to the waiter and removes the ask_id
// from the open set. Returns an error if ask_id is unknown or already
// resolved (second caller for the same ask_id).
func (r *ApprovalRouter) Resolve(askID string, d Decision) error {
	r.mu.Lock()
	entry, ok := r.open[askID]
	if !ok {
		r.mu.Unlock()
		return ErrAskUnknown
	}
	delete(r.open, askID)
	r.mu.Unlock()

	// Non-blocking send: channel is buffered cap=1 and we are the only
	// writer for this entry (map delete under mutex prevents duplicate
	// resolves from reaching this line).
	entry.Ch <- d
	close(entry.Ch)
	return nil
}

// OldestOpen returns the ask_id with the earliest OpenedAt, or ""
// if none are open. Used by `stoke approve` when --approval-id is
// omitted.
func (r *ApprovalRouter) OldestOpen() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	var (
		oldestID string
		oldestAt time.Time
		first    = true
	)
	for id, entry := range r.open {
		if first || entry.OpenedAt.Before(oldestAt) {
			oldestID = id
			oldestAt = entry.OpenedAt
			first = false
		}
	}
	return oldestID
}

// AskThroughRouter registers an ask_id, emits the prompt via op.Ask in
// a goroutine, and returns a channel that receives the first Decision
// from ANY source (socket approval, terminal op, timeout).
//
// Usage:
//
//	ch, askID, err := router.AskThroughRouter(ctx, terminalOp, prompt, options, 5*time.Minute)
//	decision := <-ch
//
// The goroutine that calls op.Ask exits silently if the router already
// resolved the ask_id via another source (e.g. an operator typed
// `stoke approve` on the socket before the terminal prompt returned).
func (r *ApprovalRouter) AskThroughRouter(
	ctx context.Context,
	op operator.Operator,
	prompt string,
	options []operator.Option,
	timeout time.Duration,
) (<-chan Decision, string, error) {
	askID, err := newAskID()
	if err != nil {
		return nil, "", err
	}
	ch, err := r.Register(askID, timeout)
	if err != nil {
		return nil, "", err
	}

	go func() {
		label, askErr := op.Ask(ctx, prompt, options)
		if askErr != nil || label == "" {
			return
		}
		// Idempotent: if the router already resolved (socket or timer),
		// Resolve returns ErrAskUnknown and we drop the terminal answer.
		_ = r.Resolve(askID, Decision{
			AskID:     askID,
			Choice:    label,
			Actor:     "cli:term",
			Timestamp: time.Now(),
		})
	}()

	return ch, askID, nil
}

// newAskID returns a 12-hex-character ask_id from crypto/rand.
func newAskID() (string, error) {
	var buf [6]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

// List returns a snapshot of open ask IDs (sorted by OpenedAt asc).
func (r *ApprovalRouter) List() []string {
	r.mu.Lock()
	entries := make([]*AskEntry, 0, len(r.open))
	for _, e := range r.open {
		entries = append(entries, e)
	}
	r.mu.Unlock()

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].OpenedAt.Before(entries[j].OpenedAt)
	})
	ids := make([]string, len(entries))
	for i, e := range entries {
		ids[i] = e.AskID
	}
	return ids
}
