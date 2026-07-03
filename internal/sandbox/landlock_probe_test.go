package sandbox

import (
	"errors"
	"strings"
	"testing"
)

// TestLandlockAvailableHelperProbe pins the routing self-test: Available must
// fail closed when THIS binary does not route the __sandbox-exec helper (the
// r1-server / r1-bench embedding case), turning a guaranteed mid-mission bash
// failure into a clear wiring-time error. When the probe passes, Available
// succeeds on a capable ABI.
func TestLandlockAvailableHelperProbe(t *testing.T) {
	stubHelperProbe := func(t *testing.T, err error) {
		t.Helper()
		orig := landlockHelperProbe
		landlockHelperProbe = func() error { return err }
		t.Cleanup(func() { landlockHelperProbe = orig })
	}

	t.Run("unrouted helper fails closed at wiring time", func(t *testing.T) {
		stubLandlockABI(t, 5) // kernel is capable...
		stubHelperProbe(t, errors.New("does not route the __sandbox-exec helper subcommand"))
		err := (&landlockWrapper{}).Available(Policy{AllowEgress: true})
		if err == nil {
			t.Fatal("Available must fail when the helper subcommand is not routed")
		}
		if !strings.Contains(err.Error(), "__sandbox-exec") {
			t.Errorf("error should name the helper subcommand: %v", err)
		}
	})

	t.Run("routed helper on capable ABI succeeds", func(t *testing.T) {
		stubLandlockABI(t, 5)
		stubHelperProbe(t, nil)
		if err := (&landlockWrapper{}).Available(Policy{AllowEgress: true}); err != nil {
			t.Errorf("Available should succeed when ABI is capable and helper routes: %v", err)
		}
	})

	t.Run("helper probe not reached when ABI too old", func(t *testing.T) {
		stubLandlockABI(t, 0)
		// Probe would pass, but ABI gate short-circuits first — the error
		// must be about landlock support, not the helper.
		stubHelperProbe(t, errors.New("SHOULD NOT BE CALLED"))
		err := (&landlockWrapper{}).Available(Policy{AllowEgress: true})
		if err == nil || strings.Contains(err.Error(), "SHOULD NOT BE CALLED") {
			t.Errorf("ABI gate must precede the helper probe, got: %v", err)
		}
	})

	t.Run("real probe routes under go test", func(t *testing.T) {
		// The default landlockHelperProbe re-execs os.Executable(), which
		// under `go test` is this binary — TestMain routes __sandbox-exec,
		// so the real probe returns nil. Exercises the un-stubbed path.
		if err := landlockHelperProbe(); err != nil {
			t.Errorf("real helper probe should route under the test binary: %v", err)
		}
	})
}
