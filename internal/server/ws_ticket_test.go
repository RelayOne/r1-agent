package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestWSTicketStore_MintValidateExpire covers the ticket lifecycle:
// a minted ticket validates until its TTL elapses, then expires.
func TestWSTicketStore_MintValidateExpire(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	s := NewWSTicketStore(30 * time.Second)
	s.SetClock(func() time.Time { return now })

	tok, expires := s.Mint()
	if !strings.HasPrefix(tok, "wt-") {
		t.Errorf("token = %q, want wt- prefix", tok)
	}
	if got := expires.Sub(now); got != 30*time.Second {
		t.Errorf("ttl = %v, want 30s", got)
	}
	if !s.Validate(tok) {
		t.Error("fresh ticket rejected")
	}
	if s.Validate("wt-not-a-ticket") {
		t.Error("unknown ticket accepted")
	}
	if s.Validate("") {
		t.Error("empty ticket accepted")
	}

	// Advance past expiry: ticket dies and is pruned.
	now = now.Add(31 * time.Second)
	if s.Validate(tok) {
		t.Error("expired ticket accepted")
	}
	s.mu.Lock()
	remaining := len(s.tickets)
	s.mu.Unlock()
	if remaining != 0 {
		t.Errorf("expired tickets not pruned: %d remain", remaining)
	}
}

// TestWSTicketHandler_MintEndpoint proves the POST /auth/ws-ticket
// response matches the web client's WsTicketSchema ({token, expiresAt}).
func TestWSTicketHandler_MintEndpoint(t *testing.T) {
	s := NewWSTicketStore(0) // default TTL
	h := s.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/auth/ws-ticket", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expiresAt"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Token == "" {
		t.Error("empty token")
	}
	exp, err := time.Parse(time.RFC3339Nano, body.ExpiresAt)
	if err != nil {
		t.Fatalf("expiresAt %q not RFC3339: %v", body.ExpiresAt, err)
	}
	if !exp.After(time.Now()) {
		t.Error("expiresAt in the past")
	}
	if !s.Validate(body.Token) {
		t.Error("minted ticket does not validate")
	}

	// Non-POST → 405.
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest("GET", "/auth/ws-ticket", nil))
	if rec2.Code != 405 {
		t.Errorf("GET status = %d, want 405", rec2.Code)
	}
}
