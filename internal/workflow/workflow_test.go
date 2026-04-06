package workflow

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/ericmacdougall/stoke/internal/config"
	"github.com/ericmacdougall/stoke/internal/engine"
	"github.com/ericmacdougall/stoke/internal/failure"
	"github.com/ericmacdougall/stoke/internal/model"
	"github.com/ericmacdougall/stoke/internal/subscriptions"
	"github.com/ericmacdougall/stoke/internal/verify"
	"github.com/ericmacdougall/stoke/internal/worktree"
)

func TestDryRunWorkflowBuildsCommands(t *testing.T) {
	repo := t.TempDir()
	policy := config.DefaultPolicy()
	wf := Engine{
		RepoRoot:     repo,
		Task:         "Update the README",
		TaskType:     model.TaskTypeDocs,
		WorktreeName: "docs-task",
		AuthMode:     engine.AuthModeMode1,
		Policy:       policy,
		DryRun:       true,
		Pools:        subscriptions.NewManager(nil),
		Worktrees:    stubManager{repo: repo},
		Runners:      engine.Registry{Claude: engine.NewClaudeRunner("claude"), Codex: engine.NewCodexRunner("codex")},
		Verifier:     verify.NewPipeline("", "", ""),
	}
	result, err := wf.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Steps) != 3 {
		t.Fatalf("expected 3 phases, got %d", len(result.Steps))
	}
	if !strings.Contains(result.Render(), "plan") {
		t.Fatalf("expected render output to include plan phase")
	}
	if !strings.Contains(result.Render(), "Stoke") {
		t.Error("render should say Stoke, not Forge")
	}
}

func TestDryRunPhaseEngineRouting(t *testing.T) {
	repo := t.TempDir()
	wf := Engine{
		RepoRoot:     repo,
		Task:         "Add Docker support",
		TaskType:     model.TaskTypeDevOps,
		WorktreeName: "devops-task",
		AuthMode:     engine.AuthModeMode1,
		Policy:       config.DefaultPolicy(),
		DryRun:       true,
		Pools:        subscriptions.NewManager(nil),
		Worktrees:    stubManager{repo: repo},
		Runners:      engine.Registry{Claude: engine.NewClaudeRunner("claude"), Codex: engine.NewCodexRunner("codex")},
		Verifier:     verify.NewPipeline("", "", ""),
	}
	result, err := wf.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// Plan always uses Claude
	if result.Steps[0].Engine != "claude" {
		t.Errorf("plan engine=%q, want claude", result.Steps[0].Engine)
	}
	// DevOps execute should use Codex per routing table
	if result.Steps[1].Engine != "codex" {
		t.Errorf("execute engine=%q, want codex for devops", result.Steps[1].Engine)
	}
}

func TestBuildRetryPromptIncludesFailureContext(t *testing.T) {
	analysis := &failure.Analysis{
		Class:     failure.BuildFailed,
		Summary:   "2 TypeScript errors in src/auth.ts",
		RootCause: "Property 'user' does not exist on type 'Request'",
		Specifics: []failure.Detail{
			{File: "src/auth.ts", Line: 45, Message: "TS2339: Property 'user' missing", Fix: "extend Express Request type"},
		},
	}

	prompt := buildRetryPrompt("Original task: add auth", 2, analysis, "src/auth.ts | 45 +++", "/tmp/worktree")

	if !strings.Contains(prompt, "RETRY CONTEXT (attempt 2)") {
		t.Error("should include attempt number")
	}
	if !strings.Contains(prompt, "2 TypeScript errors") {
		t.Error("should include failure summary")
	}
	if !strings.Contains(prompt, "Property 'user'") {
		t.Error("should include root cause")
	}
	if !strings.Contains(prompt, "src/auth.ts:45") {
		t.Error("should include specific file:line")
	}
	if !strings.Contains(prompt, "extend Express Request type") {
		t.Error("should include suggested fix")
	}
	if !strings.Contains(prompt, "src/auth.ts | 45 +++") {
		t.Error("should include diff summary")
	}
	if !strings.Contains(prompt, "DO NOT") {
		t.Error("should include constraints")
	}
}

