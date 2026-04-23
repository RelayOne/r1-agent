---
name: Harness regression
about: Report a benchmark or quality regression (SWE-bench Pro, SWE-rebench, Terminal-Bench, or internal golden missions)
title: "regression: <bench> <delta>"
labels: ["regression", "benchmark", "triage"]
assignees: []
---

<!--
  Stoke's thesis is "the harness is the product." A measurable regression on
  a harness-quality benchmark is a first-class bug, on par with a crash. Use
  this template when you see a score drop, a timeout increase, or a behavior
  change that degrades outcomes relative to a known-good reference.

  If this is just "my one task failed today," file a normal bug report
  instead. This template is for systematic regressions.
-->

## Benchmark / mission affected

- [ ] SWE-bench Pro
- [ ] SWE-rebench
- [ ] Terminal-Bench
- [ ] Internal golden missions (`bench/`)
- [ ] Custom mission suite (describe below)

## Regression summary

| Metric            | Baseline | Current | Delta |
|-------------------|----------|---------|-------|
| Pass rate         |          |         |       |
| Avg cost / task   |          |         |       |
| Avg duration      |          |         |       |
| Retry rate        |          |         |       |
| Verify-fail rate  |          |         |       |

## Reference points

- Baseline commit:                  <!-- git SHA -->
- Current commit:                   <!-- git SHA -->
- Baseline run date:                <!-- YYYY-MM-DD -->
- Current run date:                 <!-- YYYY-MM-DD -->
- Baseline reports path:            <!-- .stoke/reports/... or bench/ output -->
- Current reports path:             <!-- .stoke/reports/... or bench/ output -->

## Suspected cause

<!--
  Git diff, dependency update, provider model swap, prompt template change,
  scheduler policy change, etc. If you bisected, list the bisect range.
-->

## Tasks that now fail that previously passed

<!--
  List specific task IDs or mission names. Attach the diff between the two
  per-task reports if useful.
-->

1.
2.
3.

## Environment

- Go version:                       <!-- `go version` -->
- OS / arch:
- Provider chain in use:            <!-- e.g. Claude primary, Codex reviewer -->
- Pool config:                      <!-- pools involved; redact paths if needed -->
- Concurrency (workers):

## Attached artifacts

- [ ] Baseline `latest.json`
- [ ] Current `latest.json`
- [ ] Diff between the two reports
- [ ] Failure fingerprints (from `internal/failure` output)
- [ ] `stoke audit --dry-run` output against the current commit

## Reproduction

```bash
# Minimal command that demonstrates the regression.
stoke ...
```

## Additional context

<!--
  Recent merges in main. Upstream provider changes (model version bumps,
  rate-limit policy changes). Any speculative-execution strategy toggled.
-->

## Checklist

- [ ] I have both baseline and current runs in hand, not just a single bad run.
- [ ] The regression reproduces across at least 2 independent runs per side.
- [ ] I redacted API keys, tokens, and private file paths from attached logs.
- [ ] I know the git SHA of the baseline and current commits.
