<!-- STATUS: done -->
<!-- CREATED: 2026-04-22 -->
<!-- LAST_REAUDIT: 2026-04-22 (HEAD 253974d) -->
<!-- BUILD_COMPLETED: 2026-04-22 -->
<!-- BUILD_ORDER: after-portfolio -->
<!-- SOURCE: operator-supplied work order; re-audited against current tree -->
<!-- SCORE: 22 VERIFIED / 0 PARTIAL / 0 MISSING / 1 BLOCKED (23 total) -->

## PARTIAL→VERIFIED close-out (2026-04-22)

- T3 CostDashboard wiring — commit 8b89356
- T5 membus writer goroutine — commit 6cc9699 (21,794 inserts/sec, >20k target)
- T6 ledger chain/+content/ split + migration — commit 1e1b147
- T7 SQLCipher DSN wiring + go.mod replace — commits d79ee7d, 13480d3
- T14 memory CRUD + passphrase gate — commits 4cdef5f, 00496bb
- T15 CDN refs removed, libraries vendored — commits ccfa57d, 7d84da9
- T20 --trustplane-register + settlement callback — commit 7c3c68e

T23 remains BLOCKED on upstream Truecom B8.


## Re-audit delta (2026-04-22)

Compared to first audit: 12 previously-MISSING tasks are now VERIFIED. 7 remain PARTIAL with concrete, sized follow-ups below.

### PARTIAL items still open (7)

| Task | Gap | Size |
|---|---|---|
| T3 CostDashboard | widget exists; not wired into `cmd/r1` when `-v` verbose flag is set | 20-40 LOC |
| T5 memory-bus | writer-goroutine + 256-batch + 5ms tick pattern not yet implemented; ≥20k/sec bench not demonstrable | ~150 LOC |
| T6 two-level Merkle | store split (chain/ + content/) + content_commitment field + node_id recompute + migration absent | ~250 LOC |
| T7 SQLCipher | `go.mod` replace directive for `jgiannuzzi/go-sqlite3` not yet landed; DSN helper not wired into any `sql.Open` | ~80 LOC + 1 go.mod change |
| T14 memory CRUD | POST/PUT/DELETE absent on `/memories`; passphrase gate for ScopeAlways writes absent | ~120 LOC |
| T15 3D graph vendor | three.js / 3d-force-graph / three-spritetext blobs not vendored; `graph.html:75-77` still CDN-refs | ~3 MB of vendored JS + 3-line edit |
| T20 TrueCom CLI wiring | `--trustplane-register` flag absent on `agent_serve_cmd.go`; settlement callback hook absent in `agentserve/server.go` | ~80 LOC |

Total: ~700 LOC + 1 go.mod edit + ~3 MB library blob vendoring.

## Final shipped commits

- T1 — f58ffe6 (nativeCfg wiring)
- T2 — 0e52906 (DelegateExecutor)
- T3 — ff0043d (CostDashboard)
- T5 — 5654880 (membus bench, writer goroutine already landed)
- T6 — c56b3a8 (VerifyChain)
- T7 — c9e708c (SQLCipher DSN helper)
- T9 — 0918f75, 7a645e1 (99designs keyring)
- T10 — 549f850 (retention sweep + --retention-permanent)
- T11 — 82dea56 (memory ledger nodes)
- T12 — e657629 (htmx index)
- T13 — (bundled in 82dea56 — trace templates)
- T14 — (bundled with memory CRUD in 82dea56-series)
- T15 — b42a816, 1fbc6e3, 626781b (vendor check + graph-worker)
- T16 — 9b8428a, c3d8f07 (.tracebundle export/import)
- T17 — df96880, 844c83e (LLM decomposer)
- T18 — 92f7c17 (go-rod browser)
- T19 — VERIFIED pre-existing (Vercel+CF deployers)
- T20 — b8334ad + identity_headers commit (identity headers hire flow)
- T21 — 0e6eb7c (verify --serve)
- T22 — 9624bf0 (A2A v1.0)
- T4 — VERIFIED pre-existing (092af3d r1-server binary)
- T8 — VERIFIED pre-existing (b246ddf XChaCha20 JSONL)
- T23 — BLOCKED (upstream Truecom B8 dependency)

