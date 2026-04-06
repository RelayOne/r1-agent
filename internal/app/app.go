package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ericmacdougall/stoke/internal/boulder"
	"github.com/ericmacdougall/stoke/internal/convergence"
	"github.com/ericmacdougall/stoke/internal/config"
	"github.com/ericmacdougall/stoke/internal/hub"
	"github.com/ericmacdougall/stoke/internal/plugins"
	"github.com/ericmacdougall/stoke/internal/costtrack"
	"github.com/ericmacdougall/stoke/internal/engine"
	"github.com/ericmacdougall/stoke/internal/memory"
	"github.com/ericmacdougall/stoke/internal/model"
	"github.com/ericmacdougall/stoke/internal/preflight"
	"github.com/ericmacdougall/stoke/internal/rbac"
	"github.com/ericmacdougall/stoke/internal/replay"
	"github.com/ericmacdougall/stoke/internal/repomap"
	"github.com/ericmacdougall/stoke/internal/skill"
	"github.com/ericmacdougall/stoke/internal/skillselect"
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
	RunnerMode       string                     // runner selection: "claude" (default), "codex", "native", "hybrid"
	NativeAPIKey     string                     // API key for native runner (required when RunnerMode=native)
	NativeModel      string                     // model for native runner (default: claude-sonnet-4-6)
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

	// Load and apply hub hook configuration from .stoke/hooks.json.
	if cfg.EventBus != nil {
		hookCfg, hookErr := hub.LoadConfig(cfg.RepoRoot)
		if hookErr != nil {
			fmt.Fprintf(os.Stderr, "[hub] warning: failed to load hooks config: %v\n", hookErr)
		} else if len(hookCfg.Scripts) > 0 || len(hookCfg.Webhooks) > 0 {
			cfg.EventBus.ApplyConfig(hookCfg)
		}
		// Register built-in file protection gate from policy
		if len(policy.Files.Protected) > 0 {
			cfg.EventBus.Register(hub.FileProtectionGate(policy.Files.Protected))
		}

		// Discover and register plugin hooks as hub script subscribers.
		pluginReg := plugins.NewRegistry(filepath.Join(cfg.RepoRoot, ".stoke", "plugins"))
		if err := pluginReg.Discover(); err != nil {
			fmt.Fprintf(os.Stderr, "[plugins] warning: failed to discover plugins: %v\n", err)
		}
		for _, p := range pluginReg.Enabled() {
			for event, script := range p.Manifest.Hooks {
				cfg.EventBus.Register(hub.Subscriber{
					ID:     fmt.Sprintf("plugin.%s.%s", p.Manifest.Name, event),
					Events: []hub.EventType{hub.EventType(event)},
					Mode:   hub.ModeObserve,
					Script: &hub.ScriptConfig{
						Command:   filepath.Join(p.Dir, script),
						InputJSON: true,
					},
				})
			}
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

	// Initialize native runner if requested or API key provided
	if o.cfg.RunnerMode == "native" || o.cfg.NativeAPIKey != "" {
		nativeModel := o.cfg.NativeModel
		if nativeModel == "" {
			nativeModel = "claude-sonnet-4-6"
		}
		if o.cfg.NativeAPIKey != "" {
			native := engine.NewNativeRunner(o.cfg.NativeAPIKey, nativeModel)
			native.EventBus = o.cfg.EventBus
			runners.Native = native
		}
	}

	taskType := model.InferTaskType(o.cfg.Task)
	if strings.TrimSpace(o.cfg.TaskType) != "" {
		taskType = model.TaskType(strings.ToLower(strings.TrimSpace(o.cfg.TaskType)))
	}

	// Load skill registry and detect repo profile (best-effort)
	skillRegistry := skill.DefaultRegistry(o.cfg.RepoRoot)
	_ = skillRegistry.Load()

	profile, _ := skillselect.DetectProfile(o.cfg.RepoRoot)
	var stackMatches []string
	if profile != nil {
		stackMatches = skillselect.MatchSkills(profile)
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
		SkillRegistry:    skillRegistry,
		StackMatches:     stackMatches,
		RunnerMode:       o.cfg.RunnerMode,
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
