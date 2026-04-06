package stoke_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ericmacdougall/stoke/internal/config"
	"github.com/ericmacdougall/stoke/internal/engine"
	"github.com/ericmacdougall/stoke/internal/failure"
	"github.com/ericmacdougall/stoke/internal/model"
	"github.com/ericmacdougall/stoke/internal/plan"
	"github.com/ericmacdougall/stoke/internal/scheduler"
	"github.com/ericmacdougall/stoke/internal/session"
	"github.com/ericmacdougall/stoke/internal/subscriptions"
	"github.com/ericmacdougall/stoke/internal/taskstate"
	"github.com/ericmacdougall/stoke/internal/verify"
	"github.com/ericmacdougall/stoke/internal/workflow"
	"github.com/ericmacdougall/stoke/internal/worktree"
)

// ===========================================================================
// Dry-run workflow
// ===========================================================================

func TestDryRunWorkflow(t *testing.T) {
	repo := setupGitRepo(t)

	wf := workflow.Engine{
		RepoRoot:     repo,
		Task:         "Add request ID middleware",
		TaskType:     model.TaskTypeRefactor,
		WorktreeName: "test-task",
		AuthMode:     engine.AuthModeMode1,
		Policy:       config.DefaultPolicy(),
		DryRun:       true,
		Pools:        subscriptions.NewManager(nil),
		Worktrees:    worktree.NewManager(repo),
		Runners:      engine.Registry{Claude: engine.NewClaudeRunner("claude"), Codex: engine.NewCodexRunner("codex")},
		Verifier:     verify.NewPipeline("", "", ""),
	}

	result, err := wf.Run(context.Background())
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if len(result.Steps) != 3 {
		t.Errorf("steps=%d, want 3", len(result.Steps))
	}
	if !result.DryRun {
		t.Error("should be marked dry-run")
	}
	if !strings.Contains(result.Render(), "plan") {
		t.Error("render should mention plan phase")
	}
}

// ===========================================================================
// Git worktree lifecycle: create, commit, diff, merge, cleanup
// ===========================================================================

func TestWorktreeCreateMergeCleanup(t *testing.T) {
	repo := setupGitRepo(t)
	mgr := worktree.NewManager(repo)
	ctx := context.Background()

	handle, err := mgr.Prepare(ctx, "merge-test")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if _, err := os.Stat(handle.Path); os.IsNotExist(err) {
		t.Fatal("worktree dir missing")
	}
	if handle.BaseCommit == "" {
		t.Error("BaseCommit should be set")
	}

	// Simulate agent: write files but do NOT commit (as the prompt instructs).
	// This is the actual product flow.
	os.WriteFile(filepath.Join(handle.Path, "new.go"), []byte("package main\n"), 0644)

	// ModifiedFiles should detect uncommitted changes
	files, err := worktree.ModifiedFiles(ctx, handle)
	if err != nil {
		t.Fatalf("ModifiedFiles: %v", err)
	}
	found := false
	for _, f := range files {
		if f == "new.go" {
			found = true
		}
	}
	if !found {
		t.Errorf("new.go not in ModifiedFiles: %v", files)
	}

	// DiffSummary should be non-empty
	summary := worktree.DiffSummary(ctx, handle)
	if summary == "" || summary == "(diff unavailable)" {
		t.Error("DiffSummary should show changes")
	}

	// CommitVerifiedTree snapshots uncommitted work, resets, rebuilds
	if err := worktree.CommitVerifiedTree(ctx, handle, []string{"new.go"}, "add new.go"); err != nil {
		t.Fatalf("CommitVerifiedTree: %v", err)
	}

	// Merge to main
	if err := mgr.Merge(ctx, handle, "feat: add new.go"); err != nil {
		t.Fatalf("Merge: %v", err)
	}

	// File should exist on main
	if _, err := os.Stat(filepath.Join(repo, "new.go")); os.IsNotExist(err) {
		t.Error("merged file missing from main")
	}
	// Worktree should be cleaned up
	if _, err := os.Stat(handle.Path); !os.IsNotExist(err) {
		t.Error("worktree should be removed after merge")
	}
}

// ===========================================================================
// Dirty worktree cleanup (force remove with uncommitted changes)
// ===========================================================================