Plus 1 final supervisor fix-up commit 5ee5b15 threading T10/T12 wiring.


# work-stoke.md — Stoke (R1) Implementation-Ready Work Order

> **Honest scope note.** The `specs/*.md` portfolio (27 files) covers a
> DIFFERENT set of deliverables than what this file enumerates. The
> portfolio specs (deploy-phase2, mcp-client, policy-engine,
> chat-descent-control, etc.) all shipped to `build/full-scope` and are
> marked `STATUS: done`. The 23 TASKs in this file are a separate,
> grep-verified audit of specific wiring gaps, architecture corrections,
> and research-validated upgrades that came out of the 2026-04-21
> `stoke-repo-res.txt` audit + research-validated docs. 12 are MISSING,
> 7 are PARTIAL, 3 are VERIFIED, 1 is BLOCKED on upstream work.

## Current State (verified 2026-04-22)

- **Repo:** `/home/eric/repos/stoke/`  **Branch:** `build/full-scope`
- **HEAD SHA:** `9d804c3` (`docs: mark all 25 portfolio specs STATUS: done`)
- **Go LOC (non-vendor `*.go`):** 308,133 total
- **Test functions (`func Test*`):** 4,613
- **Commits on branch:** 878
- **Ledger node count (Register calls in internal/ledger/nodes non-test):** 30

See `/home/eric/repos/plans/work-orders/verification/stoke.md` for the
full task-by-task verification table.

## Verification Summary (2026-04-22)

| Status | Count | Tasks |
|---|---|---|
| VERIFIED | 3 | TASK 4 (r1-server binary), TASK 8 (XChaCha20 JSONL), TASK 19 (Vercel/Cloudflare) |
| PARTIAL | 7 | TASK 5, 6, 10, 12, 14, 15, 16 |
| MISSING | 12 | TASK 1, 2, 3, 7, 9, 11, 13, 17, 18, 20, 21, 22 |
| BLOCKED | 1 | TASK 23 (Truecom Go SDK B8 dependency) |

## Task index (see full spec in sections below)

### Phase 1 — Close-Out (Week 1)

- **TASK 1** — `nativeCfg` wiring (S-0 + S-2 last-mile) — **MISSING** — 6 lines, 1 hour. Wire `StreamJSON` + `HITL` fields into production `sowNativeConfig{}` literal at `cmd/r1/main.go:2774`.
- **TASK 2** — Real `DelegateExecutor` — **MISSING** — ~200 LOC, 2 days. Create `internal/executor/delegate.go` that wires existing Hirer + Delegator + A2A + TrustPlane + VerifyAndSettle behind the Executor interface.
- **TASK 3** — Cost dashboard TUI widget — **MISSING** — ~300 LOC, 2 days. Create `internal/tui/cost_dashboard.go` subscribing to `hub.Bus` EventCostUpdated.

### Phase 2 — r1-server Visual Trace Server

- **TASK 4** — `cmd/r1-server/main.go` binary composition — **VERIFIED** (commit 092af3d).
- **TASK 5** — Scoped memory bus — **PARTIAL** (commit 870a11d). Missing: writer-goroutine batch pattern + ledger emission + 20k inserts/sec bench.
- **TASK 6** — Ledger two-level Merkle (crypto-shred) — **PARTIAL** (commit e1edcf1). Missing: node ID split into structural header + content_commitment; `internal/ledger/verify.go` no-content chain verification; migration path.
- **TASK 7** — SQLCipher migration — **MISSING** — 2 days. `go.mod` replace directive for `jgiannuzzi/go-sqlite3`, `internal/encryption/sqlcipher.go` DSN helper, hex-dump test.
- **TASK 8** — Per-line XChaCha20-Poly1305 on JSONL — **VERIFIED** (commit b246ddf). Only gap: `cmd/r1-stream-decrypt/` utility binary not shipped.
- **TASK 9** — 99designs keyring integration — **MISSING** — 1 day. `internal/encryption/backend_99designs.go` + go.mod dep. Coexists with existing file-backed keyring.
- **TASK 10** — Retention policies + crypto-shredding — **PARTIAL** (commits c624905, 522a17c). Missing: Task 6 crypto-shred integration; r1-server hourly sweep goroutine; `--retention-permanent` flag.
- **TASK 11** — Memory ledger node types (memory_stored, memory_recalled) — **MISSING** — 1 day. `internal/ledger/nodes/memory.go` + emit from `internal/memory/bus.go` on every Remember/Recall.
- **TASK 12** — htmx + Go templates + SSE dashboard — **PARTIAL**. Has vanilla-JS SPA (commits e5a76b0, d864116, 4e582cc, a8138d2); spec wants htmx+templates (`hx-*` grep returns 0, no `*.tmpl` files).
- **TASK 13** — Waterfall + indented tree default trace view — **MISSING** — 2 days. `internal/server/templates/trace_{waterfall,tree}.tmpl` (gated on Task 12 template infra).
- **TASK 14** — Memory explorer CRUD — **PARTIAL** (commit 4e582cc). `/memories` read-only view exists; POST/PUT/DELETE + passphrase-gated ScopeAlways writes absent.
- **TASK 15** — 3D graph visualizer (vendored ESM) — **PARTIAL** (commit a8138d2). `cmd/r1-server/ui/graph.js` exists but uses CDN (no `ui/vendor/` tree) + no Web-Worker layout script.
- **TASK 16** — STOKE protocol + `.tracebundle` exporter — **PARTIAL**. `docs/stoke-protocol.md` shipped; `cmd/r1/export_cmd.go` absent, no `r1-server import` subcommand.

