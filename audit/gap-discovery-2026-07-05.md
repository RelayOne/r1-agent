# Gap discovery — confirmed findings (2026-07-05)

Systematic gap audit across UI/UX, backend, core, security, system. 6 dimension scouts → every finding double-verified (verify + independent trace, both must confirm real AND actionable). **26 of 28 raw findings confirmed.** 2 critical, 9 high, 12 medium, 3 low.

Full Go/web gates are green — these are latent correctness/security/UX defects, not build breaks.

## [CRITICAL] Destructive-command breaker bypassed by split flags and long options
`internal/tools/bash_breaker.go:26` — bug — SECURITY

**Detail:** bashBreakerCheck is the ONLY always-on destructive-command floor on the native agentloop bash path (tools.go:1021; the policy gate fails open with no policy client, and the pre-tool-use.sh hook only guards the Claude-Code-CLI subprocess, not the native Registry). Its two rm regexes (reRmRootHome, reRmRfShort) both require the 'r' and 'f' flags fused into a SINGLE token (-rf / -fr / -Rf). They do not match separated short flags or long options, so catastrophic deletes pass straight through to `bash -c`.

**Evidence:** Ran bashBreakerCheck against candidates in a throwaway test in internal/tools: `rm -r -f /` -> blocked=false; `rm --recursive --force /` -> blocked=false; `rm -r -f /home/eric` -> blocked=false; `rm -r -f ~` -> blocked=false; control `rm -rf /` -> blocked=true. reRmRootHome=`\brm\s+(-[a-zA-Z]*\s+)*-[a-zA-Z]*[rR][a-zA-Z]*f[a-zA-Z]*\s+...` needs r-before-f in one flag; the trailing `(-[a-zA-Z]*\s+)*` prefix group cannot consume `--recursive` (the second dash breaks `-[a-zA-Z]*\s+`). reRmRfShort has the symmetric limitation.

**Fix:** Stop matching flag-letter order in a regex. In each stripped segment, if the command word is `rm`, collect all leading `-` tokens, treat `-r/-R/--recursive` and `-f/--force` as independent booleans, and block when BOTH are set AND a subsequent operand matches the root/home target regex. Add the same operand-order-independent handling to reChmodRoot (e.g. `chmod 777 -R /` also slips today).

## [CRITICAL] Assistant/tool transcript prose is injected into the WebView as unescaped HTML (stored XSS)
`desktop/src/panels/session-view.ts:944` — security — UI/UX — desktop (Tauri 2)

**Detail:** renderMarkdown() only escapes text inside fenced/inline code spans. All other prose passes through the fenced-code, inline-code, bold/italic, and paragraph transforms without ever being HTML-escaped, and the result is written via li.innerHTML (line 802 in buildTurnElement / 906 in the indexed variant). Turn text comes from streamed LLM output and tool_result content (applySessionDelta pushes payload.text / lastTool.output verbatim into turn.chunks), so a prompt-injected model response or a malicious tool result containing e.g. `<img src=x onerror=...>` executes JavaScript inside the Tauri renderer, which has IPC access to every host verb (session spawn, fs, shell).

**Evidence:** renderMarkdown (line 948-970) never calls escapeHtml on non-code text; e.g. input "<img src=x onerror=alert(1)>" flows through unchanged and becomes `<p><img src=x onerror=alert(1)></p>`, inserted at line 802: `<div class="r1-sv-turn-text">${renderedText}</div>` inside `li.innerHTML = ...`. r1d-2.test.ts:423 only asserts XSS-escaping for tool *input*, giving false confidence; there is no test for prose HTML.

**Fix:** HTML-escape the full text at the start of renderMarkdown (before any transform), then run the markdown regexes against the escaped string; keep code-span escaping as-is. This preserves rendering while neutralizing raw tags.

## [HIGH] Consensus-rejection scan uses first matching record, not latest-per-model → resolved rejection blocks completion forever and duplicates gaps
`internal/mission/runner.go:430` — bug — CORE — agent loop and workflow engine (agentloop, engine, workflow, scheduler, mission, orchestrate)

**Detail:** runConsensus checks HasConsensus first (which correctly dedups to the latest verdict per model). When consensus is not yet reached it then scans ConsensusRecords (returned ORDER BY timestamp DESC) and routes back to PhaseExecuting on the FIRST record whose Verdict is 'reject'/'incomplete'. Unlike HasConsensus it does NOT collapse to the latest verdict per model, so a model that rejected earlier and later approved still matches its stale reject record. Every pass re-adds that model's old GapsFound with fresh UnixNano-based IDs (unbounded duplicate gaps), and the mission can never advance to Completed while the other model has not yet voted. Because this cycle is Converged→Executing (not Validating→Executing), the convergenceLoops counter at runner.go:207 never increments for it directly; the re-added blocking gaps eventually force Validating→Executing and the mission FAILS via convergence exhaustion instead of completing.

**Evidence:** for _, rec := range records { if rec.Verdict == "reject" || rec.Verdict == "incomplete" { ... for _, gapDesc := range rec.GapsFound { gapID := fmt.Sprintf("consensus-reject-%s-%d", m.ID, time.Now().UnixNano()); r.store.AddGap(...) } return PhaseExecuting, nil } }  — compare store.go:723 HasConsensus which keeps only the most-recent verdict per model. Comment at runner.go:429 even says 'Look at the most recent records' but the code takes the first match in full history.

