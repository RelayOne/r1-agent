# Stoke scope ladder — progressive convergence suite

Progressive SOW-complexity ladder for testing whether stoke can
autonomously converge on increasingly ambitious scopes. Designed to be
repeatable: every rung has a locked prose SOW + a reproducible baseline
setup + explicit success criteria.

## Why this exists

Prior cohort runs launched bespoke experiments against Sentinel's 55KB
full SOW. That's a 3-month-team project; none of them converged. The
failure mode could be: (a) SOW size, (b) session chain-of-deps,
(c) worker intelligence, (d) gate over-blocking, (e) something else.
Without a ladder, we couldn't tell.

Running the ladder top-to-bottom tells us exactly where stoke's
autonomous convergence wall sits today. Hitting a rung twice with
different worker configs (CC-sonnet / CC-opus / MiniMax / etc.)
tells us which dimension moves the wall.

## Rung structure

| Rung | SOW size | Scope | Passes when |
|---|---|---|---|
| R01 | ~0.3KB | single-file util | `greet.ts` exports working `greet()` |
| R02 | ~0.8KB | TS package scaffold | package.json + src/index.ts + README |
| R03 | ~2KB | monorepo scaffold | pnpm workspaces + turbo + stub packages |
| R04 | ~2KB | single CRUD endpoint | route + Zod schema + happy-path test |
| R05 | ~4KB | multi-file feature | login: API + schema + UI + test |
| R06 | ~8KB | cross-package feature | notification preferences in monorepo |
| R07 | ~15KB | Sentinel session slice | 1 session, ~10 tasks |
| R08 | ~55KB | Sentinel full | 23 sessions, 500+ tasks |

Each rung ships as `rungs/RN-name.md` — the prose SOW stoke reads. The
runner clones a clean baseline, copies the SOW in as `SOW.md`, and
invokes simple-loop (or sow-harness for ladder sow-variants).

## Repair ladder (runs against already-existing code)

`rungs/RR-*.md` — tests stoke's scan-and-repair + refactor mode against
codebases with known defects. To add later.

## Running

```bash
# Run one rung against the current binary, simple-loop mode:
bash plans/scope-suite/run.sh R01

# Run the full ladder sequentially:
bash plans/scope-suite/run.sh all

# Run a single rung with a specific config:
bash plans/scope-suite/run.sh R03 --worker cc-sonnet --reviewer codex

# Results append to plans/scope-suite/results.jsonl.
```

Results line format:
```json
{"ts":"2026-04-18T13:30:00Z","rung":"R01","commit":"5c49154","mode":"simple-loop","duration_s":420,"commits":3,"result":"converged","details":"..."}
```

Result values:
- `converged` — SIMPLE LOOP COMPLETE hit
- `partial` — PARTIAL-SUCCESS / plateau exit (H-29)
- `regressed` — Step-8 regression cap (H-6)
- `timeout` — wall-clock exceeded rung's per-rung budget
- `crash` — process died or hook killed
- `skipped` — prerequisite missing (e.g. LiteLLM down)

## Interpretation

A binary that converges cleanly on R01-R03 but plateaus on R05+ tells
us scope-size-with-cross-file-deps is the wall. A binary that plateaus
on R02 tells us even single-package scope doesn't converge reliably.
The ladder's job is to produce that signal quickly and repeatably.
