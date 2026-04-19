# 🎯 MILESTONE: Sow harness end-to-end R01 convergence

**Date: 2026-04-18 21:25 PDT**

## What happened

For the first time in the project's history, the sow harness converged
end-to-end on R01 hello-world. Four tasks, four commits, real working
TypeScript, passing tests:

| Commit | Task | Duration | Cost |
|---|---|---|---|
| `d7de64e` | sow(S1/T1) package.json | 42s | $0.019 |
| `0a0c1a3` | sow(S1/T2) src/greet.ts | 30s | $0.016 |
| `14846da` | sow(S1/T3) src/greet.test.ts | 31s | $0.013 |
| `a9dc7e1` | sow(S1/T4) fast-test-v2 | 21s | $0.012 |

`npm test` → **3/3 passed** on the first converged run's output.

## The 9-layer debug chain

Every "fix" exposed the next bug in the sow pipeline. Each was
independently correct; none was the whole answer.

1. `[codex config]` — `[profiles.review]` missing from `~/.codex/config.toml`
2. `e30d8d0` — Sow required `--native-*` flags (they are not ignored)
3. `957ce61` — InPlace workflow mode (reviewer sees worker writes)
4. `85661e4` — `--parallel 1 --parallel-tasks 1` on small rungs
5. `d748106` — `--workflow=serial` preset flag
6. `3fc3e06` — InPlace must not blame worker for pre-existing dirty files
7. **`5ec09bc`** — **THE core fix**: auto-upgrade `--runner claude → native`
   when LiteLLM flags are set. Sow was routing to a codex subscription
   pool that is never provisioned in native/LiteLLM deployments, causing
   every task to fail in 0.7s at $0.00 before any LLM call.
8. `5ed1d74` — AC `file_exists:PATH` colon-form parser
9. `97bb4f8` — modelsource codex alias → claude-sonnet-4-6 for LLM-API path

## What proved the fix

`/tmp/fast-test-v2-1776572062` launched 21:04 PDT. By 21:10:
- T1-T4 all dispatched to LLM (real token usage, real $)
- Each produced real code + ran `pnpm test` + `tsc --noEmit`
- Each committed atomically with a proper `sow(S1/Tn): <description>` msg
- `/tmp/fast-test-proof-1776571535` npm test: 3/3 passed

## Architecture decisions validated today

- **Per-task commits** (Option A): every task gets its own atomic commit
- **InPlace mode**: tasks run in main repo, no `.stoke/worktrees/*`
  coordination overhead
- **Deterministic fallback**: when LLM reviewer errors, deterministic
  checks take over and tasks still land
- **`--workflow=serial`**: single-session + single-task + no worktree
  isolation — matches simple-loop's convergence shape for smaller SOWs

## Mode matrix ready

- `simple-loop --fix-mode sequential` — production-ready small SOWs
- `simple-loop --fix-mode sequential --lenient-compliance` — widens
  exit criterion when compliance-gate false positives block convergence
- `sow --workflow parallel` — existing multi-worker isolation (big SOWs)
- **`sow --workflow serial` — NEW as of today, validated on R01**
- `scan-repair --mode simple-loop` — 4-phase pipeline validated on
  1792-file real codebase (ActiumChat)

## Still to validate

- R02+ on sow lanes (TS package scaffold, monorepo, CRUD endpoint,
  login flow, notifications, Sentinel slice, Sentinel full)
- Compliance sweep on prose with no identifier-shaped tokens (skipped
  silently today; R01 hello-world isn't a good test)
- Sow across deeper session DAGs (R07/R08 have 10+ sessions)

## Matrix ladder relaunched at 21:15 on binary 97bb4f8

All four lanes running in parallel:
- strict R03 (resuming past R01/R02 passes)
- lenient R03 (resuming past R01/R02 passes)
- **sow R01** (first ladder-driven sow attempt with all fixes)
- **sow-serial R01** (first ladder-driven `--workflow=serial`)

Should produce first cohort-ladder SIMPLE LOOP COMPLETE / SOW finished
cleanly within 30-60 min.
