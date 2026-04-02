package app

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/ericmacdougall/stoke/internal/config"
	"github.com/ericmacdougall/stoke/internal/engine"
	"github.com/ericmacdougall/stoke/internal/model"
	"github.com/ericmacdougall/stoke/internal/subscriptions"
	"github.com/ericmacdougall/stoke/internal/taskstate"
	"github.com/ericmacdougall/stoke/internal/verify"
	"github.com/ericmacdougall/stoke/internal/workflow"
	"github.com/ericmacdougall/stoke/internal/worktree"
)

type AuthMode string

const (
	AuthModeMode1 AuthMode = "mode1"
	AuthModeMode2 AuthMode = "mode2"
)

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
	PlanOnly         bool
	BuildCommand     string
	TestCommand      string
	LintCommand      string
	OnEvent          engine.OnEventFunc
}

type Orchestrator struct {
	cfg    RunConfig
	policy config.Policy
}

func New(cfg RunConfig) (*Orchestrator, error) {
	if cfg.State == nil {
		return nil, fmt.Errorf("task state is required (anti-deception: no legacy mode)")
	}
	if cfg.AuthMode == "" {
		cfg.AuthMode = AuthModeMode1
	}
	policy, err := config.LoadPolicy(cfg.PolicyPath)
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

func DefaultPolicyYAML() string {
	return config.DefaultPolicyYAML()
}

func (o *Orchestrator) Run(ctx context.Context) (workflow.Result, error) {
	// Auto-detect commands if not specified
	buildCmd, testCmd, lintCmd := o.cfg.BuildCommand, o.cfg.TestCommand, o.cfg.LintCommand
	if buildCmd == "" || testCmd == "" || lintCmd == "" {
		detected := config.DetectCommands(o.cfg.RepoRoot)
		if buildCmd == "" { buildCmd = detected.Build }
		if testCmd == "" { testCmd = detected.Test }
		if lintCmd == "" { lintCmd = detected.Lint }
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
		AllowedFiles:    o.cfg.AllowedFiles,
		AuthMode:        engine.AuthMode(o.cfg.AuthMode),
		Policy:          o.policy,
		DryRun:          o.cfg.DryRun,
		Pools:           pools,
		Worktrees:       worktrees,
		Runners:         runners,
		Verifier:        verifier,
		ClaudeConfigDir: o.cfg.ClaudeConfigDir,
		CodexHome:       o.cfg.CodexHome,
		OnEvent:         o.cfg.OnEvent,
		State:           o.cfg.State,
		PlanOnly:        o.cfg.PlanOnly,
	}
	return wf.Run(ctx)
}

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
