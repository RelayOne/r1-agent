// Package server — short-lived WebSocket auth tickets (audit A008).
//
// Browser WebSocket constructors cannot set an Authorization header,
// so the web client (web/src/lib/api/auth.ts) mints a short-lived
// ticket via POST /auth/ws-ticket and passes it in the second
// Sec-WebSocket-Protocol slot (["r1.bearer", <ticket>]). The mint
// endpoint itself IS bearer-authenticated — the ticket trades a
// long-lived bearer for a ~30 s token so the durable secret never
// rides the WS subprotocol (which can leak into proxy logs).
//
// Tickets are in-memory only (a daemon restart invalidates them; the
// client's reconnect path re-mints automatically) and multi-use until
// expiry (EventSource-style reconnect storms within the TTL reuse the
// cached ticket per AuthClient's cache contract).
package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// DefaultWSTicketTTL matches the ~30 s ticket lifetime documented in
// specs/web-chat-ui.md §API Client Wrapper.
const DefaultWSTicketTTL = 30 * time.Second

// WSTicketStore mints and validates short-lived WS auth tickets.
// Goroutine-safe. Expired tickets are pruned lazily on each Mint and
// Validate so the map stays bounded without a background sweeper.
type WSTicketStore struct {
	mu      sync.Mutex
	ttl     time.Duration
	nowFn   func() time.Time
	tickets map[string]time.Time // token → expiry
}

// NewWSTicketStore constructs a store. ttl <= 0 uses the default.
func NewWSTicketStore(ttl time.Duration) *WSTicketStore {
	if ttl <= 0 {
		ttl = DefaultWSTicketTTL
	}
	return &WSTicketStore{ttl: ttl, nowFn: time.Now, tickets: map[string]time.Time{}}
}

// SetClock installs an injectable clock (tests only).
func (s *WSTicketStore) SetClock(fn func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if fn != nil {
		s.nowFn = fn
	}
}

// Mint issues a fresh ticket and returns (token, expiresAt).
func (s *WSTicketStore) Mint() (string, time.Time) {
	var b [24]byte
	_, _ = rand.Read(b[:])
	token := "wt-" + hex.EncodeToString(b[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowFn()
	s.pruneLocked(now)
	expires := now.Add(s.ttl)
	s.tickets[token] = expires
	return token, expires
}

// Validate reports whether token is a live (un-expired) ticket. Uses
// a constant-time scan so ticket comparison does not leak prefix
// matches; the map is tiny (bounded by mints within one TTL).
func (s *WSTicketStore) Validate(token string) bool {
	if token == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowFn()
	s.pruneLocked(now)
	for t, exp := range s.tickets {
		if len(t) == len(token) &&
			subtle.ConstantTimeCompare([]byte(t), []byte(token)) == 1 &&
			now.Before(exp) {
			return true
		}
	}
	return false
}

// pruneLocked drops expired tickets. Caller holds s.mu.
func (s *WSTicketStore) pruneLocked(now time.Time) {
	for t, exp := range s.tickets {
		if !now.Before(exp) {
			delete(s.tickets, t)
		}
	}
}

// Handler serves POST /auth/ws-ticket. The response body matches the
// client's WsTicketSchema exactly: {"token": ..., "expiresAt": <RFC3339>}.
// Mount behind RequireBearer — the mint call authenticates with the
// daemon bearer; only the WS upgrade uses the ticket.
func (s *WSTicketStore) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		token, expires := s.Mint()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"token":     token,
			"expiresAt": expires.UTC().Format(time.RFC3339Nano),
		})
	})
}
