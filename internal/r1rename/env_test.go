// Copyright (c) 2026 Good Ventures.
// SPDX-License-Identifier: Apache-2.0

package r1rename

import (
	"testing"
)

// TestLookupEnv_ReadsCanonical is the canonical-path coverage after
// the S6-3 drop: LookupEnv returns the canonical R1_* value when set.
func TestLookupEnv_ReadsCanonical(t *testing.T) {
	t.Setenv("R1_TEST_LOOKUP_A", "canonical")
	if got := LookupEnv("R1_TEST_LOOKUP_A", "STOKE_TEST_LOOKUP_A"); got != "canonical" {
		t.Errorf("canonical lookup: got %q want %q", got, "canonical")
	}
}

// TestLookupEnv_NeitherSet verifies LookupEnv returns "" when the
// canonical var is unset.
func TestLookupEnv_NeitherSet(t *testing.T) {
	if got := LookupEnv("R1_TEST_LOOKUP_C_UNSET", "STOKE_TEST_LOOKUP_C_UNSET"); got != "" {
		t.Errorf("neither-set: got %q want empty", got)
	}
}

// TestLookupEnv_EmptyLegacyArg verifies the legacy arg is accepted
// (for call-site compatibility) but never used.
func TestLookupEnv_EmptyLegacyArg(t *testing.T) {
	t.Setenv("R1_TEST_LOOKUP_D", "canonical-still-wins")
	if got := LookupEnv("R1_TEST_LOOKUP_D", ""); got != "canonical-still-wins" {
		t.Errorf("empty-legacy: got %q want %q", got, "canonical-still-wins")
	}
}

// TestLookupEnv_S63_LegacyIgnored is the S6-3 regression guard: after
// the 90d window elapsed 2026-07-23, setting ONLY the legacy STOKE_*
// env var must return "" (not the legacy value). Before S6-3 this
// test would have returned "legacy-value-must-be-ignored".
func TestLookupEnv_S63_LegacyIgnored(t *testing.T) {
	t.Setenv("STOKE_TEST_LOOKUP_E", "legacy-value-must-be-ignored")
	got := LookupEnv("R1_TEST_LOOKUP_E_UNSET", "STOKE_TEST_LOOKUP_E")
	if got != "" {
		t.Errorf("S6-3 regression: legacy fallback still active: got %q want empty", got)
	}
}

// TestLookupEnv_S63_CanonicalWinsOverLegacy verifies that when both
// canonical and legacy are set, canonical wins. This was also the
// pre-S6-3 contract; kept as a regression guard.
func TestLookupEnv_S63_CanonicalWinsOverLegacy(t *testing.T) {
	t.Setenv("R1_TEST_LOOKUP_F", "canonical-wins")
	t.Setenv("STOKE_TEST_LOOKUP_F", "legacy-ignored")
	if got := LookupEnv("R1_TEST_LOOKUP_F", "STOKE_TEST_LOOKUP_F"); got != "canonical-wins" {
		t.Errorf("canonical-wins: got %q want %q", got, "canonical-wins")
	}
}
