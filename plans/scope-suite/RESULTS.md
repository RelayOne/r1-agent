# Scope-suite results log

Live running log of ladder results. Machine-written by `run.sh` via
`results.jsonl`; this file is the human-readable summary.

## 2026-04-18

| Rung | Mode | Binary | Duration | Result | Commits | TS | Verified |
|---|---|---|---|---|---|---|---|
| R01 | strict | 24532bc | ~9m | ✅ SIMPLE LOOP COMPLETE | 2 | 2 | `npm test` — 1 passed |
| R01 | lenient | 394111a | ~22m | ✅ SIMPLE LOOP COMPLETE | 2 | 2 | `npm test` — 1 passed |

**Key finding**: strict mode CAN converge on tightly-scoped SOWs. The
strict vs lenient difference matters only when the compliance gate's
prose-extraction finds deliverables the worker legitimately deferred.
For truly small scopes (R01-style: 1 function + 1 test), both modes
converge in well under max-rounds.

## Rungs in flight (2026-04-18 ~14:15)

- R02 strict: ROUND 3/5, 4 commits, 3 TS
- R02 lenient: Final review 1/5 after rate-limit recovery, 3 commits, 2 TS
- R03 strict: round 3 rate-limit pause, 3 commits, 4 TS
- R03 lenient: rate-limit pause, 4 commits, 5 TS
- R04 lenient: builder call 2, 4 commits, 8 TS
- E5: simple-loop strict on full Sentinel (25 H-27 hits caught)
- E9: sliced Sentinel SOW, 10 commits / 10 TS
- E4: scan-repair Phase 2 still running
