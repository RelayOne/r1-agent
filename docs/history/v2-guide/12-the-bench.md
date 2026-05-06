# 12 — The Bench

The bench is Stoke's self-evaluation infrastructure. It is how we answer "does this actually work" with evidence rather than vibes. Every rule in the supervisor, every default threshold in the wizard, every tradeoff between cost and completeness — all of them are choices, and the bench is where those choices get validated against real missions or rejected when they make things worse.

The bench is not a generic benchmark framework. It is specific to Stoke, and it takes advantage of everything the substrate already provides. Every mission produces a queryable ledger and a durable bus event log; the bench is mostly "run these missions, query the resulting ledgers and event logs for the metrics that matter." The measurement infrastructure is the ledger and bus themselves. The bench adds a runner, a golden mission set, a comparison layer against baselines, and a reporting surface. That is all.

This file specifies what the bench is, what it measures, how it runs, and how it is used to validate changes to Stoke over time.

---

## What the bench is

The bench is a package at `internal/bench` plus a CLI command at `stoke bench`. It has three components:

**The golden mission set** — a versioned collection of test missions covering the span of what Stoke is expected to handle. Each mission is a directory containing a fresh git repo (or a git tarball), a user intent written as a prompt, an acceptance criteria spec, and optionally a reference solution that human reviewers have validated. The golden set is checked into the repo at `internal/bench/golden/` and evolves with Stoke — new failure modes in production become new golden missions that prevent regression.

**The runner** — a Go program that, given a mission from the golden set, creates a sandboxed working copy, invokes Stoke on it, waits for the mission to reach a terminal state (converged, escalated, or timed out), and collects the resulting ledger and bus event log. The runner is deterministic in the sense that it records inputs and captures outputs; it is not deterministic in the sense that LLM responses are non-deterministic, so the bench is a statistical tool, not a unit-test replacement.

**The metrics and reporting layer** — queries against the collected ledger and bus event log that compute the measurements we care about, plus a comparison mode that runs the same mission against a baseline (bare Claude Code, Cursor, or a prior version of Stoke) and reports relative performance.

---

## The golden mission set

The golden set is the ground truth. If a change to Stoke causes a regression on a golden mission, that is evidence the change is wrong — not evidence the golden set is wrong. Golden missions are added carefully and removed rarely.

Each golden mission is a directory with the following shape:

```
internal/bench/golden/mission-name/
├── mission.yaml          # metadata: title, description, difficulty, category
├── intent.md             # the user's prompt as it would be delivered to Stoke
├── repo.tar.gz           # the initial state of the repository
├── acceptance.yaml       # testable acceptance criteria
├── reference-solution/   # optional: a human-validated solution for comparison
│   └── ...
└── known-pitfalls.md     # documented failure modes this mission has caught
```

Categories of missions in the golden set:

- **Greenfield** — empty repo, user asks for a new application. Tests PRD → SOW → ticket → implementation flow on an unconstrained substrate. No snapshot. Primary measurement: does Stoke produce something that meets the acceptance criteria, and at what cost.

- **Brownfield refactor** — repo with existing code, user asks for a refactor that touches snapshot code. Tests CTO consultation, snapshot defense, "does Stoke push back on unmotivated changes while allowing smart ones." Primary measurement: does the CTO correctly allow justified refactors and block unjustified ones.

- **Bug fix** — repo with a failing test, user asks for a fix. Tests focused execution, Reflexion-style fix loops, the trust rule for fix completion. Primary measurement: does Stoke fix the bug without introducing regressions, and does the fresh-context Reviewer catch attempts to paper over the failure.

- **Multi-branch coordination** — mission that naturally decomposes into parallel branches with some cross-branch coordination required. Tests SDM, branch supervisors, parent-agreement on completion, the cross-team rule. Primary measurement: does the SDM catch collisions before they ship.

- **Impossible or ambiguous** — mission that is actually infeasible, or that is underspecified enough that Stoke should escalate to the user for clarification rather than guess. Tests the trust rule for problem claims, the Judge's drift detection, the escalation path. Primary measurement: does Stoke escalate rather than hallucinate a solution, and does it escalate with a useful question rather than a generic "I can't do this."

