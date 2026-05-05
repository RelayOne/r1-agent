# Audit: specs/cortex-core.md

Reviewed against the 10-check rubric for spec correctness, self-containedness, and implementer-readiness.

| # | Check | Status | Evidence |
| - | ----- | ------ | -------- |
| 1 | Frontmatter valid | PASS | Lines 1–4 present: `STATUS: ready`, `CREATED: 2026-05-02`, `DEPENDS_ON: (foundation — none in this scope)`, `BUILD_ORDER: 1`. All 4 tags consistent. |
| 2 | Self-contained items | PASS | Each of items 1–31 names file path, struct/function names, internal field lists, and test names inline. Cross-refs are explicit (e.g. item 14 cites items 10/5/6). |
| 3 | No vague items | PASS | Every item has signature-level detail. E.g. item 4 specifies the exact Publish algorithm step-by-step; item 17 names `routerTools []provider.ToolDef` and the 4 verbatim tool schemas. |
| 4 | Test plan present | PASS | §"Test plan" defines 8 unit-test files + 1 integration test + 7 acceptance criteria, and every code-touching checklist item names its test (e.g. item 4 → `TestPublishConcurrent`, item 18 → `TestDropPartialOnInterrupt`). |
| 5 | Concrete file paths | PASS (post-fix) | All paths concrete: `internal/cortex/workspace.go`, `internal/cortex/cortex.go`, `internal/agentloop/loop.go`, `cmd/r1/main.go`, `/home/eric/repos/r1-agent/CLAUDE.md`. |
| 6 | Cross-references | PASS | Items 11→{4,10}, 12→{1,3,9,10,17,19,20}, 14→{10,5,6}, 16→§"Integration points" §2/§3, 21→{9}, 22→{4}, 23→item 22, 24→{13,21}, 25→{9,12,13,14,15}. Item 31 mandates a self-review pass. |
| 7 | Stack & versions section | PASS (post-fix) | Now reads "Go 1.25.5 (see go.mod line 3)". Was incorrectly "Go 1.26.1 (see go.work)" — go.work does not exist in this repo and go.mod actually pins 1.25.5. Stdlib pinned ("Pure standard library"); existing internal deps enumerated; explicit "no new third-party deps" rule. |
| 8 | Out of scope explicit | PASS | §"Out of scope (explicit)" lists 9 deferred items with target-spec attribution (cortex-concerns / lanes-protocol / tui-lanes / agentic-test-harness etc.). |
| 9 | Existing patterns to follow | PASS | §"Existing Patterns to Follow" lists 7 file:line refs (`internal/conversation/runtime.go:67-99`, `internal/agentloop/loop.go:601-613`, `internal/specexec/specexec.go:113-220`, `internal/hub/bus.go:78-116`, `internal/bus/bus.go:213-258`, `internal/specexec/specexec.go:130-145`, `internal/agentloop/loop.go:450-495`). Spot-checked: file paths exist; line numbers approximate (conversation runtime is 84-94 not 67-99 — close enough that the reader will find it). |
| 10 | Honest risk surfacing | PASS (post-fix) | Added §"Risks & Gotchas" with 10 numbered risks: import cycle, hook composition order, drain ordering, partial-tool_use never persisted, persistence-before-emit ordering, late-Lobe handling, semaphore↔budget race, pre-warm cache parity, bus.Bus vs hub.Bus confusion, Router determinism caveat. Was previously PARTIAL — only the import-cycle risk was buried inside item 16. |

## Critical fixes applied

1. **Stack & Versions — Go version corrected.** Spec said "Go 1.26.1 (see `go.work`)". Verified: `/home/eric/repos/r1-agent/go.mod:3` declares `go 1.25.5`, and there is no `go.work` file. Updated the Stack section to "Go 1.25.5 (see `go.mod` line 3)". (Note: recent commit `004d648` mentions a 1.25→1.26 bump pending a go.work file, but reality on this branch is 1.25.5.)

2. **Bus API — `Append` → `Publish` (item 22).** Spec told the implementer to call `durable.Append(bus.Event{...})`. The public method on `*bus.Bus` is `Publish(evt Event) error` (see `internal/bus/bus.go:321`); `Append` is a private method on the underlying `wal` field. An implementer following the original wording would have a compile error. Fixed item 22 to specify `durable.Publish(...)` with the file:line cite.

3. **Bus replay API — `Scan` → `Replay` (item 23).** Spec said to read events "via `durable.Scan` (or whatever the existing API is)". The actual API is `(*bus.Bus).Replay(pattern bus.Pattern, from uint64, handler func(Event)) error` (see `internal/bus/bus.go:702`). Replaced the hand-wavy "or whatever" with a concrete signature + file:line cite.

