# Why Stoke is a single-strong-agent + adversarial-reviewer harness, not a multi-agent system

**S-U-018 positioning brief.** Status: stable as of April 2026.

## Summary

Stoke deliberately does NOT adopt the "many cooperating agents" pattern that has dominated AI-engineering marketing since 2024. The published failure data makes the case against it:

- The Multi-Agent System Failure Taxonomy (MAST) study across 8 production multi-agent systems documented failure rates of **41–86.7%** in real deployments.
- A 2025 evaluation by the Berkeley AI Research group found that **blindly adding agents to a pipeline degrades end-to-end accuracy by up to 70%** on SWE-bench-adjacent benchmarks — agents interact destructively more often than they compound.
- UC Berkeley's 2026 follow-up: **21.3% of multi-agent failures come from premature termination** when agents hand off to each other without strict contracts.

Stoke's architecture responds to this evidence with two load-bearing decisions:

1. **One strong implementer per task.** The primary model (currently Claude 4.6 or the configured builder) writes the code. There is no "planner agent + coder agent + tester agent" ceremony around it. The implementer has full context, full tool access within its authorized scope, and explicit completion responsibility.

2. **An adversarial reviewer, different model family.** Stoke's `internal/modelsource/` ships a two-role surface (Builder vs. Reviewer) with independent routing. The reviewer model is explicitly constrained to be a different family than the builder (Codex reviews Claude's work, or vice versa). Cross-family disagreement is the signal that something is wrong — not intra-family consensus among clones.

This composition sidesteps the failure modes MAST cataloged:

| MAST failure mode | Why Stoke dodges it |
|---|---|
| Premature termination (21.3%) | Single implementer owns completion; no handoff boundary to fail. |
| Specification drift across agents | One context, one spec, one writer. |
| Agent collusion / confirmation bias | Reviewer is a different model family; required by modelsource routing. |
| Observability black holes | Every phase transition writes a ledger node; `internal/taskstate/` records 22+ structured failure codes. |
| Infinite reasoning loops | 30-task default session cap + step-count circuit breakers + deadlock watchdog. |
| Silent skill degradation | `internal/reviewereval/` measurement harness can run any (builder, reviewer) pair against ground truth and surface FP/FN rates. |

## What Stoke does NOT do

- Stoke does not run "planner + coder + tester" as separate agents. Planning, execution, and verification are phases in one agent's loop, gated by deterministic rules (`internal/workflow/` PLAN → EXECUTE → VERIFY → COMMIT).
- Stoke does not use "multi-agent voting" to resolve ambiguity. Ambiguous acceptance criteria fail the integrity gate and surface to the operator.
- Stoke does not spawn sub-agents to "handle edge cases." Edge cases either fit in the single implementer's context (by design — Stoke's context packer is relevance-weighted) or are decomposed deterministically by the scheduler into smaller tasks of the same shape.

## Where multi-agent patterns ARE valid in Stoke

Stoke uses multiple models in three specific places, none of which are "agents coordinating":

1. **Cross-model review.** A second model reviews the first's output. Not cooperation — adversarial. The two never share state beyond the diff under review.

2. **Provider fallback chain.** Anthropic → OpenAI → OpenRouter → lint-only. A chain of alternates, not a parallel committee. Each tier runs only when the prior tier is exhausted.

3. **Cross-stance governance (`internal/stance*` + `internal/supervisor/`).** Different stance personas (CTO, Dev, Reviewer, QA Lead, etc.) evaluate output from their own perspective at specific gates. Not agents interacting with each other — each stance reads the ledger and emits a structured verdict.

None of these are multi-agent systems in the MAST sense. They are single-agent systems with layered independent checks.

## References

- R-7f55ab42 (MAST study; 41-86.7% failure rates; 70% degradation from blind agent-adding).
- `docs/architecture/v2-overview.md` — full stance + supervisor architecture.
- `internal/modelsource/` — Builder vs Reviewer role routing implementation.
- `internal/reviewereval/` — measurement harness for (builder, reviewer) pair accuracy.
- `internal/taskstate/` — 22+ structured failure codes covering the deception patterns MAST found.
- The SWE-bench Pro paper (Scale AI, 2026) — shows scaffold engineering contributes ~15 points of variance on top of model choice, reinforcing that one strong scaffold beats many weak agents.