**Fix:** Mirror HasConsensus: build latest[model]=verdict from the DESC records, and only route to PhaseExecuting if some model's LATEST verdict is reject/incomplete. Also dedupe/skip gap re-creation for a rejection already turned into gaps (e.g. key gap IDs off the consensus record ID rather than time.Now()).

## [HIGH] Utilization poller silently clears an open circuit breaker, re-acquiring a rate-limited pool mid-backoff
`internal/subscriptions/manager.go:280` — bug — BACKEND — servers, APIs, services, data

**Detail:** Release() trips the circuit breaker on 3 consecutive rate-limit failures by setting Status=StatusCircuitOpen and CircuitBreakerUntil=now+5m (lines 138-140). Acquire()/AcquireExcluding() only exclude a pool while `p.Status == StatusCircuitOpen && now.Before(p.CircuitBreakerUntil)` (lines 81, 174). But UpdateUtilization(), called every poll interval by the background poller (usage.go:79), reclassifies status for any pool that is not StatusBusy (line 280): it overwrites Status to Idle/Throttled/Exhausted based purely on utilization, wiping out StatusCircuitOpen while CircuitBreakerUntil is still in the future. The very next Acquire then re-selects the pool that was just rate-limited (and, because ConsecutiveFails is highest, it is sorted last but is still a valid candidate when it is the only free pool), hammering a throttled Anthropic/Codex account and cascading 429s. The 5-minute backoff is effectively defeated on the first poll tick after a trip.

**Evidence:** manager.go:280 `if m.pools[i].Status != StatusBusy {` then switch sets StatusIdle/Throttled/Exhausted with no guard for StatusCircuitOpen; poller calls it unconditionally at usage.go:79 `m.UpdateUtilization(p.ID, data.FiveHour.Utilization, ...)`. Acquire's only circuit-open exclusion is manager.go:81 which no longer matches once Status is overwritten.

**Fix:** In UpdateUtilization, before reclassifying, preserve an active breaker: change the guard to `if m.pools[i].Status != StatusBusy && !(m.pools[i].Status == StatusCircuitOpen && time.Now().Before(m.pools[i].CircuitBreakerUntil)) {`. Update Utilization fields but leave Status/CircuitBreakerUntil untouched while the breaker window is open.

## [HIGH] Fired delayed events are replayed (re-published) on every bus restart — duplicate side effects
`internal/bus/bus.go:823` — data-loss — BACKEND — servers, APIs, services, data

**Detail:** PublishDelayed persists a `schedule` record to delayed.log and arms a timer. When the timer fires it deletes the entry from the in-memory map and calls b.Publish(de.Event) (bus.go:823, and the restore path at 891) but writes NOTHING to the delayed WAL to mark the entry consumed. The only durable states are `schedule` and `cancel`. On the next process start, restoreDelayed -> WAL.ReadDelayed returns every entry that was scheduled and not explicitly cancelled — including entries whose timer already fired — and re-arms them (past-due ones fire after 1ms). WAL.ReadDelayed's own doc claims it returns entries 'scheduled but not cancelled and not yet fired', but there is no 'fired' record so the 'not yet fired' guarantee is unimplemented. Every delayed event that fired before a restart fires a second time, causing duplicate worker.paused/spawn.requested/mission events and duplicate hook side-effects. delayed.log also never compacts.

**Evidence:** bus.go:814-824 timer callback: `delete(b.delayed, cancelID); b.mu.Unlock(); _ = b.Publish(de.Event)` with no b.wal.AppendDelayedCancel. wal.go:207 comment 'not cancelled and not yet fired' vs ReadDelayed logic (wal.go:228-235) which only honors schedule/cancel actions. CancelDelayed at bus.go:847 is the ONLY path that writes a cancel record.

**Fix:** When a delayed timer successfully fires, append a terminal record for that id to delayed.log before/after Publish (e.g. reuse `w.AppendDelayedCancel(cancelID)` or add an explicit `fired` action). Do it in both the PublishDelayed timer (bus.go:821) and restoreDelayed timer (bus.go:889) so ReadDelayed no longer resurrects consumed entries.

## [HIGH] Enforcer PreToolUse hook does not guard protected files against Bash writes
`internal/hooks/hooks.go:202` — broken-wiring — SECURITY

**Detail:** Install() writes the enforcer PreToolUse hook that is the documented protection for .env, CLAUDE.md, .claude/, .stoke/, settings.json and stoke.policy.yaml. The protected-path check (lines 186-199) only fires for TOOL_NAME in Write|Edit|MultiEdit. The Bash branch (lines 202-236) checks git/rm/curl|bash/kill patterns but has NO check for shell writes to protected paths. So a single Bash command overwrites or exfiltrates any 'protected' file, defeating the guard whose entire purpose is to stop that.

**Evidence:** Bash branch at hooks.go:202 begins `if [ "$TOOL_NAME" = "Bash" ] && [ -n "$COMMAND" ]` and never inspects redirection/edit-in-place targets. Commands like `echo pwned > CLAUDE.md`, `tee .env < /dev/stdin`, `sed -i s/.../.../ .claude/settings.json`, or `cat ~/.ssh/id_rsa >> stoke-plan.json` all reach ALLOW. The Write/Edit protected-path case block (hooks.go:190-195) that would catch a Write tool is simply never consulted for Bash.

