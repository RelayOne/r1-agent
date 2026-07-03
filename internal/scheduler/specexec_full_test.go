package scheduler

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/costtrack"
	"github.com/RelayOne/r1/internal/plan"
	"github.com/RelayOne/r1/internal/specexec"
	"github.com/RelayOne/r1/internal/worktree"
)

// rolloutRecorder captures every base invocation plus the merge/discard
// callback traffic so tests can assert the full-execution contract.
type rolloutRecorder struct {
	mu                 sync.Mutex
	calls              []plan.Task
	merged             []worktree.Handle
	discarded          []worktree.Handle
	discardedByName    []string
	byNameCtxCancelled bool
	mergeErr           error
}

func (r *rolloutRecorder) record(task plan.Task) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, task)
}

func (r *rolloutRecorder) specCalls() []plan.Task {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []plan.Task
	for _, c := range r.calls {
		if strings.Contains(c.ID, "-spec-") {
			out = append(out, c)
		}
	}
	return out
}

func (r *rolloutRecorder) directCalls() []plan.Task {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []plan.Task
	for _, c := range r.calls {
		if !strings.Contains(c.ID, "-spec-") {
			out = append(out, c)
		}
	}
	return out
}

func (r *rolloutRecorder) mergeWinner(_ context.Context, h worktree.Handle, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.mergeErr != nil {
		return r.mergeErr
	}
	r.merged = append(r.merged, h)
	return nil
}

func (r *rolloutRecorder) discardRollout(_ context.Context, h worktree.Handle) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.discarded = append(r.discarded, h)
	return nil
}

func (r *rolloutRecorder) discardByName(ctx context.Context, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ctx.Err() != nil {
		// Cleanup must run on a live, Background-derived ctx even when the
		// run ctx was cancelled — otherwise cancellation disables cleanup.
		r.byNameCtxCancelled = true
	}
	r.discardedByName = append(r.discardedByName, name)
	return nil
}

// fakeHandle returns a handle whose Path is a real (empty) temp dir so
// worktree.DiffText degrades to "" instead of touching the test repo.
func fakeHandle(t *testing.T, name string) worktree.Handle {
	t.Helper()
	return worktree.Handle{Name: name, Path: t.TempDir()}
}

// explicitDesc classifies as intent.ClassExplicit (short, unambiguous,
// no trivial indicators) so rolloutCount yields exactly 2.
const explicitDesc = "add a JSON output flag to the CLI"

func TestWithSpecExecFullSelectsByTestSignal(t *testing.T) {
	rec := &rolloutRecorder{}
	failing := fakeHandle(t, "wt-fail")
	passing := fakeHandle(t, "wt-pass")

	base := func(ctx context.Context, task plan.Task) TaskResult {
		rec.record(task)
		if !strings.Contains(task.ID, "-spec-") {
			t.Errorf("unexpected direct execution %q — winner should merge, not re-run", task.ID)
			return TaskResult{TaskID: task.ID, Success: true}
		}
		if task.PlanOnly {
			t.Errorf("rollout %q ran plan-only; full execution expected", task.ID)
		}
		if !task.NoMerge {
			t.Errorf("rollout %q missing NoMerge; a rollout must never merge itself", task.ID)
		}
		if strings.Contains(task.Description, "approach FAST") {
			// Fast but its tests fail: real signal must beat speed.
			return TaskResult{TaskID: task.ID, Success: true, TestsFailed: 1, DiffLines: 5, Worktree: failing}
		}
		time.Sleep(20 * time.Millisecond)
		return TaskResult{TaskID: task.ID, Success: true, TestsPassed: 1, DiffLines: 100, Worktree: passing}
	}

	wrapped := WithSpecExec(base, SpecExecConfig{
		Approaches:     []string{"approach FAST", "approach GREEN"},
		MaxParallel:    2,
		Timeout:        5 * time.Second,
		FullExecution:  true,
		MergeWinner:    rec.mergeWinner,
		DiscardRollout: rec.discardRollout,
	})

	result := wrapped(context.Background(), plan.Task{ID: "T1", Description: explicitDesc})
	if !result.Success {
		t.Fatalf("expected success, got: %v", result.Error)
	}
	if result.TaskID != "T1" {
		t.Errorf("TaskID = %q, want T1 (winner result must be rewritten to the real task)", result.TaskID)
	}
	if result.Worktree.Name != "" {
		t.Errorf("returned Worktree = %q, want zero-value after merge self-clean", result.Worktree.Name)
	}
	if len(rec.merged) != 1 || rec.merged[0].Name != "wt-pass" {
		t.Errorf("merged = %v, want exactly [wt-pass]", rec.merged)
	}
	if len(rec.discarded) != 1 || rec.discarded[0].Name != "wt-fail" {
		t.Errorf("discarded = %v, want exactly [wt-fail]", rec.discarded)
	}
	if n := len(rec.specCalls()); n != 2 {
		t.Errorf("rollouts = %d, want 2 (explicit task)", n)
	}
}

