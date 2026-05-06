# Spec Review — `specs/lanes-protocol.md`

**Reviewed:** 2026-05-02
**Spec status (frontmatter):** ready
**Build order:** 3 (depends on `cortex-core`)
**Reviewer:** automated 10-check rubric
**Outcome:** all critical and non-critical issues fixed inline. No outstanding items.

---

## Summary verdict

| # | Check | Result |
|---|---|---|
| 1 | Frontmatter | PASS |
| 2 | Self-contained items | PASS |
| 3 | No vague items | FIXED |
| 4 | Test plan | FIXED |
| 5 | Concrete paths | FIXED |
| 6 | Cross-refs | FIXED |
| 7 | Stack & versions | FIXED (critical) |
| 8 | Out of scope | FIXED |
| 9 | Existing patterns (hub/events.go, server/, mcp/, IPC-CONTRACT.md) | PASS |
| 10 | Risk surfacing | FIXED |

Special-focus checks:
- 6 lane event types defined with full JSON examples — **PASS**.
- 5 MCP tool schemas verbatim — **PASS**.
- Backward-compat with `desktop/IPC-CONTRACT.md` guaranteed — **PASS**.
- Replay semantics (`Last-Event-ID` + `since_seq`) precise — **FIXED** (RFC reference was wrong).
- State machine ASCII diagram present — **PASS**.

---

## 1. Frontmatter — PASS

Lines 1–4 carry `STATUS: ready`, `CREATED: 2026-05-02`, `DEPENDS_ON: cortex-core`, `BUILD_ORDER: 3`. Matches sibling specs.

## 2. Self-contained items — PASS

Every checklist item in §11 is independently actionable. Items name the file, the function/struct to add, and the assertion to write.

## 3. No vague items — FIXED

Two findings, both fixed inline:

- **Item 31** said *"Add `oklog/ulid/v2` to `go.mod` if not already present."* The dependency is already at `go.mod:22` (`github.com/oklog/ulid/v2 v2.1.1`). Rewrote item 31 to a no-op note plus a concrete `go get` command for any future bump.
- **Item 32** named `tools/lint-lane-events.go`, but no `tools/` directory exists in this repo. The convention is `scripts/` (`scripts/pre-push.sh` already present). Retargeted item 32 to `scripts/lint-lane-events.sh`, cited the convention, and wired it into the CLAUDE.md `go vet ./...` gate.

## 4. Test plan — FIXED

§10 enumerates six golden NDJSON fixtures (§10.1) with explicit filenames + scenarios; four real Go test files (§10.2–§10.5) with paths and assertions; manual checklist (§10.6); performance budget in §11 item 39.

One real defect found and fixed: the spec said §10 used `internal/schemaval/` to validate the §7 JSON Schemas. But `internal/schemaval/` is a custom field-list `Schema` struct (`{Name, Fields}`), not a JSON Schema draft 2020-12 validator. The §7 docs ARE draft 2020-12. Fixed inline:

- §10.2 and §10.4 now say `schemaval.Schema` constants are hand-translated from the §7 draft 2020-12 documents.
- Added checklist items 18a and 18b creating `internal/mcp/lanes_schemas.go` and `internal/hub/lane_schemas.go` (one `schemaval.Schema` per tool input/output and per event type) plus parity unit tests asserting the runtime schemas stay in sync with the §7/§4 documents.

## 5. Concrete paths — FIXED

Findings (all fixed):
- "per `transport.md` §Wire envelope" — relative filename only; the file lives at `specs/research/synthesized/transport.md`. Fixed at three sites in §5.4, §6.4, §5.2.
- "per `agentic.md` §1 Slack-style envelope" — same problem. Fixed at two sites in §7 and §7.6.
- "registered alongside the existing `stoke_server.go` and `codebase_server.go`" — bare filenames; expanded to `internal/mcp/...`. Fixed in §7 preamble.
- "live in `cortex_server.go`" — no leading path. Fixed to `internal/mcp/cortex_server.go` with cross-link to spec 1.