**Fix:** In the Bash branch add pattern checks that BLOCK when the command redirects/tees/sed -i/cp/mv into a protected path (`>`,`>>`,`tee`,`sed -i`,`dd of=`, `cp/mv ... <protected>`), reusing the same `.claude/ .stoke/ CLAUDE.md .env* settings.json stoke.policy.yaml` list. Also fold the native Registry path (tools.handleWrite/handleStrReplace) behind an equivalent protected-path deny, since resolvePath only confines to workDir and does not protect these files.

## [HIGH] Multi-tool expand/collapse is broken at runtime: every tool block toggles tools[0]
`desktop/src/panels/session-view.ts:827` — bug — UI/UX — desktop (Tauri 2)

**Detail:** Two turn builders exist. buildToolBlock (used by the function-declaration buildTurnElement at line 780) hardcodes `const idx = 0`, so every tool block's toggle button gets data-tool-idx="0". The corrected builder buildTurnElementWithIndexedTools (line 857) is only reachable via the module *export* alias `export { buildTurnElementFinal as buildTurnElement }` (line 1002) — that alias does NOT rebind the internal identifier, so all internal render callers (rebuildTranscript:712, appendUserTurn:733, appendTurnElement:762, refreshTurnElement:774) invoke the broken line-780 version. Clicking Expand on the 2nd/3rd tool of a turn flips tools[0].expanded; on the next session.delta re-render the block visually collapses again while tool[0] shows expanded — visible state thrash during streaming. Tests pass because r1d-2.test.ts imports the *exported* (correct) function.

**Evidence:** Line 826-827: `function buildToolBlock(tool, _turnId){ const idx = 0; // injected per-tool below in the map caller` — but it is called per-tool as `turn.tools.map((t) => buildToolBlock(t, turn.id))` (line 793) with idx never varying. The click handler reads `parseInt(btn.dataset.toolIdx ?? "0")` = 0 for all buttons (line 810).

**Fix:** Delete the non-indexed buildTurnElement (780) and buildToolBlock (826), and rename buildTurnElementWithIndexedTools to buildTurnElement so internal callers bind to the indexed version; keep the export.

## [HIGH] Subprocess crash is reported to the UI as a clean 'ok' end, leaving a dead session that looks resumable
`desktop/src-tauri/src/subprocess.rs:260` — error-handling — UI/UX — desktop (Tauri 2)

**Detail:** When the stdout reader loop ends (subprocess exited for ANY reason, including a crash or non-zero exit), it unconditionally emits `session://ended` with `reason:"ok"` and never checks child.wait()'s exit status, and never removes the dead Session from ManagerState. session-view.ts finalizeSessionTurn maps reason "ok" -> status "paused" with the Resume button enabled and no error message. The user sees a crashed run as a pausable session; clicking Resume issues rpc.resume which the stdin writer task has already torn down, surfacing an opaque 'subprocess stdin closed' / timeout instead of a real crash notice.

**Evidence:** Lines 259-266 emit `{"event":"session.ended",...,"reason":"ok",...}` after `while let Ok(Some(line)) = reader.next_line()` exits, with no exit-code inspection. The session is only removed in cancel_session (line 361), so the crashed entry lingers.

**Fix:** Await child.wait() (or take the child handle) after the read loop, emit reason "error" for non-zero/killed exit, wake any pending RPC waiters with an error, and remove the session from st.sessions.

## [HIGH] useDaemonSocket returns an unmemoized object, causing a WS unsubscribe/subscribe + REST storm on every render during streaming
`web/src/hooks/useDaemonSocket.ts:159` — broken-wiring — UI/UX — web frontend (React) + TUI (Bubble Tea)

**Detail:** useDaemonSocket() returns a fresh object literal `{ subscribe, unsubscribe, sendMessage, interrupt, connect, disconnect }` on every render. Although the inner callbacks are stable (useCallback), the returned wrapper object has a new identity each render. SessionView's subscribe effect (SessionView.tsx:58-76) lists `socket` in its dependency array `[sessionId, client, store, socket]` and its comment falsely claims 'socket is stable across renders'. `client` and `store` ARE stable (verified: R1dClientProvider and DaemonStoreProvider cache per-daemon), so `socket`'s changing identity re-runs the effect on EVERY render. SessionViewContent calls useChat(), which subscribes to `messages.byKey` / session status, so it re-renders on every coalesced streaming batch (~rAF/flush frequency, plus a synchronous flush on every message.complete). Each re-run does: cleanup → `socket.unsubscribe(sessionId)` (sends a WS unsubscribe frame + markUnsubscribed), then run → `socket.subscribe(sessionId)` (WS subscribe frame + markSubscribed) AND `client.listLanes(sessionId)` (a REST GET). markSubscribed/markUnsubscribed always allocate a new Set and commit new state (sessionsSlice.ts:83-95), so this also perturbs the store. Net effect during an active turn: the session is torn off and re-attached to its WS event stream dozens of times per second and a listLanes HTTP request is fired on every render — hammering the daemon and opening a gap on each unsubscribe where server-side lane/message deltas can be missed. LaneFocus.tsx:47-52 has the identical defect (deps `[sessionId, socket]`).

