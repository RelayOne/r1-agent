package r1env

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

// captureLog redirects log.Default() output into a buffer for the
// duration of the test. The default log destination + flags are
// restored on cleanup so other tests running in the same process are
// unaffected.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prevOut := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})
	return buf
}

func TestGet_CanonicalOnly_NoWarn(t *testing.T) {
	ResetWarnOnceForTests()
	t.Setenv("R1_TEST_CANONICAL_ONLY", "from-canonical")
	t.Setenv("STOKE_TEST_CANONICAL_ONLY", "")

	buf := captureLog(t)
	got := Get("R1_TEST_CANONICAL_ONLY", "STOKE_TEST_CANONICAL_ONLY")
	if got != "from-canonical" {
		t.Errorf("Get() = %q, want %q", got, "from-canonical")
	}
	if buf.Len() != 0 {
		t.Errorf("expected no WARN output, got: %q", buf.String())
	}
}

func TestGet_LegacyOnly_WarnsOnce(t *testing.T) {
	ResetWarnOnceForTests()
	t.Setenv("R1_TEST_LEGACY_ONLY", "")
	t.Setenv("STOKE_TEST_LEGACY_ONLY", "from-legacy")

	buf := captureLog(t)
	for i := 0; i < 5; i++ {
		got := Get("R1_TEST_LEGACY_ONLY", "STOKE_TEST_LEGACY_ONLY")
		if got != "from-legacy" {
			t.Fatalf("Get() iteration %d = %q, want %q", i, got, "from-legacy")
		}
	}

	out := buf.String()
	// Exactly one WARN line should have been emitted despite five Get()
	// calls because warnOnce collapses subsequent reads.
	if n := strings.Count(out, "WARN: legacy env STOKE_TEST_LEGACY_ONLY"); n != 1 {
		t.Errorf("expected exactly 1 WARN line, got %d in: %q", n, out)
	}
	if !strings.Contains(out, "rename to R1_TEST_LEGACY_ONLY before 2026-07-23") {
		t.Errorf("expected rename-by date in WARN, got: %q", out)
	}
}

func TestGet_BothSet_CanonicalWins_NoWarn(t *testing.T) {
	ResetWarnOnceForTests()
	t.Setenv("R1_TEST_BOTH", "from-canonical")
	t.Setenv("STOKE_TEST_BOTH", "from-legacy")

	buf := captureLog(t)
	got := Get("R1_TEST_BOTH", "STOKE_TEST_BOTH")
	if got != "from-canonical" {
		t.Errorf("Get() = %q, want canonical %q", got, "from-canonical")
	}
	if buf.Len() != 0 {
		t.Errorf("expected no WARN when canonical set, got: %q", buf.String())
	}
}

func TestGet_NeitherSet_EmptyString(t *testing.T) {
	ResetWarnOnceForTests()
	t.Setenv("R1_TEST_NEITHER", "")
	t.Setenv("STOKE_TEST_NEITHER", "")

	buf := captureLog(t)
	got := Get("R1_TEST_NEITHER", "STOKE_TEST_NEITHER")
	if got != "" {
		t.Errorf("Get() = %q, want empty string", got)
	}
	if buf.Len() != 0 {
		t.Errorf("expected no WARN for unset pair, got: %q", buf.String())
	}
}

func TestGet_EmptyLegacy_DisablesFallback(t *testing.T) {
	ResetWarnOnceForTests()
	t.Setenv("R1_TEST_NO_LEGACY", "")

	buf := captureLog(t)
	got := Get("R1_TEST_NO_LEGACY", "")
	if got != "" {
		t.Errorf("Get() = %q, want empty when canonical unset and legacy arg empty", got)
	}
	if buf.Len() != 0 {
		t.Errorf("expected no WARN when legacy arg empty, got: %q", buf.String())
	}
}

func TestGet_DistinctPairs_EachWarnsOnce(t *testing.T) {
	ResetWarnOnceForTests()
	t.Setenv("STOKE_TEST_PAIR_A", "aval")
	t.Setenv("STOKE_TEST_PAIR_B", "bval")

	buf := captureLog(t)
	_ = Get("R1_TEST_PAIR_A", "STOKE_TEST_PAIR_A")
	_ = Get("R1_TEST_PAIR_A", "STOKE_TEST_PAIR_A") // duplicate — should not warn again
	_ = Get("R1_TEST_PAIR_B", "STOKE_TEST_PAIR_B")

	out := buf.String()
	if n := strings.Count(out, "WARN: legacy env STOKE_TEST_PAIR_A"); n != 1 {
		t.Errorf("pair A expected 1 WARN, got %d: %q", n, out)
	}
	if n := strings.Count(out, "WARN: legacy env STOKE_TEST_PAIR_B"); n != 1 {
		t.Errorf("pair B expected 1 WARN, got %d: %q", n, out)
	}
}
