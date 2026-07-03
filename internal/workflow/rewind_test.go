package workflow

// rewind_test.go — RewindOnRetry: a failed attempt restores the
// pre-attempt shadow checkpoint instead of rebuilding the worktree,
// with fail-closed fallback to the §7 rebuild path.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/config"
	"github.com/RelayOne/r1/internal/engine"
	"github.com/RelayOne/r1/internal/model"
	"github.com/RelayOne/r1/internal/taskstate"
	"github.com/RelayOne/r1/internal/verify"
	"github.com/RelayOne/r1/internal/wisdom"
	"github.com/RelayOne/r1/internal/worktree"
)

const rewindProbeFile = "probe-junk.txt"

// rewindProbeRunner writes a build-breaking probe file on the FIRST
// execute call only, so attempt 1 fails verification and attempt 2
// passes — but only if the probe is actually gone from the worktree
// the second attempt runs in.
type rewindProbeRunner struct {
	*mockRunner
	executeCalls int
}

func (r *rewindProbeRunner) Run(ctx context.Context, spec engine.RunSpec, onEvent engine.OnEventFunc) (engine.RunResult, error) {
	if spec.Phase.Name == "execute" {
		r.executeCalls++
		if r.executeCalls == 1 {
			if err := os.WriteFile(filepath.Join(spec.WorktreeDir, rewindProbeFile), []byte("junk\n"), 0o644); err != nil {
				return engine.RunResult{}, err
			}
		}
	}
	return r.mockRunner.Run(ctx, spec, onEvent)
}

func newRewindEngine(repo string, rewind bool, mgr WorktreeManager, runner engine.CommandRunner) Engine {
	policy := config.DefaultPolicy()
	policy.Verification.Build = true // the probe-sensitive gate
	policy.Verification.Tests = false
	policy.Verification.Lint = false
	policy.Verification.ScopeCheck = false
	policy.Verification.CrossModelReview = false

	// Build "command": fails exactly when the probe file is present.
	buildCmd := "if [ -f " + rewindProbeFile + " ]; then echo 'probe present: injected build failure'; exit 1; fi"

	return Engine{
		RepoRoot:       repo,
		RewindOnRetry:  rewind,
		Task:           "Investigate the session model and add user authentication with token refresh, updating middleware, storage, and tests",
		TaskType:       model.TaskTypeRefactor,
		WorktreeName:   "rewind-test",
		AuthMode:       engine.AuthModeMode1,
		Policy:         policy,
		Worktrees:      mgr,
		Runners:        engine.Registry{Claude: engine.NewClaudeRunner("claude")},
		Verifier:       verify.NewPipeline(buildCmd, "", ""),
		State:          taskstate.NewTaskState("rewind-test"),
		Wisdom:         wisdom.NewStore(),
		RunnerOverride: runner,
	}
}

func TestRewindOnRetryRestoresInsteadOfRebuild(t *testing.T) {
	repo := initTestRepo(t)
	mgr := &trackingManager{stubManager: stubManager{repo: repo}}
	runner := &rewindProbeRunner{mockRunner: newMockRunner()}

	wf := newRewindEngine(repo, true, mgr, runner)
	result, err := wf.Run(context.Background())
	// Merge needs a real linked worktree branch; the stub manager can't
	// provide one, so merge-stage errors are tolerated (same as the e2e
	// workflow tests). Everything up to merge must succeed.
	if err != nil && !strings.Contains(err.Error(), "merge") {
		t.Fatalf("workflow failed (non-merge): %v", err)
	}

	if runner.executeCalls != 2 {
		t.Errorf("execute calls = %d, want 2 (fail once, pass on retry)", runner.executeCalls)
	}
	// The core contract: the retry rewound in place — no second Prepare.
	if mgr.prepareCalls != 1 {
		t.Errorf("Prepare calls = %d, want 1 (rewind must not rebuild)", mgr.prepareCalls)
	}
	// The probe written by attempt 1 was removed by the restore.
	if _, serr := os.Stat(filepath.Join(result.WorktreePath, rewindProbeFile)); !os.IsNotExist(serr) {
		t.Errorf("probe file survived the rewind (err %v)", serr)
	}
}

