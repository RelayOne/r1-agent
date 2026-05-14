package auth

// state_store.go — short-lived "state" entries for the SSO redirect
// cycle. Stores the PKCE verifier, nonce, post-login destination, and a
// client-binding hash for at most ~10 minutes. Take() is one-shot
// (deletes on read) so a replayed callback fails closed.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// StateEntry is the per-state cargo held during the redirect cycle.
type StateEntry struct {
	State        string
	CodeVerifier string
	Nonce        string
	// Next is the post-login destination path; must start with "/" and
	// must not start with "//".
	Next string
	// ClientBindingHash is sha256(ip + ":" + ua)[:8] in hex. Verified
	// on callback to defeat trivial state-cookie theft from a different
	// device.
	ClientBindingHash string
	CreatedAt         time.Time
}

// ErrStateNotFound is returned by Take when the state is missing or
// already consumed. The HTTP layer maps this to 400 state_consumed /
// state_mismatch — the wording difference doesn't matter to callers
// since both indicate the same failure mode.
var ErrStateNotFound = errors.New("auth state: not found or already consumed")

// StateStore is the interface the callback path depends on.
type StateStore interface {
	// Put stores the entry. Implementations MUST sweep entries older
	// than 10 minutes; the InMemoryStateStore does this with a
	// background goroutine started on first Put.
	Put(entry StateEntry) error

	// Take is one-shot: returns the entry then deletes it. Replays
	// return ErrStateNotFound.
	Take(state string) (StateEntry, error)
}

// ComputeClientBindingHash returns sha256(ip + ":" + ua)[:8] hex.
// Centralized so Put/Take callers cannot disagree about the input
// shape. Exported because the HTTP handlers and tests both need it.
func ComputeClientBindingHash(ip, ua string) string {
	h := sha256.Sum256([]byte(ip + ":" + ua))
	return hex.EncodeToString(h[:8])
}

// InMemoryStateStore is the default backend. Entries expire after
// stateTTL; a background sweeper trims expired entries every
// sweepInterval. Safe for concurrent Put/Take from any goroutine.
type InMemoryStateStore struct {
	mu       sync.Mutex
	m        map[string]StateEntry
	ttl      time.Duration
	sweepOK  bool
	stopCh   chan struct{}
	clock    func() time.Time
}

// NewInMemoryStateStore returns a store with the default 10-minute TTL.
func NewInMemoryStateStore() *InMemoryStateStore {
	return NewInMemoryStateStoreWithTTL(10 * time.Minute)
}

// NewInMemoryStateStoreWithTTL allows tests to use a tight TTL.
func NewInMemoryStateStoreWithTTL(ttl time.Duration) *InMemoryStateStore {
	return &InMemoryStateStore{
		m:      map[string]StateEntry{},
		ttl:    ttl,
		stopCh: make(chan struct{}),
		clock:  time.Now,
	}
}

// SetClock injects a deterministic clock for tests.
func (s *InMemoryStateStore) SetClock(fn func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if fn != nil {
		s.clock = fn
	}
}

// Put records the entry. The sweeper goroutine starts lazily on first
// Put so a never-used StateStore doesn't leak a goroutine.
func (s *InMemoryStateStore) Put(entry StateEntry) error {
	if entry.State == "" {
		return errors.New("auth state: empty state")
	}
	s.mu.Lock()
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = s.clock()
	}
	s.m[entry.State] = entry
	started := s.sweepOK
	s.sweepOK = true
	s.mu.Unlock()
	if !started {
		go s.sweepLoop()
	}
	return nil
}

// Take returns the entry and deletes it. Missing or expired returns
// ErrStateNotFound — the caller maps to 400.
func (s *InMemoryStateStore) Take(state string) (StateEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[state]
	if !ok {
		return StateEntry{}, ErrStateNotFound
	}
	delete(s.m, state)
	if !e.CreatedAt.IsZero() && s.clock().Sub(e.CreatedAt) > s.ttl {
		return StateEntry{}, ErrStateNotFound
	}
	return e, nil
}

// Stop signals the sweeper goroutine. Tests call this in t.Cleanup.
func (s *InMemoryStateStore) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sweepOK {
		close(s.stopCh)
		s.sweepOK = false
	}
}

func (s *InMemoryStateStore) sweepLoop() {
	// Sweep every (ttl/4) so a stuck entry is gone within ttl + ttl/4.
	interval := s.ttl / 4
	if interval < time.Second {
		interval = time.Second
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-tick.C:
			s.sweep()
		}
	}
}

func (s *InMemoryStateStore) sweep() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	for k, e := range s.m {
		if e.CreatedAt.IsZero() {
			continue
		}
		if now.Sub(e.CreatedAt) > s.ttl {
			delete(s.m, k)
		}
	}
}
