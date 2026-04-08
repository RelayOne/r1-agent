package plan

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// SessionScheduler orchestrates SOW execution by running sessions sequentially,
// checking acceptance criteria at each session boundary. Within each session,
// tasks are dispatched to the caller's execute function which can use Stoke's
// native parallel scheduler.
type SessionScheduler struct {
	sow         *SOW
	projectRoot string
}

// SessionResult is the outcome of executing one session.
type SessionResult struct {
	SessionID     string
	Title         string
	TaskResults   []TaskExecResult
	Acceptance    []AcceptanceResult
	AcceptanceMet bool
	Error         error
}

// TaskExecResult is a generic task execution result returned by the caller.
type TaskExecResult struct {
	TaskID  string
	Success bool
	Error   error
}

// SessionExecuteFunc runs all tasks for a single session. The caller decides
// how to schedule tasks (parallel, serial, etc.) using Stoke's native scheduler.
// It receives the session and returns results for each task.
type SessionExecuteFunc func(ctx context.Context, session Session) ([]TaskExecResult, error)

// NewSessionScheduler creates a scheduler that processes SOW sessions in order.
func NewSessionScheduler(sow *SOW, projectRoot string) *SessionScheduler {
	return &SessionScheduler{sow: sow, projectRoot: projectRoot}
}

// Run executes all sessions in order. For each session:
// 1. Runs preflight checks (infra requirements)
// 2. Calls execFn to execute the session's tasks
// 3. Checks acceptance criteria
// 4. Stops if acceptance criteria fail
//
// Returns results for all attempted sessions.
func (ss *SessionScheduler) Run(ctx context.Context, execFn SessionExecuteFunc) ([]SessionResult, error) {
	var results []SessionResult

	for _, session := range ss.sow.Sessions {
		// Check context cancellation
		if ctx.Err() != nil {
			return results, ctx.Err()
		}

		// Preflight: check infra env vars for this session
		infraReqs := ss.sow.InfraForSession(session.ID)
		if missing := checkInfraEnvVars(infraReqs); len(missing) > 0 {
			result := SessionResult{
				SessionID: session.ID,
				Title:     session.Title,
				Error:     fmt.Errorf("missing infrastructure env vars: %s", strings.Join(missing, ", ")),
			}
			results = append(results, result)
			return results, result.Error
		}

		// Execute session tasks via caller-provided function
		taskResults, err := execFn(ctx, session)

		result := SessionResult{
			SessionID:   session.ID,
			Title:       session.Title,
			TaskResults: taskResults,
		}

		if err != nil {
			result.Error = err
			results = append(results, result)
			return results, fmt.Errorf("session %s failed: %w", session.ID, err)
		}

		// Check if any tasks failed
		for _, tr := range taskResults {
			if !tr.Success {
				result.Error = fmt.Errorf("task %s failed", tr.TaskID)
				results = append(results, result)
				return results, result.Error
			}
		}

		// Check acceptance criteria
		acceptance, allPassed := CheckAcceptanceCriteria(ctx, ss.projectRoot, session.AcceptanceCriteria)
		result.Acceptance = acceptance
		result.AcceptanceMet = allPassed
		results = append(results, result)

		if !allPassed {
			return results, fmt.Errorf("session %s acceptance criteria not met:\n%s",
				session.ID, FormatAcceptanceResults(acceptance))
		}
	}

	return results, nil
}

// DryRun validates the SOW and returns a summary of what would be executed
// without actually running anything.
func (ss *SessionScheduler) DryRun() string {
	var b strings.Builder
	fmt.Fprintf(&b, "SOW: %s (%s)\n", ss.sow.Name, ss.sow.ID)
	if ss.sow.Stack.Language != "" {
		fmt.Fprintf(&b, "Stack: %s", ss.sow.Stack.Language)
		if ss.sow.Stack.Framework != "" {
			fmt.Fprintf(&b, " / %s", ss.sow.Stack.Framework)
		}
		if ss.sow.Stack.Monorepo != nil {
			fmt.Fprintf(&b, " [%s]", ss.sow.Stack.Monorepo.Tool)
		}
		fmt.Fprintln(&b)
	}

	for _, inf := range ss.sow.Stack.Infra {
		fmt.Fprintf(&b, "Infra: %s", inf.Name)
		if inf.Version != "" {
			fmt.Fprintf(&b, " %s", inf.Version)
		}
		if len(inf.Extensions) > 0 {
			fmt.Fprintf(&b, " +%s", strings.Join(inf.Extensions, ","))
		}
		fmt.Fprintln(&b)
	}

	fmt.Fprintf(&b, "\nSessions: %d\n", len(ss.sow.Sessions))
	totalTasks := 0
	for _, s := range ss.sow.Sessions {
		totalTasks += len(s.Tasks)
		phase := s.Phase
		if phase != "" {
			phase = " [" + phase + "]"
		}
		fmt.Fprintf(&b, "  %s: %s%s (%d tasks, %d criteria)\n",
			s.ID, s.Title, phase, len(s.Tasks), len(s.AcceptanceCriteria))
	}
	fmt.Fprintf(&b, "Total tasks: %d\n", totalTasks)
	return b.String()
}

// checkInfraEnvVars returns missing env vars for the given infra requirements.
func checkInfraEnvVars(reqs []InfraRequirement) []string {
	var missing []string
	for _, req := range reqs {
		for _, v := range req.EnvVars {
			if envLookup(v) == "" {
				missing = append(missing, v)
			}
		}
	}
	return missing
}

// envLookup is a var so tests can override it.
var envLookup = os.Getenv
