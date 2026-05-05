package sessionhub

import (
	"os"
	"strings"
	"testing"
)

// TestAssertCwd_MatchesNoPanic exercises the happy path: the sentinel must
// be silent when the live cwd matches the expected path. Test runs in
// `go test` cwd — we read it via os.Getwd here purely as the test's
// reference point, not as production code.
func TestAssertCwd_MatchesNoPanic(t *testing.T) {
	// LINT-ALLOW chdir-test: test reads its own cwd to feed the sentinel.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// Should not panic.
	assertCwd(wd)
}

// TestAssertCwd_MismatchPanics verifies the sentinel panics on a clear
// mismatch (passing a path the cwd cannot possibly equal). The panic
// message must include both the actual and expected paths and the
// "leaked workdir" sentinel string so on-call operators can grep for it.
func TestAssertCwd_MismatchPanics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected panic; got none")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("expected panic string, got %T: %v", r, r)
		}
		if !strings.Contains(msg, "leaked workdir") {
			t.Fatalf("panic message missing 'leaked workdir' sentinel: %q", msg)
		}
		if !strings.Contains(msg, "cwd drifted") {
			t.Fatalf("panic message missing 'cwd drifted' label: %q", msg)
		}
	}()
	// /nonexistent-r1d-sentinel-target is guaranteed not to be the cwd.
	assertCwd("/nonexistent-r1d-sentinel-target")
}

// TestAssertCwd_TrailingSlashTolerated verifies the path-normalisation
// (filepath.Clean / filepath.Abs) makes the sentinel resilient to benign
// caller mistakes — a trailing slash on the expected path must NOT trip it.
func TestAssertCwd_TrailingSlashTolerated(t *testing.T) {
	// LINT-ALLOW chdir-test: test reads its own cwd to feed the sentinel.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// Should not panic — Clean() strips the trailing slash.
	assertCwd(wd + "/")
}
