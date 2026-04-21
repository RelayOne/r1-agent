package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ericmacdougall/stoke/internal/provider"
	"github.com/ericmacdougall/stoke/internal/stream"
)

// ExtraToolHandler is a bridge that callers install on a RunSpec to
// handle tool names beyond the native registry's built-ins. When the
// agentloop invokes a tool whose name matches an ExtraTool definition,
// the corresponding handler is called and its string return becomes the
// tool_result content. If the handler returns an error, the loop sends
// is_error=true and the model sees it as a tool failure.
//
// This is how request_clarification (and any future out-of-band tools)
// plugs into the existing tool-use loop without modifying agentloop or
// tools.Registry.
type ExtraToolHandler func(ctx context.Context, input json.RawMessage) (string, error)

// ExtraTool bundles a tool definition with its handler. Callers build
// a slice of ExtraTools and pass it on RunSpec.ExtraTools; the native
// runner merges them into the tool list and dispatches calls whose
// name matches to the attached handler.
type ExtraTool struct {
	Def     provider.ToolDef
	Handler ExtraToolHandler
}

// AuthMode distinguishes between subscription-based (mode1) and user-provided API key (mode2) authentication.
type AuthMode string

const (
	// AuthModeSubscription uses the subscription pool's credentials.
	AuthModeSubscription AuthMode = "mode1"
	// AuthModeAPIKey uses the caller's own API key.
	AuthModeAPIKey AuthMode = "mode2"

	// Aliases for backward compatibility.
	AuthModeMode1 = AuthModeSubscription
	AuthModeMode2 = AuthModeAPIKey
)

// DeterminismCategory labels a phase by the reproducibility contract
// it makes with the replay system. Bit-exact phases must produce
// byte-identical output on replay (compile, lint, deterministic patch
// apply, test). Semantic phases must produce behaviorally equivalent
// output — wording / formatting may vary (LLM phases). BestEffort
// phases make no reproducibility claim (creative exploration). The
// replay player switches verification mode based on this label:
// bit-exact diff for BitExact, LLM-judge equivalence for Semantic,
// skip for BestEffort. See docs/anti-deception-matrix.md row B1.
type DeterminismCategory int

const (
	DeterminismSemantic   DeterminismCategory = iota // default — behavior equivalent, wording may vary
	DeterminismBitExact                              // byte-for-byte identical on replay
	DeterminismBestEffort                            // no reproducibility contract
)

// String returns the JSON-friendly category name.
func (d DeterminismCategory) String() string {
	switch d {
	case DeterminismBitExact:
		return "bit_exact"
	case DeterminismBestEffort:
		return "best_effort"
	default:
		return "semantic"
	}
}

// ComputeAffinity is a routing hint for heterogeneous compute. The
// scheduler uses it to prefer a substrate that matches the phase's
// structural shape — e.g. patch-apply on CPU, LLM call on GPU,
// MCTS exploration on a probabilistic substrate when one is
// available. Today everything routes to the existing execution
// engine regardless; the field is latent optionality, not active
// routing. See docs/anti-deception-matrix.md row B4.
type ComputeAffinity int

const (
	ComputeAny              ComputeAffinity = iota // default — no affinity, route anywhere
	ComputeCPUDeterministic                        // patch apply, compile, test, AST transforms
	ComputeGPUInference                            // LLM call (most current phases)
	ComputeProbabilistic                           // sampling, MCTS, exploration
)

