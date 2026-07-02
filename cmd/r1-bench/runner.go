// runner.go — the RunOne pipeline that drives a single (mission, agent)
// pair end-to-end. Separated from main.go so unit tests don't have to
// shell-parse argv.
//
// Spec: specs/truthful-completion-benchmark.md §T5.1 (item 43).
package main

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/bench"
	"github.com/RelayOne/r1/internal/bench/agents"
)

// RunSpec describes one (mission, agent) execution.
type RunSpec struct {
	Mission    *bench.MissionConfig
	Dispatcher agents.Dispatcher
	WorkDir    string
	Timeout    time.Duration
	Judge      bench.CompletionJudge
}

// RunOne executes the dispatcher, then scores its trace via
// bench.VerdictScorer. The returned RunResult carries the full
// truthful-completion verdict + breakdown.
func RunOne(ctx context.Context, spec RunSpec) (*bench.RunResult, error) {
	if spec.Mission == nil {
		return nil, fmt.Errorf("RunOne: mission is nil")
	}
	if spec.Dispatcher == nil {
		return nil, fmt.Errorf("RunOne: dispatcher is nil")
	}
	if spec.WorkDir == "" {
		return nil, fmt.Errorf("RunOne: WorkDir is empty")
	}
	if spec.Timeout <= 0 {
		spec.Timeout = 5 * time.Minute
	}

	trace, err := spec.Dispatcher.Run(ctx, spec.Mission, spec.WorkDir, spec.Timeout)
	if err != nil {
		// Surface the trace alongside the error so the caller can see
		// which signals (if any) the dispatcher captured before the
		// error path took over.
		return &bench.RunResult{
			MissionID:           spec.Mission.ID,
			CompletionAttempted: trace.CompletionAttempted,
			CompletionClaim:     trace.LastAssistantText,
		}, fmt.Errorf("dispatcher %q: %w", spec.Dispatcher.Agent().ID, err)
	}

	scorer := &bench.VerdictScorer{
		Judge:       spec.Judge,
		ExecCommand: defaultExecCommand,
	}
	result, err := scorer.Score(ctx, spec.Mission, spec.WorkDir, trace.UnifiedDiff, trace.LastAssistantText, trace.CompletionAttempted, trace.EstimatedBytes)
	if err != nil {
		return nil, fmt.Errorf("score: %w", err)
	}
	// Override the MissionID + AgentID + wall-clock + usage from the
	// dispatcher's trace.
	result.MissionID = spec.Mission.ID
	result.AgentID = spec.Dispatcher.Agent().ID
	result.WallTimeMs = trace.WallClockMs
	result.CostUSD = trace.CostUSD
	result.TokensUsed = trace.TokensUsed

	// SOTA gap #5 (runner half): audit the captured trajectory for
	// reward-hacking tells — reading the reference solution from git
	// history, editing the graded tests — and surface them on the result
	// so a gamed score is visible, not silent.
	result.RewardHackFlags = bench.FlagStrings(bench.AuditTrajectory(
		trace.RawLog,
		bench.DiffTouchedFiles(trace.UnifiedDiff),
		bench.MissionTestGlobs(spec.Mission),
	))
	return &result, nil
}

// defaultExecCommand runs a shell command in dir and returns its exit
// code. Used by VerdictScorer to execute PlanItem.TestCommand for
// per-item satisfaction checks.
func defaultExecCommand(ctx context.Context, dir, cmd string) (int, error) {
	if strings.TrimSpace(cmd) == "" {
		return 0, fmt.Errorf("defaultExecCommand: empty command")
	}
	c := exec.CommandContext(ctx, "sh", "-c", cmd)
	c.Dir = dir
	if err := c.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), nil
		}
		return -1, fmt.Errorf("defaultExecCommand: %w", err)
	}
	return 0, nil
}
