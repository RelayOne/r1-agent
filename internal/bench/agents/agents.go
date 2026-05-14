// Package agents holds the dispatcher framework used by the
// truthful-completion benchmark to run one mission against one
// coding-agent runtime under test.
//
// Each concrete dispatcher (R1, Claude Code, Cline, Aider, Codex,
// Cursor, Tether) lives in its own sibling file and registers itself
// via RegisterDispatcher at init time. The bench runner resolves an
// agent by ID through the package-level Registry map.
//
// Spec: specs/truthful-completion-benchmark.md §T4.1.
package agents

import (
	"context"
	"sync"
	"time"

	"github.com/RelayOne/r1/internal/bench"
)

// Agent is one competitor coding-agent runtime under test.
//
// The Version field is captured at run-time by the dispatcher so the
// leaderboard can attribute scores to a specific binary build (matters
// for upstream tools we don't control).
type Agent struct {
	ID          string // "r1" | "claude-code-default" | "cline" | ...
	DisplayName string // shown in published leaderboard
	Version     string // captured at run-time
}

// Dispatcher executes one mission against one agent and returns the
// raw run trace the verdict scorer needs. All implementations MUST
// respect ctx cancellation and the per-mission timeout.
type Dispatcher interface {
	Agent() Agent
	Run(ctx context.Context, mission *bench.MissionConfig, workDir string, timeout time.Duration) (Trace, error)
}

// Trace is the raw output of a single agent run. The verdict scorer
// (internal/bench/verdict.go) consumes this to decide whether a
// completion claim was truthful, and the runner (internal/bench/
// runner.go) folds it into the published RunResult.
type Trace struct {
	// CompletionAttempted is true iff the agent explicitly signaled
	// it finished the mission (e.g. end_turn for R1, stop event for
	// Claude Code, clean exit for Aider, task-complete for Codex).
	CompletionAttempted bool
	// LastAssistantText is the agent's final natural-language turn,
	// used by the LLM judge and by the Tether middleware gate.
	LastAssistantText string
	// UnifiedDiff is the `git diff` of workDir at end of run.
	UnifiedDiff string
	// EstimatedBytes is the agent's pre-flight byte estimate for the
	// changeset (only populated by dispatchers that emit one — R1).
	EstimatedBytes int64
	// WallClockMs is the dispatcher's measured wall-time.
	WallClockMs int64
	// ExitReason categorizes how the run ended. Use the
	// ExitReason* constants below; "other" is the catch-all.
	ExitReason string
	// RawLog is the agent's stdout/stderr, bounded by BoundedLog.
	RawLog string
}

// ExitReason* are the canonical Trace.ExitReason values. Any string
// not in this set should be normalized to ExitReasonOther before the
// trace leaves the dispatcher.
const (
	ExitReasonCompletionClaimed = "completion_claimed"
	ExitReasonTimeout           = "timeout"
	ExitReasonToolError         = "tool_error"
	ExitReasonRateLimit         = "rate_limit"
	ExitReasonNotSupported      = "not_supported_by_agent"
	ExitReasonTetherRefused     = "tether_gate_refused"
	ExitReasonOther             = "other"
)

// Registry maps agent IDs to their dispatchers. Populated at init
// time via RegisterDispatcher. The runner resolves the agent name
// passed via `--agent` against this map.
//
// Reads after init are read-only; writes are guarded by registryMu
// so test code (which may register fakes mid-test) doesn't race.
var (
	registryMu sync.RWMutex
	Registry   = map[string]Dispatcher{}
)

// RegisterDispatcher inserts d into the package-level Registry under
// id. Re-registering the same id silently overwrites — tests rely on
// this to swap in fakes.
func RegisterDispatcher(id string, d Dispatcher) {
	registryMu.Lock()
	defer registryMu.Unlock()
	Registry[id] = d
}

// Lookup returns the dispatcher registered under id, or nil if none.
// Callers should treat a nil return as "agent not supported in this
// build" and surface it to the user rather than panicking.
func Lookup(id string) Dispatcher {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return Registry[id]
}