// String returns the JSON-friendly affinity name.
func (c ComputeAffinity) String() string {
	switch c {
	case ComputeCPUDeterministic:
		return "cpu_deterministic"
	case ComputeGPUInference:
		return "gpu_inference"
	case ComputeProbabilistic:
		return "probabilistic"
	default:
		return "any"
	}
}

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
	ReadOnly          bool   // if true, runner uses read-only sandbox and review profile
	CompletionPromise string // statement agent must include in output to prove task completion; empty means no promise required

	// Determinism declares the phase's reproducibility contract.
	// Default DeterminismSemantic preserves existing behavior. Phases
	// known to be bit-exact (compile, lint, test, deterministic patch
	// apply) should opt into DeterminismBitExact in a follow-up sweep
	// so the replay player can verify them strictly.
	Determinism DeterminismCategory

	// Affinity declares the phase's preferred compute substrate.
	// Default ComputeAny preserves existing behavior. The scheduler
	// reads this as a routing hint, not a hard constraint — labeling
	// today buys optionality when probabilistic / neuromorphic
	// substrates become available without forcing a codebase sweep.
	Affinity ComputeAffinity
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
	// SystemPrompt is the static, cacheable portion of the instruction.
	// When set, the NativeRunner passes it to the agentloop as
	// cfg.SystemPrompt, which wraps it in a cache_control breakpoint
	// so the same block can be reused across turns and tasks without
	// paying the full input cost. CLI-backed runners ignore this
	// field (they don't support cache breakpoints in the same way).
	SystemPrompt      string
	// CompactThreshold, when > 0, enables progressive context
	// compaction inside the native agentloop: whenever the estimated
	// input token count crosses this threshold between turns, the
	// native runner's built-in compactor rewrites the message list
	// to shrink it back down. 0 = no automatic compaction.
	CompactThreshold int
	// Supervisor, when non-nil, enables a midturn spec-faithfulness
	// check inside the native agent loop. Every SupervisorConfig.
	// WritesPerCheck write_file / edit_file tool calls, the
	// supervisor scans the declared files against canonical
	// identifiers from the SOW and pushes a correction note into
	// the next user message when code has drifted from the spec.
	Supervisor *SupervisorConfig
	WorktreeDir       string
	RuntimeDir        string // outside worktree, for harness-owned files only
	Mode              AuthMode
	Phase             PhaseSpec
	PoolConfigDir     string
	SandboxEnabled    bool
	SandboxDomains    []string
	SandboxAllowRead  []string
	SandboxAllowWrite []string

	// MCPConfigPath is an optional path to an MCP server configuration file.
	// When set, the engine passes this to --mcp-config so the model gets
	// access to custom MCP tools (e.g., codebase analysis tools for
	// agentic discovery loops).
	MCPConfigPath string

	// Pool API fields (for APIRunner / GeminiRunner direct API access)
	PoolAPIKey  string
	PoolBaseURL string

	// Container runtime fields: when set, the engine wraps the CLI command
	// in a docker run invocation against the pool's container volume.
	ContainerImage string   // e.g., "ghcr.io/ericmacdougall/stoke-pool:latest"
	ContainerVol   string   // Docker volume name for credentials
	ContainerConfigDir string // Config dir path inside the container

	// ExtraTools are caller-supplied tool definitions with attached
	// handlers. The native runner merges them into the advertised
	// tool list and dispatches any call whose name matches to the
	// attached handler. Subprocess-backed runners (Claude/Codex CLI)
	// ignore this field — the CLI has its own tool set. Used today
	// by the clarification round-trip to install request_clarification.
	ExtraTools []ExtraTool

	// WorkerLogPath, when set, is an absolute file path the native
	// runner appends one JSONL record to per tool call: {ts, tool,
	// input, result_snippet, duration_ms, err, uuid, ...}. Gives
	// reviewers a deterministic, grep-able record of what the worker
	// actually did — independent of whether the worker wrote a
	// trailing natural-language summary (which workers often skip).
	// Callers create the file path (parent dir must exist) and are
	// responsible for cleanup between dispatches.
	WorkerLogPath string

	// WorkerLogContext carries correlation fields stamped onto every
	// JSONL entry written via WorkerLogPath. These let a reviewer (or
	// a post-mortem grep) tie a tool call back to its run, session,
	// task, attempt, depth, phase, and process — without having to
	// parse directory structure or cross-reference timestamps. All
	// fields are optional; empty values are omitted from the JSONL.
	WorkerLogContext WorkerLogContext
}

// WorkerLogContext groups correlation IDs and config snapshot data
// stamped onto the per-dispatch JSONL log. Populated by the caller
// when constructing a RunSpec with WorkerLogPath set; the native
// runner embeds these into the dispatch_start header and every
// subsequent tool_call record so grep/jq queries can trace any
// command back to session/task/attempt/model provenance.
type WorkerLogContext struct {
	RunID       string // stable ID for the whole SOW run (one per `stoke sow` invocation)
	DispatchID  string // unique ID for THIS worker dispatch (= this JSONL file)
	SessionID   string // e.g. "S1", "S2-descent-repair-...", "S1-fix"
	TaskID      string // task within the session
	Attempt     int    // 1-based session retry attempt
	Depth       int    // reviewer/decomposer recursion depth (0 = top-level)
	Model       string // e.g. "claude-sonnet-4-6" — the backing model
	StokeBuild  string // short git commit hash or build tag
	SOWPath     string // absolute path to SOW snapshot for this session
	PID         int    // this stoke process's PID
	PPID        int    // parent process (so ladder-driver dispatch can be traced)
	PurposeTag  string // free-form role tag, e.g. "worker", "repair", "judge", "intent-check"
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
	Native     *NativeRunner               // Stoke native runner using Anthropic API directly
	CacheStats *stream.PromptCacheStats // shared across all runners
}
