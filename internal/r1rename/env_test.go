// Copyright (c) 2026 Good Ventures.
// SPDX-License-Identifier: Apache-2.0

package r1rename

import (
	"testing"

	"github.com/ericmacdougall/stoke/internal/r1env"
)

func TestLookupEnv_CanonicalWins(t *testing.T) {
	r1env.ResetWarnOnceForTests()
	t.Setenv("R1_TEST_LOOKUP_A", "canonical")
	t.Setenv("STOKE_TEST_LOOKUP_A", "legacy")
	if got := LookupEnv("R1_TEST_LOOKUP_A", "STOKE_TEST_LOOKUP_A"); got != "canonical" {
		t.Errorf("canonical-wins: got %q want %q", got, "canonical")
	}
}

func TestLookupEnv_LegacyFallback(t *testing.T) {
	r1env.ResetWarnOnceForTests()
	t.Setenv("STOKE_TEST_LOOKUP_B", "legacy-only")
	// Canonical intentionally unset -- t.Setenv only sets, never unsets,
	// so use a fresh canonical name not assigned in TestMain.
	if got := LookupEnv("R1_TEST_LOOKUP_B_UNSET", "STOKE_TEST_LOOKUP_B"); got != "legacy-only" {
		t.Errorf("legacy-fallback: got %q want %q", got, "legacy-only")
	}
}

func TestLookupEnv_NeitherSet(t *testing.T) {
	r1env.ResetWarnOnceForTests()
	if got := LookupEnv("R1_TEST_LOOKUP_C_UNSET", "STOKE_TEST_LOOKUP_C_UNSET"); got != "" {
		t.Errorf("neither-set: got %q want empty", got)
	}
}

func TestLookupEnv_EmptyLegacyDisablesFallback(t *testing.T) {
	r1env.ResetWarnOnceForTests()
	if got := LookupEnv("R1_TEST_LOOKUP_D_UNSET", ""); got != "" {
		t.Errorf("empty-legacy: got %q want empty", got)
	}
}

func TestLookupEnv_LegacyDropDisablesFallback(t *testing.T) {
	r1env.ResetWarnOnceForTests()
	t.Setenv("R1_ENV_LEGACY_DROP", "true")
	t.Setenv("STOKE_TEST_LOOKUP_E", "legacy-should-be-dropped")
	got := LookupEnv("R1_TEST_LOOKUP_E_UNSET", "STOKE_TEST_LOOKUP_E")
	if got != "" {
		t.Errorf("legacy-drop active: got %q want empty (legacy ignored)", got)
	}
}

func TestLookupEnv_LegacyDropPreservesCanonical(t *testing.T) {
	r1env.ResetWarnOnceForTests()
	t.Setenv("R1_ENV_LEGACY_DROP", "true")
	t.Setenv("R1_TEST_LOOKUP_F", "canonical-still-wins")
	t.Setenv("STOKE_TEST_LOOKUP_F", "legacy-ignored")
	if got := LookupEnv("R1_TEST_LOOKUP_F", "STOKE_TEST_LOOKUP_F"); got != "canonical-still-wins" {
		t.Errorf("legacy-drop with canonical present: got %q want %q", got, "canonical-still-wins")
	}
}

func TestEnvLegacyDropEnabled(t *testing.T) {
	cases := []struct {
		name  string
		set   bool
		value string
		want  bool
	}{
		{"unset", false, "", false},
		{"empty", true, "", false},
		{"true", true, "true", true},
		{"True", true, "True", true},
		{"1", true, "1", true},
		{"false", true, "false", false},
		{"junk", true, "not-a-bool", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv(EnvLegacyDropEnv, tc.value)
			}
			if got := EnvLegacyDropEnabled(); got != tc.want {
				t.Errorf("EnvLegacyDropEnabled(%q) = %v want %v", tc.value, got, tc.want)
			}
		})
	}
}