**Evidence:** useDaemonSocket.ts:159 `return { subscribe, unsubscribe, sendMessage, interrupt, connect, disconnect };` (no useMemo). SessionView.tsx:70-76 cleanup calls `socket.unsubscribe(sessionId)` and the effect body calls `socket.subscribe(sessionId)` + `client.listLanes(sessionId)`, deps `[sessionId, client, store, socket]` with comment 'socket is stable across renders'. sessionsSlice.ts:83 markSubscribed always `new Set(...)` + set().

**Fix:** Wrap useDaemonSocket's return value in `useMemo(() => ({ subscribe, unsubscribe, sendMessage, interrupt, connect, disconnect }), [subscribe, unsubscribe, sendMessage, interrupt, connect, disconnect])` (all deps are stable useCallbacks, so the memo never invalidates). Alternatively, remove `socket` from the SessionView.tsx:76 and LaneFocus.tsx:52 effect dep arrays (depend only on `sessionId`/`client`/`store`).

## [HIGH] Deploy triggers pass an undeclared substitution (_CODERADAR_SAMPLE_RATE) → every SaaS deploy build fails at substitution
`services/scripts/setup-cloudbuild-triggers.sh:46` — broken-wiring — SYSTEM — infra, CI, config, ops

**Detail:** setup-cloudbuild-triggers.sh provisions r1-services-{prod,staging,dev}-deploy triggers with --substitutions="_ENV=${env},_CODERADAR_SAMPLE_RATE=${coderadar_sample_rate}". But services/cloudbuild-deploy.yaml declares ONLY _ENV in its substitutions block (lines 30-31), never references _CODERADAR_SAMPLE_RATE anywhere (it was deliberately removed per audit A089, header lines 23-28), and sets no options.substitution_option: ALLOW_LOOSE (lines 33-35). Cloud Build's default STRICT substitution rejects a build when a trigger supplies a key the template neither declares nor uses: 'key in the substitution data is not matched in the template'. So every push to main/staging/dev handled by a trigger created from this script fails before any step runs — a very likely root cause of r1-services-prod-deploy being RED.

**Evidence:** setup script: --substitutions="_ENV=${env},_CODERADAR_SAMPLE_RATE=${coderadar_sample_rate}" (line 46). grep of cloudbuild-deploy.yaml for _CODERADAR_SAMPLE_RATE returns only the comment at line 25 documenting its removal; the substitutions block is `_ENV: prod` only; no ALLOW_LOOSE present.

**Fix:** Drop _CODERADAR_SAMPLE_RATE from the --substitutions in setup-cloudbuild-triggers.sh (the sample_rate_for_env helper is dead now), OR declare/consume _CODERADAR_SAMPLE_RATE in cloudbuild-deploy.yaml, OR add options.substitution_option: ALLOW_LOOSE. Removing it from the script is the correct fix since the yaml intentionally has no reader.

## [HIGH] smoke-coderadar deploy step builds a `go 1.25.5` module with Debian's stale golang-go
`services/cloudbuild-deploy.yaml:294` — error-handling — SYSTEM — infra, CI, config, ops

**Detail:** The smoke-coderadar step runs on the cloud-sdk:slim (Debian) image and does `apt-get install -y -qq golang-go make` then `make smoke-coderadar`. That make target (Makefile lines 61-70) runs `go test -tags=coderadar_integration ./internal/hub/builtin/` and `go test -tags=coderadar_smoke ./internal/coderadar/...`. Debian's golang-go package is Go 1.19 (bookworm) / at most 1.24 (trixie), but go.mod declares `go 1.25.5`. With an older toolchain Go either hard-fails the module version gate ('go.mod requires go >= 1.25.5 (running go 1.19.x)') or silently triggers a GOTOOLCHAIN network download inside a step that other steps deliberately avoid — every other Go step in this repo's Cloud Build configs uses the pinned golang:1.25 image. Since the final `smoke` step waitFor includes smoke-coderadar, its failure reddens the whole prod deploy.

**Evidence:** cloudbuild-deploy.yaml:294 `apt-get update -qq && apt-get install -y -qq golang-go make` then line 301 `make smoke-coderadar ENV=${_ENV}`; Makefile:62 `go test -tags=coderadar_integration ...`, Makefile:70 `smoke-coderadar: test-coderadar-integration`; go.mod:3 `go 1.25.5`.

**Fix:** Run smoke-coderadar in a `golang:1.25` step (mount the built gcloud-materialized DSN via env) instead of apt-installing golang-go on cloud-sdk:slim; or split it into a golang:1.25 build step + a cloud-sdk step, matching how every other Go gate in these configs pins the toolchain.

## [MEDIUM] Native build-verification gate runs only once per dispatch, so the injected 'fix the build' errors are never re-verified
`internal/engine/native_runner.go:530` — broken-wiring — CORE — agent loop and workflow engine (agentloop, engine, workflow, scheduler, mission, orchestrate)

