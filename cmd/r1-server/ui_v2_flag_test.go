// Package main — ui_v2_flag_test.go
//
// Spec 4 §10 T1 — verifies LoadV2Config reads env vars correctly +
// strict-equality semantics + ShareEnabled independence.
package main

import "testing"

func TestLoadV2Config_DefaultDisabled(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "")
	t.Setenv("R1_SERVER_SHARE_ENABLED", "")
	cfg := LoadV2Config()
	if cfg.Enabled {
		t.Errorf("Enabled = true, want false (env unset)")
	}
	if cfg.ShareEnabled {
		t.Errorf("ShareEnabled = true, want false (env unset)")
	}
	if cfg.Renderable() {
		t.Errorf("Renderable() should be false when Enabled is false")
	}
	if cfg.CanServeShare() {
		t.Errorf("CanServeShare() should be false when both flags off")
	}
}

func TestLoadV2Config_EnabledOne(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	t.Setenv("R1_SERVER_SHARE_ENABLED", "")
	cfg := LoadV2Config()
	if !cfg.Enabled {
		t.Errorf("Enabled = false, want true (env=1)")
	}
	if !cfg.Renderable() {
		t.Errorf("Renderable() should be true when Enabled is true")
	}
	if cfg.CanServeShare() {
		t.Errorf("CanServeShare() should still be false when ShareEnabled is off")
	}
}

func TestLoadV2Config_StrictOneOnly(t *testing.T) {
	for _, val := range []string{"true", "yes", "TRUE", "on", "1 ", " 1"} {
		t.Run("env="+val, func(t *testing.T) {
			t.Setenv("R1_SERVER_UI_V2", val)
			cfg := LoadV2Config()
			if cfg.Enabled {
				t.Errorf("env=%q should NOT enable (strict-1 only); got Enabled=true", val)
			}
		})
	}
}

func TestLoadV2Config_ShareEnabledIndependent(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "")
	t.Setenv("R1_SERVER_SHARE_ENABLED", "1")
	cfg := LoadV2Config()
	if cfg.Enabled {
		t.Errorf("UI_V2 unset should keep Enabled=false")
	}
	if !cfg.ShareEnabled {
		t.Errorf("SHARE_ENABLED=1 should set ShareEnabled=true")
	}
	if cfg.CanServeShare() {
		t.Errorf("CanServeShare() requires BOTH Enabled and ShareEnabled — got true with Enabled=false")
	}

	// Both on
	t.Setenv("R1_SERVER_UI_V2", "1")
	cfg = LoadV2Config()
	if !cfg.CanServeShare() {
		t.Errorf("CanServeShare() should be true when both flags on")
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
