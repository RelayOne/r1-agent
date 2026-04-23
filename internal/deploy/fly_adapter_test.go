package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestFlyAdapter_RegisteredAtInit verifies the package-level init()
// in fly_adapter.go registered the Fly provider. We reset the
// registry first (to escape any bleed from earlier tests that did
// the same) and then re-run registerBuiltins() to simulate the init
// outcome deterministically. After that, deploy.Names() must
// include "fly" and deploy.Get("fly") must hand back a non-nil
// Deployer whose Name() round-trips.
func TestFlyAdapter_RegisteredAtInit(t *testing.T) {
	resetRegistryForTest()
	t.Cleanup(resetRegistryForTest)
	registerBuiltins()

	names := Names()
	found := false
	for _, n := range names {
		if n == "fly" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("Names() = %v, does not contain %q", names, "fly")
	}

	d, err := Get("fly")
	if err != nil {
		t.Fatalf("Get(fly) err = %v, want nil", err)
	}
	if d == nil {
		t.Fatal("Get(fly) returned nil Deployer")
	}
	if got := d.Name(); got != "fly" {
		t.Fatalf("Name() = %q, want %q", got, "fly")
	}
}

// TestFlyAdapter_DeployForcesProvider verifies the adapter overrides
// cfg.Provider to ProviderFly before delegating. Callers who looked
// us up by registry key should never hit ErrProviderUnsupported due
// to a zero-value or mis-set Provider field.
//
// We drive this through DryRun so no flyctl is invoked — the
// DryRun=true branch of Deploy returns a preview that echoes
// Provider in its header, which gives us a deterministic assertion
// target.
func TestFlyAdapter_DeployForcesProvider(t *testing.T) {
	adapter := flyAdapter{}
	cfg := DeployConfig{
		// Intentionally leave Provider zero-valued.
		AppName: "stoke-test",
		DryRun:  true,
	}
	res, err := adapter.Deploy(context.Background(), cfg)
	if err != nil {
		t.Fatalf("adapter.Deploy err = %v, want nil", err)
	}
	if !res.DryRun {
		t.Fatal("DryRun result = false, want true")
	}
	if !strings.Contains(res.Stdout, "provider=fly") {
		t.Fatalf("preview missing provider=fly header; got:\n%s", res.Stdout)
	}
}

// TestFlyAdapter_RollbackUnsupported verifies Rollback returns the
// typed ErrFlyRollbackUnsupported error rather than silently claiming
// success. The descent engine keys off this error to decide whether
// to escalate to operator-run manual rollback.
func TestFlyAdapter_RollbackUnsupported(t *testing.T) {
	adapter := flyAdapter{}
	err := adapter.Rollback(context.Background(), DeployConfig{AppName: "x"})
	if err == nil {
		t.Fatal("Rollback err = nil, want ErrFlyRollbackUnsupported")
	}
	if !errors.Is(err, ErrFlyRollbackUnsupported) {
		t.Fatalf("Rollback err = %v, want errors.Is ErrFlyRollbackUnsupported", err)
	}
}

// TestFlyAdapter_VerifyEmpty ensures Verify's URL derivation handles
// the "no AppName, no override" case by refusing to succeed against
// an empty URL rather than panicking or dialing localhost.
func TestFlyAdapter_VerifyEmpty(t *testing.T) {
	adapter := flyAdapter{}
	ok, detail := adapter.Verify(context.Background(), DeployConfig{})
	if ok {
		t.Fatalf("Verify ok = true, want false on empty config (detail=%q)", detail)
	}
	if detail == "" {
		t.Fatal("Verify returned ok=false with empty detail; operator has no diagnostic")
	}
}
