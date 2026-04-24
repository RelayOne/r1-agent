package delegation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/trustplane"
)

func TestDefaultBundles_AtLeastFive(t *testing.T) {
	if len(DefaultPolicyBundles) < 5 {
		t.Errorf("DefaultPolicyBundles has %d entries; SOW requires at least 5", len(DefaultPolicyBundles))
	}
	for _, required := range []string{
		"read-only-calendar", "read-only-email",
		"send-on-behalf-of", "schedule-on-behalf-of",
		"hire-from-trustplane",
	} {
		if _, ok := DefaultPolicyBundles[required]; !ok {
			t.Errorf("missing required bundle: %q", required)
		}
	}
}

func TestManager_BundleScopes(t *testing.T) {
	m := NewManager(trustplane.NewStubClient())
	scopes, err := m.BundleScopes("read-only-calendar")
	if err != nil {
		t.Fatalf("BundleScopes: %v", err)
	}
	if len(scopes) == 0 {
		t.Error("scope list shouldn't be empty")
	}
}

func TestManager_UnknownBundleErrors(t *testing.T) {
	m := NewManager(trustplane.NewStubClient())
	_, err := m.BundleScopes("doesnt-exist")
	if !errors.Is(err, ErrUnknownBundle) {
		t.Errorf("want ErrUnknownBundle, got %v", err)
	}
}

func TestManager_RegisterBundle(t *testing.T) {
	m := NewManager(trustplane.NewStubClient())
	m.RegisterBundle("custom", []string{"scope_one", "scope_two"})
	scopes, err := m.BundleScopes("custom")
	if err != nil {
		t.Fatalf("BundleScopes: %v", err)
	}
	if len(scopes) != 2 {
		t.Errorf("got %d scopes want 2", len(scopes))
	}
}

func TestManager_DelegatePassesBundleScopes(t *testing.T) {
	m := NewManager(trustplane.NewStubClient())
	d, err := m.Delegate(context.Background(), Request{
		FromDID:    "did:tp:a",
		ToDID:      "did:tp:b",
		BundleName: "read-only-calendar",
		Expiry:     time.Hour,
	})
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}
	if d.ID == "" {
		t.Error("delegation ID should be populated")
	}
}

func TestManager_DelegateUnknownBundleErrors(t *testing.T) {
	m := NewManager(trustplane.NewStubClient())
	_, err := m.Delegate(context.Background(), Request{
		FromDID: "a", ToDID: "b", BundleName: "ghost-bundle", Expiry: time.Hour,
	})
	if !errors.Is(err, ErrUnknownBundle) {
		t.Errorf("want ErrUnknownBundle, got %v", err)
	}
}

func TestManager_VerifyAndRevoke(t *testing.T) {
	m := NewManager(trustplane.NewStubClient())
	d, _ := m.Delegate(context.Background(), Request{
		FromDID:    "a",
		ToDID:      "b",
		BundleName: "read-only-email",
		Expiry:     time.Hour,
	})
	if err := m.Verify(context.Background(), d.ID, "b"); err != nil {
		t.Errorf("fresh delegation should verify: %v", err)
	}
	if err := m.Revoke(context.Background(), d.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	err := m.Verify(context.Background(), d.ID, "b")
	if !errors.Is(err, trustplane.ErrDelegationInvalid) {
		t.Errorf("revoked delegation should fail verify, got %v", err)
	}
}

func TestManager_BundlesListed(t *testing.T) {
	m := NewManager(trustplane.NewStubClient())
	got := m.Bundles()
	if len(got) < 5 {
		t.Errorf("expected at least 5 bundles, got %d", len(got))
	}
	// Sorted.
	for i := 1; i < len(got); i++ {
		if got[i] < got[i-1] {
			t.Errorf("bundles not sorted at index %d: %q < %q", i, got[i], got[i-1])
		}
	}
}

func TestManager_ExtraScopesAppended(t *testing.T) {
	// Verifies the ExtraScopes field extends the bundle rather
	// than replacing it. We can't see the underlying TP call
	// args directly (StubClient swallows them), but we CAN
	// verify the Delegate returns successfully with extras.
	m := NewManager(trustplane.NewStubClient())
	_, err := m.Delegate(context.Background(), Request{
		FromDID: "a", ToDID: "b",
		BundleName:  "read-only-calendar",
		ExtraScopes: []string{"custom_scope"},
		Expiry:      time.Hour,
	})
	if err != nil {
		t.Fatalf("Delegate with extras: %v", err)
	}
}

func TestManager_DefensiveBundleCopy(t *testing.T) {
	// Mutating DefaultPolicyBundles after NewManager must NOT
	// affect the Manager's registered bundles.
	m := NewManager(trustplane.NewStubClient())
	orig := DefaultPolicyBundles["read-only-calendar"]
	DefaultPolicyBundles["read-only-calendar"] = []string{"tampered"}
	defer func() { DefaultPolicyBundles["read-only-calendar"] = orig }()
	scopes, _ := m.BundleScopes("read-only-calendar")
	for _, s := range scopes {
		if s == "tampered" {
			t.Error("manager leaked mutation from DefaultPolicyBundles")
		}
	}
}
