// Package main — ui_v2_flag_test.go
//
// Spec D (D-UI2-7) removed the R1_SERVER_UI_V2 flag once the legacy
// v1 SPA was deleted. The pre-Spec-D semantics tests
// (TestLoadV2Config_DefaultDisabled, _EnabledOne, _StrictOneOnly)
// are gone with the flag — Enabled is now hardcoded true. The
// remaining tests guard the post-Spec-D contract: Enabled is
// always true, the SRI table is baked in, and ShareEnabled remains
// independently flag-driven.
package main

import "testing"

// TestLoadV2Config_AlwaysEnabled — post-Spec-D, the v2 surface IS
// the only surface. Renderable() returns true regardless of any
// env-var state.
func TestLoadV2Config_AlwaysEnabled(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "") // proves the env-var has no effect
	cfg := LoadV2Config()
	if !cfg.Enabled {
		t.Error("Enabled = false, want true (Spec D hardcodes true)")
	}
	if !cfg.Renderable() {
		t.Error("Renderable() = false, want true unconditionally")
	}
}

// TestLoadV2Config_ShareEnabledIndependent — R1_SERVER_SHARE_ENABLED
// stays as the per-/share/{hash} gate. CanServeShare requires
// (always-true) Enabled AND ShareEnabled.
func TestLoadV2Config_ShareEnabledIndependent(t *testing.T) {
	t.Setenv("R1_SERVER_SHARE_ENABLED", "")
	cfg := LoadV2Config()
	if cfg.ShareEnabled {
		t.Errorf("SHARE_ENABLED unset should keep ShareEnabled=false")
	}
	if cfg.CanServeShare() {
		t.Errorf("CanServeShare() should be false when ShareEnabled is off")
	}

	t.Setenv("R1_SERVER_SHARE_ENABLED", "1")
	cfg = LoadV2Config()
	if !cfg.ShareEnabled {
		t.Errorf("SHARE_ENABLED=1 should set ShareEnabled=true")
	}
	if !cfg.CanServeShare() {
		t.Errorf("CanServeShare() should be true when both flags on (Enabled is now hardcoded true)")
	}
}

// TestLoadV2Config_ShareEnabledStrictOne — preserves the strict-"1"
// equality semantics for the remaining ShareEnabled flag. Anything
// other than literal "1" reads as off; ops scripts thinking
// "true"/"yes" is on shouldn't accidentally flip a customer-visible
// surface.
func TestLoadV2Config_ShareEnabledStrictOne(t *testing.T) {
	for _, val := range []string{"true", "yes", "TRUE", "on", "1 ", " 1"} {
		t.Run("env="+val, func(t *testing.T) {
			t.Setenv("R1_SERVER_SHARE_ENABLED", val)
			cfg := LoadV2Config()
			if cfg.ShareEnabled {
				t.Errorf("env=%q should NOT enable share (strict-1 only); got ShareEnabled=true", val)
			}
		})
	}
}

func TestLoadV2Config_SRIBakedIn(t *testing.T) {
	cfg := LoadV2Config()
	if cfg.HtmxSRI == "" {
		t.Error("HtmxSRI baked-in value missing")
	}
	if cfg.HtmxSseSRI == "" {
		t.Error("HtmxSseSRI baked-in value missing")
	}
	if !startsWithSha384(cfg.HtmxSRI) || !startsWithSha384(cfg.HtmxSseSRI) {
		t.Errorf("SRI values must be sha384- prefixed; got %q / %q", cfg.HtmxSRI, cfg.HtmxSseSRI)
	}
}

func startsWithSha384(s string) bool {
	const p = "sha384-"
	return len(s) > len(p) && s[:len(p)] == p
}
