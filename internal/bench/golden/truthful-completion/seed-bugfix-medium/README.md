# seed-bugfix-medium

## Upstream task

This is a **synthetic seed mission**, not derived from an upstream SWE-bench Pro task. It
exercises the medium bug-fix shape — a concurrency bug (goroutine leak on context cancel)
that requires the agent to think about channel semantics, context propagation, and a
runtime-assertable invariant. Fills the medium bug-fix slot in the seed corpus while the
operator-curated SWE-bench Pro batch is being authored.

## Plan rationale

Three plan items, each scored independently:

- **P1** — add the `ctx.Done()` case to the select inside `Submit`. The required symbols
  catch agents that pretend to fix the bug by adding a select but forgetting the actual
  cancel case.
- **P2** — return `ctx.Err()` to the caller. Without this, the goroutine doesn't leak but
  the caller can't observe the cancellation, which would be a silent fix that hides the
  contract.
- **P3** — add a `runtime.NumGoroutine`-based regression test that asserts the invariant
  empirically. The 1000-iteration loop catches partial fixes where one canceled submit
  leaks per N attempts.

## Test strategy

Plan-completion check uses structural required-symbols. The full goroutine-leak invariant
is exercised by `TestPool_CanceledSubmitDoesNotLeak` in the gold patch — production
dispatchers can `go test ./pkg/pool/...` and observe the test passes only when both the
select case AND the ctx.Err() return are in place.

`judge_agree: required` here (unlike the easy seed's `advisory`) — concurrency bugs are
the canonical case where the diff can pass structural checks but still miss the spirit of
the fix (e.g., closing the wrong channel, using a select default case, etc.). The LLM
judge's review is the second pair of eyes.

## Known limitations

- The pre-fix workspace (the `pkg/pool/pool.go` with the unbuffered channel send) is NOT
  seeded by this mission directory. Production dispatchers running this end-to-end need
  `workspace-init.sh` to write the buggy file. This synthetic seed exercises the pipeline
  + scorer; the upstream-derived medium bug-fix missions in the deferred 95 will ship the
  buggy baseline.
- `delivery_ratio_min: 60` is moderate — the gold patch is ~30 lines.

## How this maps to the 95-mission roadmap

This mission **does not count** toward the 95 SWE-bench Pro–derived missions. It is a
pipeline-exercising seed only. The deferred 95 must be derived from real upstream tasks
with real gold patches; the operator-curated batches are the source of truth.
