package main

// Regression tests for audit A057: the policy's `cortex:` block per-Lobe
// enable flags must gate Lobe registration in buildCortexBackend, with
// default-on semantics when the block (or an individual flag) is absent.

import (
	"testing"

	"github.com/RelayOne/r1/internal/config"
)

// lobeIDsFromBackend collects the registered Lobe IDs via LobeStatus.
func lobeIDsFromBackend(t *testing.T, cfg config.CortexConfig) map[string]bool {
	t.Helper()
	backend, err := buildCortexBackend("a057-test", t.TempDir(), "deterministic", cfg)
	if err != nil {
		t.Fatalf("buildCortexBackend: %v", err)
	}
	ids := map[string]bool{}
	for _, info := range backend.LobeStatus() {
		ids[info.ID] = true
	}
	return ids
}

// TestBuildCortexBackend_DefaultOnWithoutCortexBlock asserts that the
// zero-value CortexConfig (no cortex: block in the policy) registers
// all 4 deterministic Lobes — the default-on contract.
func TestBuildCortexBackend_DefaultOnWithoutCortexBlock(t *testing.T) {
	ids := lobeIDsFromBackend(t, config.CortexConfig{})
	for _, want := range []string{"memory-recall", "wal-keeper", "rule-check", "antitrunc"} {
		if !ids[want] {
			t.Errorf("absent cortex block: lobe %q not registered, want default-on", want)
		}
	}
	if len(ids) != 4 {
		t.Errorf("registered %d lobes, want 4: %v", len(ids), ids)
	}
}

// TestBuildCortexBackend_ExplicitFalseDisablesLobe asserts the
// documented privacy opt-out works: `cortex: lobes: memory_recall:
// enabled: false` must skip that Lobe's construction while the other
// three remain registered.
func TestBuildCortexBackend_ExplicitFalseDisablesLobe(t *testing.T) {
	off := false
	var cfg config.CortexConfig
	cfg.Lobes.MemoryRecall.Enabled = &off

	ids := lobeIDsFromBackend(t, cfg)
	if ids["memory-recall"] {
		t.Error("memory-recall registered despite enabled: false")
	}
	for _, want := range []string{"wal-keeper", "rule-check", "antitrunc"} {
		if !ids[want] {
			t.Errorf("lobe %q missing; only memory-recall was disabled", want)
		}
	}
	if len(ids) != 3 {
		t.Errorf("registered %d lobes, want 3: %v", len(ids), ids)
	}
}

// TestBuildCortexBackend_ExplicitTrueKeepsLobe asserts an explicit
// enabled: true behaves identically to the absent flag (still on).
func TestBuildCortexBackend_ExplicitTrueKeepsLobe(t *testing.T) {
	on := true
	var cfg config.CortexConfig
	cfg.Lobes.RuleCheck.Enabled = &on

	ids := lobeIDsFromBackend(t, cfg)
	if !ids["rule-check"] {
		t.Error("rule-check missing despite explicit enabled: true")
	}
	if len(ids) != 4 {
		t.Errorf("registered %d lobes, want 4: %v", len(ids), ids)
	}
}

// TestBuildCortexBackend_AllDisabledYieldsEmptyRunnerList asserts the
// extreme opt-out (all four deterministic Lobes disabled) still builds
// a functioning workspace-only backend rather than erroring.
func TestBuildCortexBackend_AllDisabledYieldsEmptyRunnerList(t *testing.T) {
	off := false
	var cfg config.CortexConfig
	cfg.Lobes.MemoryRecall.Enabled = &off
	cfg.Lobes.WALKeeper.Enabled = &off
	cfg.Lobes.RuleCheck.Enabled = &off
	cfg.Lobes.AntiTrunc.Enabled = &off

	ids := lobeIDsFromBackend(t, cfg)
	if len(ids) != 0 {
		t.Errorf("registered %d lobes, want 0: %v", len(ids), ids)
	}
}