Verified concrete paths in spec: `internal/hub/Bus`, `internal/bus/`, `internal/mcp/types.go`, `internal/streamjson/twolane.go`, `internal/server/server.go`, `internal/cortex/lane.go` (NEW, owned by spec 1), `desktop/IPC-CONTRACT.md`, `src-tauri/src/ipc.rs`. All exist or are scoped to a named build-order spec.

## 6. Cross-refs — FIXED

- D-C3, D-C4, D-S1, D-S4, D-S6, D-S7 referenced. Verified against `docs/decisions/index.md` 2026-05-02 block — all six exist with the meanings the spec attributes to them.
- Build order references (spec 1 cortex-core, spec 6 web-chat-ui, spec 7 desktop-cortex-augmentation, spec 8 agentic-test-harness) — verified against `docs/decisions/index.md` D-2026-05-02-01 build chain. Lanes is spec 3.
- §9 says "All 11 methods in §2 of the IPC contract continue to work." `desktop/IPC-CONTRACT.md` §2.6 summary table totals **11** round-tripping methods (3+2+2+2+2). The IPC contract's §2 intro at line 86 said "Thirteen methods" — internally inconsistent with its own §2.6. **Fixed `desktop/IPC-CONTRACT.md` line 86 inline**: now reads "Eleven methods across five categories (3 Session + 2 Ledger + 2 Memory + 2 Cost + 2 Descent — matches §2.6 Total)."

## 7. Stack & versions — FIXED (critical)

Two factual errors in §2 "Stack & Versions":

1. **Go version wrong.** Spec said: *"Go 1.26.1 (matches `go.work`)."*
   - `go.mod` declares `go 1.25.5` (line 3).
   - `go.work` does **not** exist in this repo (`find /home/eric/repos/r1-agent -name go.work` returns nothing).
   - Sister spec `cortex-core.md` correctly states Go 1.25.5.
   - **Fixed:** §2 now reads "Go 1.25.5 (see `go.mod` line 3); no `go.work` exists in the repo."

2. **Wrong RFC for SSE.** §6.1 said: *"Conforms to RFC 8895 (SSE)."*
   - There is no RFC 8895 for Server-Sent Events. SSE is part of the **WHATWG HTML Living Standard**.
   - **Fixed:** §6.1 now cites `https://html.spec.whatwg.org/multipage/server-sent-events.html`.

Other stack items verified PASS: JSON-RPC 2.0, NDJSON envelope, MCP `2025-11-25`, ULID v2.1.1 (`go.mod`), WS subprotocol token `r1.lanes.v1`.

## 8. Out of scope — FIXED

Before: a single inline "OUT OF SCOPE for this spec" sentence in §9. No standalone section.

**Fixed:** Inserted §9.5 "Out of Scope" before §10 Test Plan, listing 10 explicit items owned by sibling specs:
1. Removal of `session.delta` — owned by a future minor-release contract update.
2. `internal/cortex/Lane` value type — owned by spec 1.
3. TUI lane rendering — owned by spec 4.
4. Web UI lane rendering — owned by spec 6.
5. R1D server endpoints implementation — owned by spec 5.
6. Tauri IPC plumbing — owned by spec 7.
7. `r1.cortex.notes` and `r1.cortex.publish` MCP tools — owned by spec 1.
8. Multi-session-per-daemon scheduler — owned by spec 5.
9. Authentication beyond WS subprotocol token — owned by existing `internal/rbac/`.
10. OpenTelemetry export of lane events — flagged as future work by the spec author.

## 9. Existing patterns — PASS