// TestWithSpecExecFullRecordsWinnerCompletion pins the resume-correctness
// fix: rollouts run under synthetic "-spec-" IDs, so the executor never
// records the real task done. OnWinnerMerged must fire with the REAL task
// ID on merge success so a resumed run doesn't re-execute an already-merged
// task. It must NOT fire on the merge-failure fallback (that path re-runs
// the real task through base, which records completion itself).
func TestWithSpecExecFullRecordsWinnerCompletion(t *testing.T) {
	rec := &rolloutRecorder{}
	passing := fakeHandle(t, "wt-pass")

	base := func(ctx context.Context, task plan.Task) TaskResult {
		rec.record(task)
		return TaskResult{TaskID: task.ID, Success: true, TestsPassed: 1, DiffLines: 10, Worktree: passing}
	}

	var recorded []string
	wrapped := WithSpecExec(base, SpecExecConfig{
		Approaches:     []string{"approach A", "approach B"},
		MaxParallel:    2,
		Timeout:        5 * time.Second,
		FullExecution:  true,
		MergeWinner:    rec.mergeWinner,
		DiscardRollout: rec.discardRollout,
		OnWinnerMerged: func(taskID string) { recorded = append(recorded, taskID) },
	})

	result := wrapped(context.Background(), plan.Task{ID: "T7", Description: explicitDesc})
	if !result.Success {
		t.Fatalf("expected success, got: %v", result.Error)
	}
	if len(recorded) != 1 || recorded[0] != "T7" {
		t.Errorf("OnWinnerMerged fired with %v, want exactly [T7] (the real task ID, not a -spec- ID)", recorded)
	}
}

func TestWithSpecExecFullMergeFailureFallsBack(t *testing.T) {
	rec := &rolloutRecorder{mergeErr: fmt.Errorf("merge-tree conflict with sibling task")}
	hA := fakeHandle(t, "wt-a")
	hB := fakeHandle(t, "wt-b")

	base := func(ctx context.Context, task plan.Task) TaskResult {
		rec.record(task)
		if !strings.Contains(task.ID, "-spec-") {
			// The post-failure fallback execution.
			return TaskResult{TaskID: task.ID, Success: true, CostUSD: 0.5}
		}
		if strings.Contains(task.Description, "approach WIN") {
			return TaskResult{TaskID: task.ID, Success: true, TestsPassed: 2, Worktree: hB, CostUSD: 1.0}
		}
		return TaskResult{TaskID: task.ID, Success: false, Error: fmt.Errorf("build broke"), Worktree: hA, CostUSD: 0.25}
	}

	wrapped := WithSpecExec(base, SpecExecConfig{
		Approaches:     []string{"approach LOSE", "approach WIN"},
		MaxParallel:    2,
		Timeout:        5 * time.Second,
		FullExecution:  true,
		MergeWinner:    rec.mergeWinner,
		DiscardRollout: rec.discardRollout,
	})

	result := wrapped(context.Background(), plan.Task{ID: "T1", Description: explicitDesc})
	if !result.Success {
		t.Fatalf("fallback execution should succeed, got: %v", result.Error)
	}
	if len(rec.merged) != 0 {
		t.Errorf("merged = %v, want none (merge failed)", rec.merged)
	}
	names := map[string]bool{}
	for _, h := range rec.discarded {
		names[h.Name] = true
	}
	if !names["wt-a"] || !names["wt-b"] {
		t.Errorf("discarded = %v, want both the loser AND the unmergeable winner", rec.discarded)
	}
	direct := rec.directCalls()
	if len(direct) != 1 {
		t.Fatalf("direct executions = %d, want exactly 1 fallback", len(direct))
	}
	fb := direct[0]
	if fb.ID != "T1" || fb.NoMerge || fb.PlanOnly {
		t.Errorf("fallback task = %+v, want real task T1 with NoMerge/PlanOnly unset", fb)
	}
	if !strings.Contains(fb.Description, "approach WIN") {
		t.Errorf("fallback prompt missing winning approach:\n%s", fb.Description)
	}
	if !strings.Contains(fb.Description, "build broke") {
		t.Errorf("fallback prompt missing loser insight:\n%s", fb.Description)
	}
	// 1.0 + 0.25 speculation + 0.5 fallback.
	if result.CostUSD < 1.74 || result.CostUSD > 1.76 {
		t.Errorf("CostUSD = %v, want speculation+fallback total 1.75", result.CostUSD)
	}
}

