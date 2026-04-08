# Stoke Security & Semantic Audit
**Date:** 2026-04-01
**Scope:** 24 files across stoke/cmd and stoke/internal
**Status:** Complete

---

## Finding Summary

| Severity | Count |
|----------|-------|
| Critical | 5     |
| High     | 11    |
| Total    | 16    |

---

## Findings

### F-01: SECURITY -- Token prefix leaked as account ID dedup key
- **File:** `stoke/internal/pools/pools.go`
- **Line:** 163
- **Pattern:** SECURITY
- **Severity:** critical
- **Issue:** `readAccountID()` uses the first 16 characters of an OAuth access token as a dedup key (`"tok-" + token[:16]`). This token prefix is stored in `manifest.json` on disk (line 66, `0644` permissions). Similarly, `readToken()` returns the full access token, and the caller at line 163 uses `"token-" + token[:min(16, len(token))]`. These token fragments are sufficient for brute-force or replay in some implementations, and are persisted to an unencrypted, world-readable file.
- **Fix:** Hash the token with SHA-256 before using it as a dedup key: `"tok-" + sha256hex(token)[:16]`. Change manifest file permissions from `0644` to `0600`.

### F-02: SECURITY -- Manifest file written world-readable
- **File:** `stoke/internal/pools/pools.go`
- **Line:** 68
- **Pattern:** CONFIG_LEAK
- **Severity:** critical
- **Issue:** `manifest.Save()` writes `manifest.json` with mode `0644`. This file contains pool config dirs that point to credential directories, plus token-derived account IDs (see F-01). Any local user can read it.
- **Fix:** Use `0600` permissions for `os.WriteFile` on the manifest.

### F-03: SECURITY -- Hook bypass via JSON parsing fragility
- **File:** `stoke/internal/hooks/hooks.go`
- **Line:** 96-101
- **Pattern:** SECURITY
- **Severity:** critical
- **Issue:** The PreToolUse hook parses JSON using `grep -o '"tool_name":"[^"]*"'`. This regex-based JSON parsing is fragile and bypassable. If the Claude Code CLI sends JSON with whitespace (`"tool_name" : "Bash"`), unicode escapes (`"tool_\u006eame"`), or the field order differs such that `tool_input` content contains a fake `tool_name` field first, the guard is bypassed. This is the primary security enforcement layer preventing the AI agent from running dangerous commands.
- **Fix:** Replace grep-based JSON parsing with `jq` (which is standard on most systems) or a small compiled helper binary. At minimum: `TOOL_NAME=$(echo "$INPUT" | jq -r '.tool_name // empty')`.

### F-04: SECURITY -- Scope guard shell injection via file path
- **File:** `stoke/internal/hooks/hooks.go`
- **Line:** 288-318
- **Pattern:** SECURITY
- **Severity:** critical
- **Issue:** `installScopeWriteGuard` embeds `absAllowed` and `escFile` into a bash script via string concatenation. The escaping only handles single quotes (`'`). A file path containing backticks, `$(...)`, or double-quote sequences could break out of the single-quoted context in certain edge cases, especially since the `ALLOWED=` assignment uses single quotes but the `ALLOWED_REL=` comparison appears in a double-quoted `echo` context (line 311).
- **Fix:** Write the allowed path to a separate file and read it in the hook script, or use environment variables passed at invocation time rather than embedding paths in the script body.

### F-05: SECURITY -- safeEnvMode2 passes full environment to agents
- **File:** `stoke/internal/engine/env.go`
- **Line:** 71-77
- **Pattern:** CONFIG_LEAK
- **Severity:** critical
- **Issue:** `safeEnvMode2()` copies the entire parent process environment and passes it to the AI agent subprocess. In Mode 2, this means any secrets in the operator's environment (database credentials, AWS keys, internal tokens) are visible to the AI agent. The Mode 1 functions correctly strip sensitive vars, but Mode 2 has no filtering at all.
- **Fix:** Apply the same stripping logic as Mode 1 for known-dangerous variables (ANTHROPIC_API_KEY, AWS_*, etc.) even in Mode 2, or at minimum document this as an explicit security tradeoff and warn at startup.

### F-06: RACE_CONDITION -- Scheduler accesses state maps with inconsistent locking
- **File:** `stoke/internal/scheduler/scheduler.go`
- **Line:** 62-65
- **Pattern:** RACE_CONDITION
- **Severity:** high
- **Issue:** In `Run()`, the pre-population of `s.completed` (lines 62-65) happens before the scheduler loop starts, without holding `stateMu`. While this is technically safe because no goroutines are running yet, the `hasConflict` method (line 183) acquires `lockMu` but is called from within a `stateMu` critical section (line 109-123). The `releaseFiles` method (line 200) acquires `lockMu` while called from `recordResult` which already holds `stateMu`. This nested locking (`stateMu` -> `lockMu`) is consistent, so no deadlock, but `depsOK` (line 175) reads `s.completed` and `s.failed` without holding `stateMu` -- it's called from `findDispatchable` (line 208) which IS under `stateMu`, but also from the dispatch loop (line 113) which IS under `stateMu`. This is actually correct but fragile -- any future caller of `depsOK` outside `stateMu` would be a race.
- **Fix:** Make `depsOK` and `hasConflict` private and document that they require `stateMu` to be held. Consider adding race detector annotations or assertions.