| Reference | Path | Verified |
|---|---|---|
| `internal/hub/events.go` | yes | `Event` struct at line 148; 11 payload pointers (`Tool`, `File`, `Model`, `Git`, `Cost`, `Prompt`, `Skill`, `Lifecycle`, `Test`, `Security`, plus `Custom map[string]any`). Adding `Lane *LaneEvent` is a clean additive change matching the existing pattern. |
| `internal/streamjson/twolane.go` | yes | `isCriticalType` and `EmitTopLevel` confirmed at lines 92 and 120. Spec correctly extends `isCriticalType` for new event names. |
| `internal/server/` | yes | `server.go` exists; new `handleLaneEvents` and `ws.go` are well-scoped additions. |
| `internal/mcp/` | yes | `stoke_server.go` and `codebase_server.go` confirmed. New `lanes_server.go` follows the same `ToolDefinition` registry shape. |
| `internal/bus/wal.go` | yes | Replay knobs referenced; defaults match existing implementation surface. |
| `internal/stokerr/errors.go` | yes | Reused for error codes. |
| `internal/schemaval/validator.go` | yes | Custom field-list `Schema{Name, Fields}` validator (NOT JSON Schema draft 2020-12). Spec was ambiguous about which schemas the validator consumes; fixed in check 4 above. |
| `desktop/IPC-CONTRACT.md` | yes | §9 promises additive-only changes; verified §1, §2, §3, §5, §6, §7 untouched in plan; §4 update is targeted. |
| `internal/cortex/` | does not exist yet | Correctly attributed to spec 1 (cortex-core), build-order 1; lanes spec is build-order 3. |

## 10. Risk surfacing — FIXED

Before: §9 "Risk register" had 3 risks. Solid but missed two implementation pitfalls.

**Fixed:** added two more risks to §9 risk register:
- ULID monotonicity within the same millisecond requires `ulid.MonotonicEntropy`. Naive `ulid.New(time.Now(), entropy)` will produce out-of-order IDs under bursts and break the lex-sortable invariant. Mitigation pinned to §8.1.
- Dual emission of `session.delta` + `lane.delta` doubles bytes for the main lane during the compat window. Mitigation: bounded by one minor release; measured in §10.5.

Existing risks retained:
- Double-render of main-lane text by naive consumers.
- Unknown-event panic by old clients (verified in test §10.5).
- WAL retention misconfig truncating events.

---

## Special-focus checklist

### 6 lane event types defined with full JSON examples — PASS

§4.1–§4.6 each carry: name, semantic description, full JSON example with realistic ULIDs and ISO-8601 timestamps, the enum values for any string fields, the cardinality rules ("emitted exactly once", "no more than once per second"), and which terminal-state semantics apply. `lane.created`, `lane.status`, `lane.delta`, `lane.cost`, `lane.note`, `lane.killed` — six exactly. The "adding a seventh is a wire-version bump" rule (§4 preamble) closes the future-proof gap.

### 5 MCP tool schemas verbatim — PASS

§7.1–§7.5 carry full JSON Schema draft 2020-12 input + output schemas for `r1.lanes.{list,subscribe,get,kill,pin}`. Schemas are paste-ready into Go via `json.RawMessage`. `r1.lanes.subscribe` carries `streaming: true`. §7.6 reconfirms the count and excludes the cortex-side tools (owned by spec 1).

### Backward-compat with `desktop/IPC-CONTRACT.md` guaranteed — PASS

§9 enumerates 7 verbatim guarantees (no rename, no remove, no shape change, dual-emit window, error-code reuse, identical envelope, orthogonal version handshake). Cross-checked against the actual contract: §1.4 server-pushed-event shape, §1.5 versioning header, §3.2 stoke_code mirror, §5 Tauri-only commands, §4 event table — all preserved.

### Replay semantics (`Last-Event-ID` + `since_seq`) precise — FIXED

`Last-Event-ID` (§6.1) and `since_seq` (§6.2) carry identical semantics, server-side resumption from `seq+1`, WAL-truncation `404` mapping with `data.stoke_code = "not_found"` and `data.detail = "wal_truncated"`. Gap detection (§6.3) defines the per-session and per-lane (`delta_seq`) cursors. Reserved `seq=0` for `session.bound` snapshot is documented in §5.5.

