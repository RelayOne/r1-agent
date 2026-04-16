# Why Stoke is a single-strong-agent + adversarial-reviewer harness, not a multi-agent system

**S-U-018 positioning brief.** Status: stable as of April 2026.

## Summary

Stoke deliberately does NOT adopt the "many cooperating agents" pattern that has dominated AI-engineering marketing since 2024. The published failure data makes the case against it:

- The Multi-Agent System Failure Taxonomy (MAST) study across 8 production multi-agent systems documented failure rates of **41–86.7%** in real deployments.
- A 2025 evaluation by the Berkeley AI Research group found that **blindly adding agents to a pipeline degrades end-to-end accuracy by up to 70%** on SWE-bench-adjacent benchmarks — agents interact destructively more often than they compound.
- UC Berkeley's 2026 follow-up: **21.3% of multi-agent failures come from premature termination** when agents hand off to each other without strict contracts.

Stoke's architecture responds to this evidence with two load-bearing decisions:

1. **One strong implementer per task.** The primary model (currently Claude 4.6 or the configured builder) writes the code. There is no "planner agent + coder agent + tester agent" ceremony around it. The implementer has full context, full tool access within its authorized scope, and explicit completion responsibility.

2. **An adversarial reviewer, separate routing.** Stoke's `internal/modelsource/` ships a two-role surface (Builder vs. Reviewer) with independent routing — operators select the reviewer model via `--reviewer-model` or `REVIEWER_MODEL` independently of the builder. The recommended configuration is cross-family (Codex reviews Claude's work, or vice versa). When no reviewer is explicitly configured, Stoke falls back to the `--reasoning-model` CLI flag value; if `GEMINI_KEY` is set, review can auto-route to Gemini. Operators running without explicit reviewer configuration should verify which model is actually reviewing their runs — same-family review still catches mechanical errors but loses the cross-family disagreement signal.

This composition sidesteps the failure modes MAST cataloged:

| MAST failure mode | Why Stoke dodges it |
|---|---|
| Premature termination (21.3%) | Phase transitions are driven by a structured state machine, not agent-to-agent natural-language handoff. Completion still involves LLM judgment (native SOW runs use `ReviewTaskWork` + `CheckAcceptanceCriteriaWithJudge`'s semantic judge on AC failures) but the judgment operates against explicit acceptance criteria, not ad-hoc agent consensus. |
| Specification drift across agents | One implementer per phase, one spec, structured verdict on transition. |
| Agent collusion / confirmation bias | Reviewer is routed separately via `internal/modelsource/`; cross-family configuration is recommended but not auto-enforced — operators who leave reviewer unconfigured get same-family review as a fallback. |
| Observability black holes | `internal/taskstate/` records 20 structured failure codes with fingerprints for deception patterns; phase transitions are captured in-memory via `TaskState.Advance()` and surfaced to reports. Full ledger-node wiring for every transition is a known gap (see `docs/anti-deception-matrix.md`). |
| Infinite reasoning loops | 30-task default session cap + step-count circuit breakers + deadlock watchdog. |
| Silent skill degradation | `internal/reviewereval/` measurement harness can run any (builder, reviewer) pair against ground truth and surface FP/FN rates. |

## What Stoke does NOT do

- Stoke does not run "planner + coder + tester" as separate coordinating agents. Planning, execution, and verification ARE separate phase invocations (each starts with a fresh context window — see `docs/harness-architecture.md`), but the transitions are driven by structured verdicts, not agent-to-agent natural-language negotiation. The legacy multi-phase flow lives in `internal/workflow/`; native SOW runs (the `--runner=native` path) drive review / repair / acceptance out of `cmd/stoke/sow_native.go`'s state machine instead — both paths share the same structured-transition posture.
- Stoke does not use "multi-agent voting" to resolve ambiguity. Ambiguous acceptance criteria fail the integrity gate and surface to the operator.
- Stoke does not spawn sub-agents to "handle edge cases." Edge cases either fit in the single implementer's context (by design — Stoke's context packer is relevance-weighted) or are decomposed deterministically by the scheduler into smaller tasks of the same shape.

## Where multi-agent patterns ARE valid in Stoke

Stoke uses multiple models in three specific places, none of which are "agents coordinating":

1. **Cross-model review.** A second model reviews the first's output. Not cooperation — adversarial. The two never share state beyond the diff under review.

2. **Provider fallback chain.** `internal/model.Routes` defines task-type-specific primaries (some tasks prefer Claude, some prefer Codex) with fallbacks through Anthropic, Codex, OpenRouter, the direct provider API, and finally lint-only escape hatches. A chain of alternates per task type, not a parallel committee. Normally each tier runs only when the prior tier is exhausted. When a cost tracker is attached, `model.CostAwareResolve` can walk the fallback chain BEFORE the primary is exhausted once >80% of the budget is consumed — an intentional trade-off for budget-constrained runs. See `internal/model/router.go` for the per-task-type routing table.

3. **Cross-stance governance (`internal/stance*` + `internal/supervisor/`).** Different stance personas (CTO, Dev, Reviewer, QA Lead, etc.) evaluate output from their own perspective at specific gates. Not agents interacting with each other — each stance reads the ledger and emits a structured verdict.

None of these are multi-agent systems in the MAST sense. They are single-agent systems with layered independent checks.

## References

- R-7f55ab42 (MAST study; 41-86.7% failure rates; 70% degradation from blind agent-adding).
- `docs/architecture/v2-overview.md` — full stance + supervisor architecture.
- `internal/modelsource/` — Builder vs Reviewer role routing implementation.
- `internal/reviewereval/` — measurement harness for (builder, reviewer) pair accuracy.
- `internal/taskstate/` — 20 structured failure codes with fingerprint dedup covering the deception patterns MAST found.
- The SWE-bench Pro paper (Scale AI, 2026) — shows scaffold engineering contributes ~15 points of variance on top of model choice, reinforcing that one strong scaffold beats many weak agents.