func TestWithSpecExecFullOverBudgetPassthrough(t *testing.T) {
	rec := &rolloutRecorder{}
	tracker := costtrack.NewTracker(0.01, nil)
	tracker.RecordEnvCost("seed", 1.0) // force OverBudget

	base := func(ctx context.Context, task plan.Task) TaskResult {
		rec.record(task)
		return TaskResult{TaskID: task.ID, Success: true}
	}
	wrapped := WithSpecExec(base, SpecExecConfig{
		Approaches:     []string{"a", "b"},
		FullExecution:  true,
		MergeWinner:    rec.mergeWinner,
		DiscardRollout: rec.discardRollout,
		Tracker:        tracker,
	})

	result := wrapped(context.Background(), plan.Task{ID: "T1", Description: explicitDesc})
	if !result.Success {
		t.Fatalf("passthrough failed: %v", result.Error)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.calls) != 1 || rec.calls[0].ID != "T1" || rec.calls[0].NoMerge {
		t.Errorf("calls = %+v, want exactly one unmodified execution", rec.calls)
	}
	if len(rec.merged)+len(rec.discarded) != 0 {
		t.Errorf("merge/discard traffic on an over-budget passthrough: %v / %v", rec.merged, rec.discarded)
	}
}

func TestWithSpecExecFullScalesN(t *testing.T) {
	// 35+ words, no exploratory/ambiguous indicators: intent.ClassOpenEnded.
	openEnded := "redesign the caching layer across the gateway, worker, and session services, " +
		"adding cache invalidation on write, telemetry counters for hit rates, persistence " +
		"across restarts, and regression coverage for the new behavior in every affected package"

	tightTracker := func() *costtrack.Tracker {
		tr := costtrack.NewTracker(1.0, nil)
		tr.RecordEnvCost("seed", 0.85) // <20% remaining clamps N to 2
		return tr
	}

	tests := []struct {
		name         string
		desc         string
		tracker      *costtrack.Tracker
		wantRollouts int
		wantDirect   int
	}{
		{"trivial passthrough", "fix typo in README.md", nil, 0, 1},
		{"explicit gets two", explicitDesc, nil, 2, 0},
		{"exploratory gets three", "investigate the flaky session timeouts in the gateway", nil, 3, 0},
		{"open-ended uses all approaches", openEnded, nil, 4, 0},
		{"tight budget clamps to two", openEnded, tightTracker(), 2, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &rolloutRecorder{}
			var seq int
			var seqMu sync.Mutex
			base := func(ctx context.Context, task plan.Task) TaskResult {
				rec.record(task)
				if !strings.Contains(task.ID, "-spec-") {
					return TaskResult{TaskID: task.ID, Success: true}
				}
				seqMu.Lock()
				seq++
				name := fmt.Sprintf("wt-%d", seq)
				seqMu.Unlock()
				return TaskResult{TaskID: task.ID, Success: true, TestsPassed: 1,
					Worktree: worktree.Handle{Name: name, Path: t.TempDir()}}
			}
			wrapped := WithSpecExec(base, SpecExecConfig{
				Approaches:     []string{"a", "b", "c", "d"},
				MaxParallel:    4,
				Timeout:        5 * time.Second,
				FullExecution:  true,
				MergeWinner:    rec.mergeWinner,
				DiscardRollout: rec.discardRollout,
				Tracker:        tt.tracker,
			})
			result := wrapped(context.Background(), plan.Task{ID: "T1", Description: tt.desc})
			if !result.Success {
				t.Fatalf("run failed: %v", result.Error)
			}
			if got := len(rec.specCalls()); got != tt.wantRollouts {
				t.Errorf("rollouts = %d, want %d", got, tt.wantRollouts)
			}
			if got := len(rec.directCalls()); got != tt.wantDirect {
				t.Errorf("direct executions = %d, want %d", got, tt.wantDirect)
			}
			if tt.wantRollouts > 0 {
				if len(rec.merged) != 1 {
					t.Errorf("merged = %v, want exactly one winner", rec.merged)
				}
				if len(rec.discarded) != tt.wantRollouts-1 {
					t.Errorf("discarded = %d, want %d losers", len(rec.discarded), tt.wantRollouts-1)
				}
			}
		})
	}
}