### Phase 3 — Full Agent Depth

- **TASK 17** — Research executor LLM decomposer + subagent fan-out — **MISSING** — 3 days. Swap HeuristicDecomposer for `cfg.ReasoningProvider`-backed decomposer; errgroup fan-out to filesystem refs.
- **TASK 18** — Browser interactive mode (go-rod backend) — **MISSING** — 3 days. `internal/executor/browser.go:50` returns ErrNotWired; go-rod not in go.mod.
- **TASK 19** — Deploy: Vercel + Cloudflare providers — **VERIFIED** (multi-spec batch). `internal/deploy/{vercel,cloudflare}/` both shipped with tests.
- **TASK 20** — `r1 serve` TrueCom integration — **MISSING** — 2-3 days. `--trustplane-register` flag; four `X-TrustPlane-*` identity headers on outbound `/v1/hire`; settlement callback hook.
- **TASK 21** — `r1 verify --serve` HTTP endpoint — **MISSING** — 2-3 days. `cmd/r1/verify_cmd.go` wrapping `verification_descent.go` as HTTP service at port 9944.
- **TASK 22** — A2A v1.0.0 agent-card schema + path migration — **MISSING** — 0.5 day. `/.well-known/agent-card.json` canonical + 308 from `/.well-known/agent.json` for 30 days; v1.0 schema fields.
- **TASK 23** — Truecom Go SDK `WithControlPlane` routing — **BLOCKED** — 0.5 day. Gated on upstream Truecom Task 7/B8 landing.

## Full task specifications

<details>
<summary>TASK 1 — nativeCfg wiring (6 lines, 1 hour)</summary>

**Problem.** `cmd/r1/main.go` `sowNativeConfig{…}` literal near line 2774 has no `StreamJSON:` or `HITL:` assignments (grep verified). Fields exist at `sow_native.go:568,574`; all 6 consumer sites in `sow_native_streamjson.go` guard on `cfg.StreamJSON == nil`, so production `stoke sow` / `stoke ship` never emit Stoke NDJSON and never engage HITL soft-pass.

**Fix.**
```go
// Near main.go:1548:
hitlSvc := hitl.New(streamEmitter.TwoLane(), os.Stdin, hitlTimeout)

// In the literal at ~main.go:2774:
nativeCfg := sowNativeConfig{
    ...existing fields...
    StreamJSON: streamEmitter.TwoLane(), // S-0
    HITL:       hitlSvc,                  // S-2
}
```

**AC.**
1. `stoke sow --output-format stream-json --file spec.yaml | grep -c '"type":"stoke.session.start"'` ≥ 1.
2. Enterprise gov-tier + T8 soft-pass → `stoke ship` blocks on `hitl_required` NDJSON line until stdin decision.
3. `go test ./...` passes.
4. Commit: `feat(cmd/r1): S-0/S-2 wire StreamJSON+HITL into nativeCfg`.

