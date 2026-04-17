# Session Status — resume point

Last updated: 2026-04-17 03:22 PDT

## Where we are (executive summary)

Building/testing a **build orchestrator** that can reliably produce working code without human intervention. Running **7 parallel variants** against the same SOW (Sentinel Web + Mobile, 55KB prose, pnpm+turbo monorepo) to compare architectures. Results captured in `monitor-log.md` via a 5-minute cron job.

The two real missions (not just "find the best way"):
1. **Mission 1** — *Salvage a native harness that doesn't need CC or Codex*. Use cheap models (MiniMax via LiteLLM) for everything, including review.
2. **Mission 2** — *Cheap worker + expensive reviewer*. MiniMax/LiteLLM does the bulk of writing; Anthropic/Codex/CC only used for review gates.

## Branch: `feat/smart-chat-mode`

Recent relevant commits (newest first):
- `edbd5ef` fix(provider/gemini): 32K output floor so thinking models can actually emit text (Gemini 3.x was silently returning empty — thinking tokens ate the whole budget before output text)
- `20738f2` feat(provider): native Gemini provider — text-only reviewer, no LiteLLM. `gemini://` URL scheme, uses `x-goog-api-key` header against `:generateContent` endpoint. `cmd/stoke/main.go:1331` routes it; reasoning-model propagation added in the existing type-assertion block.
- `ab3a296` fix(simple-loop): commit cadence = logical units of work, not time slices
- `70c711a` feat(simple-loop): builder continuations + tighter commit cadence (6 × 100-turn budget; detects SOW-completion text / stall)
- `cae3560` feat(engine): route build-gate through plan.Ecosystem registry (multi-language)
- `7a8b3a1` fix(engine): build-gate escape hatch — pnpm monorepos fell through to always-pass
- `31ed2e0` feat(simple-loop): Level-2 concurrent fix pipeline with worktree isolation
- `5af43ea` fix(simple-loop): strengthen parallel-worker prompt against stale reads
- `fde40b7` feat(simple-loop): enforce reviewer sign-off with iterate-until-clean fix loop
- `bbe51a4` feat(provider): worker-vs-reviewer mode for CC and Codex; simple-loop --reviewer

## Architecture decisions already made + committed

