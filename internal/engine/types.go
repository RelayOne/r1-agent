package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/ericmacdougall/stoke/internal/stream"
)

// AuthMode distinguishes between subscription-based (mode1) and user-provided API key (mode2) authentication.
type AuthMode string

const (
	AuthModeMode1 AuthMode = "mode1"
	AuthModeMode2 AuthMode = "mode2"
)

// PhaseSpec describes the configuration for a single workflow phase (plan, execute, or verify).
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

// PreparedCommand holds the fully resolved binary, arguments, environment, and working directory for an engine invocation.
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

// CommandRunner is implemented by engine backends (Claude, Codex) to prepare and execute AI agent commands.
type CommandRunner interface {
	Prepare(spec RunSpec) (PreparedCommand, error)
	Run(ctx context.Context, spec RunSpec, onEvent OnEventFunc) (RunResult, error)
}

// RunSpec contains all inputs needed for a single engine execution, including prompt, worktree, and sandbox config.
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

	// Pool API fields (for APIRunner / GeminiRunner direct API access)
	PoolAPIKey string
	PoolBaseURL string
}

// Validate checks that all required RunSpec fields are present.
func (s RunSpec) Validate() error {
	if strings.TrimSpace(s.WorktreeDir) == "" {
		return fmt.Errorf("RunSpec: missing worktree dir")
	}
	if s.RuntimeDir == "" {
		return fmt.Errorf("RunSpec: missing runtime dir")
	}
	if strings.TrimSpace(s.Prompt) == "" {
		return fmt.Errorf("RunSpec: missing prompt")
	}
	if s.Phase.Name == "" {
		return fmt.Errorf("RunSpec: missing phase name")
	}
	if s.Phase.MaxTurns <= 0 {
		return fmt.Errorf("RunSpec: max_turns must be positive, got %d", s.Phase.MaxTurns)
	}
	return nil
}

// Registry holds the available engine runners (Claude and Codex) for task dispatch.
// Also tracks prompt cache stats across all executions for cost reporting.
type Registry struct {
	Claude     *ClaudeRunner
	Codex      *CodexRunner
	CacheStats *stream.PromptCacheStats // shared across all runners
}

// NewRegistry creates a registry with prompt cache tracking.
func NewRegistry(claude *ClaudeRunner, codex *CodexRunner) Registry {
	return Registry{
		Claude:     claude,
		Codex:      codex,
		CacheStats: stream.NewPromptCacheStats(),
	}
}