func TestBuildRetryPromptPolicyViolation(t *testing.T) {
	analysis := &failure.Analysis{Class: failure.PolicyViolation, Summary: "ts-ignore used"}
	prompt := buildRetryPrompt("task", 2, analysis, "", "")
	if !strings.Contains(prompt, "@ts-ignore") {
		t.Error("policy violation retry should warn about ts-ignore")
	}
}

func TestBuildRetryPromptWrongFiles(t *testing.T) {
	analysis := &failure.Analysis{Class: failure.WrongFiles, Summary: "out of scope"}
	prompt := buildRetryPrompt("task", 2, analysis, "", "")
	if !strings.Contains(prompt, "outside the task scope") {
		t.Error("wrong files retry should warn about scope")
	}
}

func TestBuildRetryPromptNoDiff(t *testing.T) {
	analysis := &failure.Analysis{Class: failure.BuildFailed, Summary: "errors"}
	prompt := buildRetryPrompt("task", 2, analysis, "(diff unavailable)", "")
	if strings.Contains(prompt, "CHANGES FROM PREVIOUS ATTEMPT") {
		t.Error("should not include diff section when unavailable")
	}
}

func TestSlugFromTask(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Add request ID middleware", "add-request-id-middleware"},
		{"", "task"},
		{"A/B/C", "a-b-c"},
		{"Short", "short"},
	}
	for _, tt := range tests {
		got := slugFromTask(tt.input)
		if got != tt.want {
			t.Errorf("slugFromTask(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSlugTruncation(t *testing.T) {
	long := strings.Repeat("a", 100)
	got := slugFromTask(long)
	if len(got) > 32 {
		t.Errorf("slug length=%d, should be <= 32", len(got))
	}
}

func TestResultRenderIncludesCost(t *testing.T) {
	r := Result{
		WorktreePath: "/tmp/test",
		Branch:       "stoke/test",
		TaskType:     model.TaskTypeRefactor,
		TotalCostUSD: 0.1234,
	}
	rendered := r.Render()
	if !strings.Contains(rendered, "0.1234") {
		t.Error("render should include cost")
	}
}

// --- stubs ---

type stubManager struct{ repo string }

func (s stubManager) Prepare(_ context.Context, explicitName string) (worktree.Handle, error) {
	path := s.repo + "/.stoke/worktrees/" + explicitName
	runtimeDir := os.TempDir() + "/stoke-test-runtime-" + explicitName
	os.MkdirAll(runtimeDir, 0o755)
	// Initialize a minimal git repo so worktree helpers (ModifiedFiles, DiffSummary) work.
	os.MkdirAll(path, 0o755)
	exec.Command("git", "-C", path, "init", "-b", "main").Run()
	exec.Command("git", "-C", path, "config", "user.email", "test@test.com").Run()
	exec.Command("git", "-C", path, "config", "user.name", "test").Run()
	exec.Command("git", "-C", path, "config", "commit.gpgsign", "false").Run()
	exec.Command("git", "-C", path, "commit", "--allow-empty", "-m", "init").Run()
	baseCommit := "HEAD"
	if out, err := exec.Command("git", "-C", path, "rev-parse", "HEAD").Output(); err == nil {
		baseCommit = strings.TrimSpace(string(out))
	}
	return worktree.Handle{Name: explicitName, Branch: "stoke/" + explicitName, Path: path, RuntimeDir: runtimeDir, BaseCommit: baseCommit, RepoRoot: s.repo, GitBinary: "git"}, nil
}

func (s stubManager) Merge(_ context.Context, _ worktree.Handle, _ string) error { return nil }
func (s stubManager) Cleanup(_ context.Context, _ worktree.Handle) error        { return nil }
