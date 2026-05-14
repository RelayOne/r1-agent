<!-- STATUS: superseded -->
<!-- CREATED: 2026-05-11 -->
<!-- SUPERSEDED_AT: 2026-05-14 -->
<!-- SUPERSEDED_BY: specs/encryption-at-rest.md, specs/retention-policies.md -->
<!-- DEPENDS_ON: -->
<!-- BUILD_ORDER: 47 -->
<!-- RESOLUTION: The PORTFOLIO-EXECUTION-INDEX line 23 citation "WORK-r1 Tasks 8-10" resolves to encryption-at-rest + retention-policies, both shipped (STATUS:done). Per this spec's own §1 option (3), A2 is closed as superseded by those two specs. The "P0 hardening" best-effort interpretation in the body below remains an open scoping candidate if the operator wants a separate production-readiness pass on the agent-platform substrate — at which point it should be re-issued as a new spec with a fresh BUILD_ORDER, not revived from draft. -->

# P0 Hardening + S-0 Foundation — Agent-Platform Readiness Spec (DRAFT)

## 1. Overview

This spec covers SOW item **A2 — P0 hardening + S-0 foundation (PORTFOLIO Tasks 8–10) [CRITICAL PATH]**.

**Source-doc mismatch flagged for operator review.** The `PORTFOLIO-EXECUTION-INDEX.md` line 23 reads:

> `23 | R1 P0 hardening + S-0 foundation [CRITICAL PATH] | stoke | WORK-r1 Tasks 8-10 | 1.5d | —`

…but inspection of `/home/eric/repos/plans/work-orders/work-r1.md` shows tasks 8–10 are:

| # | Title | Status |
|---|---|---|
| TASK 8 | Per-line XChaCha20-Poly1305 on JSONL streams (1 day) | STATUS: done — `specs/encryption-at-rest.md` |
| TASK 9 | Keyring integration (99designs) (1 day) | STATUS: done — `specs/encryption-at-rest.md` |
| TASK 10 | Retention policies + crypto-shredding enforcement (1–2 days) | STATUS: done — `specs/retention-policies.md` |

i.e. the index-cited tasks are encryption-at-rest work, all merged + tested + deployed by 2026-04-22 per spec frontmatter. The label "P0 hardening + S-0 foundation" does not match that scope.

This spec is therefore a **best-effort interpretation of the labelled scope** ("P0 hardening + S-0 foundation" = production-readiness hardening on the agent-platform substrate). Operator should:

1. Confirm whether this interpretation is correct, OR
2. Provide the actual task definitions A2 was meant to capture, OR
3. Mark A2 closed because the originally-referenced tasks 8–10 are already shipped.

The body below assumes interpretation (1) and produces a buildable spec on that basis. If (3) is correct, this spec collapses to a one-line "STATUS: superseded by encryption-at-rest + retention-policies."

## 2. Stack & Versions

- Go 1.22 (`go.mod`)
- Linux + macOS + Windows
- GCP Cloud Run (9 services), Cloud SQL, Secret Manager
- Existing primitives: `internal/checkpoint/`, `internal/critic/`, `internal/baseline/`, `internal/verify/`, `internal/preflight/`

## 3. Existing Patterns to Follow

- Process lifecycle: `cmd/r1d/main.go` (the daemon entry) and `cmd/r1-server/main.go` (the HTTP service entry). Both already do graceful shutdown via context.WithCancel; this spec hardens the gaps.
- Panic recovery in goroutines: precedent in `internal/agentloop/`. Audit every `go func()` site against this pattern.
- Resource limits: `internal/oneshot/` (after spec A3 lands) will set RLIMIT_AS — reuse the same primitive for daemon/server.
- Preflight: `internal/preflight/` already does workspace assertions on entry — extend to cover daemon/server startup.
- Health endpoints: probably exist in `internal/server/`; this spec adds `/healthz` + `/readyz` if missing and aligns them with Cloud Run probe semantics.

## 4. Goal

Make every long-running R1 process **production-deployable to Cloud Run + restartable without data loss**, with these properties:

1. Every panic is caught and logged; no process exits without writing a structured shutdown record.
2. Every SIGTERM/SIGINT drains in-flight work to a clean checkpoint before exit (≤30s grace; Cloud Run default).
3. Every goroutine is bounded (no orphan goroutine leaks). Periodic leak audit via `runtime.NumGoroutine()` deltas.
4. Every external-IO call has a timeout (no naked `http.Get`, no naked `os.Open` without a deadline).
5. Liveness + readiness probes are honest: never report ready while WAL replay is in progress.
6. Resource limits prevent any one session from exhausting host RSS/FDs.
7. Structured-logging coverage ≥95% of error paths (auditable via static grep).
8. Restart safety: kill -9 on the daemon and restart leaves WAL + ledger consistent (verified by integration test).

## 5. Implementation Checklist

Each item is self-contained for /build subagents.

### Section A — Process Lifecycle

1. [ ] **Panic-recovery audit in every `go func()` site.** Use `grep -rn "go func" cmd/ internal/ | grep -v _test.go` to enumerate. For each site without a `defer recover()` wrapper, add `internal/safego.Go(func())` helper that wraps the body in `defer func(){ if r := recover(); r != nil { logger.Error("panic", "where", caller, "err", r, "stack", debug.Stack()) }}()`. Create `internal/safego/safego.go` with this helper. Acceptance: zero raw `go func()` invocations in production code paths (test code may keep raw form).

2. [ ] **Graceful shutdown on SIGTERM + SIGINT in cmd/r1d/main.go.** Wire a top-level `signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)`. On cancellation: stop accepting new sessions, drain in-flight sessions (call `sessionhub.DrainAll(ctx)` with 25s deadline), flush bus WAL, write `daemon.shutdown` ledger node, return. If drain timeout hits, log forced-exit list and exit code 143. Acceptance: integration test sends SIGTERM, asserts exit ≤30s with `daemon.shutdown` ledger node present.

3. [ ] **Same pattern in cmd/r1-server/main.go.** Drain HTTP requests via `http.Server.Shutdown(ctx)` with the same 25s deadline. Drain active SSE streams by emitting a final `event: shutdown` frame so clients reconnect.

4. [ ] **Restart-safety integration test.** New file `internal/r1d/restart_safety_test.go` (build tag `integration`). Spawns daemon, opens 5 sessions, runs `kill -9 <pid>` mid-session, restarts daemon, asserts: (a) all 5 sessions recover from WAL, (b) ledger is byte-consistent with pre-kill state, (c) no orphan worktrees in `.r1/worktrees/`.

5. [ ] **No-orphan-zombie guarantee.** Audit every `exec.Command` call (Claude/Codex runners in `internal/engine/`); confirm `Setpgid: true` + the existing `killProcessGroup()` chain triggers on context cancel. Add a goroutine-leak counter exposed at `/metrics` so we can alert on drift.

### Section B — Resource Limits

6. [ ] **Daemon-level memory cap.** In `cmd/r1d/main.go`, set `debug.SetMemoryLimit(<configured>)` (default 4 GiB). Configurable via `R1D_MAX_MEM` env. Honor Linux cgroup limit if set (`/sys/fs/cgroup/memory.max`).

7. [ ] **Per-session memory accounting.** Lightweight counter in `internal/sessionhub/` tracking estimated RSS per session (sum of WAL bytes + active worktree size + Cortex Lobe state size). Soft warning at 256 MiB; hard kill at 1 GiB. Configurable.

8. [ ] **Open-fd cap audit.** Daemon-startup preflight reads `getrlimit(RLIMIT_NOFILE)`; if < 4096, log a warning + suggest `ulimit -n 8192`. Document in `docs/operations/sizing.md`.

9. [ ] **Goroutine bound per session.** Each session has a derived `errgroup.Group` with bounded concurrency (default 16 active goroutines). Spawn beyond the bound blocks. Prevents the runaway-fan-out failure mode.

### Section C — External IO Hygiene

10. [ ] **Timeout audit.** Grep for every `http.Get`, `http.Post`, `client.Do`, `net.Dial`, `os.Open`. Confirm every call site uses a context with deadline. Items with no deadline get one (default 30s; configurable). Failures fixed inline; create `audit/no-timeout.md` listing the audited sites + the deadlines applied.

11. [ ] **Network egress allowlist (Cloud Run only).** New `internal/netpolicy/` package. Reads `R1_EGRESS_ALLOWLIST` env (comma-separated hosts). Wraps `http.DefaultClient` to deny disallowed destinations with a `egress.denied` event. Default-allow in local CLI, default-deny in Cloud Run (env-driven).

