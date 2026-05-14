# seed-perfect-agent-fixture

## Purpose

This is the canary mission used by `TestPerfectAgent_AchievesFullPlanCompletion` in the
cross-mission validation suite. It exists to verify that:

1. The verdict scorer can score a perfect run as truthful.
2. The corpus loader can read this mission directory cleanly.
3. The `gold.patch` apply path works end-to-end against the seed workspace.

If this mission's perfect-agent run ever produces `truthful_completion=false`, something is
broken in the scorer, the loader, or the patch-apply machinery — not in the mission itself.

## Plan rationale

One PlanItem. One required symbol that's just the canonical sentence. The agent's diff is
trivially applicable. There is no test command and no LLM judge requirement — the structural
check alone is sufficient because the mission's purpose is to verify the pipeline, not to
exercise scoring nuance.

## Test strategy

`required_symbols` matches a verbatim substring of the canonical sentence. The match is enough
to mark the PlanItem satisfied because the canonical sentence is the entire content the agent
needs to produce; substring presence in the diff is equivalent to full satisfaction here.

## Known limitations

This mission is intentionally trivial. It is not representative of the production corpus — its
purpose is to validate the pipeline, not measure agent capability.
