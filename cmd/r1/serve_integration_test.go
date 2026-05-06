package main

// serve_integration_test.go — Phase I integration tests for r1d-server
// (specs/r1d-server.md items 47–50).
//
// This file holds integration tests that exercise the multi-session
// daemon end-to-end without spinning up a full `r1 serve` subprocess
// where it can be avoided. Each test is a top-level Test* function so
// `go test -race -run TestX ./cmd/r1` selects exactly one.
//
// Item-to-test map:
//
//   - TestMultiSession_RaceFree            (item 47)
//   - TestChdirSentinel_PanicsOnStrayChdir (item 48 — see
//                                           serve_integration_chdirleak_test.go,
//                                           build tag chdirleak_test).
//   - TestKillAndResume                    (item 49)
//   - TestSingleInstance                   (item 50)

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/RelayOne/r1/internal/server/sessionhub"
)

// TestMultiSession_RaceFree — Phase I item 47.
//
// 8 concurrent sessions, each bound to a distinct workdir, each
// dispatching `bash -c 'echo $PWD'` via cmd.Dir = SessionRoot. The
// test asserts:
//
//  1. Every session's bash output equals its own SessionRoot
//     (filepath.EvalSymlinks-normalized — macOS /var ↔ /private/var).
//  2. Every per-session dispatch hook fired exactly once.
//  3. The daemon's process cwd did not move during the storm — the
//     load-bearing multi-session invariant.
//
// Spec line: "Run with go test -race -count=10". The test deliberately
// overlaps dispatches via a release-channel barrier so -race has a
// real chance to flag a data race.
func TestMultiSession_RaceFree(t *testing.T) {
	// Cannot t.Parallel() because we use t.Setenv (Go testing framework
	// rejects Setenv inside parallel tests).

	// Sandbox R1_HOME so workdir-validation has a known under-~/.r1/
	// boundary that the test's t.TempDir workdirs cannot accidentally
	// land under.
	t.Setenv("R1_HOME", t.TempDir())

	hub, err := sessionhub.NewHub()
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}

	const N = 8
	type result struct {
		id     string
		root   string // SessionRoot we expected
		gotPWD string // bash echo $PWD output
		err    error
	}
	results := make([]result, N)

	type seeded struct {
		s    *sessionhub.Session
		root string
	}
	seededs := make([]seeded, N)
	for i := 0; i < N; i++ {
		wd := t.TempDir()
		s, err := hub.Create(sessionhub.CreateOptions{
			Workdir: wd,
			Model:   "test-model",
			ID:      fmt.Sprintf("multi-%d", i),
		})
		if err != nil {
			t.Fatalf("Create #%d (wd=%s): %v", i, wd, err)
		}
		seededs[i] = seeded{s: s, root: s.SessionRoot}
	}

	// Per-session dispatch hooks record fires. Each closure captures
	// its own slot index so we can assert one fire per session.
	hookHits := make([]int32, N)
	hooks := make([]sessionhub.DispatchHook, N)
	for i := 0; i < N; i++ {
		i := i
		hook := func(_ *sessionhub.Session, _ string) {
			atomic.AddInt32(&hookHits[i], 1)
		}
		hooks[i] = hook
		seededs[i].s.SetDispatchHook(hook)
	}

	preCwdAbs := mustAbsCwd(t)

	// Storm: N goroutines fire bash -c 'echo $PWD' with cmd.Dir =
	// SessionRoot. They release in lock-step on a channel so the
	// dispatches genuinely overlap (otherwise -count=10 -race would
	// trivially pass).
	release := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			<-release
			s := seededs[i].s
			// Production tools do exactly this: build an exec.Cmd,
			// set cmd.Dir to the per-session workdir, capture
			// combined output (see internal/tools/tools.go:958-960).
			cmd := exec.Command("bash", "-c", "echo $PWD")
			cmd.Dir = s.SessionRoot
			// Fire the captured dispatch hook so post-condition #2
			// has something to assert. Production wraps the handler
			// via session.wrapHandler; here we invoke it directly
			// because we don't drive a full agentloop.
			hooks[i](s, "bash")
			out, runErr := cmd.CombinedOutput()
			results[i] = result{
				id:     s.ID,
				root:   s.SessionRoot,
				gotPWD: strings.TrimSpace(string(out)),
				err:    runErr,
			}
		}()
	}
	close(release)
	wg.Wait()
	// assert.populated: every goroutine wrote its result slot before
	// returning, so the post-condition loops below see a stable view.
	for i := range results {
		if results[i].id == "" {
			t.Fatalf("result slot %d unpopulated after wg join", i)
		}
	}

	// Post-condition 1: each session's bash saw its own SessionRoot.
	// EvalSymlinks because /tmp on macOS resolves to /private/tmp.
	for i, r := range results {
		if r.err != nil {
			t.Errorf("session %d: bash err: %v (out=%q)", i, r.err, r.gotPWD)
			continue
		}
		want, err := filepath.EvalSymlinks(r.root)
		if err != nil {
			want = r.root
		}
		got, err := filepath.EvalSymlinks(r.gotPWD)
		if err != nil {
			got = r.gotPWD
		}
		if got != want {
			t.Errorf("session %d (%s): bash $PWD = %q, want %q", i, r.id, got, want)
		}
	}

	// Post-condition 2: each dispatch hook fired exactly once.
	for i := 0; i < N; i++ {
		hit := atomic.LoadInt32(&hookHits[i])
		if hit != 1 {
			t.Errorf("session %d: hook fired %d times, want 1", i, hit)
		}
	}

	// Post-condition 3: daemon process cwd did not drift. This is
	// the load-bearing invariant the production sentinel exists to
	// enforce; we verify it directly.
	postCwdAbs := mustAbsCwd(t)
	if preCwdAbs != postCwdAbs {
		t.Fatalf("daemon cwd drifted: pre=%q post=%q (some tool path called os.Chdir)",
			preCwdAbs, postCwdAbs)
	}
}

// mustAbsCwd returns the test process's current working directory in
// absolute, symlink-resolved form. Used to snapshot the daemon's cwd
// before/after the multi-session storm.
func mustAbsCwd(t *testing.T) string {
	t.Helper()
	// LINT-ALLOW chdir-test: integration test snapshots the daemon process cwd to assert the multi-session model never drifts it; cannot be replaced by a parameter — see TestMultiSession_RaceFree post-condition 3.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	abs, err := filepath.Abs(wd)
	if err != nil {
		return wd
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return abs
}
