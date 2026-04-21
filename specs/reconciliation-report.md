# Spec Reconciliation Report — Stoke Specs vs WORK-stoke.md

**Generated:** 2026-04-20
**Scope:** 14 spec files in `/home/eric/repos/stoke/specs/` vs authoritative WORK-stoke plan.
**WORK file:** `/home/eric/repos/stoke/plans/WORK-stoke.md` (pointer only; authoritative S-series mapping pulled from the portfolio-index context provided in spawn turn).

---

## 1. Per-S-item alignment table (S-0 through S-11)

| S-item | WORK-task | Intent | Covering spec(s) | Coverage | Notes |
|--------|-----------|--------|------------------|----------|-------|
| **S-0** emitter threaded into `sowNativeConfig` | Task 10 | FOUNDATION — streamjson events threaded through SOW runner | `cloudswarm-protocol.md` (checklist #9) | **covered** | Spec explicitly threads `*streamjson.TwoLane` + `*hitl.Service` through `sowNativeConfig`; emits `plan.ready`, `task.dispatch`, `ac.result`, `task.complete`. Matches WORK intent. |
| **S-1** TUI progress renderer | Task 13 | Live dashboard for operators | `tui-renderer.md` | **covered** | Bubble Tea renderer tees off streamjson `TwoLane`; matches S-1 promise verbatim ("users see progress locally"). Depends on S-0. |
| **S-2** HITL gate with `SoftPassApprovalFunc` | Task 15 | Enterprise-tier soft-pass approval via HITL | `cloudswarm-protocol.md` (checklist #4-5) + `operator-ux-memory.md` (Part B) | **covered (split)** | `cloudswarm-protocol.md` ships the `SoftPassApprovalFunc` field + wiring. `operator-ux-memory.md` Part B adds the `Operator` interface + terminal/NDJSON impls + T8 gate-7 policy-aware Ask. Split is sensible but creates a cross-spec dependency: Part B can't land without the spec-2 field. |
| **S-3** `VerifyFunc` on `AcceptanceCriterion` | Task 11 | AC generalization for non-code tasks | `executor-foundation.md` (checklist #12-14) | **covered** | Adds `VerifyFunc func(ctx) (bool, string)` tagged `json:"-"`, extends `runACCommand` with leading branch, preserves backward-compat matrix. Matches WORK. |
| **S-4** anti-deception contract in worker prompts | Task 8 | Truthfulness contract prompt block | `descent-hardening.md` item 1 | **covered** | Verbatim TRUTHFULNESS_CONTRACT constant + dual injection (standard + repair paths). Always-on per C4 (no opt-out). |
| **S-5** forced self-check before completion | Task 8 | Pre-completion gate with parser | `descent-hardening.md` item 2 | **covered** | Verbatim PRE_COMPLETION_GATE constant + `PreEndTurnCheckFn` parser that cross-checks FILES_MODIFIED against git + AC_VERIFICATION against session transcript. |
| **S-6** provider pool | Task 12 | Unified provider pool with capability negotiation | `provider-pool.md` | **covered** | Full S-6 scope with 8-backend adapter, capability matrix, failover policy, cost events, YAML grammar, migration behind `STOKE_PROVIDER_POOL=1`. |
| **S-7** bootstrap per descent cycle | Task 9 | Re-install deps after manifest edits mid-descent | `descent-hardening.md` item 4 (checklist #5) | **covered** | Wraps RepairFunc with pre/post git HEAD diff against manifest list; calls `EnsureWorkspaceInstalledOpts` with `Force=true` + `Frozen` mode. Emits `descent.bootstrap_reinstalled`. |
| **S-8** per-file repair cap | Task 9 | Cap repairs per file at 3 | `descent-hardening.md` item 3 (checklist #4) | **covered** | Adds `FileRepairCounts map[string]int` + `MaxRepairsPerFile int=3` to `DescentConfig`. Directly cites Cursor 2.0 precedent. Explicit fail (not soft-pass) on cap hit. |
| **S-9** memory store | Task 16 | Persistent memory + retrieval | `operator-ux-memory.md` Part D | **divergent** | **See §3 flagged divergences.** My spec ships a far richer stack (SQLite+FTS5+sqlite-vec, 3-way embedder fallback, consolidation pipeline, 4 injection points, adapters) than the WORK Task 16 description ("simpler FTS5+BM25 extension of existing `internal/wisdom/sqlite.go`"). |
| **S-10** hire verify-settle | Task 17 | Delegation with A2A | `delegation-a2a.md` | **covered** | Full hire-verify-settle flow with HMAC tokens, trust clamp, A2A client/server, x402, card signing, saga compensators. Extends `DelegationContext` (does not replace). |
| **S-11** local cost tracking | Task 14 | Cost dashboard | `operator-ux-memory.md` Part G | **covered** | Cost dashboard TUI widget over existing `internal/costtrack/`; 1s tick, burn-rate, ETA. Matches WORK intent. |

### Track B architecture (Tasks 18-24)

| Task | Intent | Covering spec | Coverage | Notes |
|------|--------|---------------|----------|-------|
| **Task 18** event log promotion (promote `internal/hub/bus.go`) | Bus-as-event-system | `executor-foundation.md` (new `internal/eventlog/`) | **divergent** | **See §3.** WORK says "promote `internal/hub/bus.go`". My spec adds a new `internal/eventlog/` SQLite+WAL table alongside. Both useful but not the same mechanism. |
| **Task 19** executor interface | Task-type-agnostic Executor | `executor-foundation.md` | **covered** | `executor.Executor` interface with `Execute/BuildRepairFunc/BuildEnvFixFunc/BuildCriteria`. |
| **Task 20** research executor | Multi-agent research | `browser-research-executors.md` Part 2 | **covered** | Orchestrator-worker, 4-stage verify, effort scaling. |
| **Task 21** browser executor | Headless browser tools | `browser-research-executors.md` Part 1 | **covered** | go-rod, pool, 4 agentloop tools, auth predicate. |
| **Task 22** deploy executor | Fly.io deploy | `deploy-executor.md` + `deploy-phase2.md` | **covered (scope-expanded)** | Fly.io is canonical v1 (`deploy-executor.md`). `deploy-phase2.md` adds Vercel + Cloudflare — beyond WORK Task 22 scope as written, but consistent with D-2026-04-20-03. |
| **Task 23** delegate executor | A2A delegation | `delegation-a2a.md` | **covered** | Same spec as S-10; `DelegateExecutor` implements spec-3 Executor. |
| **Task 24** serve executor | `stoke serve` A2A endpoint | `delegation-a2a.md` Part 7 | **covered** | `stoke serve` + signed card + JWKS + x402 gating all in the delegation spec. |

---

## 2. Per-my-spec coverage table (14 rows)

| # | Spec | Role vs WORK | Assessment |
|---|------|--------------|------------|
| 1 | `descent-hardening.md` | **Authoritative for WORK Tasks 8 + 9** | Covers S-4/5/7/8. Two additions (env-issue tool `report_env_issue` + ghost-write detector) extend WORK Task 9 beyond the written S-item list but are consistent with its "anti-deception" theme. **See §3 flag 1.** |
| 2 | `cloudswarm-protocol.md` | **Authoritative for WORK Tasks 10 + 15** | S-0 emitter + S-2 HITL approval field + `stoke run` subcommand. Implements the CloudSwarm NDJSON contract from RT-CLOUDSWARM-MAP. **See §3 flag 2.** |
| 3 | `executor-foundation.md` | **Authoritative for WORK Tasks 11 + 19** | S-3 VerifyFunc + Executor interface + Router. **Adds `internal/eventlog/` SQLite table** — see §3 flag 3 (divergence vs WORK Task 18 "promote hub/bus.go"). |
| 4 | `browser-research-executors.md` | **Authoritative for WORK Tasks 20 + 21** | Browser + Research executors. |
| 5 | `delegation-a2a.md` | **Authoritative for WORK Tasks 17 + 23 + 24 (S-10)** | Full hire-verify-settle + A2A client + `stoke serve`. |
| 6 | `deploy-executor.md` | **Authoritative for WORK Task 22 (v1, Fly.io)** | Matches D-2026-04-20-03 "Fly.io only v1". |
| 7 | `operator-ux-memory.md` | **Authoritative for WORK Tasks 14, 15 (partial), 16 (expanded), plus Operator UX scope** | Seven parts (A-G) spanning plan/approve/execute UX, memory, meta-reasoner, progress.md, cost dashboard. **Part D is more ambitious than WORK Task 16 (§3 flag 4).** Parts A (plan command) and E (live meta-reasoner) and F (progress.md) are additive — not named S-items but implicit in operator UX. |
| 8 | `mcp-client.md` | **Extends beyond WORK scope** | WORK does not mention MCP client. My spec fills the RT-STOKE-SURFACE §10 gap identified in competitive analysis (RT-STOKE-SURFACE). **Additive — see §4.** |
| 9 | `deploy-phase2.md` | **Extends beyond WORK scope (scope creep relative to WORK Task 22)** | WORK Task 22 is Fly.io only; Phase 2 adds Vercel + Cloudflare. **Additive — see §4.** |
| 10 | `policy-engine.md` | **Extends beyond WORK scope** | WORK does not list a policy engine as an S-item. D-9 deferred it from spec-2. Standalone from CloudSwarm (per RT-CLOUDSWARM-MAP §4). **Additive — see §4.** |
| 11 | `chat-descent-control.md` | **Extends beyond WORK scope** | WORK does not mention chat mini-descent or `sessionctl`. Two parts: (a) chat mini-descent gate (work.md §5.1 line-item), (b) operator control plane (Unix socket IPC). **Additive — see §4 and §3 flag 5.** |
| 12 | `tui-renderer.md` | **Authoritative for WORK Task 13 (S-1)** | Live TUI renderer. |
| 13 | `provider-pool.md` | **Authoritative for WORK Task 12 (S-6)** | Unified provider pool. |
| 14 | `fanout-generalization.md` | **Additive generalization** | WORK does not list fan-out as an S-item. Extracts the existing `session_scheduler_parallel.go` pattern into reusable `internal/fanout/` for spec-4 research + spec-5 delegation consumers. **Additive but directly supports WORK Tasks 20 + 23.** See §4. |

---

## 3. Flagged divergences requiring operator decision

### Flag 1 — `descent-hardening.md` adds env-issue tool + ghost-write beyond WORK Task 8+9

**WORK says:** S-4 (anti-deception prompt), S-5 (self-check), S-7 (bootstrap), S-8 (per-file cap).

**My spec adds:** `report_env_issue` worker tool (item 6) + ghost-write detector (item 7). Both emit new bus events (`worker.env_blocked`, `descent.ghost_write_detected`) and short-circuit descent on env-blocked paths.

**Tradeoff:** Env-issue tool saves ~$0.10/AC by skipping the 5-LLM multi-analyst panel when the worker self-reports an environment blocker (empirically valuable; matches CL4R1T4S/DEVIN reference in spec). Ghost-write detector catches a real failure class (tool reports success, file empty) observed in prior `BLOCKED` signals.

**Recommendation:** **Keep both.** They are low-risk additive hooks (supervisor-rule + MidturnCheckFn) gated behind `STOKE_DESCENT=1` already, and they address concrete observed failure modes. Declare them in WORK Task 8-9 scope extension so the plan stays accurate.

### Flag 2 — `cloudswarm-protocol.md` adds `stoke run` subcommand + `--governance-tier`

**WORK says:** S-0 emitter + S-2 HITL (Task 10 + 15).

**My spec adds:** new top-level `stoke run` subcommand (not just emitter wiring inside `stoke ship`) with seven flags including `--governance-tier community|enterprise` (which determines auto-grant vs HITL-required soft-pass). Also adds exit-code contract (0/1/2/3/130/143), graceful shutdown, two-lane emitter with backpressure.

**Tradeoff:** CloudSwarm's `ExecuteStokeActivity` calls `stoke run` as a specific subcommand (per RT-CLOUDSWARM-MAP). Without it, the integration literally won't work. So the `stoke run` addition isn't scope creep — it's implicit in the S-0 integration contract.

**Recommendation:** **Keep.** WORK Task 10 naturally implies a CLI surface for CloudSwarm to invoke; this spec formalizes it. Update WORK-stoke.md to enumerate the `stoke run` CLI contract.

### Flag 3 — `executor-foundation.md` new `internal/eventlog/` vs WORK Task 18 "promote `internal/hub/bus.go`"

**WORK says:** Task 18 — "event log promotion: promote `internal/hub/bus.go` as the event system."

**My spec ships:** a new `internal/eventlog/` SQLite+WAL table with hash-chain integrity, ULID IDs, replay semantics, orphan tool-call handling, idempotence registry, JSONL mirror. Plus a helper `eventlog.EmitBus(bus, log, ev)` that publishes to both.

**Tradeoff:** The hub/bus is an in-memory typed event hub; `internal/bus/bus.go` is a durable WAL-backed event bus. The current tree has both. My spec does **not** delete either. Instead it adds a third thing (SQLite events table with hash chain) aimed at replay + audit. WORK may have intended consolidation around `hub/bus.go` as the single event system — my spec goes the opposite direction (SQLite as the persistence layer, `internal/bus/` as the live publisher).

**Recommendation:** **Discuss with operator.** Two reasonable readings:
- (a) Treat `internal/eventlog/` as complementary durable audit + replay layer. Bus remains the live publisher. `EmitBus` bridges them.
- (b) Take WORK Task 18 literally: promote `hub/bus.go`, remove `internal/bus/`, unify on a single interface. My spec would then be a deviation.

My spec implicitly picks (a). If operator meant (b), `executor-foundation.md` needs a rewrite on the event-log side.

### Flag 4 — `operator-ux-memory.md` Part D memory stack >> WORK Task 16

**WORK says:** Task 16 — "simpler FTS5+BM25 extension of existing `internal/wisdom/sqlite.go`."

**My spec ships:** full SQLite+FTS5+sqlite-vec backend, scope hierarchy (Global/Repo/Task), 3-way embedder fallback (OpenAI → local llama.cpp → NoopEmbedder BM25-only), LLM-driven consolidation pipeline ported from CloudSwarm's `memory_consolidation.py`, 4 auto-retrieval injection points, adapters for wisdom + skills, `stoke memory` subcommand with 6 verbs.

**Tradeoff:** The ambitious scope is justified by D-2026-04-20-04 (cited in the spec) and by the RT-08 end-to-end requirement. But it is a **significantly larger build** than WORK Task 16 suggests (~1500 lines of code vs ~300). It also introduces new external dependencies: sqlite-vec loadable extension, optional OpenAI embeddings, optional llama.cpp daemon.

**Recommendation:** **Operator decision.** Two sub-tradeoffs:
- (a) Ship the full stack because the consolidation + auto-retrieval + meta-reasoner (Part E) are mutually reinforcing — trimming any one weakens the other two.
- (b) Scope down to "FTS5 on existing wisdom DB" per WORK literal, defer embeddings + consolidation to a follow-up S-item.

If (b), Part D shrinks to ~200 lines and Parts E, F, G in the same spec remain valuable independently. Recommend explicitly choosing before implementation to avoid mid-build scope cuts.

### Flag 5 — `chat-descent-control.md` `sessionctl` Unix-socket IPC

**WORK says:** Nothing about operator control commands (`stoke status/approve/override/pause/resume/inject/takeover`) or Unix-socket IPC.

**My spec ships:** full `internal/sessionctl/` package (Unix socket + optional HTTP), eight CLI verbs, approval router that unifies three approval paths (CloudSwarm stdin, terminal Ask, external `stoke approve`), PTY-based takeover with auto descent re-verify on release.

**Tradeoff:** The chat mini-descent gate is directly requested by work.md §5.1 (cited in spec). The sessionctl control plane is **new scope** beyond any WORK item. Value case: it unifies three existing ad-hoc approval paths into one durable channel and adds escape-hatch controls (pause/inject/takeover) that operators will want once the agent is running long-horizon sessions.

**Recommendation:** **Keep chat mini-descent (authoritative for the work.md §5.1 line). Consider trimming sessionctl.** The approval router piece is high-leverage (unifies existing paths). The takeover PTY piece is 10% of the spec's lines and has the most integration surface. Options:
- (a) Keep full scope; it's operationally valuable.
- (b) Trim to chat mini-descent + approval router; defer pause/resume/inject/takeover to a follow-up spec.
- (c) Trim further to chat mini-descent only; ship each control verb as a separate spec when demand materializes.

### Flag 6 — `deploy-phase2.md` (Vercel + Cloudflare) beyond WORK Task 22

**WORK says:** Task 22 — deploy executor (Fly.io only per D-2026-04-20-03).

**My spec ships:** second spec explicitly expanding to Vercel + Cloudflare Workers. Honors the "Fly first" v1 decision but adds explicit v2 scope.

**Tradeoff:** Phase 2 is clearly out of WORK v1 scope. But the decision doc (D-2026-04-20-03) names "Vercel + Cloudflare follow-up" as the intended v2. Spec addresses churn risk (Wrangler 2026 flag changes, NDJSON parser, unknown-event tolerance). Worth having the spec pre-written even if not built immediately.

**Recommendation:** **Keep as pre-built v2 spec; do not build until v1 is merged and operator explicitly green-lights v2.** Mark status as `ready` but `BUILD_ORDER: 9` after Fly v1 is merged.

---

## 4. Specs added beyond WORK scope — rationale + recommendations

| Spec | Origin | Rationale | Recommendation |
|------|--------|-----------|----------------|
| `mcp-client.md` | RT-STOKE-SURFACE §10 (existing client stub has no SSE/HTTP/auth/discovery/circuit-breaker) + competitive analysis (Claude Code, Cursor, Devin all ship MCP clients) | Fills a shipped-but-stubbed package. Low-risk additive work that unlocks github/linear/slack tool access for workers. | **Keep.** Build after S-series core is green. Truthfulness contract extension (§Anti-Deception) is a clean hook into spec-1. |
| `deploy-phase2.md` | D-2026-04-20-03 "Vercel + Cloudflare follow-up" | Pre-built spec for when Fly v1 is stable | **Keep as pre-built; build later.** See flag 6. |
| `policy-engine.md` | D-9 (deferred from spec-2) + RT-02 (Cedar + OPA research) + standalone-from-CloudSwarm requirement | Enterprise governance; currently `CLOUDSWARM_POLICY_ENDPOINT` is a documented no-op in spec-2 | **Keep. Build priority low unless enterprise customer demands.** Fail-closed design is correct; null-client default-on preserves zero-config behavior. |
| `chat-descent-control.md` (the sessionctl half) | Work.md §5.1 + operator control requests | Unifies approval paths; adds escape-hatch control | **See flag 5 — trim consideration.** |
| `fanout-generalization.md` | spec-4 (research) + spec-5 (delegation) both need fan-out; session scheduler already has it; DRY extraction | Reusable primitive used by 3 consumers | **Keep. Build after spec-3 is green.** Low-risk extraction of existing, tested pattern. |

Net additive scope: ~5 specs beyond WORK literal scope. Of these:
- 2 (mcp-client, fanout-generalization) are low-risk enablers — **build.**
- 1 (deploy-phase2) is pre-built for v2 — **defer build.**
- 1 (policy-engine) is standalone enterprise — **build when demand arrives.**
- 1 (chat-descent-control) is partly in scope (work.md §5.1) and partly additive (sessionctl) — **trim sessionctl takeover + inject; keep chat gate + approval router.**

---

## 5. Missing from my spec set

Reviewing WORK Tasks 1-24 and S-items S-0 through S-11 against my 14 specs:

| Gap | WORK location | Status |
|-----|---------------|--------|
| **Track A (Tasks 1-7)** — prompt-injection + tool-output hardening | WORK lines 44-55 | **Not in my 14 specs.** WORK line 55 says "Full details: see portfolio WORK-stoke.md Track A as pasted in the portfolio planning turn." This is a material gap — 7 tasks (promptguard ports into 3 ingest paths, tool-output sanitizer, honeypot wiring, websearch domain allowlist, MCP sanitization audit, red-team corpus) have no spec file. |
| **Task 18** event-log promotion literal reading (promote `hub/bus.go`) | If interpreted as (b) in flag 3 above | **Ambiguous.** My spec ships a new `internal/eventlog/` instead. |
| **Skill manufacturing / skillmfr integration** | Not listed as S-item, but `/home/eric/repos/stoke/skillmfr/` exists | No spec covers skillmfr pipeline integration with memory (Part D mentions it as read-only via SkillAdapter). If operator wants skillmfr-as-memory-tier first-class, spec needed. |
| **`stoke attach`** CLI command | Implicit in `tui-renderer.md` quit message + `chat-descent-control.md` socket discovery | No spec owns `stoke attach`. Socket exists; attach client does not. |
| **Replay from event log** | Implicit in `executor-foundation.md` `Log.Replay` | `Replay` pseudocode exists in spec, but no spec owns the operator-facing `stoke replay <session>` CLI. |

**Recommendation:** Write a Track A spec (or 7 Track A specs) for the prompt-injection hardening work before claiming WORK is fully spec'd. The other 4 gaps are minor/optional.

---

## 6. Summary

**S-0 through S-11:** All 12 S-items have spec coverage. One (S-9 memory) diverges significantly in scope from WORK Task 16. One (S-2 HITL) is split across two specs in a coherent way.

**Track B (Tasks 18-24):** All 7 tasks have spec coverage. Task 18 (event-log promotion) is the only item where my spec's approach (new SQLite eventlog) may diverge from WORK intent (promote hub/bus.go).

**Added specs (beyond WORK):** 5 specs. 2 are low-risk enablers (mcp-client, fanout-generalization). 1 is pre-built for v2 (deploy-phase2). 1 is standalone enterprise (policy-engine). 1 has trim opportunity (chat-descent-control sessionctl half).

**Gaps:** Track A (Tasks 1-7) prompt-injection hardening has no spec file; WORK-stoke.md only points at the portfolio turn for details. This is the largest open gap.

**Decisions needed from operator:**
1. **Flag 3** — is `internal/eventlog/` acceptable as a complementary layer, or does WORK Task 18 require `hub/bus.go` promotion instead?
2. **Flag 4** — ship the full memory stack (Part D as-written) or trim to FTS5-only per WORK literal?
3. **Flag 5** — keep sessionctl full scope, or trim to chat gate + approval router?
4. **Track A** — who writes spec(s) for the 7 prompt-injection hardening tasks?
