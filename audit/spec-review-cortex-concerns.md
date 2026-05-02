# Spec Review — `specs/cortex-concerns.md`

**Reviewed:** 2026-05-02
**Reviewer:** /review-spec rubric (10 checks + Lobe verbatim audit)
**Spec status:** `STATUS: ready` / `BUILD_ORDER: 2` / `DEPENDS_ON: cortex-core`

## Rubric Summary

| # | Check | Result | Evidence (one line) |
|---|---|---|---|
| 1 | Frontmatter valid | PASS | All four HTML comment fields present at top: STATUS, CREATED, DEPENDS_ON, BUILD_ORDER. |
| 2 | Self-contained items | PASS | All 36 checklist items name a target file, function/test, and copy-paste-ready text where applicable. |
| 3 | No vague items | PASS | No "TBD"/"figure out"/"etc." — every item names a concrete behavior; "etc." appears only inside narrative prose. |
| 4 | Test plan present | PASS | `## Test Plan` section names per-Lobe `*_lobe_test.go` + `*_integration_test.go`, coverage targets, privacy-specific assertion. |
| 5 | Concrete file paths | PASS | Every Lobe specifies `internal/cortex/lobes/<name>/lobe.go`; CLI in `cmd/r1/cortex_memory_audit.go`; shared infra at `internal/cortex/lobes/llm/`. |
| 6 | Cross-references | PARTIAL | References cortex-core (spec 1), lanes-protocol (spec 3), MCP exposure (spec 8); references decision IDs `D-2026-05-02-04/05/06`, `D-C3/C5/C6/C7`, `OQ-7`, and `RT-CONCURRENT-CLAUDE-API §1/§2/§4/§5`. Decision IDs were verified to exist in `specs/cortex-core.md`. **Gap:** none of these cross-refs link the literal file path of the open-questions log (`OQ-7` is referenced but its source file is not given). |
| 7 | Stack & versions section | PASS | `## Stack & Versions` enumerates Go 1.26, all 9 internal packages, Anthropic Haiku 4.5 with pricing, cache TTL. |
| 8 | Out of scope explicit | PASS | `## Out of Scope` lists 6 items (cortex-core internals, lanes/TUI, MCP cross-process, Router LLM, mission orchestration, new supervisor rules). |
| 9 | Existing patterns to follow | PASS | `## Existing Patterns the Lobes Consume` table cites `memory.NewStore`/`Recall`/`RecallForFile`, `wisdom.NewStore`/`Learnings`/`FindByPattern`/`Record`, `tfidf.NewIndex`/`AddDocument`/`Search`, `supervisor.New`/`RegisterRules`, `bus.Bus.Publish`/`Subscribe`, `hub.Bus.Subscribe`. |
| 10 | Honest risk surfacing | PARTIAL | Privacy section explicit; backpressure handling explicit; budget cap explicit. **Missing:** no risk discussion of (a) `tfidf.Index` rebuild cost as memory corpus grows, (b) Haiku JSON-output reliability without `tools` array (PlanUpdateLobe), (c) goroutine leak risk if a Lobe panics during `Run`. |

**Lobe verbatim spec audit (special focus):**

| Lobe | Type | System prompt verbatim | Tool schema verbatim | Deterministic algorithm |
|---|---|---|---|---|
| MemoryRecallLobe | deterministic | n/a | n/a | PASS — exact algorithm: last-1000-char query → `mem.Recall(q,5)` ∪ `tfidf.Search(q,5)` → dedup by ID → top-3 Notes. |
| WALKeeperLobe | deterministic | n/a | n/a | PASS — `hub.SubscribeAll → bus.Publish` with framing prefix `cortex.hub.<type>`; backpressure threshold 1k, drop info, 30s warning Note. |
| RuleCheckLobe | deterministic | n/a | n/a | PASS — subscribe `bus.Pattern{TypePrefix:"supervisor.rule.fired"}`, severity table by name prefix, sticky Note (`ExpiresAfterRound=0`). |
| PlanUpdateLobe | LLM (Haiku) | PASS — fenced verbatim block, instructed to embed as `const planUpdateSystemPrompt`, byte-equality test required. | PASS — explicitly tool-free; output JSON schema embedded in system prompt. |
| ClarifyingQLobe | LLM (Haiku) | PASS — fenced verbatim block, byte-equality test required. | PASS — `queue_clarifying_question` schema given as full JSON object with required fields. |
| MemoryCuratorLobe | LLM (Haiku) | PASS — fenced verbatim block, byte-equality test required. | PASS — `remember` schema given as full JSON object with required fields. |

