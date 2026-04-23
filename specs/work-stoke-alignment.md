<!-- STATUS: done -->
<!-- CREATED: 2026-04-22 -->
<!-- BUILD_COMPLETED: 2026-04-22 -->
<!-- BUILD_ORDER: after-work-stoke -->
<!-- SOURCE: Portfolio alignment + CloudSwarm-R1 integration delta from operator /scope paste 2026-04-22 -->
<!-- DEPENDS_ON: work-stoke.md (TASKs 1..23 complete) -->

## Final shipped commits

- AL-1 — 38a2a40 (correlation.IDs context + ApplyHeaders seam)
- AL-2 — 9dbb848 (ReadTierHeaders for X-Model-Tier/X-Model-Resolved)
- AL-3 — 1e51cfe (SharedAuditEvent 9-field canonical helper)
- AL-4 — 9a18a6a (TRUECOM_* canonical env with TRUSTPLANE_* legacy fallback)
- CS-1 — b73cdda (EmitCostEventToStdout byte-exact for CloudSwarm)
- CS-2 — 96138e6 (HITL wire-format contract test locking stdin/stdout shape)
- CS-3 — (ledger half) 884bbb9 (LedgerAppendHook on WriteNode). Plan-status hook deferred — needs plan struct terminal-state notifier.
- CS-4 — (library half) core commits: Bus.ExportDelta + importMemoryFromFile. CLI flag wiring deferred.
- CS-5 — 61c5a20 (session-end snapshot with 5-field payload)

Seams are landed + tested. Per-caller wiring (providers calling
ReadTierHeaders, agentloop injecting correlation IDs, SOW main
parsing --import-memory flag) are additive follow-ups that don't
change any contract.


# work-stoke-alignment.md — Portfolio Alignment + CloudSwarm-R1 Integration Follow-Ups

## Scope

This spec captures the **new** items from the operator's 2026-04-22
scope paste that were NOT in `specs/work-stoke.md` TASKs 1..23. The base
spec is done (22/23 VERIFIED, 1 BLOCKED on upstream). The paste layered
two new tracks on top:

1. **Portfolio alignment seams** (SEAM-20, SEAM-21, SEAM-22) from
   `verification/PORTFOLIO-ALIGNMENT.md` §3 R1 per-repo delta.
2. **CloudSwarm-R1 integration** (`scope/CLOUDSWARM-R1-INTEGRATION.md`
   §10.2) — stdout event parity, memory preseed, session-end snapshot.

All items are grep-verified against HEAD `31c507d` on branch
`build/full-scope`.

## Task index

### Phase A — Portfolio alignment (P1 + P2)

- **AL-1** (P1, SEAM-22) — Emit `X-Stoke-Session-ID` / `X-Stoke-Agent-ID`
  / `X-Stoke-Task-ID` on outbound LLM calls in `internal/apiclient/`,
  `internal/provider/`, `internal/agentloop/`.
- **AL-2** (P2, SEAM-21) — Read `X-Model-Tier` / `X-Model-Resolved`
  response headers in `internal/provider/*` for observability.
- **AL-3** (P2, SEAM-20) — Emit `stoke.request.*` and descent events in
  canonical 9-field SharedAuditEvent shape.
- **AL-4** (operational) — Add `TRUECOM_API_URL` / `TRUECOM_API_KEY`
  canonical env names with legacy `TRUSTPLANE_*` fallback + WARN log.
  90-day window closes 2026-07-21.

### Phase B — CloudSwarm-R1 integration

- **CS-1** (P0) — Verify / add stdout cost event at LLM-client boundary:
  `{event:"cost", model, input_tokens, output_tokens, usd}` on every
  LLM call.
- **CS-2** (P0) — Freeze HITL stdin JSON wire format + add contract test.
- **CS-3** (P1) — Emit stdout events on plan-node changes, ledger
  appends, memory-scope writes.
- **CS-4** (P1) — Accept `--import-memory <path>` flag to preload
  memory-bus; emit changed-scope delta on session end.
- **CS-5** (P2) — Emit terminal `{event:"session_end", ledger_digest,
  memory_delta, cost_total, plan_summary}` event.

## Task details

### AL-1 — Outbound correlation headers (P1, SEAM-22)

**Problem.** RelayGate VERIFIED reading `X-Stoke-Session-ID` /
`X-Stoke-Agent-ID` / `X-Stoke-Task-ID` on request ingress
(`apiserver/server.go:609-617` + `receipthook/receipt.go:22-24`). Stoke
does not set them on outbound LLM requests —
`grep 'X-Stoke-Session\|X-Stoke-Agent\|X-Stoke-Task' stoke --include='*.go'`
returns 0 hits in `internal/apiclient/`, `internal/provider/`,
`internal/agentloop/`. Audit pipeline loses session→task correlation.

**Target files.**
- `internal/apiclient/` — any HTTP client that issues chat-completion
  requests.
- `internal/provider/` — Anthropic / OpenAI / Ember adapters.
- `internal/agentloop/` — the agentloop request builder.

