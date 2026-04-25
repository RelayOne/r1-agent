// Package executor defines the uniform interface every Stoke task
// type implements. Code, research, browser, deploy, and delegation
// executors all satisfy the same contract, so the verification
// descent engine operates identically across task types.
//
// Track B Task 19 lays down just the interface + CodeExecutor
// wrapper + scaffolding entries for Tasks 20-24 (research,
// browser, deploy, delegation). The actual per-type logic lands in
// subsequent commits; this package's responsibility is the
// plumbing that lets those executors drop in without refactoring
// the core.
package executor

import (
	"context"

	"github.com/RelayOne/r1-agent/internal/plan"
)

// EffortLevel determines verification aggressiveness. Simple tasks
// get a deterministic check; critical ones get full multi-analyst
// descent plus operator approval gates.
type EffortLevel int

const (
	// EffortMinimal runs only the deterministic verify pass (build,
	// test, lint for code; regex match for research). Cheapest
	// setting — use for demos and low-stakes requests.
	EffortMinimal EffortLevel = iota

	// EffortStandard runs multi-analyst review only when the
	// deterministic check fails. Default for `stoke task` and
	// `stoke ship`.
	EffortStandard

	// EffortThorough runs multi-analyst on every acceptance
	// criterion, even those that passed the deterministic check.
	// Used when confidence matters more than cost.
	EffortThorough

	// EffortCritical runs full descent (T1→T5) and blocks on
	// operator approval before dispatching any repair at T4. Used
	// for production-bound deploys and delegated work.
	EffortCritical
)

// String returns a stable, lower-case identifier for the effort
// level. Used in logs, telemetry, and the `stoke task --effort=`
// CLI flag.
func (e EffortLevel) String() string {
	switch e {
	case EffortMinimal:
		return "minimal"
	case EffortStandard:
		return "standard"
	case EffortThorough:
		return "thorough"
	case EffortCritical:
		return "critical"
	default:
		return "unknown"
	}
}

// Task is the inbound request — the operator's natural-language
// input plus optional structured metadata (from `stoke task "..."`).
// Fields are intentionally minimal for the MVP; executor-specific
// hints live in a future Annotations map.
type Task struct {
	// ID is a stable identifier assigned at CLI entry time. Used in
	// JSON dumps, logs, and cross-referenced AC IDs.
	ID string

	// Description is the raw natural-language input from the
	// operator.
	Description string

	// TaskType is the classified type. Callers can set this
	// explicitly via `--type`; otherwise the router populates it.
	TaskType TaskType

	// Spec is an optional path or inline spec document.
	Spec string

	// RequiredCaps lists capability tags (for delegation + browser
	// executors). Unused by CodeExecutor for now.
	RequiredCaps []string

	// Budget is the USD cap for this task. 0 means inherit the
	// global config budget.
	Budget float64
}

// Plan is what the planner builds from a Task before dispatch. The
// fields stay minimal for the MVP; subsequent executors extend via
// embedding or composition.
type Plan struct {
	// ID is a stable identifier assigned at CLI entry time. Used in
	// JSON dumps, AC naming, and event correlation.
	ID string

	Task     Task
	Steps    []PlanStep
	Memories []any // retrieved context from wisdom/memory system

	// Query is a free-text shortcut for Task.Description — set by
	// CLI entry points that build a Plan directly without going
	// through a full Task-construction path. Research uses this as
	// the operator's question text.
	Query string

	// Extra carries executor-specific payload. Research reads
	// `urls_by_hint` and `urls` keys; code executors may stash
	// `*plan.SOW` here. Unknown keys are ignored.
	Extra map[string]any
}

// Aliases for the string-named effort levels used by callers that
// predate the int-based enum. Keep these so tests + research_cmd
// compile without churn; semantic mapping is the natural one.
const (
	EffortLow    = EffortMinimal
	EffortMedium = EffortStandard
	EffortHigh   = EffortCritical
)

// EffortLevelFromString parses the string form of an effort level.
// Unknown inputs default to EffortStandard so a stray flag value
// doesn't crash dispatch. Used by CLI subcommands that accept a
// --effort string flag.
func EffortLevelFromString(s string) EffortLevel {
	switch s {
	case "minimal", "min":
		return EffortMinimal
	case "standard", "std", "":
		return EffortStandard
	case "thorough":
		return EffortThorough
	case "critical", "crit":
		return EffortCritical
	default:
		return EffortStandard
	}
}