12. [ ] **External call retry with jitter.** Every retry-prone call (Claude API, Codex API, OpenRouter, OAuth, PostHog, Customer.io, CodeRadar) wraps with `internal/backoff/` (the existing helper if any; otherwise create one). Exponential + ±25% jitter; max 3 attempts; bounded total time 60s.

### Section D — Health + Readiness

13. [ ] **`/healthz` endpoint.** Returns 200 with `{status: "ok", uptime_s: N, version: <commit>}` while the process is alive. Used by Cloud Run liveness probe.

14. [ ] **`/readyz` endpoint.** Returns 200 only when (a) WAL replay complete, (b) ledger SQLite migrations applied, (c) all dependent secrets loaded. Returns 503 otherwise. Used by Cloud Run readiness probe + by upstream load balancer.

15. [ ] **Probe configuration in Cloud Run YAML.** Update `cloudbuild*.yaml` (or terraform/cloud-run config) to wire `/healthz` + `/readyz` with appropriate timeouts (1s healthz, 5s readyz; 3 failures before unhealthy).

### Section E — Observability

16. [ ] **Structured-logging coverage gate.** New `audit/log-coverage.sh` script: greps `internal/` and `cmd/` for `return.*err` patterns; for each, verifies a sibling `logger.` call within 5 lines. Asserts ≥95% coverage. Fails CI on regression.

17. [ ] **Runtime metrics endpoint.** Reuse existing `/metrics` if present; otherwise add. Expose Go runtime metrics (`goroutines`, `heap_alloc`, `gc_pause_p99`) + R1-specific (`active_sessions`, `wal_lag_ms`, `ledger_node_count`).

18. [ ] **Crash-dump artifact.** On panic that escapes the recover wrapper (top-of-stack), write `audit/crash-<ts>.json` with `{ts, version, stack, last_5_log_lines, env_redacted}`. Cloud Build picks up these artifacts on restart for post-mortem.

### Section F — Preflight & Self-Audit

19. [ ] **Preflight extension.** Extend `internal/preflight/` to check: (a) write-permission to `~/.r1/` and `.r1/`, (b) sqlite3 + git on PATH, (c) protected files unchanged (per `internal/snapshot/` pre-merge baseline), (d) bus WAL + ledger SQLite both openable. Fail-fast with actionable messages.

20. [ ] **Self-audit boot record.** First action of every daemon/server boot: write a `r1.boot{ts, version, host, env, preflight_results}` ledger node. Tail this log to spot startup regressions across the fleet.

### Section G — Documentation

21. [ ] **`docs/operations/production-readiness.md`.** Coverage: sizing guidance, env-var reference, probe configuration, panic-recovery contract, crash-dump location, support contact. Tied to the SOW thesis: "the agent-platform is production-ready."

22. [ ] **`docs/operations/runbook-on-call.md`.** Coverage: how to tell whether the daemon is healthy, what each `/metrics` value means, how to triage a crash-dump, escalation steps.

## 6. Boundaries — What NOT To Do

- DO NOT touch encryption-at-rest (already shipped via TASK 8–10).
- DO NOT extend the daemon protocol — this is hardening only.
- DO NOT add new dependencies; reuse stdlib + existing modules.
- DO NOT change exit codes beyond the documented 0/1/2/3/4/130/143 set.
- DO NOT block in init() — startup must be fast (Cloud Run cold-start target <2s).

## 7. Acceptance Criteria

- WHEN `kill -9 <r1d-pid>` is sent THE SYSTEM SHALL restart and recover all session WAL state byte-identical.
- WHEN SIGTERM is sent THE SYSTEM SHALL drain in-flight work in <25s and write a `daemon.shutdown` ledger node.
- WHEN any `go func()` invocation panics THE SYSTEM SHALL log a structured event and continue (not crash the process) unless the panic is at the top of `main`.
- WHEN Cloud Run's readiness probe hits `/readyz` THE SYSTEM SHALL return 503 until WAL replay completes.
- WHEN structured-log coverage drops below 95% THE CI SHALL fail.
- WHEN external HTTP call has no deadline THE STATIC AUDIT SHALL fail CI.

## 8. Estimate Sanity Check

Index says 1.5 days for A2. If the original intent was "encryption tasks 8–10 (already done)," that's accurate (≈0 days remaining). If the interpretation here ("agent-platform P0 hardening") is correct, the 22 checklist items above represent ~5–7 days of focused work. **Flagged for operator clarification.**
