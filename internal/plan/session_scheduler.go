package plan

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// ParallelSessions, when > 0, enables the DAG-driven parallel runner.
// Callers set this to the max number of sessions that may execute
// concurrently. Zero (the default) preserves the legacy linear for-
// loop behavior exactly so opt-in use doesn't regress any existing
// deployment.
//
// Reasonable values: 2-4. Higher parallelism shifts the bottleneck to
// LiteLLM rate limits and git / install contention, not CPU. The
// runner still enforces shared-state mutexes around pnpm install,
// depcheck, and SOWState persistence so concurrent sessions don't
// corrupt each other.

// SessionScheduler orchestrates SOW execution by running sessions sequentially,
// checking acceptance criteria at each session boundary. Within each session,
// tasks are dispatched to the caller's execute function which can use Stoke's
// native parallel scheduler.
type SessionScheduler struct {
	sow         *SOW
	projectRoot string
	// State persists session outcomes to disk for resume. If nil, state
	// tracking is disabled (original fire-and-forget behavior).
	state *SOWState
	// Resume controls whether a prior state file, if present, is honored.
	// When true, completed sessions are skipped. When false, all sessions run.
	Resume bool
	// ContinueOnFailure keeps the scheduler going after a session fails its
	// acceptance criteria or encounters a task error. Default is to halt
	// immediately so the user sees the failure fast.
	ContinueOnFailure bool
	// MaxSessionRetries is the number of times a single session's tasks +
	// acceptance check is retried on failure before moving on (or halting).
	// Default 1 = no retry.
	MaxSessionRetries int
	// SmokeGate, when non-nil, is invoked after a session's acceptance
	// criteria pass but BEFORE the session is recorded as successful.
	// Verdict Fail flips AcceptanceMet false and populates result.Error
	// so the session is treated as failed (or blocked-upstream for
	// dependents). StaticOnly and Pass allow the success path to
	// continue; StaticOnly is surfaced in the end-of-run banner so the
	// operator knows what wasn't runtime-verified.
	//
	// Injected by cmd/stoke when --smoke (default on) is configured;
	// leaving it nil preserves exact pre-smoke behavior.
	SmokeGate func(session Session) (kind string, reason string, output string)
	// ParallelSessions, when >= 2, enables DAG-driven parallel session
	// dispatch. The scheduler builds a dependency graph from Session.Inputs
	// / Session.Outputs + file-scope inference + declaration-order fallback
	// and launches up to N sessions concurrently whenever their blockers
	// are resolved. Zero or one keeps the legacy sequential runner.
	ParallelSessions int
	// BuildRequiredEnvVars, when non-nil, restricts the infra-env-var
	// preflight gate to only those variables the env-var classifier
	// identified as genuinely required at build/test time. Runtime-only
	// vars (DB URLs, API endpoints, message-broker URLs) no longer
	// block session dispatch. When nil, the legacy behavior applies:
	// ALL declared env vars gate the session. Callers populate this
	// from plan.ClassifyEnvVars before Run().
	BuildRequiredEnvVars map[string]bool
	// OnProgress is called after each session completes (success or failure).
	// Used by the TUI/REPL to update its display. May be nil.
	OnProgress func(SessionResult)
	// OnSessionStart is called when a session begins (and on each retry).
	// Lets the TUI flip the session to "running" before tasks execute.
	// May be nil.
	OnSessionStart func(sessionID string, attempt int)

	// promotedAt tracks when each AppendSession-promoted session was
	// queued. Used by CheckPromotedDispatch to detect deadlocks where a
	// promoted session sits in the queue long past PromotedDispatchSLA.
	// Map is lazily initialized on first AppendSession call.
	promotedAt map[string]time.Time
}

// SessionResult is the outcome of executing one session.
type SessionResult struct {
	SessionID     string
	Title         string
	TaskResults   []TaskExecResult
	Acceptance    []AcceptanceResult
	AcceptanceMet bool
	Attempts      int
	Error         error
	Skipped       bool // true when resumed and already complete
}

// TaskExecResult is a generic task execution result returned by the caller.
type TaskExecResult struct {
	TaskID  string `json:"task_id"`
	Success bool   `json:"success"`
	Error   error  `json:"-"`
}

