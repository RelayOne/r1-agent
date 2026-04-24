// Tests for the r1 binary wrapper (work-r1-rename.md §S2-3).
//
// These tests build both `stoke` and `r1` from source into a temp
// directory and verify that `r1 --version` produces byte-identical
// output to `stoke --version` — the PR-acceptance criterion for
// zero divergence between the two names.
package main_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// buildBinaries compiles ./cmd/stoke and ./cmd/r1 into dir and
// returns their absolute paths. Skips the test if `go` is not on
// the PATH (e.g., in a minimal CI sandbox).
func buildBinaries(t *testing.T, dir string) (stokePath, r1Path string) {
	t.Helper()

	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not available on PATH")
	}

	// Resolve the module root by walking up from this test file's
	// working directory until go.mod is found. `go test` sets the
	// cwd to the package dir (cmd/r1), so the root is two levels up.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root := cwd
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatalf("could not locate go.mod above %s", cwd)
		}
		root = parent
	}

	stokeName := "stoke"
	r1Name := "r1"
	if runtime.GOOS == "windows" {
		stokeName += ".exe"
		r1Name += ".exe"
	}
	stokePath = filepath.Join(dir, stokeName)
	r1Path = filepath.Join(dir, r1Name)

	build := func(pkg, out string) {
		cmd := exec.Command(goBin, "build", "-o", out, pkg)
		cmd.Dir = root
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("go build %s: %v", pkg, err)
		}
	}
	build("./cmd/stoke", stokePath)
	build("./cmd/r1", r1Path)
	return stokePath, r1Path
}

// TestR1VersionMatchesStoke is the primary acceptance test: the r1
// wrapper must produce identical --version output to stoke, because
// it IS stoke executed via a sibling-binary exec shim.
func TestR1VersionMatchesStoke(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping binary-exec test in -short mode")
	}
	if runtime.GOOS == "windows" {
		// Windows path uses os/exec rather than syscall.Exec; the
		// behaviour is equivalent but the test harness here is
		// Unix-focused. Skip explicitly so CI on Windows stays green.
		t.Skip("skipping on windows; wrapper uses exec.Command fallback")
	}

	dir := t.TempDir()
	stokePath, r1Path := buildBinaries(t, dir)

	runVersion := func(bin string) []byte {
		cmd := exec.Command(bin, "--version")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s --version: %v", filepath.Base(bin), err)
		}
		return bytes.TrimRight(out, "\n")
	}

	stokeOut := runVersion(stokePath)
	r1Out := runVersion(r1Path)

	if !bytes.Equal(stokeOut, r1Out) {
		t.Fatalf("version output mismatch:\n  stoke: %q\n  r1:    %q", stokeOut, r1Out)
	}
	if len(stokeOut) == 0 {
		t.Fatalf("stoke --version produced empty output")
	}
}

// TestR1MissingStokeBinary verifies that r1 fails gracefully with a
// helpful message (and exit 127, the standard command-not-found
// code) when the stoke binary cannot be located.
func TestR1MissingStokeBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping binary-exec test in -short mode")
	}
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	dir := t.TempDir()
	_, r1Path := buildBinaries(t, dir)

	// Remove the sibling stoke binary so only r1 is in `dir`, and
	// scrub PATH + STOKE_BINARY so neither fallback can rescue us.
	stokeName := "stoke"
	if runtime.GOOS == "windows" {
		stokeName += ".exe"
	}
	if err := os.Remove(filepath.Join(dir, stokeName)); err != nil {
		t.Fatalf("remove sibling stoke: %v", err)
	}

	cmd := exec.Command(r1Path, "--version")
	cmd.Env = []string{"PATH=/nonexistent-dir-for-r1-test"}
	out, err := cmd.CombinedOutput()

	var exitErr *exec.ExitError
	if err == nil {
		t.Fatalf("expected r1 to fail without stoke; got success with output: %q", out)
	}
	// exec.ExitError carries the child exit code; check it's 127.
	ok := false
	if e, okCast := err.(*exec.ExitError); okCast {
		exitErr = e
		if exitErr.ExitCode() == 127 {
			ok = true
		}
	}
	if !ok {
		t.Fatalf("expected exit code 127; got err=%v, output=%q", err, out)
	}
	if !bytes.Contains(out, []byte("could not locate `stoke`")) {
		t.Fatalf("expected helpful error message; got: %q", out)
	}
}

// TestR1ForwardsExitCode verifies that exit codes from stoke pass
// through the r1 wrapper unchanged. Uses `stoke unknown-subcommand`
// which exits 2 per cmd/stoke/main.go's default case.
func TestR1ForwardsExitCode(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping binary-exec test in -short mode")
	}
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	dir := t.TempDir()
	stokePath, r1Path := buildBinaries(t, dir)

	runExit := func(bin string) int {
		cmd := exec.Command(bin, "this-subcommand-definitely-does-not-exist")
		cmd.Stdout = nil
		cmd.Stderr = nil
		err := cmd.Run()
		if err == nil {
			return 0
		}
		if e, ok := err.(*exec.ExitError); ok {
			return e.ExitCode()
		}
		t.Fatalf("%s unknown-subcmd: unexpected error %v", filepath.Base(bin), err)
		return -1
	}

	stokeCode := runExit(stokePath)
	r1Code := runExit(r1Path)
	// Assert: wrapper forwards stoke's exit code unchanged.
	if got, want := r1Code, stokeCode; got != want {
		t.Fatalf("exit code mismatch: stoke=%d r1=%d", want, got)
	}
	// Assert: stoke's "unknown subcommand" path still returns 2.
	if got, want := stokeCode, 2; got != want {
		t.Fatalf("expected stoke exit=%d on unknown subcommand; got %d", want, got)
	}
}
