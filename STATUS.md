# Session Status — resume point

Last updated: 2026-04-17 04:46 PDT

## Where we are (executive summary)

Building/testing a **build orchestrator** that can reliably produce working code without human intervention. Running **7 parallel variants** against the same SOW (Sentinel Web + Mobile, 55KB prose, pnpm+turbo monorepo) to compare architectures. Results captured in `monitor-log.md` via a 5-minute cron job.

The two real missions (not just "find the best way"):
1. **Mission 1** — *Salvage a native harness that doesn't need CC or Codex*. Use cheap models (MiniMax via LiteLLM) for everything, including review.
2. **Mission 2** — *Cheap worker + expensive reviewer*. MiniMax/LiteLLM does the bulk of writing; Anthropic/Codex/CC only used for review gates.

## Branch: `feat/smart-chat-mode`

Recent relevant commits (newest first):
- `4fc5c19` feat: **iterative SOW compliance gate + progress-signal continuations**. (1) `internal/plan/compliance.go` — deterministic walk of SOW's named deliverables, classify each as nontrivial/stub/missing; (2) `cmd/stoke/main.go` — mission-end iterative repair loop, dispatches repair sessions via `ss.AppendSession` until compliance passes or stalls; (3) `cmd/stoke/simple_loop.go` — continuations are now progress-signal bounded (2 consecutive zero-commit continuations = stop), not hard-capped at 6. Absolute cap 40.
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
| M2g | 603532 | sentinel-mm-cc-buildverify | LiteLLM | **Gemini 2.5 Pro (native)** | Mission 2 cheap-reviewer. **ON NEW BINARY** — compliance gate active. |
| M2g-3 | 603533 | sentinel-mm-gem3 | LiteLLM | **Gemini 3.1 Pro Preview (native)** | Mission 2 newest-cheap. **ON NEW BINARY** — compliance gate active. |
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
- **Anti-slop compliance gate IS NOW SHIPPED** (commit `4fc5c19`). The previously-identified gap — `ExtractDeliverables` computed per-task but never cross-checked against final repo at mission-end — is closed. New `plan.RunSOWCompliance` walks every deliverable named in SOW prose, classifies each as nontrivial/stub/missing via filename + content-definition match + 80-byte + body-line threshold. Gate fires at mission-end in `main.go` after `ss.Run`; on fail, builds repair session and `ss.AppendSession`-es it, then re-runs the scheduler. Loops up to 5 rounds; stalls when same-missing-set repeats. **M2g/M2g3 are the first runs on this gate** — both fresh at ~1m, will fire it at first mission-end.
- **D1 and D2 reached "build phase complete"** at 35 min mark. D2 has 20 real commits + 3 fix-workers dispatched concurrently. Both entering Step 4b (iterate-until-clean review loop).
- **A finally escaped S1 → Session S2 at 2h33m** (298 files). First forward session progress for A in 2.5h. M1c still stuck at 83 files / 1 commit / Session S1 at 1h elapsed — LiteLLM-only reviewer isn't pushing the worker forward. Both on OLD binary, no compliance gate.
- **M2x at 230 files at 1h05m** — still Session S1; Codex reviewer iterating per-task. Mission-2 leader by file count.
- **MS finished round 1 self-audit and entered ROUND 2** (03:53, 59m elapsed, 374 files / 3 commits at round-1 boundary). Round 1 Step 8 audit produced no additional commits — worker batched heavily. Now re-reviewing the plan for round 2 via codex.
- **M1c file count dropped 340→293** between 03:48 and 03:53 — worker is deleting + rewriting without committing. Pure-native reviewer not gating commits. Confirms rubber-stamp hypothesis: Mission-1 (LiteLLM reviewing LiteLLM) isn't catching worker's churn.
- **M1c exploded 93→340 files in 5 min** (pure-native LiteLLM+LiteLLM, previously stuck for 50 min). Commits still stuck at 1 → reviewer may be rubber-stamping file volume without enforcing commit boundaries. This is exactly the Mission-1 risk we worried about.
- **D1 gained +5 commits in the Step 4b fix loop** (10→15) — iterate-until-clean is actually landing fix commits. D2 stuck at 22 commits + 3 fix-workers dispatched; D2 log stale 11m suggests fix-workers still running.
- **D1 recovered from its stale period** — the 13-min stall at 03:39 resolved; log active again, CC fix call completed without hang.
- **Gemini 3.1 Pro may be rubber-stamping past sessions** — at 1h03m M2g3 jumped Session S1 → **S6** with ONLY 84 files / 24 TS (2.4× FEWER files than M2g at S3 with 200/63). Looks like 3.1 Pro accepts sessions without enforcing full deliverable completion. 2.5 Pro (M2g) is slower-advancing per session but producing proportionally more code per session. **2.5 Pro wins on quality-per-session if compliance gate confirms**; 3.1 Pro may be the "faster progression / emptier repo" trap.
- **Churn signal split on recovery**: M2x holding at 258 files / 82 TS (Codex reviewer pushed it back to productive work after 243→218 drop). **M1c also rebounded at 04:17** — 244 → 306 files, 51 → 57 TS — so the pure-native rubber-stamp eventually re-climbed after churn, but still at 1 commit = all new work is uncommitted scaffold. Mission 2 self-corrects via Codex; Mission 1 re-climbs but without commit hygiene.
- **Step 4b fix loop is converging aggressively**: D1 → 31 commits, D2 → 37 commits (both +6/+9 in last window). Iterate-until-clean is landing real fix commits; not stuck.
- **Session-boundary progress at the ~1:50 mark**: M2x escaped Session S1 → S3 (took 1h53m of S1 work). M1c advanced S1 → S2. Sow-harness sessions eventually clear but slowly — reviewer intensity matters less than real SOW depth.
- **MS crossed 500 files** (509) at 1h37m — only run past that milestone. At 1h42m it finished round-2 build phase; entering post-build review. Most advanced simple-loop run.
- **A stale 23 min at 3h45m** — past the 24m kill threshold. Locked at 322 files / 29 TS / Session S2 since 03:30. No hang markers; likely looping on pnpm-install AC gate. Recommend killing; zero progress possible without the Ecosystem build-gate fix that A predates.
- **D2 unstuck** — was stale 15m last cycle, now active again (2m). D1 kept producing (+3 commits, now 40); D2 still 37. Both still in build-complete iterate-until-clean loop.
- **M2g followed M2g3 into Session S6** at 1h17m. M2g 289/86 TS, M2g3 157/37 TS. Both Gemini reviewers now at the same session; 2.5 Pro still has 2× the file/TS output per session.
- **Gemini variants both accelerated past 1h mark**: M2g +73 files (206→279), M2g3 +67 files (91→158). TS: M2g 66→83, M2g3 25→37. 2.5 Pro still leading on TS but both reviewers now actively pushing real code.
- **M2x crossed 100 TS files** (101) at 2h03m in Session S3 — first run to hit that milestone.
- **Gemini 3.x reasoning models silently return empty on short `maxOutputTokens`** — thinking tokens consume the whole budget before text output. Confirmed via smoke test (`stop=MAX_TOKENS tokens=in:8,out:0 text=""`). Fixed in `edbd5ef` with a 32 K floor. M2g/M2g3 relaunched at 03:15 via native `gemini://` path (no LiteLLM proxy) — clean cache, 0 min elapsed.
- **Killed extraneous LiteLLM instances** on ports 4000, 21621, 21622 (spawned during earlier Gemini-via-LiteLLM experiments). Only :4001 remains — the shared worker proxy used by A, M1c, M2x, M2g, M2g3.
- **The scaffold/slop/lies problem is now the explicit target** (user's framing): Cline/Aider/Copilot routinely ship LLM-generated scaffolds that pretend to satisfy the ask. We need gates that cannot be bypassed. Existing gates: existence ("claimed success wrote 0 files"), spec-faithfulness (pattern match on declared files), Ecosystem compile gate (new). Missing: **SOW-compliance gate** that walks the SOW prose for named deliverables and verifies each is nontrivially implemented. An audit subagent is running (in-flight) to map the existing infrastructure before we build on it.

## Open decisions / next moves

1. **Observe the compliance gate firing on M2g/M2g3** — when they finish their sessions (~30-60 min), log will show `🕵 SOW compliance sweep (round 1/5): X/Y ok, N stub, M missing`. Verify it actually dispatches repair sessions and that those sessions produce work. If compliance passes on first try with missing/stub=0, we've confirmed the workflow end-to-end.
2. **Port the compliance gate's pattern into simple-loop** — same idea at the end of the outer ROUND loop. User wants iterative/recursive enforcement, not just reporting.
3. Port the `fixOrchestrator` pattern (worktree + commit-review + merge-on-approval) into the **sow command** so the harness gets commit-level review gates in addition to the new mission-end compliance gate. Still deferred; compliance gate should handle the most egregious scaffold slop.
4. Compare Gemini 2.5 Pro vs Gemini 3.1 Pro Preview as reviewers once M2g/M2g3 have produced comparable plan reviews (~10 min from relaunch).
5. User-asked improvements still open: stratified reviewer (cheap lint first, semantic only when lint-clean), review caching, trivial-task bypass, cheap-reviewer ladder.
6. Strategic question from user: *can native harness be made good enough to ship, or is CC the only viable backbone?* With the compliance gate shipped, M2g/M2g3 are the best stress test for "cheap worker + cheap reviewer, with mission-end compliance enforcement replacing the need for a smart-reviewer-per-commit."
7. **Known limitation**: old-binary runs (A, M1c, M2x, D1, D2, MS) will complete WITHOUT firing the compliance gate. M2x at 174 files is producing good code but may declare done with scaffolds the gate would have caught. Acceptable sunk cost given how far they are.

## Non-obvious resume instructions

- Use the cron monitor for state — don't rebuild from scratch. Monitor log `monitor-log.md` has full history with timestamps.
- Don't `cd` to parent dirs when launching stoke — there's a hook `/home/eric/repos/stoke/.claude/hooks/guard-bash-writes.sh` that blocks it. Use absolute paths + `pnpm --dir <abs>` instead of `cd <abs> && pnpm`.
- Stoke binary: `/home/eric/repos/stoke/stoke`. Rebuild: `go build -o stoke ./cmd/stoke` (always rebuild before relaunching variants when code changes).
- When resetting a sentinel repo: preserve `SOW_WEB_MOBILE.md` BEFORE `git clean -fdx` — it's untracked, gets nuked. Canonical SOW saved at `/tmp/sow-canonical.md`.
- The harness variants (sow) store state at `<repo>/.stoke/sow-state.json`. Resume-able via `stoke resume` if needed, but `--fresh --force` skips it.
- Old binaries of variants A and B are running — they don't have the Ecosystem build-gate fix. Kill+relaunch them to get the fix, OR leave them to demonstrate the old failure mode.
