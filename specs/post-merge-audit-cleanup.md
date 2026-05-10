<!-- STATUS: done -->
<!-- CREATED: 2026-05-09 -->
<!-- BUILD_STARTED: 2026-05-09 -->
<!-- BUILD_COMPLETED: 2026-05-09 -->
<!-- DEPENDS_ON: cortex-core, cortex-concerns, agentic-test-harness, web-chat-ui, anti-truncation, r1d-server -->
<!-- BUILD_ORDER: 100 -->

> **Closed.** All 10 tasks shipped via PRs #233 (TASK-1), #238 (TASK-2),
> #237 (TASK-3+5), #242 (TASK-4), #240 (TASK-6), #234 (TASK-7), #239
> (TASK-8), #235 (TASK-9), and the HANDOFF doc updates that closed
> TASK-10. Storybook-mcp-validate target was subsequently dropped
> entirely (#247) when investigation showed the npm package's
> `validate` subcommand doesn't exist. See plans/HANDOFF.md for the
> per-PR record.

# post-merge-audit-cleanup — Remaining audit-flagged work after PR #211–#229

## Overview

Six audit reports under `audit/scan-*.md` enumerated 100+ stub/gap/quality
findings across the post-9-spec codebase. PRs #211–#229 (this session)
landed 16 of the high-impact fixes. This spec batches the remaining
items into independently buildable tasks so /build can execute them as
separate subagent dispatches.

Each task is self-contained:
- File path + line range pinned
- Exact change required
- Acceptance criteria written inline
- Tests required to ship green

No task depends on another in this spec.

## Tasks

### TASK-1 — Skill analyzer Stage 5 runtime contract injection (wall_time_lt)

File: `internal/r1skill/analyze/stages.go:209-242` (function `stageContract`).

Audit ref: `audit/scan-go-stubs.md` row "internal/r1skill/analyze/stages.go:209-242 stageContract defers wall_time_lt, forall, exists contract kinds to runtime with an info diagnostic — only actual_cost_lt is decidable. Runtime-assertion injection layer for these contracts is not implemented."

MUST: Add a runtime-contract-emit pass that turns each non-decidable
contract (`wall_time_lt`, `forall`, `exists`) into a structured
`RuntimeAssertion` record on `StageResult.RuntimeAssertions []RuntimeAssertion`.
The `StageResult` struct in `result.go` (or wherever it's defined) gains
this new field. Each RuntimeAssertion carries:
  - Kind string (the contract kind)
  - Bound float64 (for wall_time_lt, the second-budget; 0 otherwise)
  - Predicate string (for forall/exists, the literal predicate text)
  - SourceLocation string (e.g. "contract[3]")

MUST: Keep the existing `info` diagnostic so audit logs still record the
deferral, but rewrite the message to say "deferred to runtime
assertion (recorded on StageResult.RuntimeAssertions)" — distinguishing
the intentional defer from a missing implementation.

MUST: Update `stages_test.go` to add `TestStageContract_EmitsRuntimeAssertions`
asserting all three kinds produce one RuntimeAssertion entry with the
right Kind/Bound/Predicate.

VERIFY: `go test ./internal/r1skill/analyze/... -count=1 -v` passes; the
new test passes; `go vet` clean.

Effort: M.

### TASK-2 — Bench golden corpus seed

File: `internal/bench/golden_test.go:19,34,69,89` and
`internal/bench/testdata/missions/` (directory likely missing).

Audit ref: `audit/scan-test-quality.md` row "Bench-regression suite skips
entirely when missions directory empty (lines 19, 69) and silently buries
per-mission failures as `t.Skipf("expected in unit test context")` (lines
34, 89). Cannot detect bench regressions."

MUST: Vendor at least one canonical bench mission as JSON under
`internal/bench/testdata/missions/canonical-hello-world.json`. The shape
is whatever `bench.LoadMissions` expects — read that loader to derive
the schema.

MUST: Update `golden_test.go` to point at `testdata/missions/` (relative
to the test binary's working dir, which is the package dir under `go
test`). The skip-on-empty path stays for portability, but with a
testdata fixture present the skip never fires in this repo.

MUST: Distinguish "expected error in unit test context" from "real
regression" via a typed error returned by the mission runner. If the
runner can return a sentinel `bench.ErrFixtureBoundary`, the test calls
`errors.Is` and skips on that one error type only — every other error
is `t.Fatalf`. If the runner can't be modified, gate the skip behind
an explicit `BENCH_ALLOW_RUNTIME_ERRORS=1` env var so CI sees real
failures.

VERIFY: `go test ./internal/bench/... -count=1 -v` runs the canonical
mission (no skip) and passes. Removing the testdata file and re-running
should hit the skip path cleanly.

Effort: M.

### TASK-3 — Web ESLint config: import the custom rule correctly

File: `web/eslint.config.js` (or `eslint.config.mjs`) and
`web/eslint-rules/require-data-testid.js`.

Audit ref: GHA run `25595806051` for PR #229 lint job — `SyntaxError:
The requested module './eslint-rules/require-data-testid.js' does not
provide an export named 'default'`.

MUST: Either (a) add `export default { meta: {...}, create: ... }` to
`require-data-testid.js` so the existing `import requireDataTestid from
"./eslint-rules/require-data-testid.js"` works, OR (b) change the
import in the eslint config to a named import matching whatever the
rule file actually exports. Pick whichever option requires the smallest
change to the rule file.

MUST: Run `cd web && npm run lint` locally; expected behavior is "no
errors" or known warnings under the existing budget.

MUST: Add a comment at the top of `require-data-testid.js` pointing at
the eslint flat-config requirement so future readers don't break the
import again.

VERIFY: `cd web && npm run lint` exits 0.

Effort: S.

### TASK-4 — Web CI workflow: re-enable lint + storybook-mcp once TASK-3 + TASK-5 land

File: `.github/workflows/web.yml`.

MUST: After TASK-3 (eslint fix) and TASK-5 (storybook-mcp version pin)
land, restore the `lint` and `storybook-mcp-validate` jobs that were
dropped in PR #229's merged version. The workflow already has the
build-test job; just add back the two jobs with the same setup-node +
npm ci pattern.

MUST: Verify on a PR that touches `web/` that all three jobs pass.

VERIFY: workflow runs all three jobs green on a PR.

Effort: S.
DEPENDS_ON: TASK-3, TASK-5.

### TASK-5 — Storybook MCP version: pin to a published release

File: `Makefile:123-128` (target `storybook-mcp-validate`).

Audit ref: GHA run for PR #229 storybook-mcp-validate job — `npm error
notarget No matching version found for storybook-mcp@^9.`

MUST: Resolve to whatever the highest-published storybook-mcp version
is on npm. Run `npm view storybook-mcp version` to discover the latest;
update the Makefile pin from `^9` to `^<published-major>` (likely
`^0.x` or similar). If the package has been deprecated or renamed,
update the comment explaining the situation and either remove the
target or point at the replacement.

MUST: Run `make storybook-mcp-validate` locally; it should either
succeed or fail on a real validation finding (not on a missing
package).

VERIFY: `npm view storybook-mcp version` returns a value AND `make
storybook-mcp-validate` does not fail with `notarget`.

Effort: S.

### TASK-6 — Per-API-call AcquireLLMSlot in batched lobes

Files: `internal/cortex/lobes/clarifyq/lobe.go`,
`internal/cortex/lobes/memorycurator/trigger.go`,
`internal/cortex/budget.go` (the LobeSemaphore is here).

Audit ref: `audit/scan-governance-gaps.md` row "cortex-concerns 5 — All
LLM Lobes call cortex.AcquireLLMSlot(ctx) before client.Messages — for
Lobes that batch multiple Haiku calls per Run, ONLY the outer Run is
gated; concurrent intra-Run calls bypass the semaphore. Spec wanted
per-call gating."

MUST: Audit each LLM Lobe under `internal/cortex/lobes/*/` for the
number of `client.ChatStream` (or equivalent) calls per Run. Lobes that
make exactly one call per Run are already correctly gated by the
runner-level semaphore; do not change them.

MUST: For Lobes that make MORE than one call per Run, wrap each
individual `client.ChatStream` call in `LobeSemaphore.Acquire(ctx)` /
`defer LobeSemaphore.Release()`. Pass the semaphore in via the Lobe
constructor (extend the existing arg list) so tests can inject a fake.

MUST: Document the pattern in `internal/cortex/lobes/llm/slot.go` (the
file that owns the slot abstraction per CLAUDE.md). Single-call lobes
keep runner-level gating; multi-call lobes use per-call gating.

MUST: Add a test for at least one multi-call Lobe asserting the
semaphore is acquired+released N times for N calls.

VERIFY: `go test ./internal/cortex/lobes/... -count=1 -timeout 60s`
passes.

Effort: M.

### TASK-7 — AntiTruncLobe Workspace integration test

File: `internal/cortex/lobes/antitrunc/integration_test.go` (NEW or
extend an existing one).

Audit ref: `audit/scan-governance-gaps.md` "anti-truncation (overall)
— AntiTruncLobe published Notes ARE NOT visible to cortex.PreEndTurnGate"
— that gap was closed in PR #196 by switching to the real
`cortex.Workspace`. This task adds the missing integration test that
proves the gap is actually closed.

MUST: Create or extend an integration test that:
  1. Constructs a real `cortex.Workspace` via `cortex.NewWorkspace(...)`.
  2. Constructs an `AntiTruncLobe` wired to that Workspace.
  3. Drives the Lobe with a history containing a known truncation
     phrase ("i'll stop here for now").
  4. Asserts the published Note appears in `Workspace.Snapshot()` AND
     that `cortex.Cortex.PreEndTurnGate` returns a non-empty block
     citing it.

MUST: Use the existing `cortex.NewFake(t)` test helper if one exists;
otherwise wire up a minimal Cortex with an AntiTrunc-enabled detector.

VERIFY: New test passes under `go test ./internal/cortex/lobes/antitrunc/...
-count=1 -v -run Integration`.

Effort: S.

### TASK-8 — Web slice file split (sessionsSlice/lanesSlice/messagesSlice)

File: `web/src/lib/store/daemonStore.ts` (the monolithic store) and
new files `web/src/lib/store/{sessionsSlice,lanesSlice,messagesSlice}.ts`.

Audit ref: `audit/scan-governance-gaps.md` web-chat-ui item 16 — "Spec
§Directory Layout lists sessionsSlice.ts, lanesSlice.ts, messagesSlice.ts.
All folded into the monolithic daemonStore.ts (works, but spec contract
not honored)."

MUST: Split the monolithic Zustand store into three slice files using
the standard Zustand slice pattern:
  - `sessionsSlice.ts`: state + actions related to sessions
    (`SessionsSlice` interface + `createSessionsSlice` factory)
  - `lanesSlice.ts`: lanes (lane buffers, pinning, tile mode)
  - `messagesSlice.ts`: chat messages, streaming state

MUST: `daemonStore.ts` becomes a thin composition root that imports
each slice and combines them via `create<DaemonStore>()(immer((set,
get, store) => ({...createSessionsSlice(set,get,store),
...createLanesSlice(...), ...createMessagesSlice(...)})))`. Public
hook signature stays the same so consumers don't break.

MUST: Update existing tests to keep passing. No behavior change.

VERIFY: `cd web && npm run typecheck && npm test` passes.

Effort: M.

### TASK-9 — Memory bus read_count increments on Recall

File: `internal/memory/membus/bus.go:569-632` (`Recall`) and the writer
goroutine at `:432-486` (`writerLoop`/`flushBatch`).

Audit ref: `audit/scan-go-stubs.md` row "internal/memory/membus/bus.go
Recall read_count increments routed through the writer (spec §5.4 step
3); the current slice leaves read_count at zero."

MUST: After Recall scans rows, dispatch the row IDs to the writer
goroutine via a new `incrementRequest{ids []int64; done chan error}`
message type. The writer batches both INSERT and UPDATE (`UPDATE
stoke_memory_bus SET read_count = read_count + 1 WHERE id IN (...)`)
in the same transaction.

MUST: Recall does NOT block on the increment outcome — the new request
is fire-and-forget per spec ("fire-and-forget per spec §5.4"). On
writer-channel-full, drop the increment (read_count stays slightly
low) rather than blocking the read path. Log a counter on the Bus
struct so operators can see drop rate.

MUST: Add a test: insert one memory, Recall it 3 times, verify
read_count == 3 in the underlying SQLite.

VERIFY: `go test ./internal/memory/membus/... -count=1 -v` passes.

Effort: M.

### TASK-10 — Update HANDOFF.md with merged-PR cascade summary

File: `plans/HANDOFF.md`.

MUST: Rewrite the HANDOFF.md to record:
  1. PRs #211–#229 merged this session (15+ PRs across audit cleanup)
  2. Dev → staging → main cascade complete (or in-progress at time of
     write)
  3. The remaining audit items split into TASK-1 through TASK-9 above
     so the next session can pick up cleanly.
  4. Anything still BLOCKED with the genuine reason.

Keep the existing structure (verified state, dispatch templates, etc.)
but refresh the merged-PR section.

VERIFY: HANDOFF.md reflects the actual state per `git log --oneline
origin/main` and `gh pr list --state merged --limit 20`.

Effort: S.

## Build order

All tasks are independent. Build order is just by task number; /build
can dispatch them in any order, including in parallel.

## Acceptance for the spec

Spec is `done` when:
- All TASK-1 through TASK-10 commits are in dev with their VERIFY
  steps green
- HANDOFF.md (TASK-10) reflects the new state
- No new audit findings introduced

## Out of scope

The following audit-flagged items are NOT in this spec because they
require larger architectural work and deserve their own spec:
- e2e shim rewrite (real WebDriver protocol) — `desktop/tests/e2e/`
- internal/executor/code.go direct task routing (Track B Task 19)
- Web pin drift (React 19→18.3, Vite 7→6, etc. per audit) — these
  changes carry breaking-API risk; defer to a dedicated dep-bump spec.