func TestWithSpecExecFullMissingCallbacksFailsClosed(t *testing.T) {
	rec := &rolloutRecorder{}
	base := func(ctx context.Context, task plan.Task) TaskResult {
		rec.record(task)
		return TaskResult{TaskID: task.ID, Success: true, PlanOutput: "1. step"}
	}
	// FullExecution requested but MergeWinner missing: must degrade to
	// plan-only speculation, never spawning NoMerge rollouts.
	wrapped := WithSpecExec(base, SpecExecConfig{
		Approaches:    []string{"a", "b"},
		MaxParallel:   2,
		Timeout:       5 * time.Second,
		FullExecution: true,
		DiscardRollout: func(context.Context, worktree.Handle) error {
			return nil
		},
	})
	result := wrapped(context.Background(), plan.Task{ID: "T1", Description: explicitDesc})
	if !result.Success {
		t.Fatalf("degraded run failed: %v", result.Error)
	}
	specs := rec.specCalls()
	if len(specs) == 0 {
		t.Fatal("no speculative calls recorded")
	}
	for _, c := range specs {
		if !c.PlanOnly {
			t.Errorf("speculative call %q not plan-only; fail-closed degradation broken", c.ID)
		}
		if c.NoMerge {
			t.Errorf("speculative call %q has NoMerge; would leak a worktree with no discard path", c.ID)
		}
	}
}

func TestWithSpecExecFullAllFailDiscardsAll(t *testing.T) {
	rec := &rolloutRecorder{}
	handles := []worktree.Handle{fakeHandle(t, "wt-1"), fakeHandle(t, "wt-2")}
	var idx int
	var idxMu sync.Mutex

	base := func(ctx context.Context, task plan.Task) TaskResult {
		rec.record(task)
		idxMu.Lock()
		h := handles[idx%len(handles)]
		idx++
		idxMu.Unlock()
		return TaskResult{TaskID: task.ID, Success: false, Error: fmt.Errorf("verify failed"), Worktree: h}
	}
	wrapped := WithSpecExec(base, SpecExecConfig{
		Approaches:     []string{"a", "b"},
		MaxParallel:    2,
		Timeout:        5 * time.Second,
		FullExecution:  true,
		MergeWinner:    rec.mergeWinner,
		DiscardRollout: rec.discardRollout,
	})
	result := wrapped(context.Background(), plan.Task{ID: "T1", Description: explicitDesc})
	if result.Success {
		t.Fatal("expected failure when every rollout fails")
	}
	if result.Error == nil || !strings.Contains(result.Error.Error(), "verify failed") {
		t.Errorf("error should carry rollout insights, got: %v", result.Error)
	}
	if len(rec.merged) != 0 {
		t.Errorf("merged = %v, want none", rec.merged)
	}
	if len(rec.discarded) != 2 {
		t.Errorf("discarded = %d handles, want all 2", len(rec.discarded))
	}
}

