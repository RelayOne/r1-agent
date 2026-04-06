package app

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/ericmacdougall/stoke/internal/config"
	"github.com/ericmacdougall/stoke/internal/costtrack"
	"github.com/ericmacdougall/stoke/internal/engine"
	"github.com/ericmacdougall/stoke/internal/model"
	"github.com/ericmacdougall/stoke/internal/replay"
	"github.com/ericmacdougall/stoke/internal/repomap"
	"github.com/ericmacdougall/stoke/internal/subscriptions"
	"github.com/ericmacdougall/stoke/internal/taskstate"
	"github.com/ericmacdougall/stoke/internal/testselect"
	"github.com/ericmacdougall/stoke/internal/verify"
	"github.com/ericmacdougall/stoke/internal/wisdom"
	"github.com/ericmacdougall/stoke/internal/workflow"
	"github.com/ericmacdougall/stoke/internal/worktree"
)

// AuthMode specifies whether the orchestrator uses subscription credentials (mode1) or user-provided API keys (mode2).
type AuthMode string

const (
	AuthModeMode1 AuthMode = "mode1"
	AuthModeMode2 AuthMode = "mode2"
)

// RunConfig holds all parameters needed to execute a single task through the workflow engine.
type RunConfig struct {
	RepoRoot         string
	PolicyPath       string
	Task             string
	TaskType         string
	TaskVerification []string // per-task verification checklist from planner
	WorktreeName     string
	AllowedFiles     []string
	DryRun           bool
	AuthMode         AuthMode
	ClaudeBinary     string
	CodexBinary      string
	ClaudeConfigDir  string
	CodexHome        string
	Pools            *subscriptions.Manager
	Worktrees        *worktree.Manager
	State            *taskstate.TaskState
	Wisdom           *wisdom.Store       // cross-task learning (nil = disabled)
	CostTracker      *costtrack.Tracker  // per-session cost tracking (nil = disabled)
	Recorder         *replay.Recorder    // session replay recording (nil = disabled)
	TestGraph        *testselect.Graph   // dependency-aware test selection (nil = run all)
	RepoMap          *repomap.RepoMap    // ranked codebase map for context (nil = disabled)
	PlanOnly         bool
	BuildCommand     string
	TestCommand      string
	LintCommand      string
	OnEvent          engine.OnEventFunc
}

// Orchestrator coordinates policy loading, engine selection, worktree management, and verification for a task.
type Orchestrator struct {
	cfg    RunConfig
	policy config.Policy
}

// New creates an Orchestrator from the given config, loading and validating the policy file.
func New(cfg RunConfig) (*Orchestrator, error) {
	if cfg.State == nil {
		return nil, fmt.Errorf("task state is required (anti-deception: no legacy mode)")
	}
	if cfg.AuthMode == "" {
		cfg.AuthMode = AuthModeMode1
	}
	policy, err := config.AutoLoadPolicy(cfg.RepoRoot, cfg.PolicyPath)
	if err != nil {
		return nil, err
	}
	// Validate policy structure
	for _, ve := range config.ValidatePolicy(policy) {
		if ve.Fatal {
			return nil, fmt.Errorf("policy error: %s", ve)
		}
	}
	return &Orchestrator{cfg: cfg, policy: policy}, nil
}

// DefaultPolicyYAML returns the default Stoke policy as a YAML string.
func DefaultPolicyYAML() string {
	return config.DefaultPolicyYAML()
}

// Run executes the full workflow: auto-detects build commands, sets up worktrees, and runs plan/execute/verify phases.
func (o *Orchestrator) Run(ctx context.Context) (workflow.Result, error) {
	// Auto-detect commands if not specified
	buildCmd, testCmd, lintCmd := o.cfg.BuildCommand, o.cfg.TestCommand, o.cfg.LintCommand
	if buildCmd == "" || testCmd == "" || lintCmd == "" {
		detected := config.DetectCommands(o.cfg.RepoRoot)
		if buildCmd == "" {
			buildCmd = detected.Build
		}
		if testCmd == "" {
			testCmd = detected.Test
		}
		if lintCmd == "" {
			lintCmd = detected.Lint
		}
	}

	verifier := verify.NewPipeline(buildCmd, testCmd, lintCmd)

	// Use shared worktree manager if provided (critical for parallel builds:
	// the merge mutex must be shared across all tasks in a build session).
	// If not provided, create a per-task manager (fine for single-task `stoke run`).
	worktrees := o.cfg.Worktrees
	if worktrees == nil {
		worktrees = worktree.NewManager(o.cfg.RepoRoot)
	}

	// Use provided pool manager (multi-pool) or create default single-pool
	pools := o.cfg.Pools
	if pools == nil {
		pools = subscriptions.NewManager([]subscriptions.Pool{
			{ID: "claude-1", Provider: subscriptions.ProviderClaude, ConfigDir: o.cfg.ClaudeConfigDir},
			{ID: "codex-1", Provider: subscriptions.ProviderCodex, ConfigDir: o.cfg.CodexHome},
		})
	}

	runners := engine.Registry{
		Claude: engine.NewClaudeRunner(o.cfg.ClaudeBinary),
		Codex:  engine.NewCodexRunner(o.cfg.CodexBinary),
	}

	taskType := model.InferTaskType(o.cfg.Task)
	if strings.TrimSpace(o.cfg.TaskType) != "" {
		taskType = model.TaskType(strings.ToLower(strings.TrimSpace(o.cfg.TaskType)))
	}

	wf := workflow.Engine{
		RepoRoot:         o.cfg.RepoRoot,
		Task:             o.cfg.Task,
		TaskType:         taskType,
		TaskVerification: o.cfg.TaskVerification,
		WorktreeName:     o.cfg.WorktreeName,
		AllowedFiles:     o.cfg.AllowedFiles,
		AuthMode:         engine.AuthMode(o.cfg.AuthMode),
		Policy:           o.policy,
		DryRun:           o.cfg.DryRun,
		Pools:            pools,
		Worktrees:        worktrees,
		Runners:          runners,
		Verifier:         verifier,
		ClaudeConfigDir:  o.cfg.ClaudeConfigDir,
		CodexHome:        o.cfg.CodexHome,
		OnEvent:          o.cfg.OnEvent,
		State:            o.cfg.State,
		Wisdom:           o.cfg.Wisdom,
		CostTracker:      o.cfg.CostTracker,
		Recorder:         o.cfg.Recorder,
		TestGraph:        o.cfg.TestGraph,
		RepoMap:          o.cfg.RepoMap,
		PlanOnly:         o.cfg.PlanOnly,
	}
	return wf.Run(ctx)
}

// Doctor checks whether the claude and codex binaries are available on PATH and returns a diagnostic report.
func Doctor(claudeBin, codexBin string) string {
	var b strings.Builder
	check := func(label, bin string) {
		if path, err := exec.LookPath(bin); err == nil {
			fmt.Fprintf(&b, "[ok] %s: %s\n", label, path)
		} else {
			fmt.Fprintf(&b, "[missing] %s: %v\n", label, err)
		}
	}
	check("claude", claudeBin)
	check("codex", codexBin)
	return b.String()
}