// TestRewindOnRetryOffRebuilds is the regression guard for design
// decision §7: without opt-in, every retry still gets a fresh worktree.
func TestRewindOnRetryOffRebuilds(t *testing.T) {
	repo := initTestRepo(t)
	mgr := &trackingManager{stubManager: stubManager{repo: repo}}
	runner := &rewindProbeRunner{mockRunner: newMockRunner()}

	wf := newRewindEngine(repo, false, mgr, runner)
	if _, err := wf.Run(context.Background()); err != nil && !strings.Contains(err.Error(), "merge") {
		t.Fatalf("workflow failed (non-merge): %v", err)
	}

	if runner.executeCalls != 2 {
		t.Errorf("execute calls = %d, want 2", runner.executeCalls)
	}
	if mgr.prepareCalls != 2 {
		t.Errorf("Prepare calls = %d, want 2 (clean rebuild per §7)", mgr.prepareCalls)
	}
}

// headlessFirstManager hands out a git repo WITHOUT any commit on the
// first Prepare — ShadowCheckpoint cannot seed its index from HEAD, so
// the rewind baseline fails and the retry must fall back to the proven
// Cleanup+Prepare rebuild. Later Prepares delegate to the normal stub.
type headlessFirstManager struct {
	trackingManager
}

func (m *headlessFirstManager) Prepare(ctx context.Context, name string) (worktree.Handle, error) {
	m.prepareCalls++
	if m.prepareCalls > 1 {
		return m.stubManager.Prepare(ctx, name)
	}
	path := filepath.Join(m.repo, ".stoke", "worktrees", name)
	if err := os.MkdirAll(path, 0o755); err != nil {
		return worktree.Handle{}, err
	}
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
	} {
		cmd := exec.Command("git", append([]string{"-C", path}, args...)...)
		if _, err := cmd.CombinedOutput(); err != nil {
			return worktree.Handle{}, err
		}
	}
	runtimeDir := filepath.Join(os.TempDir(), "stoke-test-runtime-"+name)
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return worktree.Handle{}, err
	}
	return worktree.Handle{
		Name: name, Branch: "r1/" + name, Path: path,
		RuntimeDir: runtimeDir, RepoRoot: m.repo, GitBinary: "git",
	}, nil
}

func TestRewindOnRetryFallsBackWhenBaselineUnavailable(t *testing.T) {
	repo := initTestRepo(t)
	mgr := &headlessFirstManager{trackingManager{stubManager: stubManager{repo: repo}}}
	runner := &rewindProbeRunner{mockRunner: newMockRunner()}

	wf := newRewindEngine(repo, true, mgr, runner)
	if _, err := wf.Run(context.Background()); err != nil && !strings.Contains(err.Error(), "merge") {
		t.Fatalf("workflow must complete via the rebuild fallback (non-merge error): %v", err)
	}

	if runner.executeCalls != 2 {
		t.Errorf("execute calls = %d, want 2", runner.executeCalls)
	}
	if mgr.prepareCalls != 2 {
		t.Errorf("Prepare calls = %d, want 2 (fallback to rebuild)", mgr.prepareCalls)
	}
}