// RepairFunc is the function shape returned by Executor.BuildRepairFunc.
// Aliased so call sites can name the type without repeating the full
// signature.
type RepairFunc = func(ctx context.Context, directive string) error

// EnvFixFunc is the function shape returned by Executor.BuildEnvFixFunc.
// Aliased for the same reason as RepairFunc.
type EnvFixFunc = func(ctx context.Context, rootCause, stderr string) bool

// PlanStep is one unit of work inside a Plan. Its shape varies by
// executor — CodeExecutor uses SOW Sessions + Tasks here; research
// uses decomposed sub-questions; browser uses action sequences.
type PlanStep struct {
	ID          string
	Description string
	Meta        map[string]any
}

// Deliverable is the uniform output type. Concrete executors return
// a type-asserted value (CodeDeliverable, ResearchReport, etc.) via
// the interface method.
type Deliverable interface {
	// Summary returns a short human-readable string describing the
	// result. Shown in logs and the TUI "Deliverable" field.
	Summary() string

	// Size returns a scalar measure of the deliverable (LOC for a
	// diff, word count for a report). Used by convergence to sanity
	// check non-empty output.
	Size() int
}

// TaskType is the routing-level category. Exposed so the router can
// read flags like --type and so tests can assert classification.
type TaskType int

const (
	// TaskUnknown means the classifier has not run yet, or the
	// input does not match any registered type. Callers should
	// not dispatch on this value.
	TaskUnknown TaskType = iota

	// TaskCode is the Stoke trunk: implement, refactor, debug,
	// or otherwise modify a codebase. Handled by CodeExecutor
	// (SOW pipeline).
	TaskCode

	// TaskResearch is structured information-gathering. Handled
	// by ResearchExecutor (Task 20, Anthropic research pattern).
	TaskResearch

	// TaskBrowser is driven web automation. Handled by
	// BrowserExecutor (Task 21).
	TaskBrowser

	// TaskDeploy is a deploy/provision action. Handled by
	// DeployExecutor (Task 22).
	TaskDeploy

	// TaskDelegate is a request to hire + coordinate an external
	// agent (translator, image generator, etc). Handled by
	// DelegateExecutor (Task 23/24).
	TaskDelegate

	// TaskChat is free-form conversation. Routed to the existing
	// `stoke chat` flow; no executor binding required.
	TaskChat
)

// String returns the lower-case canonical name of the task type.
// Used in logs, flags, and the `--type` CLI option.
func (t TaskType) String() string {
	switch t {
	case TaskUnknown:
		return "unknown"
	case TaskCode:
		return "code"
	case TaskResearch:
		return "research"
	case TaskBrowser:
		return "browser"
	case TaskDeploy:
		return "deploy"
	case TaskDelegate:
		return "delegate"
	case TaskChat:
		return "chat"
	default:
		return "unknown"
	}
}

// Executor is the single interface every task type implements.
// Concrete executors live alongside this file (CodeExecutor) or in
// scaffold.go (Research/Browser/Deploy/Delegate scaffolding).
type Executor interface {
	// TaskType reports the type this executor handles — used by
	// the router for registration and by tests for assertion.
	TaskType() TaskType

	// Execute produces a Deliverable from the given Plan + effort
	// level. The Plan was built by the executor's corresponding
	// planning layer; Execute is a leaf call.
	Execute(ctx context.Context, p Plan, effort EffortLevel) (Deliverable, error)

	// BuildCriteria returns the acceptance criteria that gate the
	// deliverable. Criteria may use bash commands (code executor)
	// or AcceptanceCriterion.VerifyFunc (research / browser /
	// deploy / delegation).
	BuildCriteria(task Task, deliverable Deliverable) []plan.AcceptanceCriterion

	// BuildRepairFunc returns the function called by the descent
	// engine at T4 to fix the deliverable. CodeExecutor dispatches
	// a worker; research re-searches; browser re-navigates; deploy
	// re-deploys; delegation sends a revision request to the
	// hired agent.
	BuildRepairFunc(p Plan) func(ctx context.Context, directive string) error

	// BuildEnvFixFunc returns the function called by the descent
	// engine at T5 when the failure is environmental (missing
	// deps, unreachable host, expired credentials). Return nil
	// when the executor has no env-fix primitive.
	BuildEnvFixFunc() func(ctx context.Context, rootCause, stderr string) bool
}