func TestWorktreeCleanupDirty(t *testing.T) {
	repo := setupGitRepo(t)
	mgr := worktree.NewManager(repo)
	ctx := context.Background()

	handle, _ := mgr.Prepare(ctx, "dirty-test")

	// Write files WITHOUT committing (dirty worktree)
	os.WriteFile(filepath.Join(handle.Path, "uncommitted.go"), []byte("package dirty\n"), 0644)
	os.WriteFile(filepath.Join(handle.Path, "also-dirty.go"), []byte("package dirty\n"), 0644)

	// Cleanup should succeed despite dirty state (--force)
	err := mgr.Cleanup(ctx, handle)
	if err != nil {
		t.Fatalf("Cleanup dirty worktree should succeed with --force: %v", err)
	}

	// Directory should be gone
	if _, err := os.Stat(handle.Path); !os.IsNotExist(err) {
		t.Error("dirty worktree directory should be removed")
	}

	// Dirty files should NOT be on main
	if _, err := os.Stat(filepath.Join(repo, "uncommitted.go")); !os.IsNotExist(err) {
		t.Error("uncommitted files should not leak to main")
	}
}

func TestWorktreeCleanupAlreadyRemoved(t *testing.T) {
	repo := setupGitRepo(t)
	mgr := worktree.NewManager(repo)
	ctx := context.Background()

	handle, _ := mgr.Prepare(ctx, "already-gone")

	// Remove the directory manually (simulating a crash)
	os.RemoveAll(handle.Path)

	// Cleanup should not panic; error is acceptable but not crash
	_ = mgr.Cleanup(ctx, handle)
}

// ===========================================================================
// Parallel worktrees (non-conflicting merges)
// ===========================================================================

func TestParallelWorktreesMerge(t *testing.T) {
	repo := setupGitRepo(t)
	mgr := worktree.NewManager(repo)
	ctx := context.Background()

	h1, _ := mgr.Prepare(ctx, "par-a")
	h2, _ := mgr.Prepare(ctx, "par-b")

	// Agents write files but do NOT commit (actual product flow)
	os.WriteFile(filepath.Join(h1.Path, "a.go"), []byte("package main\n"), 0644)
	if err := worktree.CommitVerifiedTree(ctx, h1, []string{"a.go"}, "add a"); err != nil {
		t.Fatalf("CommitVerifiedTree a: %v", err)
	}

	os.WriteFile(filepath.Join(h2.Path, "b.go"), []byte("package main\n"), 0644)
	if err := worktree.CommitVerifiedTree(ctx, h2, []string{"b.go"}, "add b"); err != nil {
		t.Fatalf("CommitVerifiedTree b: %v", err)
	}

	if err := mgr.Merge(ctx, h1, "feat: a"); err != nil {
		t.Fatalf("merge a: %v", err)
	}
	if err := mgr.Merge(ctx, h2, "feat: b"); err != nil {
		t.Fatalf("merge b: %v", err)
	}

	for _, f := range []string{"a.go", "b.go"} {
		if _, err := os.Stat(filepath.Join(repo, f)); os.IsNotExist(err) {
			t.Errorf("%s missing after parallel merge", f)
		}
	}
}

// ===========================================================================
// Scheduler with dependencies
// ===========================================================================

func TestSchedulerWithDeps(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "stoke-plan.json"), []byte(`{
		"id":"sched-test", "tasks":[
			{"id":"A","description":"first","files":["a.go"]},
			{"id":"B","description":"depends on A","files":["b.go"],"dependencies":["A"]},
			{"id":"C","description":"independent","files":["c.go"]}
		]
	}`), 0644)

	p, err := plan.Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var order []string
	sched := scheduler.New(2)
	results, err := sched.Run(context.Background(), p, func(ctx context.Context, task plan.Task) scheduler.TaskResult {
		mu.Lock()
		order = append(order, task.ID)
		mu.Unlock()
		return scheduler.TaskResult{TaskID: task.ID, Success: true}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Errorf("results=%d", len(results))
	}
	mu.Lock()
	defer mu.Unlock()
	if indexOf(order, "A") >= indexOf(order, "B") {
		t.Error("A must run before B")
	}
}