- **Long-horizon** — mission that requires sustained work over many consensus loops and is likely to trigger drift. Tests the Judge's drift detection rules, intent alignment checks, budget thresholds. Primary measurement: does Stoke stay on track, and does it catch itself drifting when it starts to.

- **Known footgun** — mission that contains a specific pitfall the skill library is supposed to know about. Tests whether the skill manufacturer's shipped skills actually get loaded into the right stances at the right time. Primary measurement: does the relevant skill get used, and does the stance avoid the footgun.

The golden set is versioned alongside the code. A commit that adds a new rule to the supervisor must also add or update the golden mission that exercises the new rule. A commit that changes a default threshold must include bench results showing the new threshold performs no worse on existing golden missions.

---

## What the bench measures

The metrics are queries against the collected ledger and bus event log. They are grouped by what they tell us.

### Mission-level outcomes

- **Terminal state.** For each mission: did it converge, escalate, or time out? Converged missions are compared against their acceptance criteria for correctness.
- **Acceptance criteria satisfaction.** Percentage of acceptance criteria the terminal output satisfies. Human review is required for the criteria that cannot be automated.
- **Wall-clock time to terminal.** From mission start to final state transition.
- **Cost to terminal.** Tokens across all model families, dollars converted via the wizard's cost table, wall time on the harness.

These are the top-level "did Stoke work" metrics. Everything else is diagnostic.

### Trust rule effectiveness

- **Done-declaration rejection rate.** How often does a fresh-context Reviewer dissent on a worker's `done` claim? High rates indicate either over-eager workers or over-strict reviewers; both are worth knowing. The wizard's rule strength controls the knob; the bench measures the effect.
- **Fix-completion regression rate.** How often does a fresh-context Reviewer catch that a claimed fix did not actually fix the issue, or introduced a new one? This measures whether the Reflexion-style fix rule is earning its cost.
- **Premature-impossible rejection rate.** How often does a fresh-context Reviewer send a `task.infeasible` escalation back to the worker because the worker gave up too early? Direct measurement of the premature-termination failure mode.
- **Trust rule cost overhead.** What fraction of total cost is spent on the fresh-context second opinions the trust rules force? This is the price of the trust rules, and it should be meaningful but not dominant.

### Consensus loop mechanics

- **Iterations to convergence.** Distribution across loop types. A PRD loop that usually converges in 2 iterations and suddenly starts taking 5 is a signal something regressed.
- **Dissent density.** Dissents per draft, by loop type. High dissent density on a specific loop type suggests the proposing stance is not getting the concern field it needs, or the convened partners are mis-configured.
- **Judge invocation rate.** How often does the iteration threshold rule or a drift detection rule trigger a Judge? High rates indicate loops are struggling under normal conditions; low rates indicate loops are converging cleanly.
- **Judge verdict distribution.** Of the four verdicts, which does the Judge produce most often? A high `escalate_to_user` rate means the Judge is often seeing genuinely impossible situations; a high `switch_approaches` rate means stances are going down dead ends; a high `keep_iterating` rate means the Judge is invoked unnecessarily early.
- **Partner timeout rate.** How often does `consensus.partner.timeout` fire? Partner timeouts are a signal either of infrastructure problems (a model family is down) or of genuinely hard questions that take partners a long time to answer.

### Research mechanism

- **Research request rate.** How often does any stance emit `worker.research.requested`? This measures how much uncertainty stances are encountering.
- **Research report confidence distribution.** Of the reports that come back, what fraction are `high`, `medium`, `low`, `inconclusive`? An `inconclusive` report is the researcher being honest about not finding the answer; a high rate of inconclusive reports means the questions are hard or the tool authorization is insufficient.
- **Research-to-decision latency.** How long from research request to report delivery to the requesting stance unpausing? The mechanism should be fast enough that research does not dominate mission wall time.
- **Post-research dissent rate.** When a stance does work with research support, does its subsequent draft get fewer dissents than when it works without research? This is the measurement that justifies the research mechanism's existence.

