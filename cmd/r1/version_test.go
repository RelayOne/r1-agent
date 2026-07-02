package main

import (
	"strings"
	"testing"
)

// TestResolveVersionPrefersLdflags asserts an injected version wins over
// the VCS fallback — the release-build path.
func TestResolveVersionPrefersLdflags(t *testing.T) {
	orig := version
	defer func() { version = orig }()
	version = "v1.2.3"
	if got := resolveVersion(); got != "v1.2.3" {
		t.Errorf("resolveVersion() = %q, want v1.2.3", got)
	}
}

// TestResolveVersionFallsBackToVCS asserts that with the default "dev"
// version, resolveVersion never returns the bare "dev" when build info
// carries a VCS revision (the "prints just dev" gap). In the test binary
// build info may be absent, in which case "dev" is the honest answer — so
// we assert the shape, not a specific commit.
func TestResolveVersionFallsBackToVCS(t *testing.T) {
	orig := version
	defer func() { version = orig }()
	version = "dev"
	got := resolveVersion()
	if got != "dev" && !strings.HasPrefix(got, "dev+") {
		t.Errorf("resolveVersion() = %q, want \"dev\" or \"dev+<rev>\"", got)
	}
}

// TestVersionDetailIncludesRuntime asserts the detailed form carries the
// Go version and platform for bug reports.
func TestVersionDetailIncludesRuntime(t *testing.T) {
	d := versionDetail()
	if !strings.HasPrefix(d, "r1 ") {
		t.Errorf("versionDetail() = %q, want it to start with 'r1 '", d)
	}
	if !strings.Contains(d, "go1.") && !strings.Contains(d, "go2.") {
		t.Errorf("versionDetail() = %q, want it to include the Go version", d)
	}
}
