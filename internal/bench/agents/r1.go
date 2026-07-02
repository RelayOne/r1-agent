// Package agents — R1 native dispatcher.
//
// R1Dispatcher runs the mission in-process via internal/agentloop with
// AntiTruncEnforce configurable per construction. The dispatcher's
// contract is the same shape as the other 7 competitor dispatchers:
// it captures the agent's final assistant text, the unified diff, and
// the completion-attempt bit so the verdict scorer can decide whether
// the agent's claim of completion was truthful.
//
// Spec: specs/truthful-completion-benchmark.md §T4.3.
package agents

import (
	"context"
	"fmt"
	"time"

	"github.com/RelayOne/r1/internal/bench"
)

// R1Dispatcher drives R1's native agentloop for one mission.
type R1Dispatcher struct {
	// EnforceAntiTrunc gates the anti-truncation engine. When true the
	// agent CANNOT end_turn while plan items are unchecked or
	// truncation phrases are present. When false the dispatcher
	// measures R1's behaviour with the gate off so a leaderboard run
	// can report both "R1 with gate" and "R1 without gate" rows.
	EnforceAntiTrunc bool

	// ModelInvoker, when non-nil, lets tests inject a fake model
	// driver. Production wires the real internal/agentloop.Run path
	// via the runner binary; the dispatcher itself stays
	// model-agnostic so unit tests don't reach the network.
	ModelInvoker R1ModelInvoker
}

// R1ModelInvoker is the seam the dispatcher uses to invoke the model.
// Production callers wire this to a concrete agentloop.Run thunk; the
// dispatcher does not import agentloop directly so unit tests can
// substitute a deterministic fake without dragging in the entire
// provider stack.
type R1ModelInvoker interface {
	Invoke(ctx context.Context, mission *bench.MissionConfig, workDir string, enforce bool) (R1InvocationResult, error)
}

// R1InvocationResult is what the invoker returns. Field names mirror
// the bits the dispatcher needs to populate a Trace.
type R1InvocationResult struct {
	CompletionAttempted bool
	LastAssistantText   string
	WallClockMs         int64
	ExitReason          string
	EstimatedBytes      int64
	RawLog              string
	// CostUSD and TokensUsed carry the native runner's usage
	// accounting through to the Trace (zero = not reported).
	CostUSD    float64
	TokensUsed int64
}

// Agent returns the agent identity. ID includes a suffix when
// AntiTruncEnforce is set so the leaderboard renderer can distinguish
// the two variants.
func (d *R1Dispatcher) Agent() Agent {
	id := "r1"
	display := "R1"
	if d.EnforceAntiTrunc {
		id = "r1-antitrunc"
		display = "R1 (anti-truncation enforce)"
	}
	return Agent{
		ID:          id,
		DisplayName: display,
		Version:     "dev",
	}
}

// Run drives the mission through agentloop and returns a Trace.
//
// The plan is written to plans/build-plan.md in workDir before the
// model is invoked (so the anti-truncation gate sees the checklist
// when EnforceAntiTrunc is true). The unified diff is captured via
// git diff on the working tree after the model returns.
func (d *R1Dispatcher) Run(ctx context.Context, mission *bench.MissionConfig, workDir string, timeout time.Duration) (Trace, error) {
	if mission == nil {
		return Trace{ExitReason: ExitReasonOther}, fmt.Errorf("R1Dispatcher.Run: mission is nil")
	}

	if err := WritePlan(workDir, mission.Plan); err != nil {
		return Trace{ExitReason: ExitReasonOther}, fmt.Errorf("R1Dispatcher.Run: write plan: %w", err)
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Default invoker returns a not-yet-wired trace so the dispatcher
	// is usable in tests and dry runs without dragging in the full
	// agentloop + provider stack. Production wiring lands in the
	// runner binary (T19-T22).
	invoker := d.ModelInvoker
	if invoker == nil {
		invoker = &notWiredR1Invoker{}
	}
	start := time.Now()
	result, err := invoker.Invoke(runCtx, mission, workDir, d.EnforceAntiTrunc)
	wallMs := time.Since(start).Milliseconds()
	if result.WallClockMs == 0 {
		result.WallClockMs = wallMs
	}

	if err != nil {
		return Trace{
			CompletionAttempted: result.CompletionAttempted,
			LastAssistantText:   result.LastAssistantText,
			UnifiedDiff:         safeGitDiff(workDir),
			EstimatedBytes:      result.EstimatedBytes,
			WallClockMs:         result.WallClockMs,
			ExitReason:          orDefault(result.ExitReason, ExitReasonToolError),
			RawLog:              BoundedLog([]byte(result.RawLog), 0),
			CostUSD:             result.CostUSD,
			TokensUsed:          result.TokensUsed,
		}, err
	}

	return Trace{
		CompletionAttempted: result.CompletionAttempted,
		LastAssistantText:   result.LastAssistantText,
		UnifiedDiff:         safeGitDiff(workDir),
		EstimatedBytes:      result.EstimatedBytes,
		WallClockMs:         result.WallClockMs,
		ExitReason:          orDefault(result.ExitReason, ExitReasonCompletionClaimed),
		RawLog:              BoundedLog([]byte(result.RawLog), 0),
		CostUSD:             result.CostUSD,
		TokensUsed:          result.TokensUsed,
	}, nil
}

// notWiredR1Invoker is the default invoker used when the production
// agentloop wiring isn't supplied. Returns ExitReason="other" with a
// diagnostic RawLog so tests + dry runs see a clear signal rather than
// silent success.
type notWiredR1Invoker struct{}

func (notWiredR1Invoker) Invoke(_ context.Context, _ *bench.MissionConfig, _ string, _ bool) (R1InvocationResult, error) {
	return R1InvocationResult{
		CompletionAttempted: false,
		LastAssistantText:   "",
		ExitReason:          ExitReasonOther,
		RawLog:              "R1Dispatcher: no ModelInvoker wired; production runner injects internal/agentloop.Run here.",
	}, nil
}

// safeGitDiff calls GitDiff but never propagates the error. Diff
// capture is best-effort; a failed diff just produces an empty string.
func safeGitDiff(workDir string) string {
	d, _ := GitDiff(workDir)
	return d
}

// orDefault returns s when non-empty, else fallback.
func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func init() {
	RegisterDispatcher("r1", &R1Dispatcher{})
	RegisterDispatcher("r1-antitrunc", &R1Dispatcher{EnforceAntiTrunc: true})
}

// SetR1ModelInvoker installs the production model invoker on the
// registered "r1" and "r1-antitrunc" dispatchers so `r1-bench --agent r1`
// runs r1's real native agentloop instead of the notWiredR1Invoker stub
// (SOTA gap #1). The runner binary calls this at startup; leaving it
// uncalled preserves the stubbed behavior for dry runs and tests.
func SetR1ModelInvoker(inv R1ModelInvoker) {
	for _, id := range []string{"r1", "r1-antitrunc"} {
		if d, ok := Lookup(id).(*R1Dispatcher); ok && d != nil {
			d.ModelInvoker = inv
		}
	}
}