// SessionExecuteFunc runs all tasks for a single session. The caller decides
// how to schedule tasks (parallel, serial, etc.) using Stoke's native scheduler.
// It receives the session and returns results for each task.
type SessionExecuteFunc func(ctx context.Context, session Session) ([]TaskExecResult, error)

// NewSessionScheduler creates a scheduler that processes SOW sessions in order.
func NewSessionScheduler(sow *SOW, projectRoot string) *SessionScheduler {
	return &SessionScheduler{
		sow:               sow,
		projectRoot:       projectRoot,
		MaxSessionRetries: 1,
	}
}

// LoadOrCreateState initializes the scheduler's state tracking, loading a
// prior state file if one exists at projectRoot/.stoke/sow-state.json.
// Call before Run to enable resume + progress persistence.
func (ss *SessionScheduler) LoadOrCreateState() error {
	existing, err := LoadSOWState(ss.projectRoot)
	if err != nil {
		return err
	}
	if existing == nil {
		ss.state = NewSOWState(ss.sow)
		return SaveSOWState(ss.projectRoot, ss.state)
	}
	existing.MergeSOW(ss.sow)
	ss.state = existing
	return SaveSOWState(ss.projectRoot, ss.state)
}

// State returns the scheduler's current state snapshot (may be nil).
func (ss *SessionScheduler) State() *SOWState { return ss.state }

// AppendSession extends the scheduler's session list at runtime. New
// sessions are picked up by an in-progress Run because Run iterates by
// index on the live sow.Sessions slice. This is the hook the convergence
// override flow uses to turn CTO-approved continuation items into new
// work the build will automatically execute.
//
// Appended sessions are recorded in SOWState so resume semantics still
// apply to them.
func (ss *SessionScheduler) AppendSession(session Session) {
	ss.sow.Sessions = append(ss.sow.Sessions, session)
	if ss.state != nil {
		ss.state.Sessions = append(ss.state.Sessions, SessionRecord{
			SessionID: session.ID,
			Title:     session.Title,
			Status:    "pending",
		})
		_ = SaveSOWState(ss.projectRoot, ss.state)
	}
	// Track promotion time for deadlock detection. The scheduler's
	// dispatch loop checks ss.promotedAt periodically and prints a
	// loud banner if a promoted session has been sitting undispatched
	// for >promotedDispatchSLA. Without this, a fix session that
	// silently never starts looks identical to one that's just
	// queued behind real work.
	if ss.promotedAt == nil {
		ss.promotedAt = map[string]time.Time{}
	}
	ss.promotedAt[session.ID] = time.Now()
}

// PromotedDispatchSLA is the time after which an appended session
// that has not been dispatched is treated as a deadlock signal. Set
// generously so legitimately queued sessions (waiting on real
// upstream deps) don't trigger false alarms.
const PromotedDispatchSLA = 5 * time.Minute

// CheckPromotedDispatch returns the IDs of any sessions appended via
// AppendSession that have been pending longer than PromotedDispatchSLA
// without entering the started set. Callers (the dispatch loop) print
// a deadlock banner and may force-promote (e.g. set Preempt=true) as
// a self-heal. Closes anti-deception matrix gap B5: scheduler silently
// failing to make progress while heartbeats keep firing.
func (ss *SessionScheduler) CheckPromotedDispatch(started map[string]bool, done map[string]bool) []string {
	if ss.promotedAt == nil {
		return nil
	}
	now := time.Now()
	var stalled []string
	for id, at := range ss.promotedAt {
		if started[id] || done[id] {
			delete(ss.promotedAt, id)
			continue
		}
		if now.Sub(at) > PromotedDispatchSLA {
			stalled = append(stalled, id)
		}
	}
	return stalled
}