// ===========================================================================
// Scheduler resume (pre-completed tasks skipped)
// ===========================================================================

func TestSchedulerResume(t *testing.T) {
	p := &plan.Plan{
		ID: "resume-test",
		Tasks: []plan.Task{
			{ID: "DONE", Description: "already done", Status: plan.StatusDone},
			{ID: "PEND", Description: "still pending", Status: plan.StatusPending},
		},
	}

	var executed []string
	sched := scheduler.New(1)
	results, err := sched.Run(context.Background(), p, func(ctx context.Context, task plan.Task) scheduler.TaskResult {
		executed = append(executed, task.ID)
		return scheduler.TaskResult{TaskID: task.ID, Success: true}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Errorf("results=%d, want 2 (1 pre-completed + 1 executed)", len(results))
	}
	// DONE should NOT appear in executed (it was pre-completed in scheduler)
	for _, id := range executed {
		if id == "DONE" {
			t.Error("already-done task should not be re-executed")
		}
	}
}

// ===========================================================================
// apiKeyHelper is JSON null in Mode 1
// ===========================================================================

func TestApiKeyHelperIsJSONNull(t *testing.T) {
	settings := config.BuildClaudeSettings(config.ClaudeSettingsOptions{
		Mode:                  "mode1",
		Phase:                 config.PhasePolicy{BuiltinTools: []string{"Read"}},
		SandboxEnabled:        true,
		SandboxAllowedDomains: []string{"github.com"},
		SandboxAllowWrite:     []string{"/tmp"},
		SandboxAllowRead:      []string{"/tmp"},
	})
	raw, _ := config.MarshalClaudeSettings(settings)
	var parsed map[string]interface{}
	json.Unmarshal(raw, &parsed)
	if parsed["apiKeyHelper"] != nil {
		t.Errorf("apiKeyHelper should be null, got %v", parsed["apiKeyHelper"])
	}
}

// ===========================================================================
// Protected file checking
// ===========================================================================

func TestProtectedFileRejection(t *testing.T) {
	protected := []string{".claude/", ".stoke/", "CLAUDE.md", ".env*", "stoke.policy.yaml"}

	tests := []struct {
		file string
		want bool // true = should be rejected
	}{
		{".claude/settings.json", true},
		{".stoke/session.json", true},
		{"CLAUDE.md", true},
		{".env", true},
		{".env.local", true},
		{".env.production", true},
		{"stoke.policy.yaml", true},
		{"src/auth.ts", false},
		{"README.md", false},
		{"package.json", false},
	}

	for _, tt := range tests {
		violations := verify.CheckProtectedFiles([]string{tt.file}, protected)
		got := len(violations) > 0
		if got != tt.want {
			t.Errorf("CheckProtectedFiles(%q) = %v, want %v", tt.file, got, tt.want)
		}
	}
}

// ===========================================================================
// Scope enforcement
// ===========================================================================

func TestScopeEnforcement(t *testing.T) {
	// Task declares it will modify src/auth/middleware.ts and src/types/auth.ts
	allowed := []string{"src/auth/middleware.ts", "src/types/auth.ts"}

	// Agent actually modified 3 files, one out of scope
	modified := []string{"src/auth/middleware.ts", "src/types/auth.ts", "src/routes/index.ts"}

	violations := verify.CheckScope(modified, allowed)
	if len(violations) != 1 {
		t.Fatalf("violations=%v, want 1 (src/routes/index.ts)", violations)
	}
	if violations[0] != "src/routes/index.ts" {
		t.Errorf("violation=%q", violations[0])
	}
}

func TestScopeNoRestriction(t *testing.T) {
	// No allowed files = no scope restriction
	violations := verify.CheckScope([]string{"anything.go", "anywhere.ts"}, nil)
	if len(violations) != 0 {
		t.Error("nil allowedFiles should impose no restriction")
	}
}

func TestScopeDirectoryPattern(t *testing.T) {
	allowed := []string{"src/auth/"}
	modified := []string{"src/auth/middleware.ts", "src/auth/types.ts", "src/routes/index.ts"}
	violations := verify.CheckScope(modified, allowed)
	if len(violations) != 1 || violations[0] != "src/routes/index.ts" {
		t.Errorf("violations=%v", violations)
	}
}

// ===========================================================================
// Command auto-detection
// ===========================================================================

func TestDetectGoCommands(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\ngo 1.22"), 0644)
	cmds := config.DetectCommands(dir)
	if cmds.Build != "go build ./..." || cmds.Test != "go test ./..." || cmds.Lint != "go vet ./..." {
		t.Errorf("cmds=%+v", cmds)
	}
}

func TestDetectNodeTS(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"scripts":{"build":"tsc","test":"jest"}}`), 0644)
	os.WriteFile(filepath.Join(dir, "tsconfig.json"), []byte(`{}`), 0644)
	cmds := config.DetectCommands(dir)
	if cmds.Build != "npm run build" {
		t.Errorf("build=%q", cmds.Build)
	}
	if cmds.Test != "npm test" {
		t.Errorf("test=%q", cmds.Test)
	}
}

func TestDetectEmpty(t *testing.T) {
	cmds := config.DetectCommands(t.TempDir())
	if cmds.Build != "" || cmds.Test != "" || cmds.Lint != "" {
		t.Errorf("empty dir should yield empty commands: %+v", cmds)
	}
}

// ===========================================================================
// Failure analysis + retry decisions
// ===========================================================================

func TestFailureAnalysisRetryDecision(t *testing.T) {
	// First failure: should retry
	a1 := &failure.Analysis{Class: failure.BuildFailed, Summary: "2 TS errors"}
	d1 := failure.ShouldRetry(a1, 1, nil)
	if d1.Action != failure.Retry {
		t.Error("first failure should retry")
	}

	// Same failure again: should escalate
	a2 := &failure.Analysis{Class: failure.BuildFailed, Summary: "2 TS errors"}
	d2 := failure.ShouldRetry(a2, 2, a1)
	if d2.Action != failure.Escalate {
		t.Error("same failure should escalate")
	}

	// Different failure: should retry
	a3 := &failure.Analysis{Class: failure.TestsFailed, Summary: "1 test failed"}
	d3 := failure.ShouldRetry(a3, 2, a1)
	if d3.Action != failure.Retry {
		t.Error("different failure should retry")
	}

	// Third attempt: should escalate regardless
	d4 := failure.ShouldRetry(a3, 3, a1)
	if d4.Action != failure.Escalate {
		t.Error("third attempt should escalate")
	}
}

// ===========================================================================
// Pool allocator
// ===========================================================================

func TestPoolAcquireReleaseCycle(t *testing.T) {
	m := subscriptions.NewManager([]subscriptions.Pool{
		{ID: "c1", Provider: subscriptions.ProviderClaude, Utilization: 50},
		{ID: "c2", Provider: subscriptions.ProviderClaude, Utilization: 20},
	})

	p, err := m.Acquire(subscriptions.ProviderClaude, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "c2" {
		t.Errorf("should acquire least loaded: got %s", p.ID)
	}

	// c2 is busy, next acquire should get c1
	p2, err := m.Acquire(subscriptions.ProviderClaude, "task-2")
	if err != nil {
		t.Fatal(err)
	}
	if p2.ID != "c1" {
		t.Errorf("should get next available: got %s", p2.ID)
	}

	// Both busy, should fail
	_, err = m.Acquire(subscriptions.ProviderClaude, "task-3")
	if err == nil {
		t.Error("should fail when all pools busy")
	}

	// Release one
	m.Release("c2", false)

	// Should be available again
	p3, err := m.Acquire(subscriptions.ProviderClaude, "task-3")
	if err != nil {
		t.Fatal(err)
	}
	if p3.ID != "c2" {
		t.Errorf("released pool should be available: got %s", p3.ID)
	}
}

// ===========================================================================
// Session persistence + resume
// ===========================================================================

func TestSessionSaveLoadResume(t *testing.T) {
	dir := t.TempDir()
	store := session.New(dir)

	// Save state with 2 done, 1 pending
	state := &session.State{
		PlanID: "resume-plan",
		Tasks: []plan.Task{
			{ID: "T1", Description: "done", Status: plan.StatusDone},
			{ID: "T2", Description: "done", Status: plan.StatusDone},
			{ID: "T3", Description: "pending", Status: plan.StatusPending},
		},
		TotalCostUSD: 0.50,
		StartedAt:    time.Now().Add(-5 * time.Minute),
	}
	store.SaveState(state)

	// Load and verify
	loaded, err := store.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PlanID != "resume-plan" {
		t.Errorf("plan_id=%q", loaded.PlanID)
	}
	done := 0
	for _, task := range loaded.Tasks {
		if task.Status == plan.StatusDone {
			done++
		}
	}
	if done != 2 {
		t.Errorf("done=%d, want 2", done)
	}

	// Clear
	store.ClearState()
	cleared, _ := store.LoadState()
	if cleared != nil {
		t.Error("state should be nil after clear")
	}
}

func TestAttemptHistoryRoundtrip(t *testing.T) {
	dir := t.TempDir()
	store := session.New(dir)

	store.SaveAttempt(session.Attempt{
		TaskID:      "T1",
		Number:      1,
		Success:     false,
		FailClass:   "BuildFailed",
		FailSummary: "TS2339 errors",
		DiffSummary: "+++ src/auth.ts",
	})
	store.SaveAttempt(session.Attempt{
		TaskID:  "T1",
		Number:  2,
		Success: true,
		CostUSD: 0.03,
	})

	attempts, err := store.LoadAttempts("T1")
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempts=%d", len(attempts))
	}
	if attempts[0].FailClass != "BuildFailed" {
		t.Errorf("class=%q", attempts[0].FailClass)
	}
	if attempts[0].DiffSummary != "+++ src/auth.ts" {
		t.Errorf("diff=%q", attempts[0].DiffSummary)
	}

	// Successful retry should create learned pattern
	learning, _ := store.LoadLearning()
	if len(learning.Patterns) == 0 {
		t.Error("expected learned pattern from successful retry")
	}
}

// ===========================================================================
// Credential isolation
// ===========================================================================

func TestClaudeMode1EnvStripsKeys(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ANTHROPIC_API_KEY", "leaked")
	t.Setenv("OPENAI_API_KEY", "leaked")
	t.Setenv("AWS_ACCESS_KEY_ID", "leaked")
	t.Setenv("PATH", "/usr/bin")

	runner := engine.NewClaudeRunner("claude")
	prepared, err := runner.Prepare(engine.RunSpec{
		Prompt:        "test",
		WorktreeDir:   dir,
		RuntimeDir:    filepath.Join(dir, "runtime"),
		Mode:          engine.AuthModeMode1,
		PoolConfigDir: "/pool/claude-1",
		Phase: engine.PhaseSpec{
			Name: "plan", BuiltinTools: []string{"Read"},
			MCPEnabled: false, MaxTurns: 3,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	envStr := strings.Join(prepared.Env, "\n")
	for _, key := range []string{"ANTHROPIC_API_KEY=", "OPENAI_API_KEY=", "AWS_ACCESS_KEY_ID="} {
		if strings.Contains(envStr, key) {
			t.Errorf("Mode 1 should strip %s", key)
		}
	}
	if !strings.Contains(envStr, "CLAUDE_CONFIG_DIR=/pool/claude-1") {
		t.Error("Mode 1 should set CLAUDE_CONFIG_DIR")
	}
}

func TestCodexMode1EnvStripsKeys(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OPENAI_API_KEY", "leaked")
	t.Setenv("CODEX_API_KEY", "leaked")
	t.Setenv("PATH", "/usr/bin")

	runner := engine.NewCodexRunner("codex")
	prepared, err := runner.Prepare(engine.RunSpec{
		Prompt:        "test",
		WorktreeDir:   dir,
		RuntimeDir:    filepath.Join(dir, "runtime"),
		Mode:          engine.AuthModeMode1,
		PoolConfigDir: "/pool/codex-1",
		Phase:         engine.PhaseSpec{Name: "execute", MaxTurns: 20},
	})
	if err != nil {
		t.Fatal(err)
	}

	envStr := strings.Join(prepared.Env, "\n")
	for _, key := range []string{"OPENAI_API_KEY=", "CODEX_API_KEY="} {
		if strings.Contains(envStr, key) {
			t.Errorf("Codex Mode 1 should strip %s", key)
		}
	}
	if !strings.Contains(envStr, "CODEX_HOME=/pool/codex-1") {
		t.Error("Codex Mode 1 should set CODEX_HOME")
	}
}

// ===========================================================================
// MCP isolation in prepared commands
// ===========================================================================

func TestMCPDisabledPhaseHasFullIsolation(t *testing.T) {
	dir := t.TempDir()
	runner := engine.NewClaudeRunner("claude")
	prepared, err := runner.Prepare(engine.RunSpec{
		Prompt:      "test",
		WorktreeDir: dir,
		RuntimeDir:  filepath.Join(dir, "runtime"),
		Mode:        engine.AuthModeMode2,
		Phase: engine.PhaseSpec{
			Name: "plan", BuiltinTools: []string{"Read"},
			DeniedRules: []string{}, MCPEnabled: false, MaxTurns: 3,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(prepared.Args, " ")

	if !strings.Contains(joined, "--strict-mcp-config") {
		t.Error("MCP-disabled needs --strict-mcp-config")
	}
	if !strings.Contains(joined, "mcp__*") {
		t.Error("MCP-disabled needs mcp__* in disallowedTools")
	}
}

func TestMCPEnabledPhaseNoRestrictions(t *testing.T) {
	dir := t.TempDir()
	runner := engine.NewClaudeRunner("claude")
	prepared, err := runner.Prepare(engine.RunSpec{
		Prompt:      "test",
		WorktreeDir: dir,
		RuntimeDir:  filepath.Join(dir, "runtime"),
		Mode:        engine.AuthModeMode2,
		Phase: engine.PhaseSpec{
			Name: "execute", BuiltinTools: []string{"Read"},
			DeniedRules: []string{"Bash(rm *)"}, MCPEnabled: true, MaxTurns: 20,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(prepared.Args, " ")

	if strings.Contains(joined, "--strict-mcp-config") {
		t.Error("MCP-enabled should not have --strict-mcp-config")
	}
	if strings.Contains(joined, "mcp__*") {
		t.Error("MCP-enabled should not block mcp tools")
	}
}

// ===========================================================================
// Gitignored file invariants (GPT review blocker #1)
// verified tree == merged tree: if ignored files exist, task must fail
// ===========================================================================

func TestIgnoredNewFiles_TrackedPlusIgnored(t *testing.T) {
	repo := setupGitRepo(t)
	mgr := worktree.NewManager(repo)
	ctx := context.Background()

	// Add a .gitignore
	os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("*.local\n"), 0644)
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	git("add", "-A")
	git("commit", "-m", "add gitignore")

	h, err := mgr.Prepare(ctx, "ignored-test")
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Cleanup(ctx, h)

	// Agent creates a tracked file and an ignored file
	os.WriteFile(filepath.Join(h.Path, "app.txt"), []byte("tracked\n"), 0644)
	os.WriteFile(filepath.Join(h.Path, "secret.local"), []byte("ignored secret\n"), 0644)

	// ModifiedFiles should return the tracked file but NOT the ignored one
	modified, err := worktree.ModifiedFiles(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	hasTracked := false
	for _, f := range modified {
		if f == "app.txt" {
			hasTracked = true
		}
		if f == "secret.local" {
			t.Error("ModifiedFiles should NOT include gitignored files")
		}
	}
	if !hasTracked {
		t.Error("ModifiedFiles should include tracked file app.txt")
	}

	// IgnoredNewFiles should detect the ignored file
	ignored := worktree.IgnoredNewFiles(ctx, h)
	if len(ignored) == 0 {
		t.Fatal("IgnoredNewFiles should detect secret.local")
	}
	found := false
	for _, f := range ignored {
		if f == "secret.local" {
			found = true
		}
	}
	if !found {
		t.Errorf("IgnoredNewFiles should include secret.local, got %v", ignored)
	}

	// CommitVerifiedTree should NOT include the ignored file
	err = worktree.CommitVerifiedTree(ctx, h, []string{"app.txt"}, "add tracked only")
	if err != nil {
		t.Fatalf("CommitVerifiedTree: %v", err)
	}

	// Verify the commit tree does NOT contain the ignored file
	lsCmd := exec.CommandContext(ctx, "git", "ls-tree", "-r", "--name-only", "HEAD")
	lsCmd.Dir = h.Path
	lsOut, _ := lsCmd.Output()
	if strings.Contains(string(lsOut), "secret.local") {
		t.Error("committed tree should NOT contain gitignored file secret.local")
	}
}

func TestIgnoredNewFiles_IgnoredOnly(t *testing.T) {
	repo := setupGitRepo(t)
	mgr := worktree.NewManager(repo)
	ctx := context.Background()

	// Add a .gitignore
	os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("*.local\n"), 0644)
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	git("add", "-A")
	git("commit", "-m", "add gitignore")

	h, err := mgr.Prepare(ctx, "ignored-only")
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Cleanup(ctx, h)

	// Agent creates ONLY an ignored file
	os.WriteFile(filepath.Join(h.Path, "secret.local"), []byte("secret\n"), 0644)

	// ModifiedFiles returns empty (only untracked, but ignored by gitignore)
	modified, err := worktree.ModifiedFiles(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	if len(modified) > 0 {
		t.Errorf("ModifiedFiles should be empty for ignored-only case, got %v", modified)
	}

	// IgnoredNewFiles should still detect it
	ignored := worktree.IgnoredNewFiles(ctx, h)
	if len(ignored) == 0 {
		t.Fatal("IgnoredNewFiles should detect secret.local even when no tracked changes")
	}

	// CommitVerifiedTree should return ErrNothingToCommit
	err = worktree.CommitVerifiedTree(ctx, h, []string{}, "nothing")
	if err != worktree.ErrNothingToCommit {
		t.Errorf("expected ErrNothingToCommit, got: %v", err)
	}
}

func TestAllGatesPass_RejectsWarnings(t *testing.T) {
	// Evidence with all gates passing but with warnings should NOT pass
	ev := taskstate.Evidence{
		BuildPass:      true,
		TestPass:       true,
		LintPass:       true,
		ScopeClean:     true,
		ProtectedClean: true,
		ReviewPass:     true,
		Warnings:       []string{"gitignored files present"},
	}
	if ev.AllGatesPass() {
		t.Error("AllGatesPass should return false when warnings are present")
	}

	// Same evidence without warnings should pass
	ev.Warnings = nil
	if !ev.AllGatesPass() {
		t.Error("AllGatesPass should return true with no warnings and all gates passing")
	}
}

func TestPlanCycleDetectionSelfLoop(t *testing.T) {
	p := &plan.Plan{
		ID: "test-self-loop",
		Tasks: []plan.Task{
			{ID: "A", Description: "task A", Dependencies: []string{"A"}},
		},
	}
	errs := p.Validate()
	hasCycle := false
	for _, e := range errs {
		if strings.Contains(e, "cycle") || strings.Contains(e, "self-loop") {
			hasCycle = true
		}
	}
	if !hasCycle {
		t.Errorf("Validate should detect self-loop, got errors: %v", errs)
	}
}

func TestPlanCycleDetectionMutual(t *testing.T) {
	p := &plan.Plan{
		ID: "test-mutual-cycle",
		Tasks: []plan.Task{
			{ID: "A", Description: "task A", Dependencies: []string{"B"}},
			{ID: "B", Description: "task B", Dependencies: []string{"A"}},
		},
	}
	errs := p.Validate()
	hasCycle := false
	for _, e := range errs {
		if strings.Contains(e, "cycle") {
			hasCycle = true
		}
	}
	if !hasCycle {
		t.Errorf("Validate should detect mutual dependency cycle, got errors: %v", errs)
	}
}

// ===========================================================================
// Helpers
// ===========================================================================

func setupGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	git("init")
	git("config", "user.email", "test@stoke.dev")
	git("config", "user.name", "Stoke Test")
	git("config", "commit.gpgsign", "false")
	git("checkout", "-b", "main")
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0644)
	git("add", "-A")
	git("commit", "-m", "initial")
	return dir
}

func indexOf(s []string, val string) int {
	for i, v := range s {
		if v == val {
			return i
		}
	}
	return -1
}
