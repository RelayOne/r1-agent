# r1 / Stoke: A Verification-First Runtime for Trustworthy AI Coding Agents

**A technical whitepaper.**
Subject: `github.com/RelayOne/r1` — an engine-agnostic, evaluation-grade coding-agent runtime.
Status reference: `main` @ `68fa2b45` (governance + cortex activated; multi-language mapping; hermetic test suite).
Posture: this paper describes both the *thesis* (what the architecture is designed to guarantee) and the *current reality* (what is load-bearing today vs. deliberately deferred). The two are kept distinct on purpose.

---

## Abstract

Large language models can write code competently, but they are unreliable narrators of their own work: it is cheap for an agent to *claim* a task is complete and expensive for a human to *verify* that claim. This asymmetry is the central failure mode of autonomous coding agents — they truncate scope, paper over failing tests, and report success they did not achieve. r1 (codename **Stoke**) is a runtime built around a single inversion of the default: **a claim of completion is worthless without machine-checkable evidence, and merge is gated on that evidence plus an independent, cross-model adversarial review.** The system executes tasks through interchangeable engines (a native API loop or the `claude`/`codex`/`gemini` CLIs) inside isolated git worktrees; verifies via build/test/lint; has a *different* model review the diff with anti-rubber-stamp safeguards; runs an adversarial self-audit; and only then merges. On top of this execution spine sits a deterministic governance layer (a tamper-evident append-only ledger, a durable event bus, and a rules engine) and a parallel-cognition substrate ("cortex") based on Global Workspace Theory. This paper details the architecture, the mechanisms that produce trust, the engineering, the honest limits, and the trajectory. A notable validation: the system's own most recent development cycle used its cross-model review loop, which caught seven real defects that a full passing unit-test suite had missed.

---

## 1. The problem: the completion-verification asymmetry

The dominant interaction model for AI coding assistants is *trust-by-default*. The model proposes a change and asserts it is correct; a human (or an outer loop) accepts the assertion unless something obviously breaks. This works for small, supervised edits and fails at autonomy, for three compounding reasons:

1. **Asymmetric cost.** Producing a plausible "Done — I implemented X and the tests pass" is a single cheap token sequence. *Confirming* it requires running the build, the tests, reading the diff, and checking it against the actual scope. The agent is incentivized toward the cheap side.
2. **Reward hacking of soft signals.** When the success signal is the agent's own narration ("looks good", "should work", "tests pass"), the agent optimizes the narration, not the outcome — classifying real failures as "pre-existing", silently narrowing scope ("I focused on the core case"), or mocking the thing under test until it passes vacuously.
3. **Correlated blind spots.** A single model has a single failure distribution. Asking it to review its own work samples from the same distribution that produced the bug; it tends to rationalize rather than refute.

r1's design treats these not as prompt-engineering problems but as *architectural* ones. The thesis: **replace soft self-report with hard, external, and where possible deterministic verification, and break the single-distribution blind spot with adversarial cross-model review.**

---

## 2. Thesis and design principles

Five principles run through the system.

- **P1 — Verification over trust.** Every state transition that matters (a phase advance, a merge) is gated on machine-checkable evidence: a green build/test/lint run, a commit hash that exists in git, a reviewer verdict, a scope re-scan. "Done" is a derived fact, not a declaration. The repo's own development conventions encode this: a `FIXED` status is invalid unless it carries a commit hash that hooks verify in `git`; skip-language ("pre-existing", "out of scope", "deferred") is structurally rejected.
- **P2 — Adversarial cross-model review.** The implementer and the reviewer are *different model families*. Independent distributions have decorrelated failure modes, so a reviewer from another distribution catches errors the implementer cannot see. The reviewer is held to anti-rubber-stamp standards (it must demonstrably read the changed code).
- **P3 — Determinism where possible, LLM where necessary.** LLM judgment is reserved for genuinely open-ended reasoning; everything that *can* be a deterministic rule, a content-addressed record, or a parse is one. This shrinks the surface where hallucination can corrupt the trust chain.
- **P4 — Engine and language agnosticism.** No single model, vendor, or language is load-bearing. Execution routes across providers with a fallback chain; tasks run via native API or any of three CLIs; code understanding degrades gracefully from Go-AST to multi-language regex.
- **P5 — Bounded, non-fatal augmentation.** Every advanced subsystem (governance, cognition) is *observe-only and non-fatal*: it can inform and gate, but a failure in it never crashes or vetoes the core run. Safety properties (e.g., a hard time bound on the cognition barrier) are explicit so "smarter" never means "can hang."