### Snapshot defense

- **CTO consultation rate.** How often does `snapshot.modification.requires_cto` fire? This depends heavily on the mission type; greenfield missions should have near-zero, brownfield refactors should have frequent consultations.
- **CTO approval vs denial rate.** Of CTO consultations, what fraction end in approve, deny, or escalate-to-user? A CTO that always approves is not defending the snapshot; a CTO that always denies is blocking legitimate work. Both extremes are diagnostic.
- **User override rate.** When the CTO escalates a snapshot modification to the user, how often does the user override the CTO's concern? Frequent overrides mean the CTO's threshold is too strict; rare overrides mean the CTO is well-calibrated.

### SDM effectiveness

- **Collision detection rate.** How often does the SDM emit `sdm.collision.detected` advisories on multi-branch missions?
- **Advisory action rate.** When an advisory is emitted, how often does the mission supervisor or affected branch supervisor take coordinating action as a result?
- **Missed collisions.** Missions that produced a merge conflict or a cross-branch bug that the SDM did not catch in advance. This is the SDM's miss rate and the primary signal for tuning its detection rules.

### Skill effectiveness

- **Skill load rate.** Frequency of each skill being loaded into a stance's concern field. Skills that are never loaded are candidates for retirement.
- **Skill-driven wins.** Cases where a skill was loaded and the stance's subsequent work shows evidence of having applied it (via the footgun-avoidance pattern in the relevant golden missions).
- **Skill-driven losses.** Cases where a skill was loaded and the stance's subsequent work shows evidence of misapplying it — following a pattern that was inappropriate for the specific situation. These feed back into the skill manufacturer's lifecycle management.
- **Confidence tier outcomes.** Comparison of mission outcomes when `proven` skills are loaded vs `tentative` vs `candidate`. The confidence tiers should predict outcome quality; if they do not, the skill manufacturer's validation logic needs work.

### Drift detection

- **Time-to-drift-catch.** When a loop has drifted from the original intent, how long after the drift begins does the drift rule fire and bring the Judge? Fast is good; if drift is caught only at milestone boundaries, the mission has already spent significant cost on off-track work.
- **Budget threshold trigger distribution.** How often does each threshold fire? A 120% hard stop that never fires is either well-calibrated or too permissive; a 50% warning that always fires is either too strict or genuinely useful as an early signal.

### Cost and performance

- **Tokens per mission.** Distribution across golden mission categories.
- **Model family distribution.** Of the total tokens, what fraction comes from each model family? The wizard's rule strength controls cross-family diversity; the bench shows the cost.
- **Parallelism utilization.** For multi-branch missions, how often are multiple workers actually running in parallel, and what is the wall-time speedup vs sequential?
- **Pause overhead.** Total wall time workers spent paused (waiting for research, waiting for second-opinion reviews, waiting for CTO consultation) as a fraction of total wall time. Pauses are valuable but they are also dead time; the bench shows the tradeoff.

---

## Baselines

The bench compares Stoke against baselines so that "Stoke got this mission right" is meaningful. Baselines include:

- **Bare Claude Code** — a single agent session with no Stoke infrastructure. The user's prompt goes directly to the model and the model produces a solution in one session. This is the "what does the raw model do" baseline.

- **Stoke without trust rules** — Stoke with the trust rules disabled (except where the validation gate makes them non-disable-able). This measures the cost and quality contribution of the trust rules specifically.

- **Stoke without research** — Stoke with the research rules disabled, so uncertain stances just proceed with their current information. This measures the value of the research mechanism.

- **Stoke without skills** — Stoke with an empty shipped skill library. Measures the value of the shipped skills.

- **Prior Stoke version** — the previous tagged release. Regression detection — any change that makes a golden mission worse than the prior version is flagged.

Baselines are not expected to match Stoke. The point is to measure the delta. A bare-Claude-Code baseline that converges on 30% of greenfield missions and a Stoke configuration that converges on 70% means Stoke is adding 40 points of value on greenfield; it does not mean bare Claude Code is broken. The absolute numbers are useful for trend-watching; the deltas are the structural claim.