**Fix.** At the request-building boundary, look up session/agent/task
IDs from the current context and set:
```go
req.Header.Set("X-Stoke-Session-ID", sessionID)
req.Header.Set("X-Stoke-Agent-ID", agentID)
req.Header.Set("X-Stoke-Task-ID", taskID)
```

Thread the IDs via a `CorrelationIDs struct` on the request context or
on the provider struct. If provider lacks an existing seam, add one
minimal field; do not refactor request pipelines.

**AC.**
1. Unit test: stub HTTP server captures headers; assert all three are
   non-empty when IDs are set in context.
2. Unit test: when context has no IDs (standalone Stoke run), headers
   are omitted (not set to empty string).
3. `go build ./...` + `go vet ./...` exit 0.

### AL-2 — Tier alias response-header consumption (P2, SEAM-21)

**Problem.** RelayGate Task 24 VERIFIED emitting `X-Model-Tier` /
`X-Model-Resolved` on responses (commit `33026f4,6dc2aaf` at
`internal/apiserver/server.go:677-678`). Stoke doesn't read them —
`grep 'X-Model-Tier\|X-Model-Resolved' stoke --include='*.go'` = 0 hits.
Provider pool still resolves "tier:reasoning" / "smart" via
`STOKE_PROVIDERS` registry; consuming RelayGate's header gives richer
observability.

**Target file.** `internal/provider/*` — response-handling wrapper.

**Fix.** After the HTTP response is received, extract the two headers
and log / emit via streamjson as `model_resolved`:
```go
if tier := resp.Header.Get("X-Model-Tier"); tier != "" {
    logger.Info("model_tier", "tier", tier)
}
if resolved := resp.Header.Get("X-Model-Resolved"); resolved != "" {
    cfg.StreamJSON.EmitSystem("stoke.model.resolved", map[string]string{
        "alias": cfg.Model, "resolved": resolved,
    })
}
```

**AC.**
1. Unit test: httptest server emits both headers; logger captures the
   tier value; streamjson sees one `stoke.model.resolved` event.
2. Headers absent → no log / no emit (clean standalone behavior).

### AL-3 — SharedAuditEvent 9-field shape (P2, SEAM-20)

**Problem.** Per portfolio alignment doc, `stoke.request.*` and descent
events should emit the canonical 9-field SharedAuditEvent structure
(id, ts, type, session_id, agent_id, task_id, payload, severity,
trace_parent). Today stoke emits various shapes across streamjson
emitters.

**Target files.**
- `internal/streamjson/emitter.go` — add a helper `EmitSharedAudit` that
  enforces the 9-field shape.
- Any `EmitSystem` call sites that produce `stoke.request.*` /
  `stoke.descent.*` / `stoke.ac.*` events — migrate to the canonical
  helper.

**Fix.** Introduce:
```go
type SharedAuditEvent struct {
    ID          string `json:"id"`
    Timestamp   string `json:"ts"`
    Type        string `json:"type"`
    SessionID   string `json:"session_id"`
    AgentID     string `json:"agent_id"`
    TaskID      string `json:"task_id"`
    Payload     json.RawMessage `json:"payload"`
    Severity    string `json:"severity"` // "info"|"warn"|"error"
    TraceParent string `json:"trace_parent,omitempty"`
}

func (e *Emitter) EmitSharedAudit(ev SharedAuditEvent)
```

Migrate existing callers incrementally — backward-compat: unknown fields
pass through.

**AC.**
1. `EmitSharedAudit` produces JSON with all 9 keys.
2. `stoke.descent.tier` event emitted via the helper contains
   `session_id` / `agent_id` / `task_id` populated from context.

### AL-4 — TRUECOM_* env names with legacy fallback

**Problem.** `internal/trustplane/factory.go:120` reads `TRUSTPLANE_API_URL`
/ `TRUSTPLANE_API_KEY`. Per Truecom Task 23, canonical names are
`TRUECOM_API_URL` / `TRUECOM_API_KEY`. 90d dual-accept window closes
2026-07-21.

**Target file.** `internal/trustplane/factory.go`.

**Fix.**
```go
func envOrFallback(canonical, legacy string) string {
    if v := os.Getenv(canonical); v != "" { return v }
    if v := os.Getenv(legacy); v != "" {
        log.Printf("WARN: %s is deprecated; use %s instead (removed 2026-07-21)", legacy, canonical)
        return v
    }
    return ""
}
// Callers:
url := envOrFallback("TRUECOM_API_URL", "TRUSTPLANE_API_URL")
key := envOrFallback("TRUECOM_API_KEY", "TRUSTPLANE_API_KEY")
```

**AC.**
1. Test: only `TRUECOM_API_URL` set → value used, no WARN.
2. Test: only `TRUSTPLANE_API_URL` set → value used, WARN logged.
3. Test: both set → canonical wins, no WARN.
4. Test: neither set → returns "".

### CS-1 — LLM cost stdout event parity (P0)

