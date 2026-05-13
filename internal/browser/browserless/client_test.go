// client_test.go: Browserless client unit + conformance tests.
//
// The conformance call runs against a mock CDP-over-WS server so the
// suite stays green without a Browserless account. The live test
// (browserless_live build tag) lives in integration_test.go.

package browserless

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/browser"
)

// TestClient_Conformance exercises the shared Provider contract
// against the mock CDP server. Identical body runs against the
// inhouse client; the byte-identical conformance is the load-bearing
// property of the Provider abstraction.
func TestClient_Conformance(t *testing.T) {
	t.Parallel()
	browser.ProviderConformance(t, func() (browser.Provider, error) {
		mock := startMockCDP(t)
		return NewClient(Config{
			Endpoint:      mock.wsURL,
			Token:         "test-token-abc",
			AllowInsecure: true, // mock server speaks ws://
		})
	})
}

// TestClient_NetworkPolicy exercises the policy-denial path via the
// shared helper. Browserless gets the same byte-identical assertions
// as local and inhouse.
func TestClient_NetworkPolicy(t *testing.T) {
	t.Parallel()
	browser.NetworkPolicyDenialTest(t, func() (browser.Provider, error) {
		mock := startMockCDP(t)
		return NewClient(Config{
			Endpoint:      mock.wsURL,
			Token:         "test-token-abc",
			AllowInsecure: true,
		})
	})
}

// TestConfigValidate pins the scheme rules — wss:// allowed, ws://
// rejected by default, ws:// permitted with AllowInsecure.
func TestConfigValidate(t *testing.T) {
	t.Parallel()
	if err := (Config{Endpoint: ""}).Validate(); err == nil {
		t.Errorf("empty endpoint should fail")
	}
	if err := (Config{Endpoint: "wss://x.example/"}).Validate(); err != nil {
		t.Errorf("wss should pass: %v", err)
	}
	if err := (Config{Endpoint: "ws://x.example/"}).Validate(); err == nil {
		t.Errorf("ws:// without AllowInsecure should fail")
	}
	if err := (Config{Endpoint: "ws://x.example/", AllowInsecure: true}).Validate(); err != nil {
		t.Errorf("ws:// with AllowInsecure should pass: %v", err)
	}
	if err := (Config{Endpoint: "http://x.example/"}).Validate(); err == nil {
		t.Errorf("http:// should fail")
	}
}

// TestResolveToken covers the precedence chain: explicit > env > file.
func TestResolveToken(t *testing.T) {
	// Cannot run in parallel — modifies process env.
	t.Setenv("BROWSERLESS_TOKEN", "")
	cfg := Config{}
	if got := cfg.ResolveToken(""); got != "" {
		t.Errorf("empty config → empty token; got %q", got)
	}
	cfg.Token = "explicit"
	if got := cfg.ResolveToken(""); got != "explicit" {
		t.Errorf("explicit field wins; got %q", got)
	}
	cfg.Token = ""
	t.Setenv("BROWSERLESS_TOKEN", "env-token")
	if got := cfg.ResolveToken(""); got != "env-token" {
		t.Errorf("env wins over file; got %q", got)
	}
	cfg.TenantTokens = map[string]string{"acme": "tenant-acme"}
	if got := cfg.ResolveToken("acme"); got != "tenant-acme" {
		t.Errorf("tenant token wins over env; got %q", got)
	}
	if got := cfg.ResolveToken("other"); got != "env-token" {
		t.Errorf("unknown tenant falls back to env; got %q", got)
	}
}