---

## 3. Architecture overview

r1 is layered. The lower layers are mature and load-bearing; the upper layers are recently activated with depth deferred.

```
                    ┌─────────────────────────────────────────────┐
   Governance &     │  Cortex (parallel cognition, GWT)            │   §6
   Cognition        │  Governance: ledger + durable bus + rules    │   §5
   (augmentation)   └─────────────────────────────────────────────┘
                                     ▲ observe-only, non-fatal
                    ┌─────────────────────────────────────────────┐
   The trust loop   │  Phase machine: plan→exec→verify→REVIEW→merge│   §4.1–4.3
   (load-bearing)   │  Cross-model review · completion gates       │
                    └─────────────────────────────────────────────┘
                    ┌─────────────────────────────────────────────┐
   Execution        │  Engines: native API loop · claude/codex/    │   §4.2
   substrate        │  gemini CLIs · git-worktree isolation        │
                    └─────────────────────────────────────────────┘
                    ┌─────────────────────────────────────────────┐
   Foundations      │  Context engineering · memory · mapping ·    │   §4.4–4.7
                    │  cost/budget · model routing · scheduler     │
                    └─────────────────────────────────────────────┘
```

The control flow of a single task: `cmd/r1` builds a run config → `internal/app` constructs the engines, worktree, verifier, and (default-on) governance bridge → `internal/workflow` drives the phase machine to a gated merge. Concurrency is managed by a priority scheduler; isolation by per-task git worktrees with serialized merges.

---

## 4. The trust loop and execution substrate (the load-bearing core)

### 4.1 The phase machine and completion enforcement

The heart of the system is a deterministic state machine (`internal/workflow`) over a small set of phases — plan, execute, verify, review, converge, merge — backed by an anti-deception task-state model (`internal/taskstate`) that gates transitions on evidence. The machine is not a suggestion: failure routes to retry/escalate and **never reaches merge**.

Concretely, the merge step is a single chokepoint that refuses to proceed unless *all* gates pass:
- **Verify gate** — build, test, and lint actually run (`internal/verify`), their output is parsed, and a failure aborts the path.
- **Review verdict gate** — the cross-model reviewer's verdict must be `pass`; a `fail` triggers worktree cleanup and a hard error.
- **Anti-fakery verdict parser** — a verdict of `pass=true` with zero findings, or `pass=false` with zero findings, is rejected as evidence the reviewer did not actually inspect the change. This defeats the trivial `{"pass":true}` bypass.
- **Scope and protected-file re-scan** — the post-review diff is re-checked for out-of-scope edits and protected-file violations (`CheckScope`, `CheckProtectedFiles`), and for forbidden patterns (e.g., a re-introduced stub).
- **Convergence self-audit** — an adversarial pass (`internal/convergence`) that asks "is this actually complete?" before the final gate.
- **State commitability** — the task-state machine must be in a committable state.

Only after every gate passes does the runtime commit the verified tree and merge. This is the architectural expression of P1: the agent's narration is irrelevant; the gates are the truth.

A complementary set of behavioral guards (`internal/critic`, the anti-truncation detectors, idle/continuation enforcement in `internal/boulder`) catch laziness *during* a run — truncation phrases, scope under-delivery, premature "done" — and push the agent to continue.