---

## The runner

The runner is a Go program invoked as `stoke bench run`. Its loop is:

1. Read the golden mission set from `internal/bench/golden/`
2. For each mission, create a sandboxed working directory by unpacking `repo.tar.gz` into a fresh temporary directory
3. Run Stoke against the working directory with the mission's `intent.md` as the user prompt
4. Wait for Stoke to reach a terminal state (or time out — the default timeout is 1 hour per mission, configurable)
5. Collect the resulting ledger (everything under `.stoke/ledger/`) and the bus event log (everything under `.stoke/bus/`) into an archive named by mission and timestamp
6. Run the metrics queries against the archive
7. Compare acceptance criteria against the final repo state (automated where possible, flagged for human review where not)
8. Produce a report

The runner supports parallel execution — multiple missions can run in separate sandboxes concurrently, bounded by the total cost budget the user provides. For a bench run against 20 golden missions at 4-way parallelism, the wall time is roughly 5 mission-durations instead of 20.

The runner never modifies a golden mission in place. Every run produces a fresh sandbox, and every result is recorded in `internal/bench/results/{date}-{run-id}/` for later comparison. Old results are retained — the time-series of bench results is how regression detection works.

---

## Regression detection

The bench's CI role is to catch regressions before they ship. The check is:

1. On every commit to main (and every PR to main), run the bench against a configured subset of the golden mission set (running all golden missions on every commit is too expensive; a representative subset runs on every commit, the full set runs nightly)
2. Compare the results against the baseline of the prior successful run on main
3. Flag any golden mission whose outcome regressed — converged-to-escalated, escalated-to-timeout, acceptance-criteria-satisfaction dropped, cost increased by more than a configured threshold
4. Fail the build if any flagged regression exceeds the severity threshold

The severity threshold is configurable. A minor cost regression (5% more tokens) may be acceptable if accompanied by a quality improvement; a major regression (20% more tokens with no quality change) fails the build. The wizard controls the threshold per category.

Regression detection is statistical. LLM responses are non-deterministic, so a single mission run can fail by chance. The bench runs each golden mission N times (default 3) and compares distributions. A mission is considered regressed only if the distribution has shifted meaningfully, not if a single run dropped.

---

## Tradeoff exploration

Beyond regression detection, the bench is a tool for exploring tradeoffs. "What happens if we lower the iteration threshold for PR review loops from 3 to 2?" is answered by running the bench with both configurations and comparing the results. The bench's report layer supports side-by-side comparison of runs with different configurations, showing deltas on every metric.

Common tradeoff explorations:

- **Rule strength.** The wizard's rule strength knobs (partial strength, full strength) can be swept to find the cost-quality frontier.
- **Threshold tuning.** Iteration thresholds, budget thresholds, partner timeouts — all configurable, all bench-measurable.
- **Skill library composition.** Adding, removing, or modifying skills can be validated against the footgun missions in the golden set.
- **Model family selection.** The harness's model routing can be changed, and the bench measures the impact on cost and quality.

These explorations are not meant to happen in production. They run in a dev loop, on a contributor's machine or a CI server, against the golden set. The winning configurations get committed as new defaults in the wizard.

---

## Package structure

```
internal/bench/
├── golden/
│   ├── greenfield/
│   ├── brownfield/
│   ├── bugfix/
│   ├── multi-branch/
│   ├── impossible/
│   ├── long-horizon/
│   └── footgun/
├── runner/
│   ├── runner.go
│   ├── sandbox.go
│   └── runner_test.go
├── metrics/
│   ├── outcomes.go
│   ├── trust.go
│   ├── consensus.go
│   ├── research.go
│   ├── snapshot.go
│   ├── sdm.go
│   ├── skills.go
│   ├── drift.go
│   ├── cost.go
│   └── metrics_test.go
├── baselines/
│   ├── bare_claude.go
│   ├── no_trust.go
│   ├── no_research.go
│   ├── no_skills.go
│   ├── prior_version.go
│   └── baselines_test.go
├── reporter/
│   ├── report.go
│   ├── compare.go
│   ├── regression.go
│   └── reporter_test.go
└── results/
    └── {date}-{run-id}/
```