// Run executes all sessions in order. For each session:
// 1. Runs preflight checks (infra requirements)
// 2. Calls execFn to execute the session's tasks (with retry)
// 3. Checks acceptance criteria
// 4. Persists progress after each session (if state is enabled)
// 5. Stops if acceptance criteria fail unless ContinueOnFailure is set
//
// Iteration is index-based so AppendSession mid-run actually extends the
// work queue (a classic Go gotcha: `for _, s := range slice` captures the
// slice header at the start of the loop).
//
// Returns results for all attempted sessions.
func (ss *SessionScheduler) Run(ctx context.Context, execFn SessionExecuteFunc) ([]SessionResult, error) {
	// Opt-in DAG-driven parallel execution. When disabled, fall through
	// to the legacy sequential loop below — behavior is byte-for-byte
	// identical to pre-parallel releases.
	if ss.ParallelSessions >= 2 {
		return ss.runParallel(ctx, execFn)
	}
	var results []SessionResult
	var firstErr error

	for i := 0; i < len(ss.sow.Sessions); i++ {
		session := ss.sow.Sessions[i]
		// Check context cancellation
		if ctx.Err() != nil {
			return results, ctx.Err()
		}

		// Resume: skip sessions already completed.
		if ss.Resume && ss.state != nil && ss.state.IsSessionComplete(session.ID) {
			rec := ss.state.SessionByID(session.ID)
			skipped := SessionResult{
				SessionID:     session.ID,
				Title:         session.Title,
				Acceptance:    rec.Acceptance,
				AcceptanceMet: true,
				Attempts:      rec.Attempts,
				Skipped:       true,
			}
			results = append(results, skipped)
			if ss.OnProgress != nil {
				ss.OnProgress(skipped)
			}
			continue
		}

		// Preflight: check infra env vars for this session. When
		// ss.BuildRequiredEnvVars is populated, only variables the
		// classifier flagged as build-required trigger the gate —
		// runtime-only vars pass through silently since they're
		// deployment concerns, not build concerns.
		infraReqs := ss.sow.InfraForSession(session.ID)
		if missing := checkInfraEnvVarsFiltered(infraReqs, ss.BuildRequiredEnvVars); len(missing) > 0 {
			// Loud surfacing: this is a SKIP, not a code-level failure —
			// the session never ran a single task. Print a wide banner so
			// it's visible in heartbeat-stream logs and impossible to miss
			// when scrolling. Without this, a missing env var silently
			// drops a substantive session and downstream sessions then
			// run against missing outputs.
			fmt.Printf("\n  ❌❌❌ SESSION %s BLOCKED — missing infrastructure env vars: %s\n", session.ID, strings.Join(missing, ", "))
			fmt.Printf("       title: %s\n", session.Title)
			fmt.Printf("       set the env var(s) above and re-run, or add a mock fallback\n")
			if ss.ContinueOnFailure {
				fmt.Printf("       continuing with downstream sessions, but they may produce broken output\n\n")
			} else {
				fmt.Printf("       halting (--continue-on-failure=false)\n\n")
			}
			result := SessionResult{
				SessionID: session.ID,
				Title:     session.Title,
				Error:     fmt.Errorf("BLOCKED: missing infrastructure env vars: %s", strings.Join(missing, ", ")),
			}
			results = append(results, result)
			// Record as "blocked" not "failed" — semantically distinct:
			// the session never executed, so calling it "failed" misleads
			// retry logic and post-run audits. Blocked sessions need an
			// environment fix, not a code fix.
			ss.recordSessionBlocked(session, result, result.Error)
			if firstErr == nil {
				firstErr = result.Error
			}
			if !ss.ContinueOnFailure {
				return results, result.Error
			}
			continue
		}

		// Execute with retry loop. Every attempt re-runs the task exec plus
		// the acceptance gate, so a model that missed a detail the first
		// time gets a clean second try.
		retries := ss.MaxSessionRetries
		if retries < 1 {
			retries = 1
		}
		var result SessionResult
		result.SessionID = session.ID
		result.Title = session.Title

		for attempt := 1; attempt <= retries; attempt++ {
			result.Attempts = attempt
			ss.recordSessionStart(session, attempt)
			if ss.OnSessionStart != nil {
				ss.OnSessionStart(session.ID, attempt)
			}

			taskResults, err := execFn(ctx, session)
			result.TaskResults = taskResults

			if err != nil {
				result.Error = fmt.Errorf("session %s (attempt %d) exec failed: %w", session.ID, attempt, err)
				if attempt < retries {
					continue
				}
				break
			}

			// Check individual task failures
			taskFailed := false
			for _, tr := range taskResults {
				if !tr.Success {
					taskFailed = true
					result.Error = fmt.Errorf("session %s (attempt %d) task %s failed", session.ID, attempt, tr.TaskID)
					break
				}
			}
			if taskFailed {
				if attempt < retries {
					continue
				}
				break
			}

			// Acceptance gate
			acceptance, allPassed := CheckAcceptanceCriteria(ctx, ss.projectRoot, session.AcceptanceCriteria)
			result.Acceptance = acceptance
			result.AcceptanceMet = allPassed

			// Smoke gate: runtime-level verification after ACs pass.
			// Only runs on the attempt that actually passed ACs; a
			// failing smoke flips AcceptanceMet false so the retry/
			// escalation paths treat it as a genuine session failure.
			if allPassed && ss.SmokeGate != nil {
				kind, reason, output := ss.SmokeGate(session)
				switch kind {
				case "fail":
					fmt.Printf("  ⛔ smoke gate failed for %s: %s\n", session.ID, reason)
					allPassed = false
					result.AcceptanceMet = false
					result.Error = fmt.Errorf("session %s smoke gate failed: %s\n\n%s", session.ID, reason, output)
				case "static_only":
					fmt.Printf("  ◉ smoke %s: %s\n", session.ID, reason)
					// Still counts as passed; reason surfaced in banner.
				case "pass":
					fmt.Printf("  ✔ smoke %s: %s\n", session.ID, reason)
				}
			}

			if !allPassed {
				result.Error = fmt.Errorf("session %s (attempt %d) acceptance criteria not met:\n%s",
					session.ID, attempt, FormatAcceptanceResults(acceptance))
				if attempt < retries {
					continue
				}
				break
			}

			// Success
			result.Error = nil
			break
		}

		results = append(results, result)
		if result.Error != nil || !result.AcceptanceMet {
			ss.recordSessionFailure(session, result, result.Error)
			if firstErr == nil {
				firstErr = result.Error
			}
			if ss.OnProgress != nil {
				ss.OnProgress(result)
			}
			if !ss.ContinueOnFailure {
				return results, result.Error
			}
			continue
		}
		ss.recordSessionSuccess(session, result)
		if ss.OnProgress != nil {
			ss.OnProgress(result)
		}
	}

	// End-of-run banner: if any sessions were blocked or failed under
	// ContinueOnFailure, surface them loudly so they aren't lost in the
	// stream of heartbeat output. Without this the operator has to dig
	// into sow-state.json to find that S3 was silently dropped.
	if ss.state != nil && ss.ContinueOnFailure {
		var blocked, failed []string
		for _, s := range ss.state.Sessions {
			switch s.Status {
			case "blocked":
				blocked = append(blocked, fmt.Sprintf("%s (%s)", s.SessionID, s.LastError))
			case "failed":
				failed = append(failed, fmt.Sprintf("%s (%s)", s.SessionID, s.LastError))
			}
		}
		if len(blocked) > 0 || len(failed) > 0 {
			fmt.Println()
			fmt.Println("  ════════════════════════════════════════════════════════════════")
			fmt.Println("  ❌ END-OF-RUN SUMMARY: not all sessions converged")
			if len(blocked) > 0 {
				fmt.Printf("  blocked (%d) — environment fix needed:\n", len(blocked))
				for _, s := range blocked {
					fmt.Printf("    - %s\n", s)
				}
			}
			if len(failed) > 0 {
				fmt.Printf("  failed (%d) — code fix needed:\n", len(failed))
				for _, s := range failed {
					fmt.Printf("    - %s\n", s)
				}
			}
			fmt.Println("  ════════════════════════════════════════════════════════════════")
			fmt.Println()
		}
	}

	return results, firstErr
}