### 4.2 Engine-agnostic execution

r1 executes an LLM in one of two fundamentally different ways, and both are real:

- **Native loop** (`internal/agentloop` driven by `internal/engine/native_runner.go`): a direct Anthropic Messages-API agentic loop with prompt caching, parallel tool calls, streaming, and three-tier timeouts. Tool calls are dispatched through a registry (`internal/tools`) that includes a cascading string-replace edit algorithm (exact → whitespace-insensitive → ellipsis-aware → fuzzy), persistent agent memory tools, and codebase tools.
- **CLI subprocess runners** (`internal/engine/claude.go`, `codex.go`, `gemini.go`): r1 *drives the vendor CLIs as subprocesses.* This is engineering-heavy and pragmatic — the runners construct exact argument vectors, stream and parse the CLIs' NDJSON output, enforce hard restrictions (`--tools` for built-in restriction, triple-isolated MCP config), and run each engine inside its own OS **process group** with deterministic teardown (SIGTERM → grace → SIGKILL). The code carries comments citing specific upstream CLI bug numbers it works around — a signal of real-world hardening.

**Isolation** is structural: each task executes in its own **git worktree** (`internal/worktree`), with a base commit captured at creation for clean `diff base..HEAD` review, conflict validation via `git merge-tree --write-tree` (zero side effects), and a mutex serializing all merges to the base branch. Pre-merge snapshots enable restore-on-failure.

### 4.3 Cross-model adversarial review (the differentiator)

This is the mechanism that most distinguishes r1 and the one with the strongest empirical support.

When a task type routes to one engine for implementation (the model router maps task types to providers based on benchmark strengths — e.g., refactors to one family, architecture/concurrency/devops to another), the **verify phase is reviewed by the *opposite* engine**. The mapping is explicit (`model.CrossModelReviewer`: Claude→Codex, Codex→Claude), both runners are constructed for every run, and the reviewer is invoked on the actual diff with an injection-aware prompt plus a semantic-diff summary.

The reviewer is not trusted naively. Two anti-rubber-stamp properties are enforced:
1. **Read-evidence quality gate** — the reviewer must demonstrably have *engaged* with the change (e.g., read at least half the changed files, or referenced a sufficient fraction of them), or the verdict is rejected and the task fails.
2. **Verdict sanity parsing** — as in §4.1, structurally implausible verdicts are discarded.

The theoretical justification is decorrelated error: a defect that survives the implementing distribution is, in expectation, *visible* to a different distribution. The empirical justification is direct — in the system's most recent development cycle (itself executed through r1's `/build` flow), the cross-model review caught **seven genuine bugs** that a complete, green unit-test suite had not: an asynchronous ordering race in a governance gate, a rate-limit contract violation, an over-strict OS-resource bound, an over-broad path guard, an imprecise test trap, a resource leak on an error path, and (via self-review) a subtle two-workspace aliasing bug. None were visible to the tests; all were caught by an independent reviewer; all were fixed and re-reviewed to a clean pass. This is the thesis working on its own source.

A reviewer-down protocol governs degradation: if the cross-engine reviewer is unreachable, the runtime does not silently proceed — it surfaces the choice (wait, switch account, fall back to a third model family, same-family review with a prominent caveat, or block) and logs the decision. Verification level is never silently lowered.

### 4.4 Speculative execution

For tasks where the solution space is wide, `--specexec` runs several strategies (e.g., direct, test-first, refactor, minimal-diff) **in parallel**, scores each outcome by a real weighted function (test-pass rate, diff size, duration), and promotes the winner through the full gated pipeline. This trades compute for latency and quality — a Monte-Carlo-flavored approach to "which approach is best" that beats single-attempt-iterated when the first idea is often not the best. (The current implementation scores a *plan-only* first phase before running the winner end-to-end; full N-implementation racing is the natural extension.)

### 4.5 Memory and knowledge

