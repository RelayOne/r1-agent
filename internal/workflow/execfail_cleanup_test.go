package workflow

// execfail_cleanup_test.go — regression for the worktree/branch leak on the
// execute-failure return paths in Engine.Run's retry loop. A task that reaches
// Failed is not resumed, so its worktree must be cleaned up rather than left on
// disk. Before the fix, the IsError (agent-reported error) return path returned
// without calling e.Worktrees.Cleanup.

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/RelayOne/r1/internal/engine"
	"github.com/RelayOne/r1/internal/worktree"
)

// cleanupTrackingManager wraps stubManager and counts Cleanup invocations so a
// test can assert the worktree was reclaimed on a failure return path.
type cleanupTrackingManager struct {
	stubManager
	mu           sync.Mutex
	cleanupCalls int
}

func (m *cleanupTrackingManager) Cleanup(ctx context.Context, h worktree.Handle) error {
	m.mu.Lock()
	m.cleanupCalls++
	m.mu.Unlock()
	return m.stubManager.Cleanup(ctx, h)
}

func (m *cleanupTrackingManager) cleanups() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cleanupCalls
}

// TestExecuteAgentErrorCleansUpWorktree drives the workflow to an execute phase
// that returns IsError with a non-rate-limited subtype. That advances the task
// to Failed and returns — and must clean up the worktree so it does not leak.
func TestExecuteAgentErrorCleansUpWorktree(t *testing.T) {
	repo := initTestRepo(t)
	mgr := &cleanupTrackingManager{stubManager: stubManager{repo: repo}}

	runner := newMockRunner()
	runner.FailExecute = true               // execute returns IsError
	runner.ExecuteSubtype = "timeout"       // non-rate-limited → terminal failure

	wf := newRewindEngine(repo, false, mgr, runner)
	_, err := wf.Run(context.Background())
	if err == nil {
		t.Fatal("expected an execute-phase failure error, got nil")
	}
	if !strings.Contains(err.Error(), "execute phase") {
		t.Fatalf("expected execute phase error, got: %v", err)
	}
	if mgr.cleanups() == 0 {
		t.Fatal("worktree leaked: Cleanup was never called on the execute-failure return path")
	}
}

var _ engine.CommandRunner = (*mockRunner)(nil)
