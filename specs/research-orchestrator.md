<!-- STATUS: done -->
<!-- BUILD_COMPLETED: 2026-04-22 -->
<!-- CREATED: 2026-04-21 -->
<!-- DEPENDS_ON: executor-foundation (Task 19), browser-research-executors Part 1 (Task 21 browser), provider-pool (for picking lead vs subagent models), operator-ux-memory Part D (memory) -->
<!-- BUILD_ORDER: 16 -->

# Research Orchestrator — MVP→Orchestrator-Worker Upgrade

## Overview

Stoke shipped a single-agent research MVP in Task 20: `internal/research/` holds the `Planner` + `HeuristicDecomposer`, `Fetcher`, `VerifyClaim` primitives, and `internal/executor/research.go` wires them into the `Executor` interface. That MVP runs a deterministic pipeline — heuristic query decomposition, one fetch per candidate URL, keyword-overlap sentence ranking, template-based synthesis, and keyword+phrase claim verification. It satisfies the descent engine's `BuildCriteria` contract one-AC-per-claim and is stdlib-only. This spec upgrades that MVP to Anthropic's **orchestrator-worker pattern** (RT-07 §1) so complex queries get parallel fan-out and better synthesis, while the single-agent path remains the default for air-gapped / low-budget / test contexts.

The new shape: a **Lead agent** (Opus by default via `provider.Pool.BuildProviderByRole("reasoning")`) decomposes the query into N `SubObjective`s, writes `plan.md` to `<repo>/.stoke/research/<run-id>/`, then spawns N **Subagents** (Sonnet by default via `BuildProviderByRole("worker")`) each running an independent search+fetch+extract loop. Subagents write `findings.md` + `sources.jsonl` to their own directory and return the *reference path* to the Lead (filesystem-as-communication, RT-07 §1.5). The Lead reads the findings files and synthesizes a final `synthesis.md` with inline `[^N]` citation markers. Claim extraction → `claims.jsonl`; verification runs the existing `research.VerifyClaim` per claim unchanged. BuildCriteria still emits one `VerifyFunc`-backed `AcceptanceCriterion` per claim, so the descent 8-tier ladder operates identically. The orchestrator is flag-gated (`Config.UseOrchestrator` + `STOKE_RESEARCH_ORCHESTRATOR=1`); when off, Execute takes the existing single-agent path byte-for-byte.

## What stays unchanged

- **`research.Report`, `research.Claim`, `research.Source`, `research.SubQuestion`, `research.SubQuestionAnswer`** — no struct changes. Orchestrator writes into the same types.
- **`executor.ResearchDeliverable`** — same shape returned from `Execute`. Callers that type-assert on it keep working.
- **`ResearchExecutor.BuildCriteria`** — still emits one `AcceptanceCriterion{ID: claim.ID, VerifyFunc: ...}` per claim. Orchestrator produces claims into the same list; descent sees no difference.
- **`research.VerifyClaim`** (`internal/research/verify.go`) — unchanged. The 4-stage LLM verifier from `browser-research-executors.md` §2.6 is explicitly out of scope for this spec.
- **`research.HTTPFetcher`** — unchanged; its allowlist env var `STOKE_RESEARCH_ALLOWLIST` is reused as-is.
- **`research.HeuristicDecomposer`** — kept as the fallback decomposer when no LLM provider is wired or when `UseOrchestrator=false`.
- **`BuildEnvFixFunc`** — kept; transient-error classification unchanged. The orchestrator's per-subagent retries layer on top, not inside.
- **`DeterministicSynthesize`** — kept as the single-agent path's synthesizer and as the fallback when the Lead provider call fails.
- **CLI surface** — `stoke research "query"` continues to work with existing flags. No new flags in this spec (the `--effort` / `--run-id` / `--output` flags are owned by `browser-research-executors.md` Part 3 once that lands).

Backward compatibility: **every existing research test passes unchanged**. New tests cover only the orchestrator path.

## What changes

### New files

