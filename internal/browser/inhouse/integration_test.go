//go:build inhouse_live

// integration_test.go: live in-house container test (spec C6 T13b).
//
// Build-tag-gated. The nightly bench cron starts the r1-browser
// Docker image via testcontainers-go (when available) or expects the
// caller to have a running container at INHOUSE_LIVE_ENDPOINT and an
// ID token at INHOUSE_LIVE_TOKEN.

package inhouse

import (
	"context"
	"os"
	"testing"

	"github.com/RelayOne/r1/internal/browser"
)

// TestLive_Conformance runs the shared suite against a real local
// r1-browser container.
func TestLive_Conformance(t *testing.T) {
	endpoint := os.Getenv("INHOUSE_LIVE_ENDPOINT")
	tok := os.Getenv("INHOUSE_LIVE_TOKEN")
	if endpoint == "" {
		t.Skip("INHOUSE_LIVE_ENDPOINT not set; skipping inhouse live test")
	}
	browser.ProviderConformance(t, func() (browser.Provider, error) {
		return NewClient(Config{
			Endpoint:      endpoint,
			AllowInsecure: true,
			TokenMinter:   &StubMinter{Token: tok},
		})
	})
}

// TestLive_NavigateExample is a single smoke navigation against the
// running container.
func TestLive_NavigateExample(t *testing.T) {
	endpoint := os.Getenv("INHOUSE_LIVE_ENDPOINT")
	tok := os.Getenv("INHOUSE_LIVE_TOKEN")
	if endpoint == "" {
		t.Skip("INHOUSE_LIVE_ENDPOINT not set")
	}
	c, err := NewClient(Config{Endpoint: endpoint, AllowInsecure: true, TokenMinter: &StubMinter{Token: tok}})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer c.Close(context.Background())
	s, err := c.Open(context.Background(), browser.SessionOpts{
		TenantID: "live-smoke",
		NetworkPolicy: browser.NetworkPolicy{
			Mode:  browser.PolicyAllowByDefault,
			Allow: []string{"example.com"},
		},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close(context.Background())
	nr, err := s.Navigate(context.Background(), "https://example.com/")
	if err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if nr.Status < 200 || nr.Status >= 400 {
		t.Errorf("Status = %d, want 2xx-3xx", nr.Status)
	}
}
