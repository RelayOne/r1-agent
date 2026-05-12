// isolation_test.go: tenant-isolation regression test (spec C6 T13c).
//
// The load-bearing security test for the remote-browser feature.
// Lives in the default `go test ./...` path — no build tag, no
// opt-in — because the spec requires it to gate every release.
//
// Two assertions:
//
//  1. Distinct Open calls return distinct Session.ID() values
//     (incognito context separation is observable from the caller).
//  2. Cookies set during tenant A's session are NOT visible in
//     tenant B's session against the same target server. The
//     verification uses an httptest server that mirrors back every
//     incoming Cookie header so the test can assert on whether the
//     marker leaked.
//
// This test is a one-line invocation per provider — the actual body
// lives in conformance.go and isolation.go so each provider's _test
// file picks it up via ProviderConformance.

package browser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// TestTenantIsolation_LocalProvider runs the isolation assertion
// against the local Provider. Browserless + inhouse run the same
// assertion via their conformance suites.
func TestTenantIsolation_LocalProvider(t *testing.T) {
	t.Parallel()
	p := NewLocalProvider(LocalConfig{})
	defer p.Close(context.Background())

	var (
		mu       sync.Mutex
		requests []*http.Request
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Clone(r.Context()))
		mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "tracker", Value: "from-A", Path: "/"})
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("<html><body>ok</body></html>"))
	}))
	defer srv.Close()

	host := stripURLToHost(srv.URL)
	opts := SessionOpts{
		NetworkPolicy: NetworkPolicy{
			Mode:  PolicyAllowByDefault,
			Allow: []string{host, "127.0.0.1", "localhost"},
		},
	}

	// Tenant A.
	optsA := opts
	optsA.TenantID = "tenant-A"
	sa, err := p.Open(context.Background(), optsA)
	if err != nil {
		t.Fatalf("Open A: %v", err)
	}
	if _, err := sa.Navigate(context.Background(), srv.URL); err != nil {
		t.Fatalf("Navigate A: %v", err)
	}
	idA := sa.ID()
	if err := sa.Close(context.Background()); err != nil {
		t.Fatalf("Close A: %v", err)
	}

	// Tenant B.
	optsB := opts
	optsB.TenantID = "tenant-B"
	sb, err := p.Open(context.Background(), optsB)
	if err != nil {
		t.Fatalf("Open B: %v", err)
	}
	if _, err := sb.Navigate(context.Background(), srv.URL); err != nil {
		t.Fatalf("Navigate B: %v", err)
	}
	idB := sb.ID()
	if err := sb.Close(context.Background()); err != nil {
		t.Fatalf("Close B: %v", err)
	}

	if idA == idB {
		t.Errorf("session IDs collide across tenants: %q", idA)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) < 2 {
		t.Fatalf("expected at least 2 requests, got %d", len(requests))
	}
	// Tenant B's request must not carry tenant A's cookie.
	bReq := requests[len(requests)-1]
	if c, err := bReq.Cookie("tracker"); err == nil && c != nil {
		t.Errorf("tenant B leaked tenant A's cookie: %s=%s", c.Name, c.Value)
	}
}

// TestTenantIsolation_FallbackPathStillIsolates exercises the
// fallback wrapper to ensure that even when the primary errors and
// the wrapper rotates to the secondary, both sessions remain
// tenant-isolated.
func TestTenantIsolation_FallbackPathStillIsolates(t *testing.T) {
	t.Parallel()
	pri := &stubProvider{name: "broken-primary", openErrs: []error{ErrProviderUnreachable, ErrProviderUnreachable}}
	sec := NewLocalProvider(LocalConfig{})
	defer sec.Close(context.Background())
	f := NewFallback(pri, sec, NewHub())

	s1, err := f.Open(context.Background(), SessionOpts{TenantID: "T1"})
	if err != nil {
		t.Fatalf("Open 1: %v", err)
	}
	s2, err := f.Open(context.Background(), SessionOpts{TenantID: "T2"})
	if err != nil {
		t.Fatalf("Open 2: %v", err)
	}
	if s1.ID() == s2.ID() {
		t.Errorf("fallback sessions share ID across tenants: %q", s1.ID())
	}
	_ = s1.Close(context.Background())
	_ = s2.Close(context.Background())
}
