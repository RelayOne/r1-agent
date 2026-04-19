# HANDOFF — sow harness convergence blocker (2026-04-18 20:15)

## The situation, one paragraph

It's been a week. Simple-loop works cleanly (R01/R02 strict + lenient all pass; R03+ fails on Claude rate-limit timeouts, not architecture). Sow harness has NEVER cleanly converged on R01 hello-world. Every attempt produces 1 init commit, 0 TS files, and terminates with "SOW finished with N failed sessions" where the failure cascade is:
- preflight: "working tree has uncommitted changes"
- task fails in 1-4s with cost $0.0000
- Failure codes: WRONG_FILES + PROTECTED_PATH_TOUCHED
- BUT the harness ALSO logs `"success":true` on the same task_id immediately before the failure
- Reviewer verdict: "tsconfig.json does not exist anywhere" / "greet.ts is not present" / similar claims that files weren't produced

The contradiction between `success:true` at the workflow layer AND `WRONG_FILES` failure code AND reviewer claiming files don't exist is the smoking gun. Three layers see three different truths about the same task.

## What we've tried (all committed)

Binary HEAD is `d748106 feat: --workflow=serial preset + sow-serial matrix lane`.

1. **957ce61** — InPlace mode. Before this, sow used `<repo>/.stoke/worktrees/<task-id>/` per-task worktrees. Worker wrote there; reviewer checked main repo (empty); review rejected. InPlace mode routes `handle.Path` back to RepoRoot so worker + reviewer see the same tree. **Verified working**: `git worktree list` now shows only `[main]`, no `.stoke/worktrees/*`.

2. **f901f4b** — Restored `--native-base-url / --native-api-key / --native-model` flags on the sow ladder lane. Without them sow exits immediately: "load SOW: no provider configured (check --runner / --native-api-key)".

3. **85661e4** — ladder-driver sow_flags = `--parallel 1 --parallel-tasks 1` on small rungs. Before this the harness ran 4 workers in the same cwd (InPlace mode) and they saw each other's writes as out-of-scope.

4. **d748106** — `--workflow=serial` preset flag: collapses `--parallel` + `--parallel-tasks` + `--workers` + `--per-task-worktree` to 1/1/1/false. Banner confirmed in logs: `🧩 --workflow=serial: forcing --parallel 1, --parallel-tasks 1, --workers 1, --per-task-worktree=false`. **Verified applied**: `workers: 1` appears in R01-sow-serial log.

**Despite all of the above, R01-sow-serial still fails.** That proves parallelism / isolation were not the (only) bug.

## Current hypothesis: the sow task-scope contract is broken

From looking at `internal/workflow/workflow.go:845`:

```go
protectedViolations := verify.CheckProtectedFiles(modifiedFiles, e.Policy.Files.Protected)
```

In InPlace mode, what's "modified" in the main repo cwd between task dispatches? I think every task runs with `modifiedFiles = all files touched in the repo since... when?`. If it's "since baseline", then T2 sees T1's `package.json` as modifiedFiles even though T2 didn't touch it → T2's `task.Files` = `["tsconfig.json"]` → `package.json` is "outside AllowedFiles" → WRONG_FILES.

The spawned subagent (task ID `b1v978use`) is investigating and may have already noticed this in its monitor:
> "in `--workflow=serial` path there's NO per-task commit — so the worker's writes from T1 remain uncommitted, and when T2 runs, its inPlacePreDirty snapshot will contain T1's writes (e.g., `package.json`), filtering them out of T2's modifiedFiles. That's correct behavior because T2 didn't modify package.json."

If the subagent is right, the fix is making sure `inPlacePreDirty` actually filters T1's writes out of T2's modifiedFiles. If the filtering is broken, every subsequent task fails scope.

## Where to look next

### Critical files
- `internal/workflow/workflow.go:700` — `DiffSummary(ctx, handle)` captures what the harness sees as "modified." In InPlace mode, this is git diff against... baseline? HEAD?
- `internal/workflow/workflow.go:845` — protected files check (probably fine; default Protected is short).
- `internal/workflow/workflow.go:854-866` — AllowedFiles scope check. This is the likely culprit.
- `internal/workflow/workflow.go:66-77` — `inPlaceWorktrees` stub. Its `Prepare` errors if called (correct).
- Search: `inPlacePreDirty` — the snapshot mechanism the subagent mentioned. Is it wired correctly when `Engine.InPlace=true`?