### F-07: RACE_CONDITION -- JSON session store not safe for concurrent workers
- **File:** `stoke/internal/session/store.go`
- **Line:** 50-55, 114-131
- **Pattern:** RACE_CONDITION
- **Severity:** high
- **Issue:** The `Store` struct has a `mu sync.Mutex`, but `SaveState` and `SaveAttempt` hold the mutex only during their own execution. With multiple parallel workers (scheduler goroutines), `SaveAttempt` at line 114 calls `s.readJSON` then `s.writeJSON` -- if two goroutines save attempts for different tasks simultaneously, the read-modify-write is atomic per goroutine (mutex is held), but `addLearnedPattern` at line 145 calls `LoadLearning` and `SaveLearning` without holding `s.mu`, creating a TOCTOU race on `learning.json`.
- **Fix:** Either hold `s.mu` in `addLearnedPattern`, or (better) the code at line 128 already auto-upgrades to SQLite for `workers > 1`, which avoids this. Add a guard that panics/warns if `Store` (not `SQLStore`) is used with parallel workers.

### F-08: ASYNC_ISSUES -- Goroutine leak in stream parser on context cancellation
- **File:** `stoke/internal/stream/parser.go`
- **Line:** 93-95
- **Pattern:** ASYNC_ISSUES
- **Severity:** high
- **Issue:** The inner goroutine at line 93 (`go func() { for scanner.Scan() { lines <- scanner.Text() } ... }()`) reads from `r` (subprocess stdout) and sends to `lines` channel (buffer 16). If the parent goroutine exits due to idle/global timeout (lines 131-137), the `lines` channel is never drained. The inner goroutine blocks on `lines <- scanner.Text()` until the subprocess stdout is closed. If the subprocess hangs (which is the reason for the timeout), this goroutine leaks until `killProcessGroup` closes the pipe.
- **Fix:** Use a select with a done channel in the inner goroutine, or close the reader `r` when the parser exits to unblock `scanner.Scan()`.

### F-09: ASYNC_ISSUES -- Codex runner 60s hardcoded timeout too short
- **File:** `stoke/internal/engine/codex.go`
- **Line:** 172
- **Pattern:** ASYNC_ISSUES
- **Severity:** high
- **Issue:** The Codex runner has a hardcoded `time.After(60 * time.Second)` wait timeout for process exit. For large tasks, Codex may legitimately run longer. The Claude runner uses `r.Parser.PostResultTimeout + 5*time.Second` which is configurable. The Codex runner will kill valid long-running tasks.
- **Fix:** Make the timeout configurable or derive it from the phase spec. Use the same timeout strategy as the Claude runner.

### F-10: MISSING_ERROR_HANDLING -- killProcessGroup ignores errors
- **File:** `stoke/internal/engine/claude.go`
- **Line:** 185-197
- **Pattern:** MISSING_ERROR_HANDLING
- **Severity:** high
- **Issue:** `killProcessGroup` sends SIGTERM, sleeps 3 seconds, then sends SIGKILL. Neither `syscall.Kill` call checks the error return. If SIGTERM succeeds and the process exits, SIGKILL to the dead PGID could kill a recycled PID group (unlikely but possible on heavily loaded systems). The 3-second sleep is blocking and not cancellable.
- **Fix:** Check if the process is still alive after SIGTERM before sending SIGKILL. Use `cmd.Process.Wait()` with a timeout instead of `time.Sleep`.

### F-11: MISSING_ERROR_HANDLING -- copyCredentials silently swallows all errors
- **File:** `stoke/internal/pools/pools.go`
- **Line:** 396-413
- **Pattern:** MISSING_ERROR_HANDLING
- **Severity:** high
- **Issue:** `copyCredentials` returns no error. If credential files fail to copy (disk full, permissions), the caller at line 175 proceeds as if the refresh succeeded. The old pool dir is then removed (`os.RemoveAll(configDir)`), potentially leaving the pool with stale/missing credentials.
- **Fix:** Return an error from `copyCredentials` and check it before removing the source directory.

### F-12: MISSING_ERROR_HANDLING -- SQLStore Stats ignores scan error
- **File:** `stoke/internal/session/sqlstore.go`
- **Line:** 216-219
- **Pattern:** MISSING_ERROR_HANDLING
- **Severity:** high
- **Issue:** `Stats()` calls `QueryRow(...).Scan(...)` but discards the error. If the query fails (e.g., corrupted DB), all return values are zero, which could mislead callers into thinking there are no attempts.
- **Fix:** Return an error from `Stats()` or log it.

