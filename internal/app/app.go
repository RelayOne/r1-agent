package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/ericmacdougall/stoke/internal/boulder"
	"github.com/ericmacdougall/stoke/internal/convergence"
	"github.com/ericmacdougall/stoke/internal/config"
	"github.com/ericmacdougall/stoke/internal/hub"
	"github.com/ericmacdougall/stoke/internal/costtrack"
	"github.com/ericmacdougall/stoke/internal/engine"
	"github.com/ericmacdougall/stoke/internal/memory"
	"github.com/ericmacdougall/stoke/internal/model"
	"github.com/ericmacdougall/stoke/internal/preflight"
	"github.com/ericmacdougall/stoke/internal/rbac"
	"github.com/ericmacdougall/stoke/internal/replay"
	"github.com/ericmacdougall/stoke/internal/repomap"
	"github.com/ericmacdougall/stoke/internal/subscriptions"
	"github.com/ericmacdougall/stoke/internal/taskstate"
	"github.com/ericmacdougall/stoke/internal/telemetry"
	"github.com/ericmacdougall/stoke/internal/testselect"
	"github.com/ericmacdougall/stoke/internal/validation"
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
	Boulder          *boulder.Enforcer   // idle agent detection (nil = disabled)
	CostTracker      *costtrack.Tracker  // per-session cost tracking (nil = disabled)
	Recorder         *replay.Recorder    // session replay recording (nil = disabled)
	TestGraph        *testselect.Graph   // dependency-aware test selection (nil = run all)
	RepoMap          *repomap.RepoMap    // ranked codebase map for context (nil = disabled)
	PlanOnly         bool
	BuildCommand     string
	TestCommand      string
	LintCommand      string
	OnEvent          engine.OnEventFunc
	RBACPolicy       *rbac.Policy        // RBAC enforcement (nil = no enforcement)
	RBACIdentity     string              // identity for RBAC checks (e.g., username or API key)
	Memory           *memory.Store              // cross-session persistent knowledge (nil = disabled)
	Telemetry        *telemetry.Collector       // structured metrics collector (nil = disabled)
	Convergence      *convergence.Validator     // adversarial self-audit gate (nil = auto-created)
	EventBus         *hub.Bus                   // unified event bus (nil = no events)
}

// Orchestrator coordinates policy loading, engine selection, worktree management, and verification for a task.
type Orchestrator struct {
	cfg    RunConfig
	policy config.Policy
}

// New creates an Orchestrator from the given config, loading and validating the policy file.
func New(cfg RunConfig) (*Orchestrator, error) {
	// Validate required inputs at the API boundary.
	if err := validation.NonEmpty(cfg.RepoRoot, "RepoRoot"); err != nil {
		return nil, err
	}
	if err := validation.NonEmpty(cfg.Task, "Task"); err != nil {
		return nil, err
	}
	if cfg.State == nil {
		return nil, fmt.Errorf("task state is required (anti-deception: no legacy mode)")
	}
	if cfg.AuthMode == "" {
		cfg.AuthMode = AuthModeMode1
	}

	// Run preflight checks: log warnings for any failed checks (advisory, not blocking).
	// The real build/test/lint pipeline catches actual issues; preflight is early warning.
	preflightReport := preflight.RunAll(cfg.RepoRoot, preflight.DefaultCheckers())
	for _, check := range preflightReport.Checks {
		if !check.Passed {
			fmt.Fprintf(os.Stderr, "[preflight] %s: %s\n", check.Name, check.Message)
		}
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

	// Default convergence validator: always-on adversarial self-audit.
	// Uses project-aware detection to activate domain-specific rules.
	if cfg.Convergence == nil {
		cfg.Convergence = convergence.NewValidatorForProject(cfg.RepoRoot)
	}

	return &Orchestrator{cfg: cfg, policy: policy}, nil
}

// DefaultPolicyYAML returns the default Stoke policy as a YAML string.
func DefaultPolicyYAML() string {
	return config.DefaultPolicyYAML()
}

// Run executes the full workflow: auto-detects build commands, sets up worktrees, and runs plan/execute/verify phases.
func (o *Orchestrator) Run(ctx context.Context) (workflow.Result, error) {
	// Enforce RBAC: check that the identity has build:execute permission.
	if o.cfg.RBACPolicy != nil {
		if err := o.cfg.RBACPolicy.Check(o.cfg.RBACIdentity, rbac.PermBuildExecute); err != nil {
			return workflow.Result{}, fmt.Errorf("rbac: %w", err)
		}
	}

	// Record telemetry event for task start.
	if o.cfg.Telemetry != nil {
		o.cfg.Telemetry.Record(telemetry.Event{
			Name: "task.start",
			Tags: map[string]string{"task_type": o.cfg.TaskType, "repo": o.cfg.RepoRoot},
		})
		defer func() {
			o.cfg.Telemetry.Record(telemetry.Event{
				Name: "task.end",
				Tags: map[string]string{"task_type": o.cfg.TaskType},
			})
		}()
	}

	// Load cross-session knowledge if a memory store is provided.
	if o.cfg.Memory != nil {
		entries := o.cfg.Memory.Recall(o.cfg.Task, 5)
		for _, e := range entries {
			if o.cfg.Wisdom != nil {
				o.cfg.Wisdom.Record(o.cfg.Task, wisdom.Learning{
					Category:    wisdom.Gotcha,
					Description: e.Content,
				})
			}
		}
	}

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
		Boulder:          o.cfg.Boulder,
		CostTracker:      o.cfg.CostTracker,
		Recorder:         o.cfg.Recorder,
		TestGraph:        o.cfg.TestGraph,
		RepoMap:          o.cfg.RepoMap,
		PlanOnly:         o.cfg.PlanOnly,
		Convergence:      o.cfg.Convergence,
		EventBus:         o.cfg.EventBus,
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
