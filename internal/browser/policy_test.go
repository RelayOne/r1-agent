// policy_test.go: deny-by-default network policy denial assertions
// (spec C6 T13d).
//
// One test per provider (local here; Browserless + inhouse run the
// same body via their conformance suites). Sets a policy
// `mode: deny_by_default, allow: ["example.com"]`, navigates to an
// off-allowlist host, asserts ErrNetworkDenied with the host name in
// the error payload.

package browser

import (
	"context"
	"errors"
	"testing"
)

// TestNetworkPolicy_DenyByDefault is the standalone version of
// NetworkPolicyDenialTest, scoped to the local Provider so a quick
// `go test ./internal/browser/` invocation runs the denial path.
func TestNetworkPolicy_DenyByDefault(t *testing.T) {
	t.Parallel()
	p := NewLocalProvider(LocalConfig{})
	defer p.Close(context.Background())

	policy := NetworkPolicy{
		Mode:  PolicyDenyByDefault,
		Allow: []string{"example.com"},
	}
	s, err := p.Open(context.Background(), SessionOpts{
		TenantID:      "policy-test",
		NetworkPolicy: policy,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close(context.Background())

	// Denied: not in allowlist.
	_, err = s.Navigate(context.Background(), "http://attacker.com/path")
	if err == nil {
		t.Fatalf("Navigate to attacker.com should have been denied")
	}
	var nd *NetworkDeniedError
	if !errors.As(err, &nd) {
		t.Fatalf("want *NetworkDeniedError, got %T: %v", err, err)
	}
	if nd.Host != "attacker.com" {
		t.Errorf("denial host = %q, want attacker.com", nd.Host)
	}
}

// TestNetworkPolicy_DefaultDenyEmptyAllowFails covers the
// operator-footgun guard (T5).
func TestNetworkPolicy_DefaultDenyEmptyAllowFails(t *testing.T) {
	t.Parallel()
	if err := (NetworkPolicy{Mode: PolicyDenyByDefault}).Validate(); err == nil {
		t.Errorf("deny_by_default with empty allow list must fail Validate()")
	}
}