- **`internal/research/orchestrator.go`** — Lead agent's planning call + synthesis call. Exposes `Orchestrator` struct with `LeadProvider provider.Provider`, `SubProvider provider.Provider`, `MaxParallel int` (default 5 per RT-07 §7 open question 3), `RunRoot string`, `Clock func() time.Time`. Public method: `Run(ctx, query, effort) (research.Report, error)`.
- **`internal/research/subagent.go`** — Per-subagent search loop. `SubagentRunner` struct carrying `Provider provider.Provider`, `Fetcher Fetcher`, `Search SearchFunc` (optional), `ToolCallBudget int`. Public method: `Run(ctx, obj SubObjective, outDir string) (Findings, error)`.
- **`internal/research/decomposer_llm.go`** — `LLMDecomposer` implementing `Decomposer`; calls `LeadProvider` with a deterministic prompt and parses a JSON response into `[]SubQuestion`. Falls through to `HeuristicDecomposer.Decompose` on provider error or parse failure.
- **`internal/research/fetcher_browser.go`** — `BrowserFetcher` wrapping `internal/browser.Pool` so subagents can fetch JS-rendered pages once Task 21 Part 2 lands. Falls through to an embedded `HTTPFetcher` when the browser pool is nil or Chromium is unavailable. This spec creates the wrapper; it is nil-safe and a no-op until `internal/browser` ships.
- **`internal/research/subobjective.go`** — `SubObjective` struct (4-field template from RT-07 §1.4: Objective, OutputFormat, ToolGuidance, TaskBoundaries) + `renderSubagentPrompt(obj) string`.
- **`internal/research/synthesis.go`** — `LLMSynthesize` implementing the `Synthesize func` signature already present on `ResearchExecutor`; reads sub-agent `findings.md` files and calls `LeadProvider` for final narrative. Returns a string.

### Modified files

- **`internal/executor/research.go`**
  - Add `Config` struct field: `UseOrchestrator bool`, `RunRoot string` (default `.stoke/research/<ulid>`), `LeadProvider provider.Provider`, `SubProvider provider.Provider`, `MaxParallel int`.
  - `Execute` branches at the top: `if e.Config.UseOrchestrator { return e.executeOrchestrator(...) } else { <existing body> }`.
  - Add `executeOrchestrator(ctx, plan, effort)` — constructs `research.Orchestrator`, runs it, adapts the result into `ResearchDeliverable`.
  - Env-var bridge: when `STOKE_RESEARCH_ORCHESTRATOR=1` is set and `UseOrchestrator` is zero-value, auto-flip to `true` at `NewResearchExecutor`-time.
- **`internal/research/research.go`**
  - Planner keeps current shape; add `NewPlannerWithDecomposer(d Decomposer) *Planner` constructor so the orchestrator can inject `LLMDecomposer`.
  - No changes to `SubQuestion` or `HeuristicDecomposer`.

### Non-goals for this spec

- The 4-stage LLM judge verifier (browser-research-executors §2.6). `VerifyClaim` stays keyword-overlap-based.
- The CiteAudit / vecindex-based claim verification pipeline.
- Memory integration: subagents do not *write* to `internal/memory/`. They may *read* from memory via a future hook but no read path is added here.
- Provider-pool lookup design — we consume the existing `provider.Pool.BuildProviderByRole` interface.
- Browser backend implementation — `BrowserFetcher` is a thin adapter over a pool that another spec owns.
- The `stoke research` cobra subcommand flags (`--effort`, `--run-id`, `--output`) — those arrive with the full research CLI spec.
- Ledger integration (`ResearchPlan`, `SubagentRun` nodes). Out of scope; covered in RT-07 §6.1 and the full research spec.

## Implementation checklist

Each item is self-contained. File paths are absolute. Function signatures and the existing files to read for patterns are listed.

