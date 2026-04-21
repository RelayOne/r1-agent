# Synthesized Recommendations — Stoke Full Agent Scoping

Cross-spec decisions with rationale. Read before final spec write.

## CROSS-CUTTING

### C1. Event emitter: extend `internal/streamjson/`, do NOT create `internal/events/`
**Evidence:** RT-STOKE-SURFACE §14. The emitter exists, follows Claude Code NDJSON, is wired at main.go:1476-1480, supports 5 event types (system/assistant/user/result/stream). `uuid`/`session_id`/`subtype`/`_stoke.dev/*` extensions already present.
**Action:** Add new event subtypes (descent.tier, descent.classify, descent.resolve, verify.start, hitl_required, plan.ready) as `_stoke.dev/*` or a new `subtype` tree. Keep Claude-Code wire compatibility.

### C2. Event log: SQLite + WAL, single `events` table
**Evidence:** RT-05. Stoke already uses SQLite for session.SQLStore and wisdom; one more table is trivial. Per-session JSONL rejected (cross-session FKs needed for parent/child tasks).
**Schema:** `events(id ULID PK, ts, session_id, branch_id, type, call_id, parent_id, data JSON, hash)` — hash chain for tamper detection.
**Callsite:** New `internal/eventlog/` package.

### C3. Bus already exists; make spec implementations publish to it
**Evidence:** RT-STOKE-SURFACE §12. `internal/bus/bus.go` + `wal.go` complete with pub/sub, hooks, pattern matching, WAL — but zero publishers.
**Action:** spec-1 (descent events), spec-2 (ingest from bus into stream-json), spec-3 (executor lifecycle events) all publish. Bus is the in-process nervous system; streamjson is the external wire format. Keep them separate.

### C4. Anti-deception contract: inject for ALL workers, no opt-out
**Evidence:** RT-06 MASK benchmark shows 8-12% honesty lift on small models. Opt-out invites drift.
**Action:** Inject at `sow_native.go:3909` (after canonical-names, before skills). Same block for task + repair dispatch.

### C5. Forced self-check: use `agentloop.Config.PreEndTurnCheckFn`, NOT supervisor rule
**Evidence:** RT-STOKE-SURFACE §2, §13. PreEndTurnCheckFn is the cleanest hook; supervisor rules are for cross-turn behavior.
**Action:** Parse `<pre_completion>` XML from final message, cross-check FILES_MODIFIED list against actual file state + each AC command claim against harness build. Mismatches = immediate code_bug signal → descent T4.

## spec-1 Descent Hardening

### D1. Keep `STOKE_DESCENT=1` opt-in flag; do NOT default-on yet
**Rationale:** Descent stabilizing per user. Hardening first, rollout decision in a future commit.

### D2. Per-file repair cap: `MaxRepairsPerFile = 3` (Cursor parity)
**Evidence:** RT-11 verbatim Cursor 2.0 cap.
**Action:** Add `FileRepairCounts map[string]int` + `MaxRepairsPerFile int` to `DescentConfig`. Cap enforced in T4 before calling RepairFunc. On cap hit, emit `descent.file_cap_exceeded` event + fail AC (no silent skip).

### D3. Ghost-write detection: post-tool-call supervisor hook
**Rationale:** RT-STOKE-SURFACE §2 — supervisor extraction hook exists at autoExtractTaskSupervisor. Add a rule that after any file-write tool, bash-verifies `test -f <path> && [ -s <path> ]`. If empty/missing, force retry.
**Effort:** Higher than work.md (3-4hr) — not free. Keep in P1.

### D4. Env issue reporting tool: dedicated `report_env_issue` tool
**Evidence:** Devin pattern (RT-11 and prior).
**Action:** Add tool to worker loop. When invoked, emit bus event `worker.env_blocked` + classify AC as `environment` immediately at T3 (skip multi-analyst, save ~$0.10/AC).

### D5. Bootstrap per descent cycle: re-run `ensureWorkspaceInstalled` after repair that touches package.json / go.mod / Cargo.toml / requirements.txt
**Evidence:** RT-STOKE-SURFACE §15 + work.md §3.1.
**Action:** Hook into `descent_bridge.go` RepairFunc wrapper; detect manifest touch in post-repair git diff; re-run install with registry validation (existing code).
**Frozen-lockfile:** Enable `--frozen-lockfile` mode when lockfile exists (catches hallucinated deps).

## spec-2 CloudSwarm Protocol

### D6. `stoke run` command: NEW subcommand, not alias for `stoke ship`
**Rationale:** RT-CLOUDSWARM-MAP §8. CloudSwarm calls `stoke run --output stream-json [--repo URL] [--branch NAME] [--model MODEL] TASK_SPEC`. Different signature from `stoke ship` (which is SOW-based). A `run` command that takes a free-text task spec (like Claude Code) is the correct surface.
**Action:** New `cmd/stoke/run_cmd.go`. Internally routes to: clone repo → create session → execute via existing SOW or chat-intent mechanism. Emits streamjson events throughout.

