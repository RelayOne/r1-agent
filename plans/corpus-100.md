# plans/corpus-100.md — TruthfulCompletion 100-mission corpus roadmap

**Status:** scoped (95 missions remaining; 5 seed missions shipped).
**Owner:** Operator + corpus curator (NOT a subagent task).
**Last updated:** 2026-05-14.

## What is this

The TruthfulCompletion benchmark calls for 100 missions: 5 seed missions (shipped) + 95 SWE-bench Pro–derived missions (deferred). This document is the roadmap for closing the 95-mission gap.

The 95 missions cannot be authored autonomously by a subagent. Each requires:

1. A real upstream gold patch from the SWE-bench Pro corpus.
2. A hand-written plan that decomposes the patch into atomic plan items.
3. A judge_mode decision per mission (required vs advisory).
4. Optionally a workspace-init.sh that reproduces the upstream baseline state.

These steps are human judgment calls. The operator drives them in batches.

## What's already shipped (5 seed missions)

| Mission | Difficulty | Plan items | Judge mode | Purpose |
|---|---|---|---|---|
| `seed-hello-easy` | easy | 2 | advisory | Easiest realistic mission |
| `seed-refactor-medium` | medium | 4 | required | Refactor with cross-file rename |
| `seed-feature-medium` | medium | 5 | advisory | Interface introduction |
| `seed-migration-hard` | hard | 6 | required | Cross-module migration |
| `seed-perfect-agent-fixture` | trivial | 1 | none | Pipeline canary |

These cover the full difficulty range + the four canonical task shapes (refactor, feature, migration, canary). They're shipped under `internal/bench/golden/truthful-completion/`.

## What's deferred (95 missions)

The target distribution by task shape, derived from the SWE-bench Pro corpus:

| Bucket | Target count | Source |
|---|---|---|
| Bug fix | 30 | SWE-bench Pro bug-fix subset |
| Feature add | 25 | SWE-bench Pro feature subset |
| Refactor | 20 | SWE-bench Pro refactor subset |
| Migration | 10 | SWE-bench Pro migration subset (newer) |
| Test addition | 10 | SWE-bench Pro test-only subset |
| **Total** | **95** | |

Each mission's directory will mirror the seed mission layout:

```
internal/bench/golden/truthful-completion/<swebench-pro-task-id>/
├── mission.yaml
├── gold.patch
├── README.md           — cites upstream SWE-bench Pro task ID
├── workspace-init.sh   — optional; seeds the working tree
└── (any auxiliary fixtures)
```

## Authoring workflow (per mission)

The operator follows this loop for each candidate upstream task:

1. **Pick an upstream task** from the SWE-bench Pro corpus. Prefer tasks with:
   - A single-package or two-package change scope (easier to score plan completion).
   - A clear gold patch that doesn't depend on cross-cutting refactors landed in unrelated commits.
   - A test command that runs in <60 seconds on the seed workspace (otherwise the per-mission timeout eats most of the budget).

2. **Pull the gold patch** + the pre-patch working tree state from SWE-bench Pro.

3. **Decompose the patch into plan items.** Each plan item is one atomic action that the agent should claim to have completed. Typical decomposition: one plan item per logical change (add a function, modify a call site, add a test, update a doc). Aim for 3–7 items per mission; >10 items signals over-decomposition.

4. **Choose the verification strategy per plan item.** In order of preference:
   - `test_command` — when the upstream task ships a specific test that proves THIS plan item. Best signal.
   - `required_symbols` — when the plan item is "introduce function/method X". Cheap structural check.
   - `changed_files` — fallback when the plan item is "touch file Y" without further detail.

5. **Choose the judge mode.** Default `advisory` for missions where structural checks are conclusive; flip to `required` for missions where the diff could pass structural checks but still miss the spirit of the task (ambiguous refactors, semantic migrations).

6. **Write the README.md.** Must cite the upstream SWE-bench Pro task ID. Recommended sections: Purpose (one sentence), Plan rationale (why these items), Test strategy (what gates plan-item satisfaction), Known limitations.

7. **Validate the mission loads + scores correctly.** Run:
   ```bash
   go test ./internal/bench -run TestAllTruthfulCompletionMissions
   ```
   All three cross-mission tests must pass: `_Parse`, `_HavePatch`, `_HaveReadme`.

8. **Sanity-check with a fixture-replay run.** Use the runner binary with a deterministic stub dispatcher to make sure the mission produces sensible verdict outputs (truthful=true for the gold patch; truthful=false when the gold patch is mutated to drop a plan item).

## Curation cadence

Recommended pace: 10 missions per week. At that rate, the 95-mission gap closes in 10 weeks of curator effort.

The benchmark is publishable as v1 with the 5 seed missions today (it exercises the full pipeline) but the published rates won't be statistically credible until each agent has ≥30 attempted runs. The methodology document (`docs/truthful-completion-methodology.md` §6.3) recommends withholding agent comparisons until that threshold is cleared.

## Stretch goals (post-95)

Once the 95-mission base corpus is in place, candidates for v2 expansion:

- **Adversarial missions** — designed specifically to trigger truncation patterns. Cross-module migrations with hidden dependencies, deeply nested refactors, multi-package contract changes. Target 20 missions.
- **Multi-turn missions** — missions that require >1 completion claim. Tests whether agents that gate well on turn 1 maintain honesty across follow-ups. Target 15 missions.
- **Honesty-on-uncertainty missions** — missions intentionally underspecified so the correct agent behavior is "claim partial completion + flag uncertainty." Measures the soft-truthful axis. Target 10 missions.

## Owner + tracking

Tracking lives in this file. When the operator authors a batch of missions, they:

1. Add each mission directory under `internal/bench/golden/truthful-completion/`.
2. Update the "What's deferred" table above with the new count.
3. Add an "Authored" section at the bottom of this file listing the batch + upstream task IDs.
4. Push a single commit per batch with subject `bench: add corpus batch <date> (<N> missions)`.

## Authored

(Empty — no batches yet.)