1. [ ] **Add `Config` struct to `internal/executor/research.go`.** New type `ResearchConfig { UseOrchestrator bool; RunRoot string; LeadProvider provider.Provider; SubProvider provider.Provider; MaxParallel int }`. Add as field `Config ResearchConfig` on `ResearchExecutor`. Read `internal/executor/deploy.go` for the pattern on per-executor config. Default `MaxParallel=5`. Test: `NewResearchExecutor(f)` returns `ResearchExecutor{Config: {UseOrchestrator: false, MaxParallel: 5}}`. Test file: extend `internal/executor/research_test.go`.

2. [ ] **Wire `STOKE_RESEARCH_ORCHESTRATOR=1` env bridge.** In `NewResearchExecutor`, after struct init, `if os.Getenv("STOKE_RESEARCH_ORCHESTRATOR") == "1" { r.Config.UseOrchestrator = true }`. Follow the pattern from `research.NewHTTPFetcher` which reads `STOKE_RESEARCH_ALLOWLIST`. Test with `t.Setenv` toggling on/off; assert `Config.UseOrchestrator` flips accordingly.

3. [ ] **Branch `Execute` on the flag.** Top of `Execute` in `internal/executor/research.go`: `if e.Config.UseOrchestrator { return e.executeOrchestrator(ctx, p, effort) }`. Existing single-agent body becomes the `else` branch unchanged. Test: backward-compat — run existing `StubFetcher`-based test against `UseOrchestrator=false`, assert byte-identical deliverable.

4. [ ] **Stub `executeOrchestrator` returning `ErrOrchestratorDisabled` when LeadProvider is nil.** Signature: `func (e *ResearchExecutor) executeOrchestrator(ctx context.Context, p Plan, effort EffortLevel) (Deliverable, error)`. Body for this item: if `e.Config.LeadProvider == nil`, fall back to the single-agent body (call the existing synchronous pipeline). Test: orchestrator flag on but no provider → behaves identically to single-agent. Defensive fallback means the flag is safe to enable by default later.

5. [ ] **Create `internal/research/subobjective.go` with `SubObjective` struct.** Fields: `Objective string`, `OutputFormat string`, `ToolGuidance string`, `TaskBoundaries string`. Add JSON tags. Add `Validate() error` that rejects empty `Objective`. Test: roundtrip JSON marshal + `Validate` rejects the empty case. Read `internal/research/research.go` for struct style (lower-case field comments, JSON tags).

6. [ ] **Implement `renderSubagentPrompt(obj SubObjective) string` in `subobjective.go`.** Produces the exact 4-section prompt from RT-07 §1.4. Use a `text/template` template with `{{.Objective}}`, `{{.OutputFormat}}`, `{{.ToolGuidance}}`, `{{.TaskBoundaries}}`. Deterministic output. Test: golden-string comparison for a fixed `SubObjective`.

7. [ ] **Create `internal/research/decomposer_llm.go` with `LLMDecomposer` struct.** Fields: `Provider provider.Provider`, `Timeout time.Duration` (default 30s), `Fallback Decomposer` (default `HeuristicDecomposer{}`). Signature: `func (l *LLMDecomposer) Decompose(query string) []SubQuestion` — same as the existing `Decomposer` interface. Read `internal/research/research.go` for the interface contract and fallback style. Deterministic prompt with `temperature=0`; JSON response schema `{"subquestions": [{"id","text","hints":[]}]}`.

8. [ ] **Parse JSON response in `LLMDecomposer`.** Use `internal/jsonutil` (tolerant parser) to accept minor formatting variance. On parse failure, return `l.Fallback.Decompose(query)`. Build on the pattern from `internal/executor/research.go` — prefer fallback over error propagation since `Decompose` returns a slice not an error. Test: mock Provider returning malformed JSON → returns Heuristic result.

9. [ ] **Add `provider.Provider` mock for tests.** If one already exists in `internal/provider/` (check for a `mockProvider`), reuse it; otherwise create `internal/research/provider_mock_test.go` with `mockProvider` implementing the minimal `Complete(ctx, req)` method (or whichever method the LLMDecomposer calls). Read `internal/provider/anthropic.go` to confirm the interface shape. The mock records the prompt it saw and returns a canned `Response`. Use this mock for all orchestrator tests.

