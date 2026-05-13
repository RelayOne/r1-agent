//go:build browserless_live

// integration_test.go: live Browserless cloud test (spec C6 T13a).
//
// Build-tag-gated so the default `go test ./...` stays green for
// contributors without a Browserless account. CI's nightly bench
// cron sets BROWSERLESS_TEST_TOKEN and the build tag together.

package browserless

import (
	"context"
	"os"
	"testing"

	"github.com/RelayOne/r1/internal/browser"
)

// TestLive_Conformance runs the shared ProviderConformance suite
// against the real wss://chrome.browserless.io. Requires
// BROWSERLESS_TEST_TOKEN; skips otherwise.
func TestLive_Conformance(t *testing.T) {
	tok := os.Getenv("BROWSERLESS_TEST_TOKEN")
	if tok == "" {
		t.Skip("BROWSERLESS_TEST_TOKEN not set; skipping live Browserless test")
	}
	browser.ProviderConformance(t, func() (browser.Provider, error) {
		return NewClient(Config{
			Endpoint: "wss://chrome.browserless.io",
			Token:    tok,
		})
	})
}

// TestLive_NavigateExample runs a single end-to-end navigate against
// example.com — a smoke check the nightly cron can grep on for "did
// the Browserless cloud at all work today".
func TestLive_NavigateExample(t *testing.T) {
	tok := os.Getenv("BROWSERLESS_TEST_TOKEN")
	if tok == "" {
		t.Skip("BROWSERLESS_TEST_TOKEN not set; skipping live Browserless test")
	}
	c, err := NewClient(Config{Endpoint: "wss://chrome.browserless.io", Token: tok})
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