// A rollout that timed out (Spec.Timeout) or whose run was cancelled
// returns a zero-valued Handle, so the Handle-based DiscardRollout can't
// reach its worktree and it leaks. The wrapper must fall back to cleaning
// up by the deterministic spec task ID — and that cleanup must survive a
// cancelled run ctx (it runs on a Background-derived, separately-bounded
// ctx).
func TestWithSpecExecFullLeakedRolloutsCleanedByName(t *testing.T) {
	rec := &rolloutRecorder{}
	base := func(ctx context.Context, task plan.Task) TaskResult {
		rec.record(task)
		if !strings.Contains(task.ID, "-spec-") {
			return TaskResult{TaskID: task.ID, Success: true}
		}
		// Timed-out / cancelled rollout: failed, and no Handle to discard.
		return TaskResult{TaskID: task.ID, Success: false, Error: context.DeadlineExceeded}
	}
	wrapped := WithSpecExec(base, SpecExecConfig{
		Approaches:           []string{"a", "b"},
		MaxParallel:          2,
		Timeout:              5 * time.Second,
		FullExecution:        true,
		MergeWinner:          rec.mergeWinner,
		DiscardRollout:       rec.discardRollout,
		DiscardRolloutByName: rec.discardByName,
	})

	// Cancel the run ctx up front: cleanup must still happen.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := wrapped(ctx, plan.Task{ID: "T1", Description: explicitDesc})
	if result.Success {
		t.Fatal("expected failure when every rollout fails")
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.discarded) != 0 {
		t.Errorf("handle-based discards = %v, want none (all rollouts returned zero handles)", rec.discarded)
	}
	got := map[string]bool{}
	for _, n := range rec.discardedByName {
		got[n] = true
	}
	if !got["T1-spec-strategy-1"] || !got["T1-spec-strategy-2"] {
		t.Errorf("by-name discards = %v, want both T1-spec-strategy-1 and T1-spec-strategy-2 (leak swept)", rec.discardedByName)
	}
	if rec.byNameCtxCancelled {
		t.Error("cleanup ran on the cancelled run ctx; a cancelled run must not disable rollout cleanup")
	}
}

func TestWithSpecExecFullEmptyHandleNotMerged(t *testing.T) {
	rec := &rolloutRecorder{}
	base := func(ctx context.Context, task plan.Task) TaskResult {
		rec.record(task)
		if !strings.Contains(task.ID, "-spec-") {
			return TaskResult{TaskID: task.ID, Success: true}
		}
		// Zero-diff rollout: workflow cleaned up, no handle to merge.
		return TaskResult{TaskID: task.ID, Success: true, TestsPassed: 1}
	}
	wrapped := WithSpecExec(base, SpecExecConfig{
		Approaches:     []string{"a", "b"},
		MaxParallel:    2,
		Timeout:        5 * time.Second,
		FullExecution:  true,
		MergeWinner:    rec.mergeWinner,
		DiscardRollout: rec.discardRollout,
	})
	result := wrapped(context.Background(), plan.Task{ID: "T1", Description: explicitDesc})
	if !result.Success {
		t.Fatalf("run failed: %v", result.Error)
	}
	if len(rec.merged) != 0 {
		t.Errorf("merged = %v, want none for zero-value handles", rec.merged)
	}
	if len(rec.directCalls()) != 1 {
		t.Errorf("direct executions = %d, want 1 fallback re-execution", len(rec.directCalls()))
	}
}

func TestWithSpecExecThreadsSelector(t *testing.T) {
	// The comparative Selector must reach specexec.Run through the
	// plan-only branch and be able to override the score-sorted winner.
	var chosen string
	var chosenMu sync.Mutex
	var selectorSeen int

	base := func(ctx context.Context, task plan.Task) TaskResult {
		if task.PlanOnly {
			if strings.Contains(task.Description, "approach SLOW") {
				time.Sleep(30 * time.Millisecond)
			}
			return TaskResult{TaskID: task.ID, Success: true}
		}
		chosenMu.Lock()
		chosen = task.Description
		chosenMu.Unlock()
		return TaskResult{TaskID: task.ID, Success: true}
	}

	wrapped := WithSpecExec(base, SpecExecConfig{
		Approaches:  []string{"approach FAST", "approach SLOW"},
		MaxParallel: 2,
		Timeout:     5 * time.Second,
		Selector: func(ctx context.Context, outcomes []specexec.Outcome) (string, string, error) {
			selectorSeen = len(outcomes)
			// Pick the slower strategy the scorer would rank second.
			return "strategy-2", "reads better on review", nil
		},
	})

	result := wrapped(context.Background(), plan.Task{ID: "T1", Description: "refactor auth module"})
	if !result.Success {
		t.Fatalf("run failed: %v", result.Error)
	}
	if selectorSeen < 2 {
		t.Fatalf("selector saw %d outcomes, want both", selectorSeen)
	}
	chosenMu.Lock()
	defer chosenMu.Unlock()
	if !strings.Contains(chosen, "approach SLOW") {
		t.Errorf("phase-2 prompt = %q, want the selector's pick (approach SLOW)", chosen)
	}
}
