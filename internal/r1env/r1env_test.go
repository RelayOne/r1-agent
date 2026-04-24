package r1env

import (
	"testing"
)

// S6-3 (2026-07-23) drop: legacy STOKE_* fallback removed from Get.
// These tests pin the canonical-only contract and regression-guard
// against re-introducing the legacy read.

func TestGet_ReadsCanonical(t *testing.T) {
	t.Setenv("R1_TEST_CANONICAL", "from-canonical")
	got := Get("R1_TEST_CANONICAL", "STOKE_TEST_CANONICAL")
	if got != "from-canonical" {
		t.Errorf("Get() = %q, want %q", got, "from-canonical")
	}
}

func TestGet_CanonicalUnset_ReturnsEmpty(t *testing.T) {
	// Canonical intentionally unset; legacy intentionally unset.
	got := Get("R1_TEST_CANONICAL_UNSET_X", "STOKE_TEST_LEGACY_UNSET_X")
	if got != "" {
		t.Errorf("Get() = %q, want empty string", got)
	}
}

// TestGet_S63_LegacyIgnored is the S6-3 regression guard: setting
// ONLY the legacy STOKE_* var must NOT return the legacy value.
// Before S6-3 this test would have returned "legacy-must-be-ignored".
func TestGet_S63_LegacyIgnored(t *testing.T) {
	// Canonical intentionally unset.
	t.Setenv("STOKE_TEST_S63_LEGACY", "legacy-must-be-ignored")
	got := Get("R1_TEST_S63_CANONICAL_UNSET", "STOKE_TEST_S63_LEGACY")
	if got != "" {
		t.Errorf("S6-3 regression: legacy fallback still active: got %q want empty", got)
	}
}

func TestGet_BothSet_CanonicalWins(t *testing.T) {
	t.Setenv("R1_TEST_BOTH", "from-canonical")
	t.Setenv("STOKE_TEST_BOTH", "from-legacy")
	got := Get("R1_TEST_BOTH", "STOKE_TEST_BOTH")
	if got != "from-canonical" {
		t.Errorf("Get() = %q, want canonical %q", got, "from-canonical")
	}
}

func TestGet_EmptyLegacyArg_OK(t *testing.T) {
	t.Setenv("R1_TEST_EMPTY_LEGACY", "canonical-value")
	got := Get("R1_TEST_EMPTY_LEGACY", "")
	if got != "canonical-value" {
		t.Errorf("Get() = %q, want %q", got, "canonical-value")
	}
}

// TestResetWarnOnceForTests_IsNoOp verifies the shim remains callable
// post-S6-3 so existing test helpers that call it still compile.
func TestResetWarnOnceForTests_IsNoOp(t *testing.T) {
	ResetWarnOnceForTests()
}