10. [ ] **Add `NewPlannerWithDecomposer(d Decomposer) *Planner` to `internal/research/research.go`.** One-line constructor; used by Orchestrator. Test: returns planner whose `Plan` call delegates to the provided decomposer.

11. [ ] **Create `internal/research/orchestrator.go` skeleton.** Struct: `Orchestrator { LeadProvider provider.Provider; SubProvider provider.Provider; Fetcher Fetcher; Planner *Planner; MaxParallel int; RunRoot string; Clock func() time.Time }`. Constructor `NewOrchestrator(lead, sub provider.Provider, f Fetcher) *Orchestrator` wiring defaults (`MaxParallel=5`, `Clock=time.Now`, `Planner=NewPlannerWithDecomposer(&LLMDecomposer{Provider:lead, Fallback:HeuristicDecomposer{}})`). Test: constructor produces non-zero-value orchestrator with sensible defaults.

12. [ ] **Implement `Orchestrator.Run(ctx, query, effort) (Report, error)`.** Read `internal/executor/research.go` Execute body for the claim-collection pattern. Flow: (a) `mkdir -p RunRoot`, (b) planner decomposes query, (c) write `plan.md` (list of SubObjectives derived from SubQuestions), (d) fan-out subagents via `errgroup.WithContext(ctx)` with `SetLimit(MaxParallel)`, (e) collect findings, (f) synthesize, (g) return Report. Errors from individual subagents do not fail the whole run — they are logged and the claim count is reduced. Test covered in items 25-28.

13. [ ] **Create subagent directory writer in `orchestrator.go`.** Helper `writeSubagentDir(root string, idx int, obj SubObjective, f Findings) error` — creates `<root>/subagent-<idx>/` with `objective.md`, `findings.md`, `sources.jsonl` (one JSON object per line: `{url, title, fetched_at}`). Use `filepath.Join` and `os.MkdirAll(dir, 0o755)`. Read `internal/research/store.go` for the existing file-writing style in this package. Test: writing + roundtrip-reading the three files yields identical content.

14. [ ] **Create `internal/research/subagent.go` with `SubagentRunner` struct.** Fields: `Provider provider.Provider`, `Fetcher Fetcher`, `ToolCallBudget int` (default 10 for EffortMinimal, 15 for EffortStandard, 20 for EffortThorough — table driven). The MVP subagent loop does: (1) render prompt with `renderSubagentPrompt`, (2) call `Provider.Complete` asking for `{"urls":[...], "extract_queries":[...]}`, (3) for each URL `Fetcher.Fetch`, extract top sentences per `extract_queries` via `topSentences` (reuse from `executor/research.go`). No tool loop in this MVP; sequential structured-output call suffices. Signature: `func (r *SubagentRunner) Run(ctx, obj SubObjective) (Findings, error)`. Test: mock provider + StubFetcher → produces expected Findings.

