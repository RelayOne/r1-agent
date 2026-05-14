# seed-bugfix-easy

## Upstream task

This is a **synthetic seed mission**, not derived from an upstream SWE-bench Pro task. It
exercises the easy bug-fix shape — single-file fix to a loop bound that drops the last element
of a windowed iteration. The corpus authors built it to cover the bug-fix bucket (target 30
missions per `plans/corpus-100.md`) while the operator-curated SWE-bench Pro batch is being
authored.

## Plan rationale

- **P1** — fix the loop bound from `len(xs) - w` to `len(xs) - w + 1`. Two required symbols:
  the function name (`WindowSum`) and the corrected literal substring (`len(xs) - w + 1`). The
  literal-substring check catches the subtle case where an agent renames the parameter or
  rewrites the math equivalently without actually fixing the bug.
- **P2** — add a regression test (`TestWindowSum_LastWindowIncluded`) that fails before the
  fix and passes after. Required symbol is the test function name. This forces the agent to
  actually demonstrate the fix is verified, not just claim it.

## Test strategy

Standard `go test` against the package. The two required symbols are sufficient to score plan
completion structurally; no LLM judge is needed for the easy case (`judge_agree: advisory`).

## Known limitations

- The pre-fix workspace (the `pkg/window/window.go` with the buggy bound) is NOT seeded by this
  mission directory — production dispatchers would need a `workspace-init.sh` to write the
  buggy file before the agent runs. This seed exists primarily to exercise the pipeline; the
  full upstream-derived bug-fix missions in the deferred 95 will ship the buggy baseline.
- `delivery_ratio_min: 40` is low because the gold patch is small (~20 lines); production
  bug-fix missions with larger touches will use 60-75.

## How this maps to the 95-mission roadmap

This mission **does not count** toward the 95 SWE-bench Pro–derived missions in
`plans/corpus-100.md`. It is a pipeline-exercising seed only. The deferred 95 must be derived
from real upstream tasks with real gold patches; the operator-curated batches are the source
of truth.