**Detail:** PreEndTurnCheckFn guards the ecosystem/bash build gate behind a closure-scoped `buildChecked` bool that is set true on the first invocation. The agentloop's contract (loop.go:632) is: on a non-empty return it injects the errors ('Do NOT end your turn until the build passes') and forces another turn. But on that next end_turn attempt buildChecked is already true, so the entire build block is skipped and the function returns "" — end_turn is accepted even if the build is still broken. The Cline-pattern guarantee (verify-until-green in the same context) is reduced to a one-shot advisory: the model can ignore the injected errors or emit a bad fix and still terminate with a red build.

**Evidence:** buildChecked := false
cfg.PreEndTurnCheckFn = func(messages []agentloop.Message) string {
    if !buildChecked {
        buildChecked = true
        if msg := runEcosystemGate(...); msg != "" { return msg }
        ... detectBuildCommand fallback ...
    }
    // second and later end_turns skip the whole block
    if extraCheck != nil { ... }
    return ""
}

**Fix:** Re-run the build gate on every end_turn attempt (drop the once-only latch) or cache the compile result keyed on the current worktree diff/HEAD so it re-verifies after the model edits, rather than unconditionally passing after the first check.

## [MEDIUM] Throttle/promptguard/policy tool denials increment consecutiveErrors and can hard-abort a run with max_errors
`internal/agentloop/loop.go:826` — error-handling — CORE — agent loop and workflow engine (agentloop, engine, workflow, scheduler, mission, orchestrate)

**Detail:** executeTools sets hasError=true for infrastructure denials — throttle (l.checkThrottle), ValidateToolInput, hub Deny policy, and validateAgentloopToolInput — identically to a genuine tool failure. Run() then does `if hasError { consecutiveErrors++ }` and aborts the whole loop with StopReason=max_errors once consecutiveErrors >= MaxConsecutiveErrs (default 3). So (a) a rate-limited session that retries throttled tools 3 turns in a row is killed rather than allowed to wait/recover, and (b) a single turn that performs several successful edits plus one throttled/denied tool counts as an error turn — three such productive turns abort the run. validateAgentloopToolInput runs unconditionally (not gated by nil config), so promptguard rejections always feed this counter.

**Evidence:** if dec := l.checkThrottle(ctx, tc.Name); !dec.Allowed { mu.Lock(); hasError = true; results[idx] = ContentBlock{...IsError:true} ...}  →  Run(): if hasError { consecutiveErrors++ } else { consecutiveErrors = 0 }; if consecutiveErrors >= l.config.MaxConsecutiveErrs { result.StopReason = "max_errors"; return result, fmt.Errorf("aborted after %d consecutive tool errors", consecutiveErrors) }

**Fix:** Track denial results (throttle/policy/promptguard) separately from model-caused handler errors and exclude them from the consecutiveErrors abort budget; or reset/decrement consecutiveErrors when the turn also produced at least one successful tool_result.

## [MEDIUM] Worktree + branch leak on execute-failure and pool-exhaustion returns in the retry loop
`internal/workflow/workflow.go:830` — data-loss — CORE — agent loop and workflow engine (agentloop, engine, workflow, scheduler, mission, orchestrate)

**Detail:** Inside the EXECUTE+VERIFY retry loop, three error-return paths advance the task to Failed (or abort) and return WITHOUT calling e.Worktrees.Cleanup(ctx, handle): the runErr genuine-failure return (line 830), the execResult.IsError non-rate-limited return (line 839), and the all-pools-exhausted return (line 802). Every sibling error path in the same function (budget at 655, prepare-failure at 699/707, convergence at 1276, merge gates at 1309/1313, etc.) explicitly cleans up the handle, and the only deferred cleanup (line 439) just runs AfterTask hooks — it does not remove the worktree. A task that reaches Failed is not resumed, so its git worktree and branch leak permanently on disk. Under repeated agent errors this accumulates orphan worktrees.

**Evidence:** if execResult.IsError && execResult.Subtype != "rate_limited" {
    _ = e.advanceState(taskstate.Failed, ...)
    return result, fmt.Errorf("execute phase (attempt %d): agent reported error (subtype=%s)", attempt, execResult.Subtype)
}  // no e.Worktrees.Cleanup(ctx, handle) — contrast line 655/699 which do clean up

**Fix:** Add e.Worktrees.Cleanup(ctx, handle) before the returns at lines 802, 830, and 839 (leaving the intentional ctx-cancelled resume path at 826-828 alone), matching the cleanup discipline of the other error returns.

## [MEDIUM] SQLStore.SaveLearning wipes the patterns table then re-inserts without a transaction — data loss on mid-loop failure
`internal/session/sqlstore.go:205` — data-loss — BACKEND — servers, APIs, services, data

**Detail:** SaveLearning runs `DELETE FROM patterns` as its own committed statement, then loops issuing individual INSERTs. There is no surrounding transaction. If any INSERT fails (UNIQUE constraint on `issue` from a duplicate in l.Patterns, disk full, SQLITE_BUSY after the 5s timeout, or a cancelled process between the DELETE and the loop), the DELETE has already committed and the store is left with all learned patterns erased and only a partial (or empty) set restored. Learning accumulated across sessions is silently lost.

**Evidence:** sqlstore.go:205-213: `if _, err := s.db.Exec("DELETE FROM patterns"); err != nil {...}` immediately followed by `for _, p := range l.Patterns { ... s.db.Exec("INSERT INTO patterns ...") }` — no s.db.Begin()/tx.Commit(). Two duplicate `issue` values in the input alone trigger the UNIQUE(issue) constraint at migrate.go and abort mid-loop.