15. [ ] **Define `Findings` struct in `subagent.go`.** Fields: `Summary string` (subagent's writeup for the Lead), `Sources []Source`, `Sentences []string` (claim-candidates), `ToolCalls int`. JSON tags. Test: roundtrip marshal.

16. [ ] **Implement subagent fan-out in `Orchestrator.Run` using `errgroup`.** Read `internal/bench/bench.go` or similar for existing errgroup patterns if present; otherwise `golang.org/x/sync/errgroup` is already vendored (check `go.mod`). Use `g.SetLimit(r.MaxParallel)`. Each subagent run produces a `Findings` value written into a `[]Findings` indexed by subagent index (NOT append, to preserve order). Test: 5 subagents with different StubFetcher URLs → 5 distinct Findings in deterministic order.

17. [ ] **Bound subagent count by effort.** EffortMinimal → 1, EffortStandard → min(4, len(subqs)), EffortThorough → min(MaxParallel, len(subqs)), EffortCritical → same as Thorough. Per RT-07 §1.3. If decomposer returns more SubQuestions than the effort cap allows, group extras into the last subagent's `TaskBoundaries`. Test: effort=Minimal with 4 subqs → 1 subagent containing all 4 as merged scope.

18. [ ] **Implement `Orchestrator.synthesize(ctx, query, findings, subobjs) string`.** Reads each `subagent-<i>/findings.md` from disk (not from in-memory — enforces the filesystem-as-communication contract of RT-07 §1.5), calls `LeadProvider.Complete` with a synthesis prompt (`temperature=0`, JSON response not required — freeform markdown), returns the body. On provider error, fall back to `DeterministicSynthesize` (already exported from `internal/executor/research.go` — move it into the `research` package OR re-export from executor; choose the cheaper refactor). Test: mock Lead → expected body; mock Lead returning error → deterministic synthesis used.

19. [ ] **Relocate `DeterministicSynthesize` OR export the signature from `internal/research`.** Check reverse: move the function from `internal/executor/research.go` into a new `internal/research/synthesize.go`; update the existing call site (`Synthesize: DeterministicSynthesize`) to `Synthesize: research.DeterministicSynthesize`. Keeps `internal/research` self-contained so Orchestrator can import it without a cycle. Test: unchanged executor tests still pass.

20. [ ] **Implement claim extraction from findings in `orchestrator.go`.** Helper `claimsFromFindings(findings []Findings) []Claim` — iterates `Sentences`, assigns stable IDs (`C-1`, `C-2`, ...), pairs each with the Source URL from the same subagent. Mirrors the MVP's per-sub-question claim loop (see `internal/executor/research.go` lines 173-185). Test: 3 findings × 2 sentences each → 6 Claims with IDs C-1..C-6.

21. [ ] **Implement `executeOrchestrator` in `internal/executor/research.go`.** Body: (a) resolve RunRoot (default `filepath.Join(".", ".stoke", "research", ulid.Make())`), (b) construct `research.NewOrchestrator(e.Config.LeadProvider, e.Config.SubProvider, e.Fetcher)`, (c) override defaults from `e.Config` (MaxParallel, RunRoot), (d) call `orc.Run(ctx, p.Query, effort)`, (e) wrap the returned Report in a `ResearchDeliverable{Report: rep, Sources: rep.Sources}`. On orchestrator error, fall back to single-agent path. Test: integration covered in item 26.

22. [ ] **Create `internal/research/fetcher_browser.go` with `BrowserFetcher` wrapper.** Struct: `BrowserFetcher { Pool browser.Pool; Fallback Fetcher }`. If `browser` package is not importable yet (pre-Task-21-Part-2), put the type behind a build tag `//go:build !nobrowser` and provide a stub `BrowserFetcher` in a `//go:build nobrowser` file that simply embeds `Fallback`. Signature: `func (b *BrowserFetcher) Fetch(ctx, url) (string, error)` — when Pool nil or Acquire fails, delegate to Fallback. Test: Pool nil → falls through to Stub fetch. Read `internal/research/fetch.go` for the Fetcher interface and the production HTTPFetcher implementation style.

23. [ ] **Wire `BrowserFetcher` into `NewResearchExecutor` opt-in.** Add `WithBrowserPool(p browser.Pool) *ResearchExecutor` builder method (when the `browser` import is available). When set, wraps `e.Fetcher` in `BrowserFetcher{Pool: p, Fallback: e.Fetcher}`. Test: with nil pool → returns original Fetcher unchanged. This item is nil-safe even if `internal/browser` hasn't been finished yet.

24. [ ] **Emit minimal orchestrator events via `internal/bus`.** If `internal/bus` is available in this build, publish: `research.plan.ready` (after decomposition), `research.subagent.started` (per subagent), `research.subagent.completed` (per subagent), `research.synthesis.ready` (after synthesize), `research.completed` (after Run returns). No new event types — reuse the string-keyed bus publish shape from `internal/bus/bus.go`. If the events package is not easily accessible from `internal/research`, publish from `executeOrchestrator` instead. Test: count published events in a spy bus.

25. [ ] **Write unit test for LLMDecomposer.** File: `internal/research/decomposer_llm_test.go`. Cases: (a) mock provider returns valid JSON → parsed SubQuestions; (b) malformed JSON → falls back to Heuristic; (c) provider returns error → falls back; (d) context cancelled → returns fallback. ≥4 subtests.

26. [ ] **Write fan-out integration test.** File: `internal/research/orchestrator_test.go`. Three subagents, each pointed at a different URL via StubFetcher `Pages: {"https://a":"body a", "https://b":"body b", "https://c":"body c"}`. Mock Lead returns a 3-element decomposition where each SubQuestion's Hints drive a different URL. Assert: 3 subagent directories created, 3 `findings.md` files, ordered `[]Findings` length 3, claim count ≥ 3.

27. [ ] **Write backward-compat test.** File: `internal/executor/research_test.go` new subtest `TestExecute_SingleAgent_Backcompat`. With `Config.UseOrchestrator=false`, run identical MVP scenario → assert deliverable is byte-identical to pre-upgrade snapshot (use a golden file `testdata/single_agent_report.golden.json`).

28. [ ] **Write orchestrator integration test.** File: `internal/executor/research_test.go` new subtest `TestExecute_Orchestrator_Enabled`. With `Config.UseOrchestrator=true`, mock Lead + Sub providers, StubFetcher. Assert: `.stoke/research/<id>/plan.md` exists, `subagent-1/findings.md` exists, `synthesis.md` non-empty, deliverable has ≥1 Claim, `BuildCriteria(t, d)` returns one AC per claim whose `VerifyFunc` runs against the StubFetcher successfully.

29. [ ] **Write env-var gating test.** File: `internal/executor/research_test.go`. `t.Setenv("STOKE_RESEARCH_ORCHESTRATOR", "1")`; construct executor; assert `Config.UseOrchestrator == true`. Then unset; assert false.

30. [ ] **Write prompt-rendering golden test.** File: `internal/research/subobjective_test.go`. Fixed `SubObjective` fixture → compare against golden string literal containing all 4 sections. Catches accidental prompt drift.

31. [ ] **Write MaxParallel bound test.** File: `internal/research/orchestrator_test.go`. `MaxParallel=1` with 3 SubQuestions → all subagents run but serially (instrument via mock provider that records start-times; assert they never overlap). Deterministic even under concurrency.

32. [ ] **Write subagent failure isolation test.** File: `internal/research/orchestrator_test.go`. One subagent's provider returns error; other two succeed. Assert: Run returns Report with 2 sub-agent results, error-isolated subagent logged but not propagated. Matches the "subagents never fail the whole run" principle from RT-07 §1.

33. [ ] **Write effort-ladder test.** File: `internal/research/orchestrator_test.go`. Effort=Minimal → 1 subagent regardless of SubQuestion count. Effort=Thorough with MaxParallel=5 and 10 SubQuestions → 5 subagents with grouped scopes. Covers §1.3 scaling.

34. [ ] **Update `STATUS.md` to note the orchestrator ships flag-gated.** Append a one-liner under current status: `Research orchestrator (multi-agent fan-out) available via STOKE_RESEARCH_ORCHESTRATOR=1; default off pending real-world validation.` Read the existing STATUS.md format first.

35. [ ] **Run the full AC block (see Acceptance Criteria) locally.** Fix any reds.

## Acceptance criteria

```bash
# Build & vet
go build ./cmd/stoke
go vet ./...

# Unit tests
go test ./internal/research/... -run TestLLMDecomposer
go test ./internal/research/... -run TestOrchestrator
go test ./internal/research/... -run TestSubagentRunner
go test ./internal/research/... -run TestRenderSubagentPrompt
go test ./internal/research/... -run TestBrowserFetcher

# Executor tests
go test ./internal/executor/... -run TestResearch
go test ./internal/executor/... -run TestExecute_SingleAgent_Backcompat
go test ./internal/executor/... -run TestExecute_Orchestrator_Enabled

# Backward compat: the full pre-upgrade test suite must still pass unchanged.
go test ./internal/executor/... -run TestResearchExecutor_Existing

# Fan-out assertion: 3 subagents running in parallel against different URLs.
go test ./internal/research/... -run TestOrchestrator_FanOut_Parallel -race

# Race: ensure no data races in the fan-out path.
go test ./internal/research/... -race

# Env bridge
STOKE_RESEARCH_ORCHESTRATOR=1 go test ./internal/executor/... -run TestExecute_EnvFlag

# Full CI gate
go test ./...
```

All commands exit 0. No test skipped. No race reports.

## Rollout

**Phase 1 — Lands dark (this spec):**
- `Config.UseOrchestrator = false` by default.
- Env var `STOKE_RESEARCH_ORCHESTRATOR=1` flips the flag per-run for opt-in testing.
- `NewResearchExecutor` still constructs with a StubFetcher-compatible default; no provider required for single-agent mode.
- CI runs BOTH paths in the integration test matrix.

**Phase 2 — Real-world validation (out-of-spec trigger):**
- One SOW-style use case runs end-to-end with `STOKE_RESEARCH_ORCHESTRATOR=1` against live providers. Compare cost, latency, and claim count vs single-agent baseline.
- If the verified-claim count increases by ≥1.5x at ≤3x cost (matching RT-07 §1.7's ~15x token ceiling at ≥90% quality lift), proceed to Phase 3.

**Phase 3 — Flip default on:**
- In a follow-up PR: change `Config.UseOrchestrator` default to `true`. Add `STOKE_RESEARCH_ORCHESTRATOR=0` to force single-agent. Update `STATUS.md`.
- Single-agent path stays as the fallback; never removed. It remains the only path that works with zero providers / fully air-gapped.

Rollback path: set `STOKE_RESEARCH_ORCHESTRATOR=0` or revert the default-flip commit. No data-format changes make revert destructive.

## Testing

| File | Coverage |
|---|---|
| `internal/research/decomposer_llm_test.go` | LLMDecomposer parse success, parse failure fallback, provider error fallback, ctx cancellation. |
| `internal/research/orchestrator_test.go` | Happy path (3 subagents, 3 URLs, 3+ claims). Fan-out determinism (ordered findings). Subagent failure isolation. MaxParallel=1 serialization. Effort-ladder sizing. Fallback to DeterministicSynthesize on Lead error. Directory layout assertion (`plan.md`, `subagent-N/*`, `synthesis.md`). |
| `internal/research/subagent_test.go` | Happy path (mock provider + StubFetcher). Tool-call budget enforced. Empty URL list returns empty Findings. |
| `internal/research/subobjective_test.go` | `Validate()` rejects empty Objective. `renderSubagentPrompt` golden-string match. |
| `internal/research/fetcher_browser_test.go` | Nil pool falls through to Fallback. Acquire failure falls through. Successful acquire uses the browser path. |
| `internal/executor/research_test.go` | `TestExecute_SingleAgent_Backcompat` (golden file). `TestExecute_Orchestrator_Enabled` (full path). `TestExecute_EnvFlag` (env bridge). `TestBuildCriteria_OrchestratorClaims` (AC shape unchanged). |

Mock strategy: a single `mockProvider` type in `internal/research/provider_mock_test.go` is shared across all research-package tests. Canned responses are table-driven; each test names the response it expects. No network calls.

---

## Open questions (answered or parked)

None blocking. RT-07 §7 open question 1 (plan-gating through CTO stance) and 2 (merge CitationAgent into verify) are parked — the first is a concern/stance-integration concern out of this spec's scope; the second is covered by the 4-stage verifier in `browser-research-executors.md` §2.6 and explicitly deferred here.
