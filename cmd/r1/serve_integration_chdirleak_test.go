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
// NOTE: no automated lane invokes this tag today — this is a manual
// diagnostic. The ALWAYS-RUNNING coverage for the sentinel chain lives
// in internal/server/sessionhub/dispatch_test.go
// (TestDispatchTool_PanicsOnCwdDrift, which drives the real
// wrapHandler + defaultDispatchHook + assertCwd) and sentinel_test.go,
// both in the default `go test ./...` gate.
//
// Coverage — all PRODUCTION code, driven through the public API
// (no test-local mirror of the sentinel exists in this file):
//
//   sessionhub.Session.Run                 — installs defaultDispatchHook
//                                            when no DispatchHook is set.
//   dispatch.go::defaultDispatchHook       — invokes assertCwd against
//                                            SessionRoot.
//   wrapHandler                            — fires the hook BEFORE the
//                                            inner tool handler.
//   sentinel.go::assertCwd                 — panics on cwd drift.
//
// Asserts: the panic message contains both "cwd drifted" and
// "leaked workdir" — the same operator-grep markers
// internal/server/sessionhub/sentinel_test.go pins. Because this test
// recovers the panic emitted by the PRODUCTION assertCwd (not a copy),
// any change to sentinel.go's format or a gutted sentinel fails this
// test whenever it is run.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/RelayOne/r1/internal/agentloop"
	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/server/sessionhub"
	"github.com/RelayOne/r1/internal/stream"
)

// chdirLeakProvider drives one tool_use turn followed by an end_turn,
// forcing exactly one tool dispatch per Run so the sentinel's
// pre-handler hook fires exactly once. Mirrors the toolUseProvider
// pattern proven in internal/server/sessionhub/dispatch_test.go.
type chdirLeakProvider struct {
	idx atomic.Int64
}

func (p *chdirLeakProvider) Name() string { return "chdirleak-tool-use" }
func (p *chdirLeakProvider) Chat(_ provider.ChatRequest) (*provider.ChatResponse, error) {
	i := p.idx.Add(1) - 1
	if i == 0 {
		return &provider.ChatResponse{
			Content: []provider.ResponseContent{
				{Type: "tool_use", ID: "u1", Name: "noop",
					Input: map[string]any{}},
			},
			StopReason: "tool_use",
		}, nil
	}
	return &provider.ChatResponse{
		Content:    []provider.ResponseContent{{Type: "text", Text: "done"}},
		StopReason: "end_turn",
	}, nil
}
func (p *chdirLeakProvider) ChatStream(req provider.ChatRequest, _ func(stream.Event)) (*provider.ChatResponse, error) {
	return p.Chat(req)
}

// TestChdirSentinel_PanicsOnStrayChdir injects a stray os.Chdir from
// a goroutine and verifies the PRODUCTION per-session sentinel panics
// with the expected message format on the next tool dispatch.
//
// Mechanism:
//
//  1. Create a session at workdir A.
//  2. From a goroutine, call os.Chdir(B). The chdir is process-
//     global, so the daemon's cwd is now B (≠ A).
//  3. Drive Session.Run with a mock provider that issues one
//     tool_use. NO DispatchHook is installed, so Run falls back to
//     defaultDispatchHook, which calls the real assertCwd against
//     s.SessionRoot via wrapHandler — the exact production chain.
//  4. The dispatch MUST panic out of Run; recover() captures the
//     message and we assert the production-format markers.
//  5. Restore the pre-test cwd (best-effort; the build tag is the
//     real isolation against polluting the run).
func TestChdirSentinel_PanicsOnStrayChdir(t *testing.T) {
	t.Setenv("R1_HOME", t.TempDir())

	sh, err := sessionhub.NewHub()
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}

	wdA := t.TempDir()
	wdB := t.TempDir()

	s, err := sh.Create(sessionhub.CreateOptions{
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
	// WaitGroup so the dispatch below runs AFTER the chdir has
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
	// assert.drift-installed: the process cwd must now be wdB. The
	// fixture is fully controlled, so failure here is a broken
	// fixture, not an environment quirk.
	got, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd post-inject: %v", err)
	}
	gotClean, _ := filepath.EvalSymlinks(got)
	wantClean, _ := filepath.EvalSymlinks(wdB)
	if gotClean != wantClean {
		t.Fatalf("fixture broken: drift not observable: got %q, want %q", gotClean, wantClean)
	}

	// Drive the production dispatch chain. DispatchHook is left
	// UNSET so Session.Run installs defaultDispatchHook, which fires
	// the real sentinel.go::assertCwd through wrapHandler before the
	// inner handler. The panic propagates out of Run (agentloop runs
	// a single tool call on the caller's goroutine and installs no
	// recover), where we capture it.
	var innerReached atomic.Bool
	handler := func(_ context.Context, _ string, _ json.RawMessage) (string, error) {
		innerReached.Store(true)
		return "ok", nil
	}

	var panicked bool
	var panicMsg string
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
				panicMsg, _ = r.(string)
			}
		}()
		_, _ = s.Run(context.Background(), sessionhub.RunOptions{
			Provider:    &chdirLeakProvider{},
			Handler:     handler,
			LoopConfig:  agentloop.Config{MaxTurns: 3},
			UserMessage: "go",
		})
	}()

	if !panicked {
		t.Fatal("no panic — production sentinel failed to detect cwd drift")
	}
	if innerReached.Load() {
		t.Error("inner tool handler ran despite cwd drift — wrapHandler must fire the sentinel FIRST")
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