// TestClient_NoTokenErrors asserts that an unconfigured tenant
// produces a clear ErrNoToken / ErrNoTenantToken at Open.
func TestClient_NoTokenErrors(t *testing.T) {
	mock := startMockCDP(t)
	c, err := NewClient(Config{
		Endpoint:      mock.wsURL,
		AllowInsecure: true,
		// No Token, no env, no tenant_tokens.
		FileTokenPath: "/nonexistent/r1/browser.json",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Setenv("BROWSERLESS_TOKEN", "")
	_, err = c.Open(context.Background(), browser.SessionOpts{TenantID: "anyone"})
	if !errors.Is(err, browser.ErrNoToken) {
		t.Errorf("Open without token = %v, want ErrNoToken", err)
	}

	// With tenant_tokens map, an unknown tenant gets the more
	// specific ErrNoTenantToken.
	c.cfg.TenantTokens = map[string]string{"acme": "tok"}
	_, err = c.Open(context.Background(), browser.SessionOpts{TenantID: "globex"})
	if !errors.Is(err, browser.ErrNoTenantToken) {
		t.Errorf("unknown tenant = %v, want ErrNoTenantToken", err)
	}
}

// TestClient_AuthFailedTearsDown asserts that a 401/403 dial
// surfaces ErrAuthFailed and tears down the tenant connection.
func TestClient_AuthFailedTearsDown(t *testing.T) {
	mock := startMockCDP(t)
	mock.requireToken = "expected-token"
	c, err := NewClient(Config{
		Endpoint:      mock.wsURL,
		Token:         "wrong-token",
		AllowInsecure: true,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	em := &SliceEmitter{}
	c.WithEmitter(em)

	_, err = c.Open(context.Background(), browser.SessionOpts{TenantID: "acme"})
	if !errors.Is(err, browser.ErrAuthFailed) {
		t.Fatalf("Open with bad token = %v, want ErrAuthFailed", err)
	}
	found := false
	for _, e := range em.Events() {
		if e.Name == EvtTokenInvalid {
			found = true
		}
	}
	if !found {
		t.Errorf("expected EvtTokenInvalid emission, got %+v", em.Events())
	}
}

// TestClient_CostUnits covers the 30s-block billing formula.
func TestClient_CostUnits(t *testing.T) {
	t.Parallel()
	cases := []struct {
		d    time.Duration
		want int
	}{
		{0, 1},
		{1 * time.Second, 1},
		{29 * time.Second, 1},
		{30 * time.Second, 1},
		{31 * time.Second, 2},
		{60 * time.Second, 2},
		{61 * time.Second, 3},
		{45 * time.Second, 2},
	}
	for _, c := range cases {
		if got := costUnits(c.d); got != c.want {
			t.Errorf("costUnits(%s) = %d, want %d", c.d, got, c.want)
		}
	}
}

// TestCostSink_TenantAttribution exercises spec T12's non-negotiable
// rule: every cost record carries the tenant + mission. Three 45s
// sessions across two tenants → 3×2=6 units distributed by tenant.
func TestCostSink_TenantAttribution(t *testing.T) {
	t.Parallel()
	sink := NewInMemoryCostSink()
	sink.RecordSession(browser.ProviderBrowserless, "acme", "m1", 2, 45.0)
	sink.RecordSession(browser.ProviderBrowserless, "acme", "m1", 2, 45.0)
	sink.RecordSession(browser.ProviderBrowserless, "globex", "m2", 2, 45.0)

	got := sink.UnitsByTenant()
	if got["acme"] != 4 || got["globex"] != 2 {
		t.Errorf("UnitsByTenant = %v, want acme=4 globex=2", got)
	}
	rows := sink.Rows()
	if len(rows) != 3 {
		t.Errorf("Rows count = %d, want 3", len(rows))
	}
	for _, r := range rows {
		if r.TenantID == "" || r.MissionID == "" {
			t.Errorf("cost record missing attribution: %+v", r)
		}
	}
}

// TestBudgetExhausted asserts Open fails fast when the configured
// cost sink reports OverBudget=true.
func TestBudgetExhausted(t *testing.T) {
	t.Parallel()
	mock := startMockCDP(t)
	sink := NewInMemoryCostSink()
	sink.SetBudget("mission-X", 0) // already over

	c, err := NewClient(Config{
		Endpoint:      mock.wsURL,
		Token:         "tok",
		AllowInsecure: true,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	c.WithCostSink(sink)

	// Manually push some units to exceed the zero budget.
	sink.RecordSession(browser.ProviderBrowserless, "acme", "mission-X", 1, 30.0)

	_, err = c.Open(context.Background(), browser.SessionOpts{
		TenantID:  "acme",
		MissionID: "mission-X",
	})
	if !errors.Is(err, browser.ErrBudgetExhausted) {
		t.Errorf("Open over-budget = %v, want ErrBudgetExhausted", err)
	}
}

// TestClient_Events asserts the full event sequence for a navigate
// + close cycle: opened → navigate → closed.
func TestClient_Events(t *testing.T) {
	t.Parallel()
	mock := startMockCDP(t)
	em := &SliceEmitter{}
	c, err := NewClient(Config{
		Endpoint:      mock.wsURL,
		Token:         "tok",
		AllowInsecure: true,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	c.WithEmitter(em)

	opts := browser.SessionOpts{
		TenantID:  "acme",
		MissionID: "m1",
		NetworkPolicy: browser.NetworkPolicy{
			Mode:  browser.PolicyAllowByDefault,
			Allow: []string{"mock"},
		},
	}
	s, err := c.Open(context.Background(), opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.Navigate(context.Background(), "http://mock/example"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	events := em.Events()
	names := make([]string, len(events))
	for i, e := range events {
		names[i] = e.Name
	}
	want := []string{EvtSessionOpened, EvtNavigate, EvtSessionClosed}
	if !containsSequence(names, want) {
		t.Errorf("event sequence = %v, want subsequence %v", names, want)
	}
	// Spec requirement: every event carries tenant_id + provider.
	for _, e := range events {
		if e.Attrs["tenant_id"] == nil || e.Attrs["tenant_id"] == "" {
			t.Errorf("event %s missing tenant_id: %+v", e.Name, e.Attrs)
		}
		if e.Attrs["provider"] != browser.ProviderBrowserless {
			t.Errorf("event %s provider = %v, want %s", e.Name, e.Attrs["provider"], browser.ProviderBrowserless)
		}
	}
}

// containsSequence checks whether want appears as a subsequence of got.
func containsSequence(got, want []string) bool {
	i := 0
	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
		}
	}
	return i == len(want)
}

// TestRedactToken assures we never log the full credential.
func TestRedactToken(t *testing.T) {
	t.Parallel()
	if r := redactToken("abcdef123456"); r == "abcdef123456" || !strings.Contains(r, "…") {
		t.Errorf("redactToken did not redact: %q", r)
	}
	if r := redactToken("short"); r != "redacted" {
		t.Errorf("redactToken(short) = %q, want redacted", r)
	}
}

// TestHostOfURL pins the host parser.
func TestHostOfURL(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"https://example.com/foo":         "example.com",
		"http://user:pass@x.com:8080/?q":  "x.com",
		"":                                "",
		"https://X.COM/":                  "x.com",
	}
	for in, want := range cases {
		if got := hostOfURL(in); got != want {
			t.Errorf("hostOfURL(%q) = %q, want %q", in, got, want)
		}
	}
}