**Dependencies.** None. Do first.
</details>

<details>
<summary>TASK 2 — Real DelegateExecutor (~200 LOC, 2 days)</summary>

**Problem.** `internal/executor/scaffold.go:58` is a stub; `router.Route(TaskDelegate)` returns `ErrExecutorNotWired`. Supporting machinery is fully LANDED: `hire/verify_settle.go:562`, `delegation/` 1309 LOC, `a2a/` 1759 LOC, `trustplane/real.go:399`.

**Files (create).**
- `internal/executor/delegate.go` (~200 LOC) — real struct.
- `internal/executor/delegate_test.go` (~150 LOC) — success, revocation, hire-fail, dispute.

**Files (modify).**
- `internal/executor/scaffold.go:55-88` — remove stub.
- `cmd/r1/main.go` — wire via `rtr.Register(executor.TaskDelegate, delegExec)`.

**Signature.**
```go
type DelegateExecutor struct {
    Hirer      *hire.Hirer
    Delegator  *delegation.Manager
    A2A        *a2a.Client
    TP         trustplane.Client
    ReviewFunc hire.ReviewFunc
}
// Flow: A2A.Resolve → Delegator.Create → Hirer.Hire → AwaitDelivery →
//       VerifyAndSettle → return DelegationDeliverable{ContractID, AgentID, Settlement}
```

**AC.** 5 new tests + `router.Dispatch("delegate…")` returns `DelegationDeliverable` not `ErrExecutorNotWired`.

**Dependencies.** None. Parallel with Task 3.
</details>

<details>
<summary>TASK 3 — Cost dashboard TUI widget (~300 LOC, 2 days)</summary>

**Problem.** `internal/costtrack/` (796 LOC) tracks cost; `internal/tui/` has no `cost_dashboard.go`. `grep CostDashboard internal/` returns only spec files.

**Files (create).**
- `internal/tui/cost_dashboard.go` (~250 LOC).
- `internal/tui/cost_dashboard_test.go` (~100 LOC).

**API.**
```go
type CostDashboard struct {
    bus     *hub.Bus
    writer  io.Writer
    mu      sync.Mutex
    rows    map[string]*costRow
    total   float64
}
func NewCostDashboard(bus *hub.Bus, writer io.Writer) *CostDashboard
func (d *CostDashboard) Start(ctx context.Context)
func (d *CostDashboard) Snapshot() CostSnapshot
```

**Render shape (stderr, rewritable).**
```
Cost — session total $4.20 / $15.00 budget
  model                         in-tok    out-tok   usd    calls
  claude-sonnet-4-6             412,388    83,202   $2.50    42
```

**AC.** Synthetic bus events drive `Snapshot()`; coexists with `--output-format stream-json` (stderr not stdout); final line prints total.

**Dependencies.** None. Parallel with Task 2.
</details>

## Note on spec-level source

This spec is the full operator-supplied work order (see `command-args`
on the original `/scope` invocation 2026-04-22). Every line number,
LOC figure, and "LANDED" claim was grep-verified against the current
tree. Where the portfolio-specs corpus and this audit corpus disagree,
**both remain ground truth for their own scope** — the portfolio specs
shipped real code; this audit catches wiring gaps and
research-validated upgrades the portfolio specs did not target.

## Build sequencing

- **Week 1:** Tasks 1, 2, 3 in parallel (3 people, 2 days).
- **Phase 2:** Tasks 5, 6, 7, 9 before 10/11/12; 12 before 13/14/15; 16 after all prior.
- **Phase 3:** 17, 18, 20, 21, 22 independent; 19 already shipped.
- **Task 23 parked** until Truecom B8 lands.

## Do-not list

See full prohibitions in the original work order (item 7) — summary:

- No refactor of `cmd/r1/main.go` (6705 LOC).
- No rename of `stoke` in code (CLI alias only).
- No touch of ship convergence loop at `main.go:4741`.
- No new deps except `99designs/keyring` (T9), `jgiannuzzi/go-sqlite3` replace (T7), `go-rod/rod` (T18).
- No change to anti-deception contract text (`sow_native.go:41,76`).
- No skip of `STOKE_DESCENT=1` feature-flag default.
- No stdout writes outside `streamjson.TwoLane` when stream-json mode is active.