**Fix:** Wrap the DELETE + inserts in a single transaction: `tx, _ := s.db.Begin(); tx.Exec("DELETE FROM patterns"); for ... tx.Exec(INSERT...) with rollback on error; tx.Commit()`. Consider `INSERT OR REPLACE` to tolerate duplicate issues.

## [MEDIUM] A2A InMemoryTaskStore grows without bound — unauthenticated peers can exhaust memory
`internal/a2a/task.go:212` — perf — BACKEND — servers, APIs, services, data

**Detail:** Submit() inserts a new *Task into s.tasks keyed by a fresh UUID on every a2a.task.submit RPC and there is no eviction, TTL, or cap on the map — the TaskStore interface has no Delete and nothing sweeps terminal tasks. cmd/r1-a2a runs the RPC endpoint with auth OFF by default (STOKE_A2A_TOKEN/R1_A2A_TOKEN empty = open, per main.go:57/73-76 and the printed `auth=%t`). An open A2A server therefore lets any peer POST unlimited /a2a/rpc submits, each retaining its prompt bytes, history (up to 256 entries) and artifacts forever, until the process OOMs. Per-task History is bounded but the number of tasks is not.

**Evidence:** task.go:212 `s.tasks[id] = t` with map created once in NewInMemoryTaskStore (task.go:177); no delete/expiry anywhere in the file; interface (task.go:154-164) has no removal method. cmd/r1-a2a/main.go:73 `srv := a2a.NewServer(card, store, token)` with token possibly "".

**Fix:** Add a bounded/TTL sweep: evict terminal tasks older than a retention window (e.g. sweep goroutine dropping IsTerminal tasks past CreatedAt+TTL), or cap map size with FIFO eviction of terminal tasks. Alternatively refuse Submit when len(s.tasks) exceeds a configured max.

## [MEDIUM] Enforcer hook git-mutation and rm guards evaded by `git -C` and split rm flags
`internal/hooks/hooks.go:209` — security — SECURITY

**Detail:** The generated PreToolUse hook's Bash guards are anchored grep patterns that assume the subcommand immediately follows the binary, and the rm guard only matches the fused `-rf`. Global-option forms and split flags slip past, so 'blocked' git mutations and destructive deletes still execute in the managed worktree.

**Evidence:** hooks.go:209 `grep -qE 'git\s+push'` misses `git -C . push` and `git --git-dir=... push`; line 210 `git\s+rebase` misses `git -C . rebase`. hooks.go:220-221 `rm\s+-rf\s+/` and `rm\s+-rf\s+~` miss `rm -r -f /`, `rm -fr /`, and `rm --recursive --force ~`. These are stricter-looking than they are because the regex hard-codes token adjacency and single-flag fusion.

**Fix:** Tolerate intervening global options between `git` and the subcommand (e.g. `git(\s+-\S+|\s+--\S+=\S+)*\s+(push|rebase|reset)`), and replace the single-flag rm regex with a check for recursive AND force flags in any order/spelling (mirror the corrected Go breaker) before a root/home operand.

## [MEDIUM] Lane subscription failure is silently swallowed — rail stays stuck on 'No lanes yet.'
`desktop/src/panels/lane-rail.ts:101` — error-handling — UI/UX — desktop (Tauri 2)

**Detail:** On a hard subscribe failure, subscribeLanes' hook path synthesizes a killed LaneEvent with lane_id:"" (laneSubscription.ts:154-161) as its documented error-surfacing mechanism. But lane-rail's applyEvent 'killed' case does `next.get(ev.lane_id)` = get("") -> undefined -> no-op, and mountLaneRail's own .catch swallows the error (line 223-226 'Swallow'). Net result: if session_lanes_subscribe rejects (host verb missing, session not found, channel drop), the rail shows the normal empty 'No lanes yet.' state forever with zero indication anything failed.

**Evidence:** applyEvent killed branch: `const cur = next.get(ev.lane_id); if (cur) {...}` — for lane_id "" cur is undefined so nothing renders. lane-rail.ts:223 catch body is only a comment. The empty message (line 179) is 'No lanes yet.' regardless of failure.

**Fix:** In the subscribe .catch (and/or on a killed event with empty lane_id) set an error flag and pass it to LaneSidebar as an error/emptyMessage (or fire toast.error from lib/toast), so the failure is visible.

## [MEDIUM] TUI truncation helpers byte-slice multibyte UTF-8, corrupting rendering of lane activity / titles / task text
`internal/tui/lanes/lanes_update.go:239` — ux-break — UI/UX — web frontend (React) + TUI (Bubble Tea)

**Detail:** `truncate(s, n)` compares byte length (`len(s) <= n`) against a display-cell budget and slices at a byte offset (`s[:n-1]`). Lane titles, roles, models, and especially `Activity` come straight from streamed LLM output (translateHubLaneEvent copies ev.Lane.Block.Text / Thinking / NoteSummary into snap.Activity), which routinely contains non-ASCII (em dashes, quotes, emoji, CJK). When such a string exceeds the width, `s[:n-1]` cuts on a byte boundary in the middle of a multibyte rune, emitting invalid UTF-8 that renders as a replacement glyph / mojibake in the lane box. The same byte-slice pattern appears in renderLanePeer (`row[:width]`, lanes_view.go:308-310), viewStatusBar short-form (`short[:w]`, lanes_view.go:424-427), and interactive.go's `truncStr` (`s[:n]` / `s[:n-3]`, interactive.go:418-422) used for task descriptions and tool-result content. No panic (indices stay in range), but the panel/dashboard shows corrupted text whenever real agent output is wider than a lane cell.