**Problem.** CloudSwarm supervisor sidecar parses
`{event:"cost", model, input_tokens, output_tokens, usd}` stdout events
and writes to `llm_usage`. Stoke emits cost via streamjson but per-LLM-call
granularity at the client boundary must be verified.

**Target file.** The LLM-client boundary in `internal/provider/` or
`internal/costtrack/`.

**Fix.** Verify on every successful LLM completion we emit a stdout
line matching exactly:
```json
{"event":"cost","model":"<name>","input_tokens":<int>,"output_tokens":<int>,"usd":<float>}
```
The existing `CostDashboard` (T3) already tracks these. Add a tiny
serializer at emit time that matches CloudSwarm's parser (trailing
newline, LF not CRLF).

**AC.**
1. Integration test: stub provider returns usage; assert stdout contains
   the exact event shape.
2. CloudSwarm handler regex matches the line.

### CS-2 — HITL wire-format freeze (P0)

**Problem.** CloudSwarm resumes a Temporal signal on HITL stdin response.
Wire format must be frozen with a contract test.

**Target file.** `internal/hitl/hitl.go` — contract test in
`internal/hitl/wire_format_contract_test.go`.

**Fix.** Add a test that asserts the exact JSON shape of the request
emitted on stdout (when HITL blocks) and the parser accepts the exact
shape CloudSwarm sends on stdin:
```json
// Outbound (hitl_required):
{"type":"hitl_required","ask_id":"...","prompt":"...","options":[...],"timeout_s":300}
// Inbound (decision):
{"type":"hitl_decision","ask_id":"...","choice":"...","reason":"..."}
```

**AC.**
1. Test passes today against current code (freeze).
2. Test fails loudly if any field is added/removed/renamed without a
   coordinated CloudSwarm change.

### CS-3 — Plan / ledger / memory stdout events (P1)

**Problem.** CloudSwarm's workspace-pane rendering needs structured
events on every plan-node change, ledger append, memory-scope write.
Currently most of these flow through the bus but not via stdout.

**Target files.**
- `internal/plan/` — emit on plan-node change.
- `internal/ledger/store.go` — emit on `WriteNode`.
- `internal/memory/membus/bus.go` — emit on `Remember` (already
  partially — check).

**Fix.** At each site, call `streamjson.EmitSystem` with the right
subtype:
- `stoke.plan.node_updated` with `{node_id, status, title}`.
- `stoke.ledger.appended` with `{node_id, type, parent_hash}` (no content).
- `stoke.memory.stored` (already emitted per T11).

**AC.**
1. Test: a short SOW run emits at least one of each event type.

### CS-4 — Memory preseed + session-end delta (P1)

**Problem.** CloudSwarm holds per-task memory across sessions. Supervisor
needs a way to preseed R1's memory bus from a JSON snapshot and capture
the delta at session end.

**Target files.**
- `cmd/stoke/main.go` — add `--import-memory <path>` flag; on parse,
  read JSON and populate `membus.Bus` via Remember before the main loop.
- `internal/memory/membus/bus.go` — `ExportDelta(since time.Time) map[Scope][]MemoryRow`
  for dumping changed scopes.
- Session-end path — emit a final `stoke.session.memory_delta` event
  carrying the delta JSON.

**AC.**
1. `stoke sow --import-memory foo.json --file spec.yaml` loads the
   bus with the snapshot; subsequent Recalls see those rows.
2. At session end, stdout has a `stoke.session.memory_delta` event
   listing rows changed during the session.

### CS-5 — Session-end snapshot (P2)

**Problem.** CloudSwarm needs a clean hand-off point when the subprocess
exits. Currently `streamjson.EmitSystem("stoke.session.end", ...)` is
emitted but without a canonical shape.

**Target file.** `cmd/stoke/sow_native_streamjson.go` — extend the
session.end emit.

**Fix.** At session end emit:
```json
{"type":"stoke.session.end","data":{
  "session_id":"...",
  "ledger_digest":"sha256:...",
  "memory_delta_ref":"path/to/export",
  "cost_total":12.34,
  "plan_summary":{"tasks_completed":N,"tasks_failed":M}
}}
```

**AC.**
1. Integration test: synthetic run produces exactly one `stoke.session.end`
   with all 5 fields.

## Sequencing

- AL-1 and AL-4 first (P1/operational, ~1 day each).
- CS-1 / CS-2 next (P0 cloudswarm contract — ~0.5 day each).
- AL-2 / CS-3 / CS-4 / AL-3 / CS-5 in any order (~1-2 days each).

Total: ~6-8 days serialized; ~3 days parallelized across 3 subagents.

## Out of scope / explicitly deferred

- ALIGNMENT-NOTE A (X-TrustPlane-* scheme divergence). Top of spec says
  "R1 is canonical, no R1 change needed" — RelayGate must migrate to
  ms-timestamp + `${METHOD}.${path}.${ts_ms}.${sha256hex(body)}` scheme.
  No work in this repo.
- BLOCKED T23 (Truecom Go SDK WithControlPlane) — still waiting on
  upstream Truecom B8.