- `ClaudeCodeProvider.WorkerMode` field: reviewer mode uses `--print` text-only, worker uses `-p --dangerously-skip-permissions --output-format json --max-turns 100`. `NewClaudeCodeWorker` constructor. `cmd/stoke/main.go:2195` routes sow's native-runner CC path through the worker variant. Fixes the bug where CC-as-worker was writing text instead of files.
- `CodexProvider.WorkerMode` field: default reviewer uses `--sandbox read-only --skip-git-repo-check --output-last-message` + `--json`. Based on web-search findings (codex `--full-auto` and bypass-sandbox known to hang per openai/codex issues #7852, #15524). JSONL events are authoritative over exit codes (#15536).
- `simple-loop --reviewer=codex|cc-opus|cc-sonnet` flag routes reviews. `claudeReviewCall` is text-only (no tools) for CC-as-reviewer.
- `simple-loop --fix-mode=sequential|parallel|concurrent`:
  - sequential: one big CC call per fix round, iterate until reviewer approves (max 5 rounds).
  - parallel: `splitReviewIntoIssues` + goroutine pool of `fix-workers`.
  - concurrent: `fixOrchestrator` at `cmd/stoke/fix_orchestrator.go`. Per-flagged-commit git worktree at `<repo>.fixes/wt-N/` branch `fix-N`; CC fixes in isolation; re-reviewed; only merged to main on reviewer **approval** (smart trigger, not a timer).
- `internal/engine/native_runner.go` `PreEndTurnCheckFn` now calls `runEcosystemGate` first (uses `plan.Ecosystem` registry via new exported `plan.Ecosystems()` / `plan.EcosystemFor()` helpers). Fallback `detectBuildCommand` now covers C#/.NET, Java/Kotlin, Swift, Ruby, Elixir in addition to the original set. The old escape hatch — pnpm monorepos with no root tsconfig falling through to `npx tsc --noEmit || true` — is gone.

## Running instances (8 alive + 1 killed)

| V | PID | Repo | Worker | Reviewer | Purpose |
|---|---|---|---|---|---|
| A | 333361 | sentinel-build-verify | LiteLLM/MiniMax | — | Mission 1 baseline no-review, 2h10m elapsed (OLD binary) |
| M1c | 476901 | sentinel-cc-sonnet-opus | LiteLLM | LiteLLM | **Mission 1 complete** (pure-native) |
| M2x | 476902 | sentinel-simple-opus | LiteLLM | **Codex (GPT-5)** | Mission 2 gold — current compile leader |
| M2g | 572293 | sentinel-mm-cc-buildverify | LiteLLM | **Gemini 2.5 Pro (native, no LiteLLM)** | Mission 2 cheap-reviewer; `--reasoning-base-url gemini://` |
| M2g-3 | 572294 | sentinel-mm-gem3 | LiteLLM | **Gemini 3.1 Pro Preview (native)** | Mission 2 newest-cheap; same path |
| D1 | 518491 | sentinel-simple-loop | CC-sonnet | Codex | simple-loop SEQ fix + builder continuations |
| D2 | 518492 | sentinel-simple-sonnet | CC-sonnet | Codex | simple-loop CONC fix + builder continuations |
| MS | 518493 | sentinel-mm-simple | CC→LiteLLM (MiniMax) | Codex | simple-loop CC tool-use against MiniMax via env-redirect |

**KILLED**: B (sentinel-mm-cc-buildverify → slot reused by M2g) — `pnpm install` signal-killed, stuck in repair loop 1/3. Original "Mission 2 with CC reviewer". Same failure pattern as A: tsconfig gaps, build gate (pre-fix) didn't catch it. Spent $2.62 in 1h25m.

D1/D2/MS were kill+reset+relaunched at ~02:54 with builder continuations + logical-unit commit cadence. M2g launched 03:04, M2g-3 launched 03:08.

## Monitoring

- Cron job ID `631613eb` fires every 5 minutes (`*/5 * * * *`). Uses the monitor prompt the user sends. Appends snapshots to `/home/eric/repos/stoke/monitor-log.md`.
- Monitor output HAD a labeling bug: "Commits" column was actually `git ls-files | wc -l` (tracked files). Fixed in the 02:44 snapshot — uses `git rev-list --count HEAD` now.
- Compile tests cached at `/tmp/compile-*.log`. Most recent result: **M2x is the first variant with passing install+typecheck** (4 of 6 TS packages pass; ui-web has empty-scaffold TS18003; ui-mobile missing entirely).

## Findings / non-obvious state

- **Neither A nor B compile** despite 100-280 files on disk. Root cause: per-package `"typecheck": "tsc --noEmit"` scripts without per-package `tsconfig.json` files → tsc prints `--help` and exits 1. The pre-fix `detectBuildCommand` fell through to `npx tsc --noEmit || true` for pnpm-workspace-without-root-tsconfig and ALWAYS PASSED. This is now fixed (commits `7a8b3a1`, `cae3560`).
- **M2x produces real code**: Zod schemas with E.164 regex / UUID / datetime-with-offset, domain-correct enums (`DeviceKind: radar|environmental|hub` per SOW), 0 dangling imports, 1 TODO across 120 files. Not fast-and-broken.
- **Simple-loop's commit-watch review had a placebo bug**: "queuing fix for CC" was printed but nothing was queued. The final-review block almost never fired because `lastReviewedHead` was updated on each watch tick, short-circuiting the `currentHead != lastReviewedHead` check. Fixed in `fde40b7`.
- **Codex sizer/env-var classifier both failed during M2x startup** — "no last agent message; wrote empty content" — the 0-byte race the `--output-last-message` + retry tried to fix. Harmless (sessions proceeded at default size) but worth investigating.
- **MS (MiniMax via CC env-redirect) DOES work** — after fix with `ANTHROPIC_AUTH_TOKEN=Bearer <key>` + empty `ANTHROPIC_API_KEY` (copied from `./runclaude --litellm` env). At 03:00 MS has 40 files in 6 min (6.88/min), producing real TS. The prior "doesn't work" finding was a config bug, not an architectural one.
- **D2 (concurrent fix mode + continuations + logical-commit-unit prompt) is accelerating**: 77 files + 4 real commits in 6 min post-relaunch = 13.28 files/min, double earlier runs. The new commit-cadence prompt ("logical units, not time slices") appears to be producing faster + more-reviewable output.
- **M2x remains Mission 2 leader** — 41+ min in Session S1, 160 files, 5 substantive commits, ~4 files/min sustained. No AC gate hit yet; no build-gate fires yet.
- **Simple-loop continuations are firing correctly**: at 03:22 D1/D2/MS all on continuation 2. D2 dispatched 2 fix-workers (d:2 m:0 a:0). Continuation loop works end-to-end.
- **Architectural review finding (03:20 conversation)**: user flagged 3 concerns — continuation cap 6 is arbitrary (true, should be progress-signal-based); reviewer only reviews commits (true, but feature/milestone tiers likely overengineering before compliance gate lands); reviewer tool-calls (unnecessary — chunked scope already handles it). Compliance gate first, then progress-signal continuations, defer tier hierarchy.
- **Existing anti-slop infrastructure is RICH**: `plan.ExtractDeliverables` at `internal/plan/deliverable.go:121` already pulls "components (X,Y,Z)" / "including X,Y,Z" / "implement X,Y,Z" from SOW prose. `scanPlaceholderStubs` at `cmd/stoke/sow_spec_guard.go:169` catches 12 stub patterns. `checkSpecFaithfulness` at line 258 enforces declared-file existence + placeholder scan pre-AC. Zombie-task override at `sow_native.go:3618` flips "reviewer approved" → "incomplete" when zero files written. **The gap**: `ExtractDeliverables` output is injected per-task but never cross-checked against final repo at mission-end. SOW says "6 shared packages"; extractor knows; no final gate verifies all 6 exist as nontrivial implementations.
- **Gemini 3.x reasoning models silently return empty on short `maxOutputTokens`** — thinking tokens consume the whole budget before text output. Confirmed via smoke test (`stop=MAX_TOKENS tokens=in:8,out:0 text=""`). Fixed in `edbd5ef` with a 32 K floor. M2g/M2g3 relaunched at 03:15 via native `gemini://` path (no LiteLLM proxy) — clean cache, 0 min elapsed.
- **Killed extraneous LiteLLM instances** on ports 4000, 21621, 21622 (spawned during earlier Gemini-via-LiteLLM experiments). Only :4001 remains — the shared worker proxy used by A, M1c, M2x, M2g, M2g3.
- **The scaffold/slop/lies problem is now the explicit target** (user's framing): Cline/Aider/Copilot routinely ship LLM-generated scaffolds that pretend to satisfy the ask. We need gates that cannot be bypassed. Existing gates: existence ("claimed success wrote 0 files"), spec-faithfulness (pattern match on declared files), Ecosystem compile gate (new). Missing: **SOW-compliance gate** that walks the SOW prose for named deliverables and verifies each is nontrivially implemented. An audit subagent is running (in-flight) to map the existing infrastructure before we build on it.

## Open decisions / next moves

1. **Build the SOW-compliance gate** (TOP PRIORITY, anti-scaffold/anti-slop/anti-lie). Audit subagent is mapping existing infrastructure (existence guard, spec-faithfulness, AC gates, convergence audit, 17 personas). Design will extract SOW-declared deliverables, cross-check against repo with file-size floor + stub-signature detection + test presence, and block mission completion on any "scaffold/missing" verdict. Report due when subagent returns.
2. Port the `fixOrchestrator` pattern (worktree + commit-review + merge-on-approval) into the **sow command** so the harness gets commit-level review gates instead of declared-file-list review gates. User's architectural ask; deferred while we solve #1.
3. When M1c/M2x hit their first AC gate, check: does the new Ecosystem build-gate catch the missing-tsconfig pathology? Watch for "Build gate failed" in their logs.
4. Compare Gemini 2.5 Pro vs Gemini 3.1 Pro Preview as reviewers once M2g/M2g3 have produced comparable plan reviews (~10 min from relaunch).
5. User-asked improvements still open: stratified reviewer (cheap lint first, semantic only when lint-clean), review caching, trivial-task bypass, cheap-reviewer ladder.
6. Strategic question from user: *can native harness be made good enough to ship, or is CC the only viable backbone?* Current read — M2x's LiteLLM-worker + Codex-reviewer config is the most promising answer. M2g/M2g3 are the cheap-reviewer stress tests for this thesis.

## Non-obvious resume instructions

- Use the cron monitor for state — don't rebuild from scratch. Monitor log `monitor-log.md` has full history with timestamps.
- Don't `cd` to parent dirs when launching stoke — there's a hook `/home/eric/repos/stoke/.claude/hooks/guard-bash-writes.sh` that blocks it. Use absolute paths + `pnpm --dir <abs>` instead of `cd <abs> && pnpm`.
- Stoke binary: `/home/eric/repos/stoke/stoke`. Rebuild: `go build -o stoke ./cmd/stoke` (always rebuild before relaunching variants when code changes).
- When resetting a sentinel repo: preserve `SOW_WEB_MOBILE.md` BEFORE `git clean -fdx` — it's untracked, gets nuked. Canonical SOW saved at `/tmp/sow-canonical.md`.
- The harness variants (sow) store state at `<repo>/.stoke/sow-state.json`. Resume-able via `stoke resume` if needed, but `--fresh --force` skips it.
- Old binaries of variants A and B are running — they don't have the Ecosystem build-gate fix. Kill+relaunch them to get the fix, OR leave them to demonstrate the old failure mode.