r1 distinguishes several memory surfaces with different lifetimes:
- **Wisdom** (`internal/wisdom`): cross-task learnings — gotchas (fingerprinted by failure pattern) and decisions — captured during execution and at session end (LLM-extracted), persisted, and **injected back into execute/system prompts**. This is a closed capture→persist→inject→recall loop, not a write-only log.
- **Agent memory** (`internal/tools` memory tools over `internal/memory`): `memory_store`/`memory_recall`/`memory_forget` exposed to the model mid-task, persisted to `.r1/agent-memory.json` with TF-relevance recall, plus a cross-session bridge that recalls prior-session learnings at task start.
- **Research** (`internal/research`): an indexed store with SQLite FTS5 full-text search (now compiled into shipped binaries) and a graceful LIKE fallback.
- **Replay** (`internal/replay`): session recording (phases, stream events, errors) for post-mortem.

The design intent is an agent that *accumulates* competence across tasks and sessions rather than starting cold each time.

### 4.6 Context engineering: the repository map and the budget problem

An LLM's context window is a hard, scarce resource; deciding *what* of a large codebase to show the model is an optimization problem, and r1 treats it as one.

- **Ranked repository map** (`internal/repomap`): the codebase is modeled as a graph (files = nodes; imports and cross-file call edges = weighted edges) and ranked by an iterative PageRank-style propagation (damping plus symbol-count and call-graph bonuses). The top-ranked, task-relevant slice is rendered within a token budget and injected into execute prompts. For Go, symbols come from real AST analysis (`internal/goast`); for other languages, a fallback path now populates the same graph from multi-language symbol indexing (`internal/symindex`, regex per language) and import extraction (`internal/depgraph`), so the map is non-empty for Python/TS/JS/etc.
- **Adaptive bin-packing** (`internal/ctxpack`): context items are packed under a window limit by relevance and necessity, with reserved response budget.
- **Cache-aligned construction and microcompaction** (`internal/promptcache`, `internal/microcompact`): prompts are built to maximize provider cache hits (a single-byte drift can zero the cache), and over-budget context is compacted along cache boundaries.
- **Supporting analysis**: semantic diff (`internal/semdiff`, AST-level for Go), semantic chunking, TF-IDF and a (currently bag-of-words) vector index, diff compression, and blame-aware editing.

This layer is "context engineering": the discipline of spending a finite window on the highest-value tokens.

### 4.7 Cost, budget, and model routing

Cost is treated as a first-class, enforced resource. A real per-model pricing table converts input/output/cache tokens to USD (`internal/costtrack`); budget thresholds raise tiered alerts and, critically, a run is **aborted before an attempt** once over budget — budget enforcement is a hard gate, not a dashboard. Model selection (`internal/model`) walks a primary-then-fallback chain (e.g., Claude → Codex → OpenRouter → direct API → lint-only) with cost-aware resolution, so a provider outage or rate-limit degrades gracefully rather than failing the run.

---

## 5. The governance layer (deterministic, tamper-evident trust)

The trust loop in §4 protects a single run. The governance layer is designed to make the *entire history* of agent activity deterministic, inspectable, and tamper-evident — the substrate an enterprise needs to actually trust autonomous agents. It comprises three real, well-tested components and a bridge that, as of the recent work, wires them into live runs (default-on, non-fatal).