### F-13: MISSING_VALIDATION -- No input validation on Ember worker ID in URL paths
- **File:** `stoke/internal/compute/ember.go`
- **Line:** 154, 168-169, 191-214, 233-234, 251-260
- **Pattern:** SECURITY / MISSING_VALIDATION
- **Severity:** high
- **Issue:** The `flareWorker` methods construct URL paths by concatenating `w.id` directly into the path (e.g., `/v1/workers/"+w.id+"/exec"`). The `w.id` comes from the Ember API response (line 86). If the API is compromised or returns a crafted ID containing path traversal characters (`../`), it could redirect requests to unintended endpoints.
- **Fix:** Validate that `w.id` matches a safe pattern (alphanumeric + hyphens) when parsing the spawn response. Use `url.PathEscape(w.id)` in URL construction.

### F-14: INCOMPLETE_IMPL -- Codex stderrDone channel not fully drained on timeout
- **File:** `stoke/internal/engine/codex.go`
- **Line:** 176-180
- **Pattern:** ASYNC_ISSUES
- **Severity:** high
- **Issue:** On timeout (line 172), the code tries to drain `stderrDone` with a `select` but uses `default` which means if stderr hasn't finished yet, the goroutine at line 86-93 is leaked (it's blocked writing to `stderrDone` or reading `stderr`). The comment says "Drain stderrDone to prevent goroutine leak" but the `default` case means it doesn't actually drain -- it just moves on.
- **Fix:** After calling `killProcessGroup(cmd)`, wait for `stderrDone` with a short timeout (e.g., 5 seconds) instead of using `default`.

### F-15: MISSING_VALIDATION -- Review verdict tree-SHA comparison on truncated strings
- **File:** `stoke/internal/workflow/workflow.go`
- **Line:** 528
- **Pattern:** MISSING_VALIDATION
- **Severity:** high
- **Issue:** The error message at line 528 uses `preReviewTree[:12]` and `postReviewTree[:12]` for display. If either SHA is shorter than 12 characters (e.g., empty string or error), this will panic with an index out of range. `preReviewTree` is checked for non-empty at line 523, but `postReviewTree` could theoretically be very short.
- **Fix:** Use a safe truncation helper: `preReviewTree[:min(12, len(preReviewTree))]`.

### F-16: MISSING_VALIDATION -- No TLS certificate verification on Ember/managed API calls
- **File:** `stoke/internal/compute/ember.go`, `stoke/internal/managed/proxy.go`, `stoke/internal/remote/session.go`
- **Lines:** ember.go:31, proxy.go:59, session.go:52
- **Pattern:** SECURITY
- **Severity:** high
- **Issue:** All three files create `&http.Client{}` with default settings. While Go's default TLS verification is enabled, the Ember endpoint URL comes from an environment variable (`EMBER_API_URL`). If an attacker can set this env var to an HTTP (non-TLS) URL, all API keys and tokens are sent in cleartext. There is no validation that the endpoint uses HTTPS.
- **Fix:** Validate that `endpoint` starts with `https://` before use, or at minimum log a warning for non-HTTPS endpoints in production.

---

## Patterns Not Found (Clean)

The following patterns were checked and no issues were found:

- **Path traversal in file operations:** The `hooks.go` `safeWrite` function properly checks every path component for symlinks. The `RuntimeDir` is outside the worktree, preventing agent-writable symlink attacks. The worktree path is validated.
- **eval/exec of user content in Go code:** All `exec.Command` calls use argument arrays (not shell interpolation). No `sh -c` with string concatenation found.
- **Goroutine leaks in subscription manager:** `StartPoller` properly exits on context cancellation. `WaitForPool` respects context.
- **Deadlocks in scheduler:** Lock ordering is consistent (stateMu -> lockMu), no inverse ordering found.
- **Config crash at runtime:** `LoadConfig` / `DefaultPolicy` provide safe defaults. Missing env vars return disabled configs rather than crashing.

---

## Recommendations (Priority Order)

1. **F-03 (Hook bypass):** This is the highest-priority fix. The bash hooks are the primary runtime security boundary. Replace grep-based JSON parsing with `jq` or a compiled helper.
2. **F-05 (Mode 2 env leak):** Either strip known secrets or prominently warn operators.
3. **F-01 + F-02 (Token leak + permissions):** Hash tokens and fix file permissions.
4. **F-04 (Scope guard injection):** Externalize the allowed path instead of embedding in bash.
5. **F-08 + F-14 (Goroutine leaks):** Fix stream parser and codex runner cleanup.
6. **F-13 (Ember path traversal):** Validate worker IDs.
7. **F-11 (Silent credential copy failure):** Return errors from copyCredentials.
