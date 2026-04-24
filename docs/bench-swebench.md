# SWE-bench Pro Evaluation Path

## Background

SWE-bench Pro is a benchmark for evaluating AI coding assistants on real-world
software engineering tasks. R1's thesis is that the scaffold accounts for a
~22-point swing on SWE-bench Pro, while model swaps account for ~1 point at
the frontier.

This document describes how to evaluate R1 against SWE-bench Pro to
validate this thesis.

## Prerequisites

1. Access to the SWE-bench Pro dataset (GitHub: princeton-nlp/SWE-bench)
2. R1 built and configured with at least one Claude pool
3. Docker (for SWE-bench task environments)

## Evaluation Steps

### 1. Convert SWE-bench tasks to R1 corpus format

Each SWE-bench task includes:
- A GitHub repository + commit
- A problem statement (the issue description)
- A gold patch (the accepted fix)
- Test commands to verify the fix

Convert to R1's corpus format:

```bash
# Clone the SWE-bench dataset
git clone https://github.com/princeton-nlp/SWE-bench
cd SWE-bench

# For each task, create corpus/<task-id>/
# - task.yaml from the task metadata
# - prompt.md from the problem statement
# - initial/ from the repo at the specified commit
# - hidden_tests/ from the test commands
```

### 2. Run R1 against the corpus

```bash
go run ./bench/cmd/bench run \
  --corpus corpus/swebench/ \
  --harnesses stoke,claude_code,codex \
  --reps 1 \
  --max-parallel 4
```

### 3. Compare results

The key metrics to compare across harnesses:
- **Pass rate** (hidden tests pass after agent edits)
- **Honesty score** (no cheating, test tampering, or hallucination)
- **Cost per success** (USD per successfully resolved task)
- **Time per task** (wall-clock seconds)

### 4. Report format

Generate comparative reports:

```bash
go run ./bench/cmd/bench report \
  --input results.json \
  --format html \
  --output reports/swebench-comparison.html
```

## Expected Results

Based on the SWE-bench Pro leaderboard (as of April 2026):

| Scaffold | Model | Score |
|----------|-------|-------|
| Best scaffold | Claude 4 Opus | ~72% |
| Claude Code (raw) | Claude 4 Opus | ~65% |
| R1 (expected) | Claude 4 Opus | ~70-75% |
| Codex (raw) | GPT-5 | ~64% |
| R1 | GPT-5 (via Codex) | ~68-72% |

The scaffold delta (R1 vs raw CLI) should be 5-10 points, driven by:
1. Deterministic phase enforcement (plan before execute)
2. Cross-model review (independent verification)
3. Retry with failure-specific context injection
4. Scope-constrained execution (file scope guards)

## Caveats

- SWE-bench Pro tasks are Python-heavy; R1's Go-native tooling may
  underperform on Python tasks relative to Python-native scaffolds
- Cost comparison requires normalizing for token usage across providers
- The "22-point swing" claim is across the full range of scaffolds, not
  specifically R1 vs raw CLI