The CLI commands exposed:

- `stoke bench run` — run the golden set and produce a report
- `stoke bench run --subset greenfield,bugfix` — run a subset
- `stoke bench run --config path/to/override.yaml` — run with a configuration override for tradeoff exploration
- `stoke bench compare run-a run-b` — compare two prior runs
- `stoke bench regression --baseline main` — detect regressions against the last successful run on main
- `stoke bench golden add path/to/mission` — add a new golden mission with validation

---

## What the bench does not do

- **Evaluate the quality of generated code by itself.** For parts of acceptance criteria that require subjective judgment (is the code well-structured, is the naming clean), the bench flags results for human review rather than auto-scoring them. The bench measures what is measurable and delegates the rest.

- **Replace unit tests.** The bench is statistical and expensive. Unit tests on the supervisor's rules, the ledger's validation, the bus's ordering — these all run in milliseconds and must pass on every commit. The bench runs in minutes to hours and supplements the unit tests, it does not replace them.

- **Guarantee non-regression.** The bench catches the regressions it is designed to catch. A failure mode not represented in the golden set can still slip through. Adding new golden missions as new failure modes are discovered is an ongoing process, not a one-time setup.

- **Run in production.** The bench is a development tool. It does not run on a user's missions; it runs on the golden set in a controlled environment. User missions have their own telemetry (covered elsewhere in the runtime), not bench measurements.

- **Measure anything not in the ledger or bus.** If a property matters and the bench needs to measure it, the property has to produce ledger nodes or bus events. This is a constraint on the rest of Stoke — anything that matters must be observable, and the bench is where observability pays off.

---

## Validation gate

Before the bench can be trusted as a validation tool, it has to pass its own validation gate:

1. ✅ `go vet ./...` clean, `go test ./internal/bench/...` passes with >70% coverage
2. ✅ `go build ./cmd/r1` succeeds and the `stoke bench` subcommands are reachable
3. ✅ The runner can execute a single golden mission end-to-end in a sandboxed working directory without polluting the host filesystem
4. ✅ The runner correctly collects the ledger and bus event log from the working directory into the results archive
5. ✅ The runner recovers cleanly from a mission that times out (the mission is recorded as timed-out, the sandbox is cleaned up, subsequent missions in the batch are not affected)
6. ✅ The metrics queries produce correct results on a synthetic ledger + bus log with known properties (verified by test fixtures)
7. ✅ Every metric defined in this file has a corresponding query in `internal/bench/metrics/` with its own unit test
8. ✅ The golden mission set has at least one mission per category, with a known reference outcome that the bench can verify
9. ✅ Running the bench on a known-good commit produces results that match a recorded baseline (deterministic for the non-LLM parts of the measurement pipeline)
10. ✅ The regression detection correctly flags a synthetic regression introduced in a test scenario
11. ✅ The regression detection correctly does not flag a random fluctuation within the configured noise tolerance
12. ✅ Parallel execution of multiple missions in separate sandboxes produces the same results as sequential execution (no cross-mission contamination)
13. ✅ `stoke bench golden add` validates the new mission's structure (mission.yaml, intent.md, acceptance.yaml, repo.tar.gz all present and well-formed) before accepting it
14. ✅ The baselines can be executed alongside Stoke for side-by-side comparison
15. ✅ The reporter produces a human-readable report including the metrics, the comparison against baselines, and any flagged regressions
16. ✅ The validation gate is committed to `STOKE-IMPL-NOTES.md`

---

## Forward references

This file is component 12 of the new guide. The only component that comes after it is component 13 (implementation order and validation gates), which sequences the build of all components including this one. The bench references every prior component because it measures every prior component's behavior, so the forward references here run backward to everything already specified rather than forward to anything not yet written.

The next file to write is `13-the-implementation-order.md`. That file sequences the build of Stoke: which component comes first, what validation gates have to pass before the next component can be built on top of it, and the overall phases of the project from "empty repo" to "shippable v1." It threads the per-component validation gates from components 2–12 into an execution plan.