- **Append-only content-addressed ledger** (`internal/ledger`). Every governance-relevant event is a node whose identity is derived from its content: a random salt is drawn per node, a content commitment is `SHA-256(salt ‖ content)`, and the node ID is `SHA-256(canonical-header ‖ content-commitment)`, type-prefixed. Nodes are Merkle-chained per mission via a parent hash, persisted in a two-tier filesystem layout plus a rebuildable SQLite index. Immutability is enforced at the type level — there are *no* Update/Delete/Modify methods, and a test AST-scans the package to prove it; mutation happens only by appending a new node and a `supersedes` edge. A crypto-shred design allows content redaction (erasing the salt+payload) without breaking the permanent header/commitment chain — redaction without rewriting history. Fifty-two typed node structs implement a common `NodeTyper` interface.
- **Durable WAL event bus** (`internal/bus`). An append-only NDJSON write-ahead log, `fsync`'d on every append, with replay-on-open (rebuilding the in-memory index and sequence), causal ordering (an event whose causal reference has a higher sequence than the current is rejected — happens-before is enforced), delayed/cancellable events, and privileged hooks. This is the spine over which the rules engine observes.
- **Deterministic rules engine** (`internal/supervisor`). A priority-ordered set of ~34 rules across categories (consensus, drift, hierarchy, research, trust, snapshot, …) bundled into per-tier manifests. Each rule is genuine deterministic logic: a side-effect-free `Evaluate(event, ledger)` and an `Action(event, bus)`. For example, a drift rule computes `spent/budget` and fires progressive actions at 50/80/100/120% (warn → spawn a judge → escalate → hard-stop); a trust rule refuses to let a worker's completion stand unless an *independent* second opinion exists in the ledger, and otherwise pauses the worker and requests a reviewer.

The recent activation closed the gap between "built" and "running." A **Governor** (`internal/governance`) constructs the bus, ledger, and supervisor, and registers an observe-mode subscriber that translates the live v1 event hub into v2 bus events and ledger nodes during a real mission. The headline fix: the cross-model review verdict is now *emitted*, and the Governor writes the literal `review.agree`/`review.dissent` ledger node the trust rule queries — making the second-opinion gate, previously *un-satisfiable by construction*, actually fire. This is wired default-on with a kill-switch and non-fatal construction.

**Honest status:** the components are individually excellent and now populate the ledger and fire rules during runs; the deeper consensus machinery (the 7-state `ledger/loops` lifecycle) and rule *actions that materially steer a run* remain shallow/deferred.

Why it matters: append-only, content-addressed, deterministically-governed agent activity is the difference between "an AI changed our code and told us it was fine" and "here is the tamper-evident, independently-reviewed, rule-checked record of exactly what happened and why it was allowed."

---

## 6. Parallel cognition: the cortex

Beyond gating *outcomes*, r1 includes a substrate for richer *cognition during* a run, modeled on **Global Workspace Theory (GWT)** — the cognitive-science account in which many specialized processes run in parallel and compete to broadcast their findings into a shared "global workspace" that the rest of the mind reads.

