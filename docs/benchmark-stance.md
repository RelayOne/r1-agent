# Benchmark stance

**Short version:** Stoke does not report SWE-bench Verified scores. We track
SWE-bench Pro, SWE-rebench, and Terminal-Bench because Verified is
contaminated and its top-line numbers no longer mean what they used to.

## Why Verified is out

OpenAI formally stopped reporting SWE-bench Verified scores in
February 2026 after demonstrating that every frontier model can
reproduce gold patches verbatim — a textbook training-data
contamination signal. Top published scores cluster at ~80% with
10–20 points of observed variance driven by *scaffold tricks* rather
than model capability: agent harness tuning, retrieval heuristics,
test-time compute knobs. A harness that over-indexes on Verified is
selling you its tuning against a known-leaked corpus, not the thing
you actually want (fewer bugs shipped on your real codebase).

Concretely, relying on Verified scores in mid-2026 marketing
signals one of two things:

- The author hasn't caught up with the contamination disclosure and
  the OpenAI retraction.
- The author is knowingly quoting a metric whose headline number is
  no longer a fair comparator.

Neither is a posture Stoke wants.

## What we track instead

Three benchmarks that each attack a specific weakness of Verified.

### SWE-bench Pro (Scale AI)

Reported mid-2025 as a refresh of the original SWE-bench with harder
tasks, tighter grading, and far less contamination exposure.
Published top-model scores sit in the 23–59% band — an order of
magnitude closer to the real-world failure rates we see in
production. Microsoft's internal .NET corpus (closest public proxy
for "big internal monorepo") reports ~68% on their best runs. Those
numbers match what we observe on Stoke's own bench corpus, which is
the sanity check we want a benchmark to provide.

### SWE-rebench (Nebius)

Built around periodic corpus refresh so models can't memorize the
ground truth by training cutoff alone. Useful as a *delta* signal:
"does this harness improvement hold up when the corpus rotates?" If
it does, we believe the improvement. If it doesn't, we've over-fit.

### Terminal-Bench (Stanford / Laude)

Terminal-native tasks — the harness has to actually execute and
observe shell commands, not just propose patches. Catches a class of
failure that patch-only benchmarks can't see: agents that produce
code that compiles but doesn't actually work when you run it. This
is the benchmark closest to what Stoke's verify stage does every
commit.

## Why this matters for harness engineering

Stoke's central design claim is that *the harness is the product*.
SWE-bench Pro supports that directly: the same base model moves ~15
points when the scaffold is tuned. Terminal-Bench supports it from
the other side: the same model can pass a Verified task and fail its
terminal equivalent because the scaffold didn't actually *run the
code*. A harness team that reports Verified numbers without Pro or
Terminal-Bench comparators is either missing the point or hiding it.

## What a benchmark claim from Stoke looks like

- We publish the base-model-vs-harness delta, not an absolute score.
  "Sonnet 4.6 + Stoke at the reviewer seat is 14 points above Sonnet
  4.6 alone on SWE-bench Pro" is a useful claim. "Stoke gets 81% on
  SWE-bench Verified" is not.
- We publish the harness configuration that produced the number:
  builder model, reviewer model, worker count, session parallelism,
  cache-breakpoint discipline. Without that, the number is
  unreproducible.
- We publish negative results when the delta is flat or negative.
  "This change looked good in synthetic tests and cost us 3 points on
  SWE-rebench" is the kind of data that builds trust in the delta
  methodology.

## What Stoke commits to

- No Verified scores in README, marketing, or external comms.
- Delta-vs-baseline reporting on Pro / rebench / Terminal-Bench in
  any published benchmark claim.
- Harness configuration recorded with every claim.
- Negative results published alongside positive ones.

That is the entirety of the benchmark stance. It is intentionally
short because the underlying commitment is simple: stop quoting a
contaminated number and start measuring the thing the product
actually does.
