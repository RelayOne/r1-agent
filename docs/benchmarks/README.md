# Benchmarks

Published benchmark reports for R1. Each page documents methodology
first, then numbers. Where numbers are projections (pricing models,
token estimates) we say so; where numbers are measured (corpus runs)
we stamp them with commit, hardware, date, and corpus identifier.

## Published reports

- [prompt-cache.md](prompt-cache.md) — Anthropic prompt-cache savings.
  Pricing-model projections across three agentic-workload profiles
  (short 5-turn, standard 20-turn, long 50-turn). Explains how cache
  hits are tracked at both structuring (`internal/agentloop/cache.go`)
  and telemetry (`internal/stream/cache.go`) layers. Reproduction
  path for live-telemetry measurements included.

## Planned reports

The entries below are listed in `mint.json` navigation and referenced
by earlier portfolio work orders; their content lives elsewhere in
the repo today and will be ported into this directory as the
measurements land.

- SWE-bench Pro — evaluation path is in
  [../bench-swebench.md](../bench-swebench.md). Measured deltas vs.
  harness-only scaffold controls are not yet published here.
- SWE-rebench — contamination-resistant evaluation. Planned;
  methodology work happens under `plans/`.
- Terminal-Bench — terminal-task evaluation. Planned.
- Anti-deception matrix — see
  [../anti-deception-matrix.md](../anti-deception-matrix.md).
- Corpus format — see
  [../bench-corpus-format.md](../bench-corpus-format.md).

## How to contribute a benchmark report

1. Use the template in `prompt-cache.md`. Methodology section first,
   measurement footprint (commit, Go version, host arch, date), then
   numbers.
2. Separate **projections** (from pricing models or static analysis)
   from **measurements** (from live corpus runs). Never mix them in
   the same table.
3. When you publish a measurement, include the reproduction
   command line so reviewers can re-run it. If the measurement is
   non-deterministic (LLM involved), record the seed / temperature
   / corpus commit and set `--reps >= 3`.
4. Numbers without a reproduction path are not benchmarks. They are
   marketing. Flag them as such, or do not publish them.

## The benchmark stance

R1 reports deltas on SWE-bench Pro, SWE-rebench, and Terminal-Bench
— not contaminated SWE-bench Verified numbers. See
[../benchmark-stance.md](../benchmark-stance.md) for the published
evaluation posture.