### D7. `hitl_required` is the ONLY mandatory protocol event
**Evidence:** RT-CLOUDSWARM-MAP §2-3. CloudSwarm stores all other events verbatim under `stoke_events` but only actions on `hitl_required`.
**Action:** Spec extends streamjson with rich event taxonomy for DASHBOARD purposes, but protocol conformance only requires `hitl_required` + stdin decision reader.

### D8. Stdin decision reader: plain JSON line (supervisor decodes base64)
**Evidence:** RT-CLOUDSWARM-MAP §3. Supervisor base64-decodes before writing to our stdin. We read plain JSON: `{"decision":bool,"reason":str,"decided_by":str}`.
**Action:** New `internal/hitl/` package. Blocking line-read with timeout (default 1h standalone, 15min+ok in CloudSwarm mode). Goroutine + channel + `select` pattern per RT-04 §7 (os.Stdin.SetDeadline unsupported on Linux pipes).

### D9. Policy hook: DEFER to a later spec
**Evidence:** RT-CLOUDSWARM-MAP §4. CloudSwarm does not gate Stoke with Cedar; policy is skill-level.
**Action:** Drop policy hook from spec-2. Optionally revisit after Tier 3 specs land. Keep `CLOUDSWARM_POLICY_ENDPOINT` env-var-detection stub as a no-op so future work doesn't break the contract.

### D10. `STOKE_MAX_WORKERS` env var: keep, but NOT set by CloudSwarm today
**Evidence:** RT-CLOUDSWARM-MAP §5. CloudSwarm enforces concurrency at API submission, not in Stoke.
**Action:** Stoke reads env var if present as a self-imposed cap. Emit a `concurrency.cap` event on startup so CloudSwarm can verify.

### D11. Exit codes
- 0 = all sessions passed (incl. soft-passes)
- 1 = ≥1 session failed
- 2 = budget exhausted
- 3 = operator aborted (HITL rejected)
- 130 = SIGINT
- 143 = SIGTERM

## spec-3 Executor Foundation

### D12. Wrap existing SOW flow as `CodeExecutor`, do NOT rewrite
**Rationale:** Less risk, less churn during H-91 stabilization.
**Action:** New `internal/executor/code.go` calls into existing `sow_native.go`. Interface:
```go
type Executor interface {
    Execute(ctx, plan, effort) (Deliverable, error)
    BuildRepairFunc(plan) RepairFunc
    BuildEnvFixFunc() EnvFixFunc
    BuildCriteria(task, deliverable) []AcceptanceCriterion
}
```

### D13. Generalize `AcceptanceCriterion`: add `VerifyFunc`, keep `Command` priority
**Evidence:** RT-STOKE-SURFACE §1 AC struct.
**Action:** Add `VerifyFunc func(ctx) (passed bool, output string)` to AC struct. In `runACCommand`: if both Command and VerifyFunc set → Command wins (backward compat). If only VerifyFunc → call it.

### D14. Task router: lightweight keyword + LLM fallback
**Rationale:** No need for a big router framework. Most task types are signaled by command (`stoke plan` → code, `stoke research` → research). Chat/`stoke run TASK` uses a cheap Haiku classifier.
**Action:** New `internal/router/` with `Classify(input string) TaskType`. Used only by `stoke chat` + `stoke run` free-text entry.

## spec-4 Browser + Research

### D15. Browser: `github.com/go-rod/rod`
**Evidence:** RT-01. Pool-ready, MIT, pure Go, preserves single-binary distribution.
**Caveat:** Last tagged release v0.116.2 (July 2024) but main has 2026 commits — pin to a specific SHA until a new tag. chromedp is the escape hatch if go-rod stalls (1-week port).

### D16. Browser tool exposed via agentloop tool set
**Rationale:** Workers need browser for smoketest + deploy verification.
**Action:** `tools/browser_navigate`, `tools/browser_screenshot`, `tools/browser_extract_text`, `tools/browser_console_errors`. Gated by trust level (no arbitrary URL fetch for untrusted workers).

### D17. Research executor: Opus 4.7 lead + Sonnet 4.5 subagents; default parallelism cap 5
**Evidence:** RT-07.
**Action:** New `internal/executor/research.go`. Subagents write to `.stoke/research/<run-id>/subagent-<N>/findings.md` (filesystem-as-comm per Anthropic pattern). Lead reads files, synthesizes. Separate CitationAgent verifies each claim against source URL via browser (RT-07 4-stage pipeline).

### D18. Claim verification as AC set
**Evidence:** RT-07 BuildCriteria shape.
**Action:** For each claim, one AC with `VerifyFunc` (fetch URL, extract passage, LLM-judge match). Plus hard criteria (report exists, no contradictions, ≥0.9 coverage). Soft-pass at coverage ≥0.8 with annotations.

