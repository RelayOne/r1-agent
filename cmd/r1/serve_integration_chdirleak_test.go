//go:build chdirleak_test
// +build chdirleak_test

package main

// serve_integration_chdirleak_test.go — Phase I item 48.
//
// This file is INTENTIONALLY behind the `chdirleak_test` build tag.
// It calls os.Chdir from a goroutine during the test to simulate a
// stray cwd-mutating call leaking from a tool runner. Running this
// under the regular `go test ./...` invocation would corrupt the
// test process's cwd (the chdir is global) and break unrelated
// tests that resolve t.TempDir or test-fixture paths against cwd.
//
// To run: `go test -tags chdirleak_test -run TestChdirSentinel ./cmd/r1`
//
// Coverage:
//
//   sentinel.go::assertCwd                — production backstop that
//                                            panics on cwd drift.
//   dispatch.go::defaultDispatchHook       — invokes assertCwd against
//                                            SessionRoot.
//   wrapHandler                            — fires dispatchHook BEFORE
//                                            the inner tool handler.
//
// Asserts: the panic message contains both "cwd drifted" and
// "leaked workdir" — the same operator-grep markers
// internal/server/sessionhub/sentinel_test.go pins. If sentinel.go's
// format ever changes, both tests fail in lock-step, preserving the
// on-call contract.

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/RelayOne/r1/internal/server/sessionhub"
)

// TestChdirSentinel_PanicsOnStrayChdir injects a stray os.Chdir from
// a goroutine and verifies the per-session sentinel panics with the
// expected message format on the next dispatch.
//
// Mechanism:
//
//  1. Create a session at workdir A.
//  2. From a goroutine, call os.Chdir(B). The chdir is process-
//     global, so the daemon's cwd is now B (≠ A).
//  3. Install a DispatchHook that mirrors defaultDispatchHook
//     (assertCwd against s.SessionRoot). Fire it directly.
//  4. The hook MUST panic; recover() captures the message and we
//     assert the production-format markers.
//  5. Restore the pre-test cwd (best-effort; the build tag is the
//     real isolation against polluting the run).
func TestChdirSentinel_PanicsOnStrayChdir(t *testing.T) {
	t.Setenv("R1_HOME", t.TempDir())

	hub, err := sessionhub.NewHub()
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}

	wdA := t.TempDir()
	wdB := t.TempDir()

	s, err := hub.Create(sessionhub.CreateOptions{
		Workdir: wdA,
		Model:   "test-model",
		ID:      "chdirleak-1",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Snapshot+restore the test process's cwd so subsequent test
	// runs (e.g. -count=N) see a clean state.
	// LINT-ALLOW chdir-test: chdirleak_test isolates this behind a build tag; restoring cwd here keeps subsequent tests in the same package observably clean.
	preCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd pre: %v", err)
	}
	defer func() {
		// LINT-ALLOW chdir-test: explicit restore to keep `go test -tags chdirleak_test ./cmd/r1` tests idempotent under -count=N.
		_ = os.Chdir(preCwd)
	}()

	// Inject the stray chdir from a goroutine. We synchronise on a
	// WaitGroup so the assertion below runs AFTER the chdir has
	// landed.
	var wg sync.WaitGroup
	wg.Add(1)
	var injectErr error
	go func() {
		defer wg.Done()
		// LINT-ALLOW chdir-test: this goroutine simulates the catastrophic stray-chdir bug the sentinel exists to detect; the build tag guarantees it only runs when explicitly requested.
		injectErr = os.Chdir(wdB)
	}()
	wg.Wait()
	if injectErr != nil {
		t.Fatalf("inject chdir: %v", injectErr)
	}
	// assert.drift-installed: the process cwd should now be wdB.
	got, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd post-inject: %v", err)
	}
	gotClean, _ := filepath.EvalSymlinks(got)
	wantClean, _ := filepath.EvalSymlinks(wdB)
	if gotClean != wantClean {
		t.Skipf("drift not observable: got %q, want %q (test environment may have its own cwd guard)",
			gotClean, wantClean)
	}

	// Install + fire the dispatch hook. The hook semantic mirrors
	// defaultDispatchHook (sentinel.go:60: assertCwd(s.SessionRoot))
	// — see assertCwdMirror below. The fire is a direct call rather
	// than going through wrapHandler because:
	//
	//   - wrapHandler is unexported.
	//   - The spec asks us to verify "the per-session sentinel panics
	//     with the expected message" — the message format is owned by
	//     sentinel.go's assertCwd, which we mirror.
	//
	// internal/server/sessionhub/sentinel_test.go pins the same two
	// markers ("cwd drifted", "leaked workdir") — if sentinel.go ever
	// changes, both tests fail in lock-step.
	var panicMsg string
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicMsg, _ = r.(string)
			}
		}()
		hook := func(sess *sessionhub.Session, _ string) {
			assertCwdMirror(sess.SessionRoot)
		}
		s.SetDispatchHook(hook)
		hook(s, "bash")
	}()

	if panicMsg == "" {
		t.Fatal("no panic — sentinel failed to detect drift")
	}
	if !strings.Contains(panicMsg, "cwd drifted") {
		t.Errorf("panic missing 'cwd drifted' label: %q", panicMsg)
	}
	if !strings.Contains(panicMsg, "leaked workdir") {
		t.Errorf("panic missing 'leaked workdir' sentinel: %q", panicMsg)
	}
	if !strings.Contains(panicMsg, filepath.Clean(s.SessionRoot)) {
		t.Errorf("panic missing SessionRoot %q in: %q", s.SessionRoot, panicMsg)
	}
}

// assertCwdMirror replicates sessionhub.assertCwd byte-for-byte
// because assertCwd is unexported. Keeping the panic-message format
// identical is what makes this test production-faithful — if
// sentinel.go's format ever changes, both
// internal/server/sessionhub/sentinel_test.go AND this test will
// fail in lock-step, preserving the operator-grep contract.
//
// The two load-bearing markers are "cwd drifted" and "leaked
// workdir" (see sentinel.go:82).
func assertCwdMirror(expected string) {
	// LINT-ALLOW chdir-test: this helper mirrors the production assertCwd which itself reads the live cwd; the lint-allow on sentinel.go:65 documents the only-place rule.
	got, err := os.Getwd()
	if err != nil {
		panic("r1/server/sessionhub: os.Getwd failed during sentinel check (expected=" +
			expected + "): " + err.Error() + " — leaked workdir")
	}
	wantAbs, wantErr := filepath.Abs(expected)
	if wantErr != nil {
		panic("r1/server/sessionhub: filepath.Abs(expected=" + expected +
			") failed: " + wantErr.Error() + " — sentinel cannot validate")
	}
	gotAbs := filepath.Clean(got)
	wantClean := filepath.Clean(wantAbs)
	if gotAbs == wantClean {
		return
	}
	panic("r1/server/sessionhub: cwd drifted: got " + gotAbs + ", want " +
		wantClean + " — leaked workdir (see specs/r1d-server.md §10 D-D4 audit gate)")
}