// recordSessionStart marks a session as running in state.
func (ss *SessionScheduler) recordSessionStart(session Session, attempt int) {
	if ss.state == nil {
		return
	}
	rec := ss.state.SessionByID(session.ID)
	if rec == nil {
		return
	}
	rec.Status = "running"
	rec.Attempts = attempt
	if rec.StartedAt.IsZero() {
		rec.StartedAt = time.Now()
	}
	_ = SaveSOWState(ss.projectRoot, ss.state)
}

// recordSessionSuccess marks a session as done with acceptance met.
func (ss *SessionScheduler) recordSessionSuccess(session Session, result SessionResult) {
	if ss.state == nil {
		return
	}
	rec := ss.state.SessionByID(session.ID)
	if rec == nil {
		return
	}
	rec.Status = "done"
	rec.AcceptanceMet = true
	rec.Acceptance = result.Acceptance
	rec.TaskResults = result.TaskResults
	rec.Attempts = result.Attempts
	rec.LastError = ""
	rec.FinishedAt = time.Now()
	_ = SaveSOWState(ss.projectRoot, ss.state)
}

// recordSessionFailure persists a failed session outcome.
func (ss *SessionScheduler) recordSessionFailure(session Session, result SessionResult, err error) {
	if ss.state == nil {
		return
	}
	rec := ss.state.SessionByID(session.ID)
	if rec == nil {
		return
	}
	rec.Status = "failed"
	rec.AcceptanceMet = result.AcceptanceMet
	rec.Acceptance = result.Acceptance
	rec.TaskResults = result.TaskResults
	rec.Attempts = result.Attempts
	if err != nil {
		rec.LastError = err.Error()
	}
	rec.FinishedAt = time.Now()
	_ = SaveSOWState(ss.projectRoot, ss.state)
}

