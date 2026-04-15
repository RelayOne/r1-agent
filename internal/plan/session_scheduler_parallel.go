// Package plan — session_scheduler_parallel.go
//
// DAG-driven parallel session dispatch. Wrapped around the same
// per-session logic the sequential runner uses so parallel and
// sequential paths cannot diverge in their task execution or
// acceptance-gate behavior.
//
// Activated only when SessionScheduler.ParallelSessions >= 2.
// When disabled, Run() falls through to the legacy linear loop and
// this file's runParallel is never called.

package plan

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// runParallel is the DAG-scheduled session runner. It:
//
//  1. Builds a SessionDAG from Session.Inputs / Session.Outputs with
//     file-scope inference and declaration-order fallback (see
//     session_dag.go).
//  2. Maintains a ready queue. A session is ready when all its deps
//     (by DAG) are in the completed set AND it is not already running.
//  3. Spawns up to ParallelSessions goroutines, each calling the same
//     execFn + acceptance logic the sequential runner uses.
//  4. Blocks dependent sessions when an upstream session fails AND
//     ContinueOnFailure is false; surfaces them as blocked rather
//     than running against broken outputs.
//
// Shared-state serialization:
//
//   - ensureWorkspaceInstalled already uses installedOnceMu + a once-set
//     flag so the first caller's install wins; subsequent callers see
//     the flag and return immediately. That is safe for parallel callers.
//   - runDepCheck uses depcheckOnceMu with the same pattern — safe.
//   - recordSessionStart / recordSessionSuccess / recordSessionBlocked /
//     recordSessionFailure read+write ss.state and flush to disk.
//     We take stateMu around each call so concurrent record calls
//     serialize cleanly.
func (ss *SessionScheduler) runParallel(ctx context.Context, execFn SessionExecuteFunc) ([]SessionResult, error) {
	dag := BuildSessionDAG(ss.sow)
	fmt.Println(dag.Summary(ss.sow))
	fmt.Printf("  🛣 parallel session runner active (max %d concurrent)\n", ss.ParallelSessions)

	// Deadlock watchdog pulse. The main dispatch loop calls
	// CheckPromotedDispatch once per iteration, but it drops into
	// wg.Wait() when no new session is ready. If a parent session
	// appends a promoted fix and then keeps running for >SLA without
	// another completion, the loop never re-enters the check. This
	// goroutine polls at SLA/2 so the banner fires even during long
	// waits. It exits when watchdogCtx is canceled at function
	// return. (codex P2 fix for 00e20fe.)
	watchdogCtx, watchdogCancel := context.WithCancel(ctx)
	defer watchdogCancel()
	go func() {
		t := time.NewTicker(PromotedDispatchSLA / 2)
		defer t.Stop()
		for {
			select {
			case <-watchdogCtx.Done():
				return
			case <-t.C:
				// Watchdog only reads completion state; mutations go
				// via ClearPromoted which has its own mutex.
				stalled := ss.CheckPromotedDispatch(map[string]bool{}, map[string]bool{})
				if len(stalled) > 0 {
					fmt.Printf("\n  ⚠️  DEADLOCK WATCHDOG (pulse): %d promoted session(s) undispatched > %s\n", len(stalled), PromotedDispatchSLA)
					for _, id := range stalled {
						fmt.Printf("       %s (not dispatched after promotion)\n", id)
						ss.ClearPromoted(id)
					}
				}
			}
		}
	}()

	// Everything below this point is concurrency-sensitive.
	var (
		stateMu   sync.Mutex // guards ss.state mutations + disk flushes
		resultsMu sync.Mutex
		results   []SessionResult
		firstErr  error
		completed = map[string]bool{} // session IDs that finished successfully
		failed    = map[string]bool{} // session IDs that failed or were blocked
		done      = map[string]bool{} // every session that has reached a terminal state (success | fail | blocked)
	)

	// Snapshot of session metadata. ss.sow.Sessions is the live list
	// the harness mutates via AppendSession (continuations, fix-DAG
	// promotions, decomp overflow). The dispatch loop below re-reads
	// the live list each iteration so newly appended sessions get
	// picked up — sessions / order / dag are also rebuilt on demand.
	rebuildMaps := func() (map[string]Session, []string) {
		s := make(map[string]Session, len(ss.sow.Sessions))
		o := make([]string, 0, len(ss.sow.Sessions))
		for _, sess := range ss.sow.Sessions {
			s[sess.ID] = sess
			o = append(o, sess.ID)
		}
		return s, o
	}
	sessions, order := rebuildMaps()

	// Resume support: pre-mark already-completed sessions as done +
	// completed so the dispatch loop skips them and downstream sessions
	// see their deps as satisfied. Sequential Run() does this inline at
	// the top of every iteration; the parallel path needs the same skip
	// or `--resume --parallel N` re-executes everything. (Codex P1.)
	if ss.Resume && ss.state != nil {
		for _, sess := range ss.sow.Sessions {
			if ss.state.IsSessionComplete(sess.ID) {
				rec := ss.state.SessionByID(sess.ID)
				skipped := SessionResult{
					SessionID:     sess.ID,
					Title:         sess.Title,
					Acceptance:    rec.Acceptance,
					AcceptanceMet: true,
					Attempts:      rec.Attempts,
					Skipped:       true,
				}
				results = append(results, skipped)
				completed[sess.ID] = true
				done[sess.ID] = true
				if ss.OnProgress != nil {
					ss.OnProgress(skipped)
				}
			}
		}
	}

	sem := make(chan struct{}, ss.ParallelSessions)
	var wg sync.WaitGroup

	// recordResult is called under resultsMu + stateMu by each session
	// goroutine when it finishes. Keeps ordering stable with the
	// declaration order so post-run reports are readable.
	recordResult := func(id string, r SessionResult) {
		resultsMu.Lock()
		results = append(results, r)
		resultsMu.Unlock()
	}

	// recordTerminal flips a session's state to done and returns the
	// current completed + done sets snapshot.
	recordTerminal := func(id string, success bool) {
		stateMu.Lock()
		defer stateMu.Unlock()
		done[id] = true
		if success {
			completed[id] = true
		} else {
			failed[id] = true
		}
	}

	isReady := func(id string) bool {
		for _, dep := range dag.Deps[id] {
			if failed[dep] {
				// An upstream session failed. If we're configured to
				// continue on failure, we still run the dependent
				// session — best effort against possibly-broken state.
				// Otherwise the dependent session will be blocked
				// below by the blocker-check path.
				if !ss.ContinueOnFailure {
					return false
				}
				continue
			}
			if !completed[dep] {
				return false
			}
		}
		return true
	}

	runOne := func(id string) {
		defer wg.Done()
		session := sessions[id]
		// Preempt sessions skip the semaphore entirely (acquired
		// nowhere, released nowhere) so a fix/promoted session can
		// run even when all N regular slots are occupied by long-
		// running parents.
		if !session.Preempt {
			defer func() { <-sem }()
		}

		// Infra env var preflight — reuses the existing classifier-
		// filtered check so the parallel path behaves identically on
		// env-blocked sessions.
		infraReqs := ss.sow.InfraForSession(session.ID)
		if missing := checkInfraEnvVarsFiltered(infraReqs, ss.BuildRequiredEnvVars); len(missing) > 0 {
			fmt.Printf("\n  ❌❌❌ SESSION %s BLOCKED — missing infrastructure env vars: %s\n", session.ID, strings.Join(missing, ", "))
			fmt.Printf("       title: %s\n", session.Title)
			result := SessionResult{
				SessionID: session.ID,
				Title:     session.Title,
				Error:     fmt.Errorf("BLOCKED: missing infrastructure env vars: %s", strings.Join(missing, ", ")),
			}
			stateMu.Lock()
			ss.recordSessionBlocked(session, result, result.Error)
			stateMu.Unlock()
			recordResult(id, result)
			recordTerminal(id, false)
			resultsMu.Lock()
			if firstErr == nil {
				firstErr = result.Error
			}
			resultsMu.Unlock()
			if ss.OnProgress != nil {
				ss.OnProgress(result)
			}
			return
		}

		retries := ss.MaxSessionRetries
		if retries < 1 {
			retries = 1
		}
		var result SessionResult
		result.SessionID = session.ID
		result.Title = session.Title

		// Capture pre-session compile-error baseline so the
		// integrity gate can diff for NEW errors. Cheap pre-dispatch
		// cost vs. the alternative (zero regression detection). Only
		// fires when the gate is enabled.
		var compileBaseline map[string][]CompileErr
		if !ss.SkipIntegrityGate {
			compileBaseline = CaptureCompileBaseline(ctx, ss.projectRoot, collectSessionFiles(ss.projectRoot, session))
		}

		for attempt := 1; attempt <= retries; attempt++ {
			if ctx.Err() != nil {
				break
			}
			result.Attempts = attempt
			stateMu.Lock()
			ss.recordSessionStart(session, attempt)
			stateMu.Unlock()
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
			// Acceptance gate — same call the sequential runner uses.
			// ensureWorkspaceInstalled + runDepCheck inside this call
			// are dedupe-safe against concurrent callers (see their
			// once-set guards).
			acceptance, allPassed := CheckAcceptanceCriteria(ctx, ss.projectRoot, session.AcceptanceCriteria)
			result.Acceptance = acceptance
			result.AcceptanceMet = allPassed
			// Smoke gate — same hook the sequential runner uses. Fail
			// flips AcceptanceMet false so retry / escalation sees a
			// real failure; StaticOnly is logged but allows success.
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
			result.Error = nil
			break
		}

		success := result.Error == nil && result.AcceptanceMet

		// Post-success integrity gate. Runs every registered
		// ecosystem's import/surface probes against this session's
		// declared files. Any findings are promoted as a fix session
		// via AppendSession so the next scheduler iteration dispatches
		// them. Skipped on failure (the session already has issues
		// to address via the normal retry/escalation path).
		if success && !ss.SkipIntegrityGate {
			if report, err := RunIntegrityGate(ctx, ss.projectRoot, session, compileBaseline); err == nil && report != nil && len(report.Directives) > 0 {
				fmt.Printf("\n  🛡 integrity gate: %d finding(s) in session %s:\n", len(report.Directives), session.ID)
				for _, d := range report.Directives {
					first := d
					if i := strings.Index(d, "\n"); i >= 0 {
						first = d[:i]
					}
					if len(first) > 240 {
						first = first[:237] + "..."
					}
					fmt.Printf("     • %s\n", first)
				}
				fixSession := synthIntegrityFixSession(ss.projectRoot, session, report)
				// DAG splice: any not-yet-started downstream session
				// whose Inputs overlap this source session's Outputs
				// must now also depend on the fix session's synthetic
				// output artifact. Prevents a consumer from running
				// against outputs the integrity gate has flagged.
				ss.SpliceIntegrityFixDep(session, fixSession)
				ss.AppendSession(fixSession)
			}
		}

		stateMu.Lock()
		if success {
			ss.recordSessionSuccess(session, result)
		} else {
			ss.recordSessionFailure(session, result, result.Error)
		}
		stateMu.Unlock()

		recordResult(id, result)
		recordTerminal(id, success)

		if !success {
			resultsMu.Lock()
			if firstErr == nil {
				firstErr = result.Error
			}
			resultsMu.Unlock()
		}
		if ss.OnProgress != nil {
			ss.OnProgress(result)
		}
	}

	// Main dispatch loop. On each iteration we walk the declaration-
	// ordered session list, find any ready-not-yet-started session,
	// spawn a goroutine, and block on the semaphore for concurrency
	// bounds. When no more sessions are ready we drain the wait group
	// and exit. Terminal blocked-by-upstream sessions are surfaced
	// after each completion so we don't wait on them forever.
	//
	// On every iteration we also (a) re-snapshot ss.sow.Sessions to
	// pick up sessions that AppendSession added mid-run (continuations,
	// fix-DAG promotions, decomp overflow — codex P1), and (b) check
	// ctx for cancellation so a --timeout or operator interrupt
	// returns ctx.Err() instead of nil-success (codex P2).
	started := map[string]bool{}
	for {
		if ctx.Err() != nil {
			if firstErr == nil {
				firstErr = ctx.Err()
			}
			break
		}
		// On --continue-on-failure=false, stop dispatching ANY new
		// session as soon as we have a recorded failure — even if the
		// failed session and the next ready session are independent.
		// The sequential runner halts on first failure regardless of
		// dependencies; the parallel runner must mirror that.
		// (codex P2.)
		if !ss.ContinueOnFailure {
			stateMu.Lock()
			anyFailure := len(failed) > 0
			stateMu.Unlock()
			if anyFailure {
				break
			}
		}
		// Pick up any sessions appended via AppendSession since the
		// last iteration so continuations actually get dispatched.
		sessions, order = rebuildMaps()
		dag = BuildSessionDAG(ss.sow)
		// Deadlock watchdog: any session promoted via AppendSession
		// that has been pending past PromotedDispatchSLA without
		// dispatch is almost certainly deadlocked behind its parent
		// in the DAG. Print a loud banner so the operator sees the
		// condition instead of trusting heartbeats. Force-clear the
		// stalled entry from promotedAt to suppress repeat banners
		// while we wait for the operator to intervene.
		stateMu.Lock()
		stStart := map[string]bool{}
		for k, v := range started {
			stStart[k] = v
		}
		stDone := map[string]bool{}
		for k, v := range done {
			stDone[k] = v
		}
		stateMu.Unlock()
		if stalled := ss.CheckPromotedDispatch(stStart, stDone); len(stalled) > 0 {
			fmt.Printf("\n  ⚠️  DEADLOCK WATCHDOG: %d promoted session(s) undispatched > %s — likely DAG cycle or unresolved parent dep:\n", len(stalled), PromotedDispatchSLA)
			for _, id := range stalled {
				deps := dag.Deps[id]
				fmt.Printf("       %s — deps: %v (clear them or set Preempt=true)\n", id, deps)
				// Suppress repeat banners (codex P1: use exported
				// ClearPromoted which takes the promotedMu properly;
				// the previous direct map delete here could race with
				// concurrent AppendSession writes).
				ss.ClearPromoted(id)
			}
		}
		progress := false
		// Collect blocked-but-not-dispatched sessions whose upstream failed
		// AND we can't continue-on-failure. Mark them as blocked and
		// terminate so downstream dependents can resolve too.
		stateMu.Lock()
		for _, id := range order {
			if done[id] || started[id] {
				continue
			}
			for _, dep := range dag.Deps[id] {
				if failed[dep] && !ss.ContinueOnFailure {
					session := sessions[id]
					result := SessionResult{
						SessionID: session.ID,
						Title:     session.Title,
						Error:     fmt.Errorf("BLOCKED: upstream session %s failed", dep),
					}
					ss.recordSessionBlocked(session, result, result.Error)
					done[id] = true
					failed[id] = true
					stateMu.Unlock()
					recordResult(id, result)
					if ss.OnProgress != nil {
						ss.OnProgress(result)
					}
					stateMu.Lock()
					progress = true
					break
				}
			}
		}
		stateMu.Unlock()

		for _, id := range order {
			stateMu.Lock()
			if done[id] || started[id] || !isReady(id) {
				stateMu.Unlock()
				continue
			}
			started[id] = true
			sess := sessions[id]
			stateMu.Unlock()
			// Preempt priority: fix / promoted sessions bypass the
			// regular parallelism semaphore — they always get a
			// slot. Without this, a fix session appended while 4
			// workers are running on long sessions waits forever
			// behind them (observed in run 32: S12-deep-T222
			// promoted, never dispatched, watchdog fired). A
			// priority path means the fix runs concurrently with
			// the blocked parent.
			if !sess.Preempt {
				sem <- struct{}{}
			}
			wg.Add(1)
			// runOne handles its own sem release conditionally on
			// session.Preempt — wrapper just spawns it.
			go runOne(id)
			progress = true
		}
		if !progress {
			// Nothing new was started this pass. Either we are fully
			// drained, or every remaining session is waiting on an
			// in-flight dep. Either way: drain the current cohort,
			// then loop once more to see if new sessions are ready.
			wg.Wait()
			// After draining, loop once more to pick up newly-ready
			// sessions; if still none, we're done. Refresh DAG first
			// so any preempt fix sessions appended during the drain
			// are DAG roots (no inferred deps) and become ready.
			sessions, order = rebuildMaps()
			dag = BuildSessionDAG(ss.sow)
			stateMu.Lock()
			anyLeft := false
			for _, id := range order {
				if !done[id] && !started[id] {
					anyLeft = true
					break
				}
			}
			stateMu.Unlock()
			if !anyLeft {
				break
			}
		}
	}
	wg.Wait()

	// End-of-run summary (matches the sequential runner's trailing
	// banner for consistent operator experience).
	if ss.state != nil && ss.ContinueOnFailure {
		var blocked, failedList []string
		for _, s := range ss.state.Sessions {
			switch s.Status {
			case "blocked":
				blocked = append(blocked, fmt.Sprintf("%s (%s)", s.SessionID, s.LastError))
			case "failed":
				failedList = append(failedList, fmt.Sprintf("%s (%s)", s.SessionID, s.LastError))
			}
		}
		if len(blocked) > 0 || len(failedList) > 0 {
			fmt.Println()
			fmt.Println("  ════════════════════════════════════════════════════════════════")
			fmt.Println("  ❌ END-OF-RUN SUMMARY (parallel): not all sessions converged")
			if len(blocked) > 0 {
				fmt.Printf("  blocked (%d):\n", len(blocked))
				for _, s := range blocked {
					fmt.Printf("    - %s\n", s)
				}
			}
			if len(failedList) > 0 {
				fmt.Printf("  failed (%d):\n", len(failedList))
				for _, s := range failedList {
					fmt.Printf("    - %s\n", s)
				}
			}
			fmt.Println("  ════════════════════════════════════════════════════════════════")
			fmt.Println()
		}
	}

	// Preserve declaration-order in the returned slice so post-run
	// reports read left-to-right the way the SOW is written.
	resultsMu.Lock()
	byID := map[string]SessionResult{}
	for _, r := range results {
		byID[r.SessionID] = r
	}
	resultsMu.Unlock()
	ordered := make([]SessionResult, 0, len(results))
	for _, id := range order {
		if r, ok := byID[id]; ok {
			ordered = append(ordered, r)
		}
	}
	return ordered, firstErr
}
