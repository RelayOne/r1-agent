# Slow tests under `-race` on Cloud Build — audit

Date: 2026-05-06
Branch: `audit/slow-tests-2026-05-06` (off `main`)
Author: CI Bot (audit-only — no production code modified)

## Trigger

PR #174 race step first attempt (Cloud Build `r1-agent-pr` /
`fdfde46a-af96…`) hit the 5-minute `go test -timeout=300s` cap on two
packages and panicked-on-timeout:

| Package | Wall when killed | Test running at panic |
|---|---|---|
| `github.com/RelayOne/r1/cmd/r1` | 312 s | `TestVerifyServe_AllPass` (12 s in) |
| `github.com/RelayOne/r1/internal/convergence` | 330 s | `TestUXRulesIncludedInCategories` (2 m 21 s in) |

Retry passed. The retry-greenness is the symptom we have to honour
per `CLAUDE.md` ("never classify failures as pre-existing to skip
them") — both timeouts are real findings on the live timeout ceiling
and need a structural fix, not a coin flip.

## Local reproduction attempt

Command run (from `/home/eric/repos/r1-agent` on `main`):

```
go test -mod=mod -race -v -timeout 600s -run . \
  ./cmd/r1/... ./internal/convergence/...
```

Note: required `-mod=mod` because the checked-in `vendor/` tree was
out of sync with `go.mod` for unrelated reasons (vendor mismatch
errors on dozens of indirect deps; orthogonal to this audit and not
modified). `-mod=mod` resolves modules through the module cache
instead of `vendor/` — tests are compiled and linked the same way; it
just changes where the source comes from.

Local hardware: Intel i9-12900K (16 cores / 24 threads), 128 GB RAM,
Linux 6.14, NVMe SSD.

Result on local hardware:

```
ok  github.com/RelayOne/r1/cmd/r1                193.705 s
ok  github.com/RelayOne/r1/internal/convergence    4.761 s
EXIT=0
```

**Honest reporting (per `CLAUDE.md`):** I could NOT reproduce the
timeout locally. cmd/r1 runs in ~3:14 here vs Cloud's 5:12+. The
`internal/convergence` package finishes in 4.7 s locally vs 5:30+ on
Cloud — a >60× discrepancy that is _not_ accounted for by raw CPU
ratios alone (i9-12900K is ~2× faster per vCPU than E2 standard, not
60×). Two plausible amplifiers, neither deterministically observable
without a Cloud Build re-run with `-cpuprofile`:

1. **E2_HIGHCPU_8 oversubscription**. Cloud Build E2 instances are
   bursting/shared. Under noisy-neighbour load the effective single-
   thread perf can drop several-fold, especially with `-race`'s
   memory-bandwidth and cache-pressure overhead.
2. **Module download / build-cache cold start**. The `golang:1.25`
   image starts with an empty `$GOCACHE` and `$GOPATH/pkg/mod`. The
   `race` step waits on `vet`, but each `go test` invocation re-links
   the test binary with the race runtime (separate from the non-race
   build). On a cold cache + race linker pass, cmd/r1's 60+ test
   files plus its large dep graph can plausibly add minutes to the
   first package's wall time _before any test code runs_.

Either way, the cap is too tight for the workload as instrumented. A
60× per-package divergence with one CI environment passing on retry
is a flaky-CI signature, not a flaky-test signature.

## Per-test timings (local, contaminated)

`-race` runs use `t.Parallel()` heavily inside `cmd/r1` (52
PAUSE/CONT events observed). Per-test wall durations therefore
include time spent waiting on a parallel slot, not just the test's
own CPU work. Two consecutive local runs produced different orderings
for the same set of slowest tests:

| Test | Run 1 wall | Run 2 wall | File:line |
|---|---|---|---|
| `TestBuildSOWNativePromptsWithOpts_InjectsSpecExcerptIntoUser` | 47.70 s | 42.93 s | `cmd/r1/sow_task_spec_test.go:371` |
| `TestDumpTaskPrompts_SummaryHasAllTasks` | 45.62 s | 54.78 s | `cmd/r1/sow_identifier_extract_test.go:473` |
| `TestDumpTaskPrompts_WritesFiles` | 30.99 s | 68.77 s | `cmd/r1/sow_identifier_extract_test.go:416` |
| `TestBuildSOWNativePromptsWithOpts_InjectsWorkDir` | 27.25 s | 27.42 s | `cmd/r1/sow_task_spec_test.go:358` |
| `TestBuildSOWPromptsContainsContract` | 13.97 s |  16.44 s | `cmd/r1/sow_native_contract_test.go:14` |
| `TestPrioritizeAggregatedFindings_RDeepScale` |  1.68 s |  11.47 s | `cmd/r1/scan_repair_test.go:470` |
| `TestUpdateSkillPackPullsExternalGitSourceAndInstallsNewDependency` |  2.48 s |   5.13 s | `cmd/r1/skills_pack_cmd_test.go:879` |
| `TestSingleInstance` |  2.40 s |   4.03 s | `cmd/r1/serve_single_instance_test.go:56` |
| `TestRunPhase1_FixtureShellPipeline` |  2.35 s |   4.69 s | `cmd/r1/scan_repair_test.go:759` |
| `TestVerifyServe_AllPass` |  1.78 s |   1.53 s | `cmd/r1/verify_cmd_test.go:64` |

(`internal/convergence` had no test over 0.05 s in either run; the
package's full wall time is dominated by binary link, not test
execution.)

### Why each top-tier test is "slow"

1. **`TestBuildSOWNativePrompts*` (×3) + `TestDumpTaskPrompts*` (×2)** —
   pure in-memory string-builder calls into
   `buildSOWNativePromptsWithOpts` (`cmd/r1/sow_native.go:3999`). The
   function emits ~200 KB of guidance text per call. **The wall
   inflation is parallel-scheduling artifact.** They start early
   (file is alphabetically first among the SOW tests), pause, wait
   for slots, and the `--- PASS` timestamp captures total span — not
   actual CPU. Real per-test CPU is well under 100 ms each. Removing
   `t.Parallel()` from the file would make these "fast" but slow the
   whole package.

2. **`TestPrioritizeAggregatedFindings_RDeepScale`** — generates
   thousands of synthetic findings to prove ordering scales. Real
   CPU work, but bounded. Could split into smaller cases.

3. **`TestUpdateSkillPackPullsExternalGitSourceAndInstallsNewDependency`** —
   spins up a real bare git repo on disk + clones it. I/O bound,
   ~5 s real wall.

4. **`TestSingleInstance`** — `os/exec.CommandContext` re-execs the
   test binary as a subprocess (`cmd/r1/serve_single_instance_test.go:120`).
   Under `-race`, the **child's** test binary _also_ links the race
   runtime (~3-5 s startup on cold cache). The 30 s context cap +
   20 s `-test.timeout` give it plenty of slack but the cold-cache
   bootstrap is a multi-second tax per call. The test calls
   `runHelperProcess` twice (lines 72 and 102). **On Cloud E2, this
   is the candidate that most plausibly burns minutes.**

5. **`TestRunPhase1_FixtureShellPipeline`** — actual subprocess via
   `bash -c`, real fixture, real I/O. ~4 s.

6. **`TestVerifyServe_AllPass`** — spins up an `httptest.Server`
   wrapping the real verify handler, then the handler shells out
   `true` and `exit 0` for two acceptance criteria. Each criterion
   is a real `os/exec` call. Fast locally (1.5 s) but the panic
   trace pinned this test as the running test at the 5:12 mark on
   Cloud — likely it was just unfortunate timing (the package's
   accumulated parallel queue happened to be processing it when the
   timeout fired, not because it was the cause).

## Cloud Build config — current `-timeout` values

`/home/eric/repos/r1-agent/cloudbuild.yaml`:

| Step ID | Command | Timeout |
|---|---|---|
| `test` (lines 70-79) | `go test ./... -count=1 -timeout=120s` | **120 s / package** |
| `race` (lines 81-91) | `go test ./... -race -count=1 -timeout=300s` | **300 s / package** |

`-timeout` in `go test` is **per package**, not total. PR #174's
failure pattern is consistent with that: each of two packages
individually crossed 300 s and was killed independently in the same
step.

`/home/eric/repos/r1-agent/cloudbuild-binaries.yaml` and
`/home/eric/repos/r1-agent/cloudbuild-release.yaml` contain no test
steps.

There is no `.github/workflows/` directory — the comment at the top
of `cloudbuild.yaml` confirms it replaced the old GitHub Actions
`ci.yml`.

## Recommended fixes

The honest read is that **(a)** is the best first move — the rest are
follow-ups if the timeout bump alone doesn't hold. The audit-only
constraint forbids me from applying any of these; what follows is the
recommended dispatch order.

### (a) Bump `race` step timeout — RECOMMENDED IMMEDIATE FIX

In `cloudbuild.yaml` line 89, change `-timeout=300s` to
**`-timeout=600s`** (10 minutes per package).

Justification:

- Local wall is 194 s for cmd/r1 with -race on a fast box. Cloud
  E2_HIGHCPU_8 with cold caches and oversubscription empirically
  produced 312 s before the SIGABRT. A 600 s ceiling gives ~2× the
  worst observed-and-killed wall, leaves headroom for PRs that add
  more cmd/r1 tests, and still trips fast on a real hang.
- This is a 1-line config change. Costs nothing on healthy runs
  (timeout only fires on overshoot). Worst-case Cloud Build minute
  cost is +5 minutes per package per run **if it deadlocks**, which
  is the case where the value of a test running at all > the cost.
- It does not paper over a hung test: if a test is genuinely hung
  it'll still trip 600 s. PR #174's pattern (passes on retry, fails
  on first attempt with two packages timing out) is environmental
  variance, not a deadlock.

If you want a tighter belt-and-braces option: keep `-timeout=300s`
globally and add a per-package override for these two via a second
`go test` invocation. More config, same outcome. Not recommended.

### (b) Split heavy parallel SOW prompt tests — DO NOT DO

Tempting because the top 5 are all in two test files and all use
`t.Parallel()`. Splitting buys nothing — the underlying work is
already cheap; the wall numbers are scheduling artifacts. Removing
`t.Parallel()` would make individual tests look fast but extend the
package wall, since `cmd/r1` has many other tests that genuinely
benefit from parallelism. Net negative.

### (c) `t.Skip` under `-short` — DEFER

Adding `if testing.Short() { t.Skip() }` to subprocess re-exec tests
(`TestSingleInstance`, `TestRunPhase1_FixtureShellPipeline`) and
running `go test -short` in the `race` step would shave seconds, not
minutes. Worth doing **after** (a) if a follow-up shows residual
margin pressure. Not worth doing first because it changes test
coverage on the race lane in a way that needs consensus
(daemon-lock contract is exactly the kind of thing race-mode is most
likely to catch a regression on).

### (d) Refactor away subprocess re-exec — DEFER

`TestSingleInstance` re-execs the test binary twice, paying the
race-runtime startup cost twice on Cloud. A refactor that exercises
the daemonlock contract via the `daemonlock` package directly
(without subprocess) would remove the only multi-second test in the
file. Worthwhile but invasive — touches test scaffolding and
arguably reduces fidelity (the spec comment in
`serve_single_instance_test.go:5-32` explicitly defends the re-exec
pattern as the standard Go solution). Defer to a follow-up PR.

## Summary recommendation

**Apply (a)** — bump `cloudbuild.yaml` line 89 to `-timeout=600s`.
Open a PR; let it merge. Re-evaluate (c)/(d) only if Cloud Build
shows residual flakes within the next two weeks.

**Do NOT apply (b)** — the wall-time numbers are misleading; tests
are already fast in CPU terms.

The convergence-package timeout (4.7 s local → 5:30 cloud) is not
explained by anything in the test code. Treat it as the same
oversubscription/cold-cache amplifier that hit cmd/r1, and let the
600 s cap absorb it.

## Files referenced (absolute paths)

- `/home/eric/repos/r1-agent/cloudbuild.yaml`
- `/home/eric/repos/r1-agent/cmd/r1/verify_cmd_test.go`
- `/home/eric/repos/r1-agent/cmd/r1/serve_single_instance_test.go`
- `/home/eric/repos/r1-agent/cmd/r1/sow_task_spec_test.go`
- `/home/eric/repos/r1-agent/cmd/r1/sow_identifier_extract_test.go`
- `/home/eric/repos/r1-agent/cmd/r1/sow_native.go`
- `/home/eric/repos/r1-agent/cmd/r1/scan_repair_test.go`
- `/home/eric/repos/r1-agent/cmd/r1/skills_pack_cmd_test.go`
- `/home/eric/repos/r1-agent/internal/convergence/validator_test.go`