### The actual test run
`/home/eric/repos/scope-suite-runs/R01-sow-serial-20260418T181121/stoke-run.log` — the live failure with `workflow=serial` banner confirming the preset applied.

### Repro recipe
```bash
rm -rf /tmp/test-sow
mkdir -p /tmp/test-sow && cd /tmp/test-sow && git init -b main -q
cp /home/eric/repos/stoke/plans/scope-suite/rungs/R01-hello-world.md SOW.md
git add SOW.md && GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t \
  GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t \
  git commit -q -m "init"

PORT=$(cat ~/.litellm/proxy.port)
KEY=$(grep '^LITELLM_MASTER_KEY=' ~/.litellm/.env | cut -d= -f2- | tr -d '"'"'")
/home/eric/repos/stoke/stoke sow \
  --repo /tmp/test-sow --file /tmp/test-sow/SOW.md \
  --native-base-url "http://localhost:$PORT" \
  --native-api-key "$KEY" \
  --native-model claude-sonnet-4-6 \
  --reviewer-source codex \
  --workflow serial --fresh 2>&1 | tail -30
```

Run this to repro. Should fail with `SOW finished with N failed sessions`. Look at the `✗ [T1] failed (1.6s, $0.0000, attempt 1)` lines — task takes <5s, costs nothing, NEVER actually calls the LLM.

### Why this matters

The `$0.0000` cost is the key signal — **the worker never calls the LLM**. Something in the workflow engine is rejecting the task BEFORE worker dispatch. Preflight's "uncommitted changes" warning is a clue: the workflow engine is deciding "tree is dirty, I refuse to dispatch this task" → task fails immediately → attempted 3 times → session fails.

The LLM isn't being called at all. That means the fix is in `internal/workflow/workflow.go` or `internal/engine/*.go`, not in prompts or reviewer logic.

## Simple-loop status (for context — ignore if focused on sow)

| Rung | strict | lenient |
|---|---|---|
| R01 | ✅ 2cmt/2TS test passes | ✅ 2cmt/2TS test passes |
| R02 | ✅ 3cmt/3TS tsc clean | ✅ 3cmt/2TS tsc clean |
| R03 | timeout 40min | timeout 40min (Claude rate-limit) |

R03 timed out because Claude account is saturated (operator's personal Max subscription). Not a code issue. Will come back clean when limits reset.

## What NOT to spend time on

- Chasing parallelism-in-sow. We proved `--workflow=serial` (all knobs at 1) doesn't fix it.
- Blaming the reviewer. The worker never calls the LLM — the problem is upstream of review.
- Per-task worktrees. InPlace mode is confirmed working (no `.stoke/worktrees/*` dirs).
- FallbackPair. That's for rate-limit swap, unrelated.

## What's probably the fix

Read `internal/workflow/workflow.go` looking for the scope check at attempt dispatch time (before worker runs). If preflight marks the tree "dirty" from the previous task's in-flight write and refuses to dispatch, that's the bug. In InPlace mode, preflight needs to diff against the per-task baseline (HEAD at task start) not `git status`.

## Pulse

Subagent `a4e84f3610a5ee78c` was dispatched at 20:12 PDT to investigate + fix + test. It has full context on the `inPlacePreDirty` snapshot machinery. If it reports back with a commit, verify it builds and launch a test R01-sow-serial run against it. If it hit limits mid-investigation, continue from its monitor event: "✓ session S1 expanded in 22s (5 tasks, 4 ACs)" — that was its test run starting.

Simple-loop lanes can be retried when Claude rate limits reset (~2-4h). State file at `plans/scope-suite/ladder-state.json`; unblock reason fields + relaunch `bash plans/scope-suite/ladder-driver.sh --parallel`.

## Outstanding issues (not blocking)

- H-27/H-28 declared-symbol gate: validated working on E5 (25 hits). Regex variant default; tree-sitter opt-in via `STOKE_H27_TREESITTER=1`.
- H-29 plateau-exit (gapCountProgressTracker): shipped, tests green, not yet fired in production (no loop has run long enough to plateau).
- FallbackPair bidirectional + background pinger: shipped at `5c72716`. Pings CC+codex every 5min; waits on both when both are down. Not yet triggered.
- Monitoring crons `e2768301 2297014b 3cf39acb` live.

## The one-line summary

**The sow harness refuses to dispatch tasks in InPlace mode because preflight sees the previous task's uncommitted write as a dirty tree and fails the task before the LLM is ever called. Fix is in `internal/workflow/workflow.go` around the preflight/protected-files/scope checks.**