// TestBuildSpecTranscriptWiring: every phase dispatch carries a
// transcript path under .stoke/transcripts, and shadow checkpoints
// follow the RewindOnRetry opt-in.
func TestBuildSpecTranscriptWiring(t *testing.T) {
	repo := initTestRepo(t)
	handle := worktree.Handle{Name: "wire-test", Path: repo, RuntimeDir: t.TempDir()}
	phase := engine.PhaseSpec{Name: "execute"}

	e := newRewindEngine(repo, true, stubManager{repo: repo}, nil)
	spec := e.buildSpec(phase, handle)
	if spec.TranscriptPath == "" {
		t.Fatal("TranscriptPath not set")
	}
	want := filepath.Join(repo, ".stoke", "transcripts", "wire-test-execute.jsonl")
	if spec.TranscriptPath != want {
		t.Errorf("TranscriptPath = %q, want %q", spec.TranscriptPath, want)
	}
	if _, err := os.Stat(filepath.Dir(spec.TranscriptPath)); err != nil {
		t.Errorf("transcript dir not created: %v", err)
	}
	if !spec.ShadowCheckpoints {
		t.Error("ShadowCheckpoints should be on when RewindOnRetry is set")
	}

	e.RewindOnRetry = false
	if spec := e.buildSpec(phase, handle); spec.ShadowCheckpoints {
		t.Error("ShadowCheckpoints should follow the RewindOnRetry opt-in")
	}

	e.RewindOnRetry = true
	e.InPlace = true
	if spec := e.buildSpec(phase, handle); spec.ShadowCheckpoints {
		t.Error("ShadowCheckpoints must stay off for in-place runs")
	}
}

// committingProbeRunner writes AND commits a build-breaking probe on the
// FIRST execute call, advancing the branch HEAD. The rewind must undo both
// the file AND the commit so attempt 2 starts from a genuinely clean branch
// (not just a clean working tree with a polluting commit still on HEAD).
type committingProbeRunner struct {
	*mockRunner
	executeCalls int
}

func (r *committingProbeRunner) Run(ctx context.Context, spec engine.RunSpec, onEvent engine.OnEventFunc) (engine.RunResult, error) {
	if spec.Phase.Name == "execute" {
		r.executeCalls++
		if r.executeCalls == 1 {
			if err := os.WriteFile(filepath.Join(spec.WorktreeDir, rewindProbeFile), []byte("junk\n"), 0o644); err != nil {
				return engine.RunResult{}, err
			}
			for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", "intermediate agent commit"}} {
				cmd := exec.Command("git", append([]string{"-C", spec.WorktreeDir}, args...)...)
				if out, err := cmd.CombinedOutput(); err != nil {
					return engine.RunResult{}, fmt.Errorf("git %v: %v\n%s", args, err, out)
				}
			}
		}
	}
	return r.mockRunner.Run(ctx, spec, onEvent)
}

// TestRewindOnRetryResetsBranch is the regression guard for the rewind
// branch-reset fix: an intermediate commit made during a failed attempt
// must not survive into the retry.
func TestRewindOnRetryResetsBranch(t *testing.T) {
	repo := initTestRepo(t)
	mgr := &trackingManager{stubManager: stubManager{repo: repo}}
	runner := &committingProbeRunner{mockRunner: newMockRunner()}

	wf := newRewindEngine(repo, true, mgr, runner)
	result, err := wf.Run(context.Background())
	if err != nil && !strings.Contains(err.Error(), "merge") {
		t.Fatalf("workflow failed (non-merge): %v", err)
	}
	if runner.executeCalls != 2 {
		t.Errorf("execute calls = %d, want 2 (fail once, pass on retry)", runner.executeCalls)
	}
	if mgr.prepareCalls != 1 {
		t.Errorf("Prepare calls = %d, want 1 (rewind must not rebuild)", mgr.prepareCalls)
	}
	wt := result.WorktreePath
	// The probe committed by attempt 1 is gone from the working tree.
	if _, serr := os.Stat(filepath.Join(wt, rewindProbeFile)); !os.IsNotExist(serr) {
		t.Errorf("probe file survived the rewind (err %v)", serr)
	}
	// And the intermediate commit is gone from the branch history.
	if out, gerr := exec.Command("git", "-C", wt, "log", "--oneline").CombinedOutput(); gerr == nil {
		if strings.Contains(string(out), "intermediate agent commit") {
			t.Errorf("intermediate attempt commit survived the rewind:\n%s", out)
		}
	}
	// The committed blob must not be reachable from HEAD.
	if err := exec.Command("git", "-C", wt, "cat-file", "-e", "HEAD:"+rewindProbeFile).Run(); err == nil {
		t.Errorf("probe blob still reachable from HEAD after rewind — branch not reset")
	}
}