**Evidence:** lanes_update.go:243 `if len(s) <= n { return s }` and :249 `return s[:n-1] + "…"` — byte length vs cell count, byte slice. lanes_transport.go:321-333 feeds LLM text into snap.Activity which flows through truncate. interactive.go:419 `if len(s) <= n { return s }` / :421 `return s[:n-3] + "..."`.

**Fix:** Make truncation rune-aware: convert to `[]rune` (or use uniseg/lipgloss width + rune iteration) before measuring and slicing, e.g. `r := []rune(s); if len(r) <= n { return s }; return string(r[:n-1]) + "…"`. Apply the same to truncStr and to the `row[:width]` / `short[:w]` clamps in lanes_view.go.

## [MEDIUM] deploy.sh binds DATABASE_URL secret + mounts a Cloud SQL instance the coord-api image never reads
`services/deploy.sh:125` — broken-wiring — SYSTEM — infra, CI, config, ops

**Detail:** The manual deploy path deploy_one() for r1-coord-api adds --set-secrets DATABASE_URL=r1-$env-shared-DATABASE_URL:latest and --add-cloudsql-instances $PROJECT:$REGION:r1-$env-pg. But r1-coord-api service code reads no DATABASE_URL (grep of services/r1-coord-api returns nothing), and the CI deploy (cloudbuild-deploy.yaml, audit A089, lines 23-28) explicitly REMOVED both because there is no reader and no coord-api persistence layer. The two deploy paths have diverged: an operator running services/deploy.sh prod will mount a Cloud SQL instance `relayone-488319:us-central1:r1-prod-pg` that, per that same audit note, has no consumer and may not exist — `gcloud run deploy` fails if the instance/secret is absent, or needlessly attaches a DB to a stateless service.

**Evidence:** deploy.sh:125 `--set-secrets="DATABASE_URL=r1-$env-shared-DATABASE_URL:latest,..."` and :126 `--add-cloudsql-instances="$PROJECT:$REGION:r1-$env-pg"`; grep DATABASE_URL over services/r1-coord-api/ → no hits; cloudbuild-deploy.yaml:23-28 documents removal of DATABASE_URL/Cloud SQL for lack of a reader.

**Fix:** Remove the DATABASE_URL secret binding and --add-cloudsql-instances from the r1-coord-api case in deploy.sh so the manual path matches the CI deploy; re-add both together only when a persistence layer that reads DATABASE_URL lands.

## [MEDIUM] vendor-check CI step is a no-op that cannot catch an inconsistent vendor tree
`cloudbuild.yaml:36` — missing-validation — SYSTEM — infra, CI, config, ops

**Detail:** The vendor-check step exists to gate the vendored build, but its entire body is `ls vendor/modules.txt > /dev/null`. That only asserts the file exists; it does not detect a stale/inconsistent vendor directory (a dep added to go.mod without re-running `go mod vendor`). The real failure mode — `go: inconsistent vendoring` — is therefore not caught here; it surfaces downstream in build/test/race (all of which use -mod=vendor), producing a confusing red far from the actual cause and after minutes of compilation. The step's name and placement imply a real consistency gate that isn't implemented.

**Evidence:** cloudbuild.yaml:36 `ls vendor/modules.txt > /dev/null` is the whole vendor-check script; build/test/race steps all pass `-mod=vendor` and depend on vendor-check via waitFor.

**Fix:** Replace the body with an actual consistency check, e.g. `go mod verify` plus `cp go.mod go.mod.bak && go mod vendor && git diff --exit-code vendor/ go.mod go.sum` (or `go mod vendor -v` then fail on any diff), so an out-of-sync vendor tree fails fast with a clear message.

## [MEDIUM] Shared build-output dirs symlinked into every parallel worktree corrupt concurrent builds / mutate the main repo
`internal/worktree/manager.go:154` — data-loss — SYSTEM — infra, CI, config, ops

**Detail:** symlinkSharedDeps symlinks node_modules, vendor, .venv, target (Rust), __pycache__, .gradle, .m2 from the main repo into each new worktree. The scheduler runs worktrees in parallel, and several of these are WRITABLE build-output/cache dirs, not read-only dep caches: `target`, `__pycache__`, and `.gradle` hold incremental build state. Two concurrent worktrees building the same Rust/Python/Gradle project write through the symlink into the SAME repoRoot directory simultaneously, corrupting incremental state; likewise an agent running `npm install`/`pip install` writes through node_modules/.venv into the main repo, mutating the very tree the harness treats as the trusted baseline. This is a real concurrency + data-integrity hazard, not just a perf optimization.

**Evidence:** manager.go:154-162 sharedDepDirs() returns {node_modules, vendor, .venv, target, __pycache__, .gradle, .m2}; symlinkSharedDeps (169-195) creates `worktree/<dir> -> repoRoot/<dir>`; Prepare() calls it for every worktree and the scheduler dispatches worktrees in parallel.