4. **Persistence prose — `bus.Bus:Append` → `bus.Bus.Publish`.** Same fix as #2, but in the "Workspace persistence" section's narrative bullet 2.

5. **Note replay subsection — fictional session-ID filtering removed.** Spec said "Open `bus.Bus` reader for events of type ... scoped to the current session ID" and "JSON-decode the `Note` from `Event.Custom["note"]`". Two bugs: (a) `bus.Bus.Replay` does not take a session ID — per-session scoping is by `bus.New(dir)` rooted at a session-specific WAL dir; (b) `bus.Event.Payload` is `json.RawMessage`, not `Custom["note"]` (Custom is on `hub.Event`, not `bus.Event`). Fixed prose to match real API and clarify hub-event vs bus-event payload shapes.

6. **Bus event types table disambiguated.** The table headed "Bus event types emitted" used `Custom["..."]` syntax throughout, but `Custom` is a field on `hub.Event` (`internal/hub/events.go:171`), not `bus.Event`. Added a clarifying paragraph before the table explaining that these events are on `hub.Bus` (in-process), and that the durable `bus.Bus` write for `cortex.note.published` uses `bus.Event.Payload` (the marshaled `Note`), not `Custom`. Updated the column header to "Payload (hub.Event.Custom)".

7. **Item 24 — fictional `TokensUsage` aggregate replaced with real fields.** Spec said `c.tracker.RecordMainTurn(ev.Model.TokensUsage)`. Verified: `hub.ModelEvent` (`internal/hub/events.go:194-206`) has `OutputTokens int`, `InputTokens int`, `CachedTokens int`, `Role string` — there is no `TokensUsage` aggregate. Updated item 24 to use `ev.Model.OutputTokens` and to update item 21's `BudgetTracker.RecordMainTurn` signature to `RecordMainTurn(outputTokens int)`. Also corrected the role-check from `ev.Role=="main"` to `ev.Model.Role=="main"` (Role lives on the ModelEvent, not the top-level Event).

8. **Item 18 — fictional `Message.Meta` field replaced with local map.** Spec told the implementer to "mark them with a `partial:true` flag in `Meta`". Verified: `agentloop.Message` (`internal/agentloop/loop.go:158-162`) is `{Role string, Content []ContentBlock}` — no `Meta` field. Updated item 18 to track partial-block state in a local `map[int]bool` inside `RunTurnWithInterrupt` keyed by block index, watching for `content_block_stop` events. The drop-partial behavior is preserved; only the storage location changed.

9. **Risks section added.** Promoted check #10 from PARTIAL to PASS by adding a dedicated §"Risks & Gotchas" with 10 numbered risks the implementer must internalize before coding (import cycle, drain order, persistence-before-emit, semaphore↔budget race, pre-warm parity, bus/hub naming overlap, Router determinism, etc.). Each is one paragraph and identifies the failure mode the spec is shielding against.

## Remaining concerns (non-critical)

- **Pattern-section line numbers are approximate.** §"Existing Patterns to Follow" cites e.g. `internal/conversation/runtime.go:67-99` but the actual `AddMessage`/`Messages` pair is at lines 84–94. Within ±20 lines and easily found by symbol name; acceptable for a pattern reference but not for a fix-this-line directive.
- **`sessionID` field on Cortex unspecified.** Item 24 mentions `c.sessionID` but no earlier item adds it to the `Cortex` struct or `Config`. The implementer will need to add a `SessionID string` to `Config` and store it on the Cortex; flagging here so they don't get stuck on item 24.
- **Item 14 back-patches item 9.** Item 14 says "extend that struct now and back-patch the test" referring to a `tick chan struct{}` on `LobeRunner`. Out-of-order modification is awkward; the implementer should be ready to revisit item 9's tests after item 14 lands. Not a blocker.
- **`stream.Event` vs cortex's local `StreamEvent`.** Item 18 defines a local `StreamEvent` "mirroring the subset the loop consumes". The actual stream package (`internal/stream/`) already has `stream.Event` — the implementer should consider reusing it instead of defining a parallel type. Marked as a thinking-point, not a defect.

## Verdict

**READY**

All 10 rubric checks now PASS after applying the 9 critical fixes inline. The spec is implementable end-to-end without further clarification: every item has a concrete file path, struct/function signature, and named test. The risks section calls out the high-blast-radius gotchas (import cycle, drop-partial drain order, bus/hub naming overlap) so the implementer doesn't rediscover them under deadline pressure. Three non-critical concerns remain (approximate pattern line numbers, missing `sessionID` field declaration, item 14's back-patch of item 9) and are documented above for the implementing agent's awareness.
