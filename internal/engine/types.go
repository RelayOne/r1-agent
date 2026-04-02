package engine

import (
	"context"

	"github.com/ericmacdougall/stoke/internal/stream"
)

type AuthMode string

const (
	AuthModeMode1 AuthMode = "mode1"
	AuthModeMode2 AuthMode = "mode2"
)

type PhaseSpec struct {
	Name         string
	BuiltinTools []string
	AllowedRules []string
	DeniedRules  []string
	MCPEnabled   bool
	MaxTurns     int
	Prompt       string
	Sandbox      bool
	ReadOnly     bool // if true, runner uses read-only sandbox and review profile
}

type PreparedCommand struct {
	Binary          string
	Args            []string
	Dir             string
	Env             []string
	Notes           []string
	LastMessagePath string // codex --output-last-message path (for review output)
}

// RunResult captures everything from a single engine execution.
type RunResult struct {
	Prepared   PreparedCommand
	CostUSD    float64
	DurationMs int64
	NumTurns   int
	Tokens     stream.TokenUsage
	Subtype    string // success, error_max_turns, error_during_execution, rate_limited
	IsError    bool
	ResultText string
	ExitCode   int
}

// OnEventFunc is called for each streaming event during execution.
// Used by the TUI and headless runner for live progress.
type OnEventFunc func(ev stream.Event)

type CommandRunner interface {
	Prepare(spec RunSpec) (PreparedCommand, error)
	Run(ctx context.Context, spec RunSpec, onEvent OnEventFunc) (RunResult, error)
}

type RunSpec struct {
	Prompt            string
	WorktreeDir       string
	RuntimeDir        string // outside worktree, for harness-owned files only
	Mode              AuthMode
	Phase             PhaseSpec
	PoolConfigDir     string
	SandboxEnabled    bool
	SandboxDomains    []string
	SandboxAllowRead  []string
	SandboxAllowWrite []string
}

type Registry struct {
	Claude *ClaudeRunner
	Codex  *CodexRunner
}
