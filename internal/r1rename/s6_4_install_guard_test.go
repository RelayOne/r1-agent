// Copyright (c) 2026 Good Ventures.
// SPDX-License-Identifier: Apache-2.0

package r1rename

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// S6-4 regression guards: after 2026-07-23 the stoke binary install
// path + Homebrew stoke formula have been dropped. These tests scan
// the top-level install.sh + .goreleaser.yml and fail if a legacy
// surface is re-introduced by a later commit.

// repoRoot walks up from this test file to the repo root. We rely on
// the fixed relative path "internal/r1rename" from the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func readRepoFile(t *testing.T, relative string) string {
	t.Helper()
	path := filepath.Join(repoRoot(t), relative)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// TestS64_InstallSh_NoStokeBinaryInstallStep asserts the install.sh
// script does not contain the pre-S6-4 "install_one stoke" line that
// installed the legacy stoke binary. The canonical "install_one r1"
// entry must be present instead.
func TestS64_InstallSh_NoStokeBinaryInstallStep(t *testing.T) {
	source := readRepoFile(t, "install.sh")

	// Regression: the legacy install call must be absent.
	if strings.Contains(source, `install_one stoke "`) {
		t.Error(`S6-4 regression: install.sh still calls 'install_one stoke ...' -- legacy binary install must be removed`)
	}

	// Canonical r1 install must be present.
	if !strings.Contains(source, `install_one r1 `) {
		t.Error("S6-4 regression: install.sh missing 'install_one r1' canonical binary install step")
	}
}

// TestS64_InstallSh_NoStokeBinBuildFromSource asserts the
// build_from_source path no longer compiles cmd/stoke into a
// `stoke-bin` artifact.
func TestS64_InstallSh_NoStokeBinBuildFromSource(t *testing.T) {
	source := readRepoFile(t, "install.sh")

	// The pre-S6-4 line was:
	//   (cd "${tmp_dir}/stoke" && go build ... -o "${tmp_dir}/stoke-bin" ./cmd/stoke)
	// Check the distinctive artifact name is gone.
	if strings.Contains(source, "stoke-bin") {
		t.Error("S6-4 regression: install.sh still references stoke-bin artifact in build_from_source")
	}
	// Canonical r1-bin build must remain.
	if !strings.Contains(source, "r1-bin") {
		t.Error("S6-4 regression: install.sh missing r1-bin canonical build step")
	}
}

// TestS64_Goreleaser_NoStokeBrewFormula asserts .goreleaser.yml no
// longer declares a `- name: stoke` brew formula. Only `- name: r1`
// must remain.
func TestS64_Goreleaser_NoStokeBrewFormula(t *testing.T) {
	source := readRepoFile(t, ".goreleaser.yml")

	// The brew formula block uses the canonical goreleaser key
	//   - name: <formula-name>
	// under the top-level `brews:` section. A simple substring check
	// on `  - name: stoke` is sufficient because the same token would
	// have collided with the build-id block `  - id: stoke` otherwise
	// (which uses `id:` not `name:`).
	if strings.Contains(source, "- name: stoke\n") {
		t.Error("S6-4 regression: .goreleaser.yml still declares a 'stoke' brew formula; must be dropped")
	}
	// Canonical r1 formula must remain.
	if !strings.Contains(source, "- name: r1\n") {
		t.Error("S6-4 regression: .goreleaser.yml missing 'r1' brew formula")
	}
}