// recordSessionBlocked persists a session that was skipped before any
// task executed because a precondition (e.g. missing env var) wasn't
// satisfied. Distinct from "failed" so post-run audits and retry logic
// can treat blocked sessions as needing environment fixes, not code
// fixes.
func (ss *SessionScheduler) recordSessionBlocked(session Session, result SessionResult, err error) {
	if ss.state == nil {
		return
	}
	rec := ss.state.SessionByID(session.ID)
	if rec == nil {
		return
	}
	rec.Status = "blocked"
	rec.AcceptanceMet = false
	rec.Attempts = 0
	if err != nil {
		rec.LastError = err.Error()
	}
	rec.FinishedAt = time.Now()
	_ = SaveSOWState(ss.projectRoot, ss.state)
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
	totalCriteria := 0
	for _, s := range ss.sow.Sessions {
		totalTasks += len(s.Tasks)
		totalCriteria += len(s.AcceptanceCriteria)
		phase := s.Phase
		if phase != "" {
			phase = " [" + phase + "]"
		}
		fmt.Fprintf(&b, "  %s: %s%s (%d tasks, %d criteria)\n",
			s.ID, s.Title, phase, len(s.Tasks), len(s.AcceptanceCriteria))
		// Show how acceptance criteria will be checked so the user can
		// audit them without running anything.
		for _, ac := range s.AcceptanceCriteria {
			how := "manual"
			switch {
			case ac.Command != "":
				how = "$ " + ac.Command
			case ac.FileExists != "":
				how = "exists: " + ac.FileExists
			case ac.ContentMatch != nil:
				how = fmt.Sprintf("%s ~ %q", ac.ContentMatch.File, ac.ContentMatch.Pattern)
			}
			fmt.Fprintf(&b, "    - [%s] %s: %s\n", ac.ID, ac.Description, how)
		}
	}
	fmt.Fprintf(&b, "Total tasks: %d, Total criteria: %d\n", totalTasks, totalCriteria)

	// If state exists, show what would be skipped on resume.
	if state, _ := LoadSOWState(ss.projectRoot); state != nil {
		var done []string
		for _, s := range state.Sessions {
			if s.Status == "done" && s.AcceptanceMet {
				done = append(done, s.SessionID)
			}
		}
		if len(done) > 0 {
			fmt.Fprintf(&b, "\nResume state: %d/%d sessions already complete: %s\n",
				len(done), len(ss.sow.Sessions), strings.Join(done, ", "))
		}
	}

	return b.String()
}

// checkInfraEnvVars returns missing env vars for the given infra requirements.
func checkInfraEnvVars(reqs []InfraRequirement) []string {
	return checkInfraEnvVarsFiltered(reqs, nil)
}

// checkInfraEnvVarsFiltered is checkInfraEnvVars with an optional filter
// that restricts the gate to only classifier-approved build-required
// variables. A nil filter reverts to legacy behavior (gate on every
// declared var). An empty non-nil filter (len==0) gates on nothing —
// the classifier decided every var is runtime-only.
func checkInfraEnvVarsFiltered(reqs []InfraRequirement, buildRequired map[string]bool) []string {
	var missing []string
	for _, req := range reqs {
		for _, v := range req.EnvVars {
			if buildRequired != nil && !buildRequired[v] {
				continue // classifier says runtime-only — don't gate
			}
			if envLookup(v) == "" {
				missing = append(missing, v)
			}
		}
	}
	return missing
}

// envLookup is a var so tests can override it.
var envLookup = os.Getenv