## spec-5 Delegation + A2A

### D19. A2A SDK: `github.com/a2aproject/a2a-go` v2.2.0
**Evidence:** RT-03. Official, spec-v1.0-compatible.
**Action:** Stoke imports for client (find/hire) and server (`stoke serve`) code. Custom HTTP handlers NOT needed.

### D20. Extend `internal/delegation/scoping.go` with trust-clamp + token + budget
**Evidence:** RT-09.
**Action:** Add fields to existing `DelegationContext` (don't replace): `DelegatorTrustLevel`, `EffectiveTrustLevel = min(delegator, executor)`, `DelegationDepth`, `MaxDepth=3`, `DelegationToken` (HMAC), `ParentTaskID`, `BudgetReserved`.
**HMAC verifier**: ship REAL from day 1 (not CloudSwarm's V-114 stub). Symmetric HS256, key via `STOKE_DELEGATION_SECRET` env var for v1; support rotation later.

### D21. Saga: add BudgetRefund compensator
**Evidence:** RT-09. `internal/delegation/saga.go` already has 3 settlement policies.
**Action:** One new compensator `BudgetRefund` that calls TrustPlane `refund`. Wire into saga's policy for failed/over-budget delegations.

### D22. Payment extension: follow `a2a-x402` pattern
**Evidence:** RT-03. A2A core omits payment by design.
**Action:** TrustPlane exposes as an A2A extension URI. Gate WORKING→COMPLETED on escrow receipt; tie Release/Refund to COMPLETED/FAILED transitions.

### D23. `stoke serve` as hireable agent: A2A card at `/.well-known/agent-card.json`
**Evidence:** RT-03.
**Action:** Server exposes A2A methods (SendMessage, SendStreamingMessage, GetTask, etc.) plus a signed agent card advertising capabilities (`code`, `research`, `browser`, `deploy`).

## spec-6 Deploy

### D24. First provider: Fly.io; second: Vercel; third: Cloudflare
**Evidence:** RT-10. Fly.io has widest language coverage, derivable URL, explicit rollback, NDJSON match.
**Action:** Ship Fly.io in spec-6 primary; Vercel + Cloudflare as follow-ons.
**Implementation:** Shell-out to `flyctl` with `--detach` + poll `flyctl status --json`. URL = `{app}.fly.dev` (derivable from fly.toml).

### D25. Auto-rollback conditions: status≠200 AND console errors AND t>30s
**Evidence:** RT-10. Avoids false positives during warm-up.

### D26. Deploy verification uses browser tool from spec-4
**Dependency:** spec-6 depends on spec-4.

## spec-7 Operator UX + Memory

### D27. `stoke plan`: separate command, produces `plan.json`
**Evidence:** RT-11.
**Action:** New `cmd/stoke/plan_cmd.go`. Emits `plan.ready` event. `stoke ship --sow …` remains the combined path.
**Resumability:** `bus.Event{Kind:"plan.approved"}` persisted in event log (from spec-3); `stoke execute --plan plan.json` picks up.

### D28. Ask/Notify split: `Operator` interface, two implementations
**Evidence:** RT-11.
**Action:**
```go
type Operator interface {
    Notify(kind NotifyKind, format string, args ...any)
    Ask(prompt string, opts []Option) (string, error)
    Confirm(prompt string) bool
}
```
Terminal impl (standalone). NDJSON impl (CloudSwarm, using `hitl_required` for Ask, regular streamjson for Notify).

### D29. Intent Gate: pre-dispatch classifier + read-only enforcement
**Evidence:** RT-11 Factory DROID verbatim.
**Action:** `internal/router/intent_gate.go`. Action-verb scan + LLM (Haiku) fallback. DIAGNOSE mode masks write tools at harness/tools auth layer.

### D30. Memory: add scope hierarchy (global/repo/task) on top of existing tiers
**Evidence:** RT-08. Stoke's memory/ already has CoALA tiers + contradiction — just needs scope + retrieval + SQLite+FTS5.
**Action:** New `internal/memory/sqlite.go` (SQLite+FTS5 backend), `internal/memory/scope.go` (hierarchy), `internal/memory/retrieve.go` (auto-retrieval at planner/worker/delegation injection points). Defer sqlite-vec + embeddings to v2.

### D31. Live meta-reasoner: gated by `STOKE_META_LIVE=1`, skip on clean sessions
**Evidence:** RT-08 ~$0.04/session transition.
**Action:** New `internal/metareason/` (or extend existing). Reuse `costtrack.OverBudget` as hard stop.

### D32. Cost dashboard: TUI widget rendering from `costtrack/`
**Evidence:** work.md §5.2. Data already tracked.
**Action:** Render only. Half-day work.

### D33. progress.md: emit at session/task boundaries
**Evidence:** RT-07 + work.md §2.5.
**Action:** `internal/plan/progress_renderer.go`. Hook into SessionScheduler.OnProgress.