The original RFC 8895 citation was wrong; fixed to the WHATWG HTML Living Standard URL.

### State machine ASCII diagram present — PASS

§3.3 diagram covers all six states with explicit transitions. §3.3 transition table enumerates the legal edges. Pin is correctly drawn as orthogonal to state. Terminal states (`done | errored | cancelled`) explicitly defined as no-further-deltas, with surface-side discard rule.

---

## Inline fixes applied

| File:section (post-fix) | Change |
|---|---|
| `specs/lanes-protocol.md` §2 | Go version 1.26.1 → 1.25.5; remove false "matches `go.work`"; cite `go.mod` line 3; pin ULID version. |
| `specs/lanes-protocol.md` §6.1 | Strike "RFC 8895"; cite WHATWG HTML Living Standard SSE section. |
| `specs/lanes-protocol.md` §5.2 | `transport.md` → `specs/research/synthesized/transport.md`. |
| `specs/lanes-protocol.md` §5.4 | `transport.md` → `specs/research/synthesized/transport.md`; add D-S6 cross-ref. |
| `specs/lanes-protocol.md` §6.3 | `agentic.md` → `specs/research/synthesized/agentic.md`. |
| `specs/lanes-protocol.md` §6.4 | `transport.md` → `specs/research/synthesized/transport.md`. |
| `specs/lanes-protocol.md` §7 preamble | Bare filenames `stoke_server.go` / `codebase_server.go` → `internal/mcp/...`; `agentic.md` → repo-relative path. |
| `specs/lanes-protocol.md` §7.6 | `cortex_server.go` → `internal/mcp/cortex_server.go`; cross-link to spec 1. |
| `specs/lanes-protocol.md` §9 risk register | Added two risks (ULID monotonicity, dual-emission byte overhead). |
| `specs/lanes-protocol.md` §9.5 (NEW) | Inserted dedicated "Out of Scope" section with 10 explicit items. |
| `specs/lanes-protocol.md` §10.2, §10.4 | Disambiguated `internal/schemaval/` (custom field-list validator) from §7 JSON Schema draft 2020-12 documents; specified hand-translated runtime schemas at `internal/hub/lane_schemas.go` and `internal/mcp/lanes_schemas.go`. |
| `specs/lanes-protocol.md` §11 item 2 | Cited `internal/hub/events.go:148`, listed all 11 existing payload pointer fields, specified insertion order and JSON tag for the new `Lane` field. |
| `specs/lanes-protocol.md` §11 item 18 | Specified the §7 schemas embed verbatim as `json.RawMessage` in `ToolDefinition`. |
| `specs/lanes-protocol.md` §11 items 18a, 18b (NEW) | Add `internal/mcp/lanes_schemas.go`, `internal/hub/lane_schemas.go`, plus parity unit tests asserting runtime schemas match §7/§4. |
| `specs/lanes-protocol.md` §11 item 31 | Removed conditional language; cite `go.mod:22` directly. |
| `specs/lanes-protocol.md` §11 item 32 | `tools/lint-lane-events.go` → `scripts/lint-lane-events.sh` (no `tools/` directory in repo). Wired into `go vet` gate. |
| `desktop/IPC-CONTRACT.md` line 86 | "Thirteen methods" → "Eleven methods across five categories (3 Session + 2 Ledger + 2 Memory + 2 Cost + 2 Descent — matches §2.6 Total)". Resolves internal inconsistency between §2 intro and §2.6 summary. |

---

## 50-word summary

Strong spec — six events, five MCP tools, ASCII state machine, full IPC backward-compat all present and concrete. Two critical errors fixed inline: spec claimed Go 1.26.1 with `go.work` (repo has `go 1.25.5` and no `go.work`); cited nonexistent "RFC 8895" for SSE (correct source is WHATWG HTML). Added Out-of-Scope section, two risks, and tightened cross-reference paths.