**Fix:** Restrict sharing to genuinely read-only immutable dependency caches (drop target, __pycache__, .gradle, and any dir a build writes into), or symlink only read-only module caches (~/.m2, GOPATH/pkg/mod) rather than in-tree output dirs; for writable dep dirs, copy-on-write or install per-worktree instead of symlinking into repoRoot.

## [LOW] A2A RPC bearer check uses a non-constant-time string compare (timing side channel on the token)
`internal/a2a/httpserver.go:280` — security — BACKEND — servers, APIs, services, data

**Detail:** checkBearer compares the presented token to the configured secret with `parts[1] == want` — a byte-wise early-exit comparison that leaks token length and matched-prefix length via response timing over many probes. The rest of the codebase deliberately uses subtle.ConstantTimeCompare for exactly this (server/auth_middleware.go:59, server/ws_ticket.go:85), so the A2A gate is an inconsistent weak spot on a network-facing auth boundary.

**Evidence:** httpserver.go:280 `return parts[1] == want` inside checkBearer, called from the /a2a/rpc auth gate at httpserver.go:185. Contrast auth_middleware.go:59 which uses subtle.ConstantTimeCompare.

**Fix:** Replace with a length-checked constant-time compare: `return len(parts[1]) == len(want) && subtle.ConstantTimeCompare([]byte(parts[1]), []byte(want)) == 1`.

## [LOW] Discovery-wizard 'Reconnect' failure produces no user feedback
`desktop/src/components/discovery-wizard-mount.tsx:155` — ux-break — UI/UX — desktop (Tauri 2)

**Detail:** handleReconnect awaits invoke('daemon_reconnect') and on rejection only console.error's, with an inline comment acknowledging 'Component currently doesn't accept an error prop, so we leave the wizard open'. The spinner resets to 'Reconnect' (discovery-wizard.tsx:106 finally) but the user gets zero on-screen indication of why reconnect failed (daemon still not found, sidecar spawn failed, etc.) — the button just flickers. This is the primary first-launch recovery flow, and daemon_reconnect returns a structured IpcError precisely so it can be shown.

**Evidence:** discovery-wizard-mount.tsx:151-159 catch body is comment-only ('Surface failure inline by re-rendering with a hint? ... we leave the wizard open'). The host returns a real taxonomy error via discovery_error_to_ipc (ipc.rs:555).

**Fix:** Surface the rejection via lib/toast.ts toast.error(err.message) (or thread an error string into DiscoveryWizard as a new prop) so a failed reconnect explains itself.

## [LOW] Remote lanes transport reconnect never prunes reaped lanes; stale lanes persist forever after resubscribe
`internal/tui/lanes/lanes_update.go:84` — broken-wiring — UI/UX — web frontend (React) + TUI (Bubble Tea)

**Detail:** On reconnect, remoteTransport.runOnce re-issues session.subscribe and emits a fresh full-snapshot `list` event (lanes_transport.go:806-813), and the local transport replays List on every Subscribe. The laneListMsg handler installs missing lanes and updates existing ones but never removes lanes that are absent from the new snapshot. So if the daemon reaped a lane while the TUI was disconnected (or between reconnects), that lane keeps rendering in the panel indefinitely and keeps contributing to status-bar counts / aggregate cost. The model has no lane-removal path at all, so a snapshot-driven resync cannot correct drift.

**Evidence:** lanes_update.go:91-123 iterates `msg.Lanes` doing 'install missing / update existing' with no reconciliation against lanes already in `m.lanes`; there is no delete branch anywhere in Update. Comment at :86-90 explicitly only handles 'install missing, update existing'.

**Fix:** In the laneListMsg branch, build a set of snapshot IDs and drop any lane in m.lanes not present in it (rebuilding laneIndex afterward), so a full-snapshot replay is authoritative. Guard so this only applies to full 'list' snapshots, not incremental ticks.

## [LOW] StatusBar latency segment is never wired — always renders an em-dash
`web/src/components/StatusBar.tsx:123` — broken-wiring — UI/UX — web frontend (React) + TUI (Bubble Tea)

**Detail:** StatusBar documents a 'WS round-trip latency in ms (when the heartbeat reports it)' segment, but `latencyMs` is a prop that no caller ever supplies: SessionView.tsx:168, DaemonHome.tsx:122, and LaneFocus.tsx:106 all render `<StatusBar store={store} sessionId={...} />` with no latency. The ResilientSocket heartbeat (ws.ts) sends ping/pong but never records or surfaces an RTT, and nothing plumbs it into the store. Result: the latency readout is permanently '— ms', a dead telemetry field the UI advertises as live.

**Evidence:** StatusBar.tsx:123-127 `{typeof latencyMs === "number" ? \`${Math.round(latencyMs)} ms\` : "— ms"}`. No `latencyMs=` prop passed at any StatusBar call site (SessionView.tsx:168, DaemonHome.tsx:122, LaneFocus.tsx:106). ws.ts ping/pong path never computes an RTT.

**Fix:** Either record ping-send/pong-receive timestamps in ResilientSocket, expose the RTT via onEnvelope/store, and pass it as `latencyMs`; or remove the latency segment until it is backed by real data so the UI does not advertise a non-functional metric.