All 6 Lobes are specified to the rubric's bar. LLM Lobes have verbatim system prompts and verbatim tool schemas; deterministic Lobes have step-by-step algorithms.

## Critical fails (require inline fix)

**C-1 — `cortex.Note` field-name mismatch with cortex-core (spec 1).**
The cortex-core spec defines:
```go
type Note struct { ID, LobeID, Severity, Title, Body string; Tags []string; Resolves; EmittedAt; Round; Meta map[string]any }
```
But `cortex-concerns.md` writes Notes with fields `Source`, `Tag` (singular), `Refs`, `Action`, `ExpiresAfterRound` — none of which exist in cortex-core. This will not compile. **Fix:** rename `Source → LobeID`, `Tag → Tags []string`, fold `Refs`/`Action`/`ExpiresAfterRound` into the existing `Meta map[string]any`. Add a Note in `Boundaries` clarifying that no struct fields are added to cortex-core; everything goes through `Meta`.

**C-2 — `hub.Bus` API surface does not match what the spec assumes.**
The spec writes `hub.Bus.Subscribe(EventType, handler)` and `hub.Bus.SubscribeAll(...)`. The actual hub (`internal/hub/bus.go`) exposes `Register(sub Subscriber) / Unregister / Emit / Gate / Transform / SubscriberCount`. There is no `Subscribe` method and no `SubscribeAll`. **Fix:** in the Existing-Patterns table and in WALKeeperLobe behavior, replace `hub.Bus.Subscribe(...)` with `hub.Bus.Register(Subscriber{ID, Filter, Handler})`; replace `SubscribeAll` with "register a Subscriber whose `Filter` returns true for every EventType".

**C-3 — Config hot-reload is asserted to exist but does not.**
Privacy section says: "Operator can disable individually without a daemon restart (config hot-reload via existing `internal/config` watcher)." `internal/config/` contains `claude_settings.go`, `detect.go`, `mcp_servers.go`, `policy.go`, `studio.go` — no watcher. **Fix:** soften to "operator can disable per-Lobe in `~/.r1/config.yaml`; takes effect on daemon restart" until a watcher is built (out of scope for this spec).

## Non-critical findings (not fixed inline; left as commentary in report)

- N-1 — `apiclient` type name drift: spec uses `apiclient.Tool` (actual: `ToolDef`), `apiclient.MessagesRequest` (actual: `Request`), and treats `apiclient.Client` as an interface (actual: concrete struct). Builders should use the real types or define a small interface in `internal/cortex/lobes/llm/`. Implementor must reconcile at code time but spec readers can follow the intent.
- N-2 — `OQ-7` is referenced for the default `AutoCurateCategories=[fact]` but the open-questions log path is not cited. A cross-ref to its file would help.
- N-3 — `tfidf.Index.AddDocumentChunked` listed in patterns table but unused by any of the 6 Lobes.
- N-4 — Risk surface for the Haiku PlanUpdateLobe's tool-free JSON output is missing — model might emit non-JSON prose; the spec already says "malformed JSON → no Note + warning log" so the risk is handled, just not surfaced as a risk.

## Verdict

**APPROVED WITH FIXES.** The spec is detailed, self-contained, includes verbatim system prompts and tool schemas as required by the rubric, and supplies clear deterministic algorithms for all three deterministic Lobes. Three critical issues block compilation/integration with cortex-core: (1) Note struct field mismatch, (2) wrong hub.Bus method names, (3) phantom config watcher. These are fixed inline in this revision. After the fixes the spec is implementation-ready and consistent with cortex-core.
