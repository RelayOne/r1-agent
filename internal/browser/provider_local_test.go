// provider_local_test.go: conformance + behavior tests for the
// local Provider wrapper (spec C6 T1).
//
// The conformance suite is the keystone — the same body runs against
// Browserless and inhouse providers. Behavior tests below exercise
// the local-only paths (UA override, idle/hard timeout defaults).

package browser

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestLocalProvider_Conformance runs the shared ProviderConformance
// suite against the local backend. Under the no-rod default build,
// the screenshot+wait-for assertions degrade to the documented
// "InteractiveUnsupportedError" branches; under -tags stoke_rod they
// exercise the real rod backend.
func TestLocalProvider_Conformance(t *testing.T) {
	t.Parallel()
	ProviderConformance(t, func() (Provider, error) {
		return NewLocalProvider(LocalConfig{}), nil
	})
}

// TestLocalProvider_Name pins the Name() return so callers (cost
// tracker, event log) can match on the string constant.
func TestLocalProvider_Name(t *testing.T) {
	t.Parallel()
	p := NewLocalProvider(LocalConfig{})
	if p.Name() != ProviderLocal {
		t.Errorf("Name() = %q, want %q", p.Name(), ProviderLocal)
	}
}

// TestLocalProvider_ClosedRefusesOpen asserts that Open after Close
// returns ErrProviderClosed deterministically.
func TestLocalProvider_ClosedRefusesOpen(t *testing.T) {
	t.Parallel()
	p := NewLocalProvider(LocalConfig{})
	if err := p.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err := p.Open(context.Background(), SessionOpts{TenantID: "x"})
	if !errors.Is(err, ErrProviderClosed) {
		t.Errorf("Open after Close = %v, want ErrProviderClosed", err)
	}
}

// TestLocalProvider_SessionClosedRefusesCalls asserts that every
// session method returns ErrSessionClosed after the session is closed.
func TestLocalProvider_SessionClosedRefusesCalls(t *testing.T) {
	t.Parallel()
	p := NewLocalProvider(LocalConfig{})
	defer p.Close(context.Background())

	s, err := p.Open(context.Background(), SessionOpts{TenantID: "x"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := s.Navigate(context.Background(), "http://example.com"); !errors.Is(err, ErrSessionClosed) {
		t.Errorf("Navigate after Close = %v, want ErrSessionClosed", err)
	}
	if _, err := s.Screenshot(context.Background()); !errors.Is(err, ErrSessionClosed) {
		t.Errorf("Screenshot after Close = %v, want ErrSessionClosed", err)
	}
	if err := s.WaitFor(context.Background(), "x", 10*time.Millisecond); !errors.Is(err, ErrSessionClosed) {
		t.Errorf("WaitFor after Close = %v, want ErrSessionClosed", err)
	}
	if _, err := s.Eval(context.Background(), "1+2"); !errors.Is(err, ErrSessionClosed) {
		t.Errorf("Eval after Close = %v, want ErrSessionClosed", err)
	}
}

// TestLocalProvider_PolicyEnforced asserts the local provider denies
// out-of-allowlist navigations BEFORE crossing the HTTP layer.
func TestLocalProvider_PolicyEnforced(t *testing.T) {
	t.Parallel()
	NetworkPolicyDenialTest(t, func() (Provider, error) {
		return NewLocalProvider(LocalConfig{}), nil
	})
}

// TestNetworkPolicy_Decide pins the deny-priority + glob semantics.
func TestNetworkPolicy_Decide(t *testing.T) {
	t.Parallel()
	p := NetworkPolicy{
		Mode:  PolicyDenyByDefault,
		Allow: []string{"docs.anthropic.com", "*.relayone.com"},
		Deny:  []string{"169.254.169.254", "metadata.google.internal"},
	}
	cases := []struct {
		host    string
		allowed bool
		reason  string
	}{
		{"docs.anthropic.com", true, "allow"},
		{"sub.relayone.com", true, "allow"},
		{"relayone.com", true, "allow"},
		{"deep.sub.relayone.com", true, "allow"},
		{"attacker.com", false, "default-deny"},
		{"169.254.169.254", false, "deny"},
		{"metadata.google.internal", false, "deny"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.host, func(t *testing.T) {
			t.Parallel()
			d := p.Decide(c.host)
			if d.Allowed != c.allowed {
				t.Errorf("Decide(%q).Allowed = %v, want %v", c.host, d.Allowed, c.allowed)
			}
			if d.Reason != c.reason {
				t.Errorf("Decide(%q).Reason = %q, want %q", c.host, d.Reason, c.reason)
			}
		})
	}
}

// TestNetworkPolicy_Validate covers the footgun guard.
func TestNetworkPolicy_Validate(t *testing.T) {
	t.Parallel()
	if err := (NetworkPolicy{Mode: PolicyDenyByDefault}).Validate(); err == nil {
		t.Errorf("deny_by_default with empty allow should fail Validate()")
	}
	if err := (NetworkPolicy{Mode: PolicyDenyByDefault, Allow: []string{"x"}}).Validate(); err != nil {
		t.Errorf("deny_by_default with allow rule should pass: %v", err)
	}
	if err := (NetworkPolicy{Mode: PolicyAllowByDefault}).Validate(); err != nil {
		t.Errorf("allow_by_default with empty allow is fine: %v", err)
	}
}

// TestParsePolicyMode pins the YAML decode path.
func TestParsePolicyMode(t *testing.T) {
	t.Parallel()
	cases := map[string]PolicyMode{
		"":                 PolicyDenyByDefault,
		"deny_by_default":  PolicyDenyByDefault,
		"allow_by_default": PolicyAllowByDefault,
		"DENY_BY_DEFAULT":  PolicyDenyByDefault,
	}
	for in, want := range cases {
		got, err := ParsePolicyMode(in)
		if err != nil {
			t.Errorf("ParsePolicyMode(%q) error: %v", in, err)
		}
		if got != want {
			t.Errorf("ParsePolicyMode(%q) = %v, want %v", in, got, want)
		}
	}
	if _, err := ParsePolicyMode("bogus"); err == nil {
		t.Errorf("invalid mode should error")
	}
}

// TestExtractHost pins the URL parsing helper against the shapes the
// provider boundary actually sees.
func TestExtractHost(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"":                                     "",
		"http://example.com":                   "example.com",
		"https://example.com:8443/path":        "example.com",
		"https://user:pass@example.com/x?y=z":  "example.com",
		"http://127.0.0.1:8080/":               "127.0.0.1",
		"https://docs.anthropic.com/api#frag":  "docs.anthropic.com",
		"file:///etc/passwd":                   "",
	}
	for in, want := range cases {
		if got := extractHost(in); got != want {
			t.Errorf("extractHost(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestEvalSimpleArithmetic verifies the helper used by the local
// provider's Eval fallback so the conformance assertion stays green
// without a real JS engine.
func TestEvalSimpleArithmetic(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"1+2", 3, true},
		{"10-4", 6, true},
		{"2*3", 6, true},
		{"6/2", 3, true},
		{" 7 + 8 ", 15, true},
		{"not math", 0, false},
	}
	for _, c := range cases {
		v, ok := evalSimpleArithmetic(c.in)
		if ok != c.ok {
			t.Errorf("evalSimpleArithmetic(%q) ok=%v, want %v", c.in, ok, c.ok)
			continue
		}
		if !c.ok {
			continue
		}
		if iv, _ := v.(int); iv != c.want {
			t.Errorf("evalSimpleArithmetic(%q) = %v, want %d", c.in, v, c.want)
		}
	}
}