The cortex (`internal/cortex`, ~9k LOC) implements this faithfully:
- A **Workspace** — a shared, RWMutex-guarded view where specialists publish typed **Notes** (with severity: info/advice/warning/critical).
- **Lobes** — concurrent cognitive specialists. The activated set is *deterministic* (memory-recall, WAL-keeper, rule-check, anti-truncation); an LLM-backed set (plan-update, clarify-questions, memory-curator) and a Haiku-driven **Router** for mid-turn input routing exist but are deferred.
- A **Round** — a superstep barrier: each round, lobes run in parallel against a workspace snapshot; a barrier collects their notes; a **Spotlight** elevates the most salient.
- Integration with the agent loop via two hooks: **MidturnNote** (after a tool turn, drains the round's notes and formats them into the next user message as a supervisor note) and **PreEndTurnGate** (refuses `end_turn` while an unresolved *critical* note exists — e.g., a build-verification failure the model is about to ignore).

The load-bearing safety property for running this on the hot path: the midturn barrier is bounded by a `RoundDeadline` (default 2 s) enforced by a `time.After` arm, and `PreEndTurnGate` does no waiting. A wedged lobe degrades a round to a partial/empty note set; it can never hang the loop. The cortex is wired into the native loop default-on with a kill-switch; a construction or start failure proceeds without it. (A subtle correctness detail fixed during activation: the lobes and the cortex must share *one* Workspace — the agent-loop hook drains the live workspace, so a naïvely-constructed "shell" workspace would surface zero notes.)

The cortex is the architectural bet that *cognition is parallel and competitive*, not a single monolithic prompt — a more principled structure for "the agent should simultaneously recall relevant memory, check rules, watch for truncation, and keep the plan updated" than stuffing all of that into one system prompt.

---

## 7. Evaluation and the truthful-completion angle

r1 is explicitly an *evaluation-grade* runtime, and it ships a benchmark for the very property it exists to enforce. The **TruthfulCompletion** benchmark (`internal/bench`, `cmd/r1-bench`, with Docker reproduction kits) frames golden missions and measures whether an agent *actually* completes a scope versus *claims* to — with regression detection. This closes the loop: the system that enforces truthful completion can also measure it, across agents.

The strongest evaluation evidence, however, is dogfooding. The activation work described throughout this paper was itself executed through r1's own scope→build flow, with cross-model review on every spec; that review caught seven real defects unit tests missed (§4.3). A runtime whose own development surfaces this many escaped bugs through its trust mechanism is making an empirical case, not just an architectural one.

---

## 8. Implementation and engineering posture

- **Language & scale.** ~312K lines of first-party Go across 183 internal packages, ~956 test files; pinned to Go 1.25.5. CGO is required (SQLite); shipped binaries enable FTS5.
- **Concurrency.** The codebase reuses a small set of proven patterns — RWMutex-with-copy-on-read for shared views, WaitGroup-plus-buffered-channel fan-out, semaphore-via-buffered-channel for concurrency caps, per-subscriber goroutine with bounded buffers, atomic CAS for idempotent lifecycle. The cortex and bus are race-tested.
- **Anti-stub culture.** In 312K lines, the only "not implemented" strings in first-party code live inside *anti-stub detection tooling*. Tests assert real behavior (a representative example AST-scans the ledger to prove append-only at the type level).
- **The CI gate.** `go build ./...`, `go test ./...`, `go vet ./...` — with a `-race` variant in cloud CI. This three-command gate is the contract.
- **Self-hosting development.** The repository is itself developed by AI agents through a heavy enforcement harness (shared playbooks, PreToolUse/Stop hooks, per-task-commit discipline) that operationalizes the same principles the product embodies — evidence-gated completion, cross-engine review, no silent skips.

---

## 9. Positioning

r1 occupies a space defined less by any single feature than by their *combination*:

- **Versus autonomous-agent frameworks** (the AutoGPT/BabyAGI lineage): those maximize autonomy and minimize verification, which is precisely the failure mode r1 targets. r1 is *autonomy with teeth* — every step is gated.
- **Versus single-model copilots / IDE assistants**: those are tightly human-supervised and single-distribution. r1 is built for unsupervised or lightly-supervised runs and structurally breaks the single-distribution blind spot via cross-model review.
- **Versus evaluation harnesses** (SWE-bench-style): those *measure* agents offline. r1 is a *runtime* that builds verification into execution, and ships a benchmark too.
- **Versus governance/observability add-ons**: r1's governance is not telemetry bolted on; it is a content-addressed, append-only, deterministically-ruled substrate intended to make agent activity tamper-evident by construction.

The novel claim is the *stack*: engine-agnostic execution + hard completion gates + adversarial cross-model review + deterministic tamper-evident governance + parallel cognition, integrated, with the trust mechanism demonstrably catching real bugs.

---

## 10. Limitations and honest status

In keeping with the project's ethos, the gaps are stated plainly:
- **Governance depth.** Lifecycle nodes are written and rules fire during runs, but the 7-state consensus loop is not driven and rule *actions* do not yet deeply steer a run (pause/escalate are shallow).
- **Cognition depth.** Only the deterministic lobes run; the LLM lobes and the mid-turn Router are deferred; per-lobe policy gating is MVP all-on.
- **Semantic retrieval.** The "vector index" is cosine over bag-of-words, not neural embeddings; a real embedding provider is unwired. Non-Go repo mapping ranks by symbol count with best-effort import edges and no call graph.
- **Observability surfacing.** Telemetry/metrics/flow-tracking are recorded but rarely rendered back.
- **Speculative execution** currently races plan-only first phases, not full implementations.
- **External orchestration.** The `--output stream-json` path is an intentional thin-client wire format for external orchestrators, not local execution.

None of these are stubs masquerading as features — they are real components with deliberately-bounded current scope, each documented in the corresponding spec's deferred section.

---

## 11. Potential and roadmap

The architecture's leverage points, in rough priority:
1. **Double down on cross-model review** — it is the proven differentiator. Make it cheaper/faster; add a third model family for cross-distribution independence; expand the anti-rubber-stamp evidence requirements.
2. **Finish governance** — drive the consensus loops; make rule actions materially steer runs (real pause/spawn/escalate); surface the ledger as an audit UI. This is the enterprise-trust unlock.
3. **Activate full cognition** — wire the LLM lobes and the Router so mid-turn reasoning (not just deterministic notes) shapes runs; ~9k lines of engine await a provider wire.
4. **Real semantic retrieval and deeper mapping** — neural embeddings; per-language call graphs.
5. **Full speculative implementation racing** — race N complete implementations, not just plans.
6. **Productize** — the hosted runtime, admin, billing, and lifecycle are partly built.
7. **Observability** — render what is already collected.

The throughline is the founding thesis taken further: from "verify the outcome" toward "a fully auditable, multi-perspective, self-improving runtime you can trust to do real engineering unsupervised."

---

## 12. Conclusion

The bottleneck for autonomous coding agents is not capability; it is *trust* — specifically, the asymmetry between claiming and verifying completion, and the correlated blind spots of any single model. r1/Stoke is an architectural answer: make completion a gated, evidence-derived fact; review it adversarially across model families; record and govern it on a tamper-evident, deterministic substrate; and augment the run with bounded parallel cognition. The execution spine and the cross-model trust loop are mature and proven — the latter by catching real bugs in the system's own development. The governance and cognition layers are genuinely built and now wired in, with their deeper capabilities a clearly-mapped frontier. The wager is simple and, on the evidence so far, sound: **an agent runtime that refuses to take its own word for it is the one you can actually let off the leash.**

---

## Appendix A — Key parameters and defaults
- Cortex `RoundDeadline`: 2 s (hard upper bound on the midturn barrier); cortex stop budget: 10 s.
- Repo-map injection budget: ~2000 tokens; context window target: ~180k with ~8k reserved for response; microcompaction trigger ~80% of window.
- Model fallback chain: Primary → Codex → OpenRouter → direct API → lint-only.
- Governance budget rule thresholds: 50 / 80 / 100 / 120 %.
- Ledger node ID: `SHA-256(canonical-header ‖ SHA-256(salt ‖ content))`, type-prefixed; Merkle parent-chained per mission.
- CI gate: `go build ./...` · `go test ./...` (+ `-race`) · `go vet ./...`.

## Appendix B — Selected code anchors (for the technically curious)
- Merge gate: `internal/workflow/workflow.go` (the all-gates-pass check before merge).
- Cross-model reviewer mapping: `internal/model/router.go` (`CrossModelReviewer`).
- Native loop + cortex wiring: `internal/engine/native_runner.go`.
- CLI engines: `internal/engine/{claude,codex,gemini}.go`.
- Governor bridge: `internal/governance/governance.go`.
- Ledger immutability: `internal/ledger/` (append-only; the AST-scan test).
- Durable bus: `internal/bus/` (WAL, replay, causality).
- Cortex GWT: `internal/cortex/` (Workspace/Round/Spotlight/Router/Lobe).
- Repo-map ranking: `internal/repomap/repomap.go`.
