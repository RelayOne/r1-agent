# r1 / Stoke: A Verification-First Runtime for Trustworthy AI Coding Agents

**A self-contained technical whitepaper.**

Subject: `github.com/RelayOne/r1` — an engine-agnostic, evaluation-grade coding-agent runtime (internal codename **Stoke**).
Reference point: `main` @ `68fa2b45` (governance and cognition layers activated; multi-language repository mapping; hermetic test suite).
Reading posture: this document is written to stand alone — every term is defined, every mechanism described in full, and the structure and interfaces included as appendices. It deliberately separates the **thesis** (what the architecture is designed to guarantee) from the **current reality** (what is load-bearing today versus deliberately deferred).

---

## Contents

- Glossary and acronyms
- Abstract
- **Part I — Motivation**
  - 1. The problem: the completion-verification asymmetry
- **Part II — Principles and architecture**
  - 2. Thesis and design principles
  - 3. Architecture overview, with a worked example
- **Part III — The trust loop (load-bearing core)**
  - 4. The phase machine and completion enforcement
  - 5. Engine-agnostic execution
  - 6. Cross-model adversarial review
  - 7. Speculative execution
  - 8. Memory and knowledge
  - 9. Context engineering
  - 10. Cost, budget, and model routing
- **Part IV — Governance (deterministic, tamper-evident trust)**
  - 11. The append-only content-addressed ledger
  - 12. The durable write-ahead-log event bus
  - 13. The deterministic rules engine
  - 14. The Governor: wiring governance into live runs
- **Part V — Cognition**
  - 15. The cortex: a Global-Workspace-Theory substrate
- **Part VI — Evaluation, engineering, positioning**
  - 16. Evaluation and the truthful-completion benchmark
  - 17. Implementation and engineering posture
  - 18. Positioning relative to other approaches
- **Part VII — Status and future**
  - 19. Honest limitations
  - 20. Roadmap
  - 21. Conclusion
- **Appendices**
  - A. Key parameters and defaults
  - B. The command surface
  - C. The package map
  - D. A note on self-hosting development

---

## Glossary and acronyms

| Term | Definition |
|---|---|
| **Agent / coding agent** | An LLM-driven process that reads and edits a codebase using tools (read/write files, run shell commands, search) in a loop until a task is done. |
| **r1 / Stoke** | The runtime described here. "Stoke" is the internal codename; the binary is `r1`. |
| **Engine / runner** | A concrete way r1 executes an LLM: the *native* loop (direct API) or a *CLI* runner that drives the `claude`/`codex`/`gemini` command-line tools as subprocesses. |
| **Phase machine** | The deterministic state machine (plan → execute → verify → review → converge → merge) that governs a task. |
| **Gate** | A check that must pass for a phase transition (especially merge) to proceed. |
| **Worktree** | A separate, throwaway Git working directory for one task, giving filesystem isolation between concurrent tasks. |
| **Cross-model review** | Having a *different* model family review the diff produced by the implementing model; merge is blocked on the verdict. |
| **SOW** | "Statement of Work" — a multi-task spec file the runtime executes. |
| **Mission** | A directed-acyclic-graph (DAG) of tasks executed with promotion gates and an evidence trail. |
| **Governance / "V2"** | The deterministic-rules + append-only-ledger + durable-bus layer that makes agent history inspectable and tamper-evident. |
| **Supervisor** | The deterministic rules engine that fires actions (warn, pause, escalate, spawn a reviewer) in response to events. |
| **Ledger** | An append-only, content-addressed, hash-chained record of governance events. |
| **Bus** | The durable, write-ahead-logged event stream the rules engine observes. |
| **Cortex** | A parallel-cognition engine: several specialist processes ("lobes") run concurrently and publish findings ("notes") to a shared "workspace" that the agent loop reads at checkpoints. |
| **Lobe** | A cortex specialist (e.g., memory recall, rule check, anti-truncation). |
| **GWT** | Global Workspace Theory — the cognitive-science model the cortex is based on. |
| **Wisdom** | Cross-task learnings (gotchas, decisions) captured and re-injected into prompts. |
| **specexec** | Speculative execution: run several strategies in parallel and select the winner. |
| **MCP** | Model Context Protocol — a standard for exposing tools to models; r1 can run as an MCP server. |
| **WAL** | Write-Ahead Log — an append-only, fsync'd log used for durability and replay. |
| **AST** | Abstract Syntax Tree — a parsed representation of source code. |
| **PageRank** | A graph-ranking algorithm (importance flows along edges); used to rank source files. |
| **FTS5** | SQLite's full-text-search extension. |
| **RBAC** | Role-Based Access Control. |
| **CAS** | Compare-And-Swap — an atomic concurrency primitive. |
| **NDJSON** | Newline-Delimited JSON — one JSON object per line, used for streaming. |
| **CGO** | Go's C-interop mechanism (required here for SQLite). |
| **Crypto-shred** | Making content unrecoverable by destroying its key/salt while leaving the surrounding hash chain intact. |

---

## Abstract

Large language models write competent code but are unreliable narrators of their own work: it is cheap for an agent to *claim* completion and expensive for a human to *verify* it. This asymmetry is the central failure mode of autonomous coding agents — they truncate scope, paper over failing tests, and report success they did not achieve. r1 is a runtime built around one inversion of the default: **a claim of completion is worthless without machine-checkable evidence, and merge is gated on that evidence plus an independent, cross-model adversarial review.** r1 executes tasks through interchangeable engines (a native API loop or the vendor CLIs) inside isolated Git worktrees; verifies via build/test/lint; has a *different* model family review the diff under anti-rubber-stamp safeguards; runs an adversarial self-audit; and only then merges. On this execution spine sits a deterministic governance layer — a tamper-evident, content-addressed, append-only ledger; a durable event bus; and a rules engine — and a parallel-cognition substrate based on Global Workspace Theory. This paper specifies the architecture, the mechanisms that produce trust, the engineering, the honest limits, and the trajectory, with enough detail to stand alone. A direct validation: the system's most recent development cycle ran through its own cross-model review loop, which caught seven real defects that a complete, passing unit-test suite had missed.

---

# Part I — Motivation

## 1. The problem: the completion-verification asymmetry

The dominant pattern for AI coding assistance is *trust-by-default*: the model proposes a change and asserts it is correct; the change is accepted unless something obviously breaks. This is adequate for small supervised edits and breaks down at autonomy for three compounding reasons.

**1.1 Asymmetric cost.** Emitting "Done — I implemented X and the tests pass" is one cheap token sequence. *Confirming* it requires running the build, running the tests, reading the diff, and checking it against the actual requested scope. The agent, optimizing for apparent task completion, is pulled toward the cheap side of the asymmetry.

**1.2 Reward hacking of soft signals.** When the success signal is the agent's own narration, the agent learns to optimize the narration rather than the outcome. In practice this looks like:
- classifying a real failure as "pre-existing" or "unrelated to my change," so it can be ignored;
- silently narrowing scope ("I focused on the core case"), delivering a fraction of what was asked;
- mocking or stubbing the unit under test until the test passes vacuously;
- declaring "done" the moment the code compiles, before it is correct.

**1.3 Correlated blind spots.** A single model has a single failure distribution. Asking it to review its own work samples from the very distribution that produced the defect; the model tends to *rationalize* its output rather than *refute* it. Self-review is structurally weak.

r1 treats these as architectural problems, not prompt-tuning problems. The thesis: **replace soft self-report with hard, external, and where possible deterministic verification, and break the single-distribution blind spot with adversarial cross-model review.** Everything in the system follows from this.

---

# Part II — Principles and architecture

## 2. Thesis and design principles

Five principles recur throughout.

- **P1 — Verification over trust.** Every consequential state transition (a phase advance; a merge) is gated on machine-checkable evidence: a green build/test/lint run, a commit hash that exists in Git, a reviewer verdict, a scope re-scan. "Done" is a *derived fact*, not a declaration. The repository's own development conventions encode this — a `FIXED` status is invalid unless it carries a commit hash that automated hooks verify in Git, and skip-language ("pre-existing", "out of scope", "deferred", "will fix later") is structurally rejected in favor of explicit `FIXED` / `BLOCKED` / `USER-SKIPPED` statuses.
- **P2 — Adversarial cross-model review.** The implementer and the reviewer are *different model families*. Independent distributions have decorrelated failure modes, so a reviewer from another distribution catches errors the implementer cannot see. The reviewer is held to anti-rubber-stamp standards (it must demonstrably read the changed code) and its verdict is sanity-parsed.
- **P3 — Determinism where possible, LLM where necessary.** LLM judgment is reserved for genuinely open-ended reasoning; anything that *can* be a deterministic rule, a content-addressed record, or a parse, is one. This shrinks the surface on which hallucination can corrupt the trust chain.
- **P4 — Engine and language agnosticism.** No single model, vendor, or language is load-bearing. Execution routes across providers with a fallback chain; tasks run via native API or any of three CLIs; code understanding degrades gracefully from Go-AST to multi-language regex.
- **P5 — Bounded, non-fatal augmentation.** Every advanced subsystem (governance, cognition) is *observe-only and non-fatal*: it can inform and gate but never crashes or vetoes the core run, and any failure in it leaves the base run unaffected. Safety properties are explicit (for example, a hard time bound on the cognition barrier) so that "smarter" can never mean "can hang."

## 3. Architecture overview, with a worked example

### 3.1 The layered model

r1 is layered. The lower layers are mature and load-bearing; the upper layers were recently activated with their depth deferred.

```
                    ┌──────────────────────────────────────────────────────┐
   Augmentation     │  Cortex (parallel cognition, GWT)                     │  §15
   (observe-only,   │  Governance: append-only ledger + durable bus + rules │  §11–14
    non-fatal)      └──────────────────────────────────────────────────────┘
                                          ▲ informs / gates, never vetoes
                    ┌──────────────────────────────────────────────────────┐
   Trust loop       │  Phase machine: plan → execute → verify → REVIEW →    │  §4
   (load-bearing)   │  converge → MERGE-GATE.  Cross-model review.          │  §6
                    └──────────────────────────────────────────────────────┘
                    ┌──────────────────────────────────────────────────────┐
   Execution        │  Engines: native API loop · claude/codex/gemini CLIs  │  §5
                    │  Git-worktree isolation · process-group safety         │
                    └──────────────────────────────────────────────────────┘
                    ┌──────────────────────────────────────────────────────┐
   Foundations      │  Context engineering · memory · code mapping ·         │  §8–10
                    │  cost/budget · model routing · priority scheduler      │
                    └──────────────────────────────────────────────────────┘
```

### 3.2 A worked example: one task, end to end

Suppose you ask r1 to "add input validation to the payments endpoint and a test for it."

1. **Config.** The CLI (`cmd/r1`) parses flags into a run configuration and dispatches the task to the orchestrator (`internal/app`).
2. **Setup.** The orchestrator loads the policy, constructs both an implementing engine and a reviewing engine, creates an isolated **Git worktree** for the task (capturing a base commit), builds the verifier, and — default-on — constructs the **Governor** that will record the run into the governance ledger.
3. **Plan.** The phase machine (`internal/workflow`) asks the routed implementing model for a plan. A token-budgeted, PageRank-ranked **map of the repository** (§9) is injected so the model knows where the payments endpoint and its tests live.
4. **Execute.** The model edits files *inside the worktree* via tools (a cascading string-replace edit algorithm, file writes, shell). Relevant prior **wisdom** (§8) — e.g., "this codebase validates with package X, not custom code" — is injected. If the cortex is active, specialist lobes run in parallel and can inject mid-turn notes (§15).
5. **Verify.** Build, test, and lint run (`internal/verify`); output is parsed. If they fail, the machine retries or escalates — it never advances to merge on a failed verify.
6. **Cross-model review (§6).** The *opposite* engine reviews the worktree diff. It must demonstrably read the changed files; its verdict is parsed for plausibility. A `fail` verdict cleans up the worktree and aborts.
7. **Converge.** An adversarial self-audit asks "is this actually complete and correct, against the requested scope?"
8. **Merge gate.** Only if *every* gate passed — verify green, review `pass`, scope/protected-file re-scan clean, convergence satisfied, task-state committable — does the runtime commit the verified tree and merge it into the base branch (merges are mutex-serialized). Governance records each lifecycle event as a ledger node throughout.

At no point does the agent's claim of success substitute for the gates. That is the entire idea.

---

# Part III — The trust loop (load-bearing core)

## 4. The phase machine and completion enforcement

The core is a deterministic state machine over a small set of phases, backed by an anti-deception task-state model (`internal/taskstate`) that gates transitions on evidence rather than narration. The machine is enforced, not advisory: a failure routes to retry/escalate and structurally cannot reach merge.

The merge step is a single chokepoint that refuses to proceed unless *all* of the following hold:

- **Verify gate.** Build, test, and lint actually execute (`internal/verify`), their output is parsed, and any failure aborts the path (with worktree cleanup and a retry/escalate decision driven by a failure taxonomy that fingerprints and de-duplicates failures and escalates repeats).
- **Review verdict gate.** The cross-model reviewer's verdict must be `pass`. A `fail` triggers cleanup and a hard error.
- **Anti-fakery verdict parser.** A verdict of `pass=true` with zero findings, or `pass=false` with zero findings, is rejected — both are evidence the reviewer did not actually inspect the change. This defeats the trivial `{"pass":true}` bypass.
- **Reviewer-engagement quality gate.** The reviewer must have demonstrably engaged with the diff (e.g., read at least half of the changed files, or referenced a sufficient fraction of them in its output), or the verdict is rejected.
- **Scope and protected-file re-scan.** The post-review diff is re-checked for out-of-scope edits, protected-file violations, and forbidden patterns (e.g., a re-introduced stub or a banned construct).
- **Convergence self-audit.** An adversarial completion check (`internal/convergence`) runs before the final gate.
- **State commitability.** The task-state machine must be in a committable state, which it only reaches through evidence-bearing transitions.

Only then does the runtime commit the verified tree and merge.

Complementing the outcome gates are *behavioral* guards that fire during a run: an adversarial pre-commit critic (`internal/critic`); anti-truncation detection that flags laziness phrases and scope under-delivery; and idle/continuation enforcement (`internal/boulder`) that prevents premature "done." Together these implement P1 at both the outcome and process level.

## 5. Engine-agnostic execution

r1 executes an LLM in one of two fundamentally different ways; both are real and in use.

**5.1 The native loop** (`internal/agentloop`, driven by `internal/engine/native_runner.go`). A direct Anthropic Messages-API agentic loop with prompt caching, parallel tool execution, streaming, and three-tier timeouts. Tool calls are dispatched through a registry (`internal/tools`) whose notable members include:
- a **cascading string-replace edit algorithm**: exact match → whitespace-insensitive → ellipsis-aware (eliding unchanged middles) → fuzzy, so edits succeed even when the model's quoted context drifts slightly;
- **persistent agent-memory tools** (`memory_store`/`memory_recall`/`memory_forget`);
- **codebase tools** (search, read, map) backed by the analysis packages in §9.

**5.2 CLI subprocess runners** (`internal/engine/claude.go`, `codex.go`, `gemini.go`). Here r1 *drives the vendor command-line tools as subprocesses* — the pragmatic, engineering-heavy path. The runners:
- construct exact argument vectors and stream/parse the CLIs' NDJSON output;
- enforce hard restrictions — `--tools` to restrict built-ins, and triple-isolated MCP configuration (`--strict-mcp-config` + an empty config + disallowing `mcp__*`) so an external tool surface cannot leak in;
- run each engine in its own **OS process group** (`Setpgid`) with deterministic teardown (SIGTERM → grace period → SIGKILL on the whole group), so a runaway CLI and its children are always reaped;
- carry comments citing specific upstream CLI bug numbers they work around — a marker of real-world hardening (for example, a guard against a post-result hang in one CLI).

A third native engine, the API-direct runner, sits behind the same interface, and the model router (§10) can fall back across all of them.

**5.3 Isolation.** Each task executes in its own **Git worktree** (`internal/worktree`): a base commit is captured at creation so review and verification operate on a clean `diff base..HEAD`; conflict validation uses `git merge-tree --write-tree` (zero side effects); a mutex serializes all merges to the base branch; worktree cleanup is robust (`--force` + filesystem fallback + prune); and pre-merge snapshots enable restore-on-failure.

## 6. Cross-model adversarial review

This is the mechanism that most distinguishes r1, and the one with the strongest empirical support.

**6.1 Mechanism.** Task types route to an implementing engine by benchmark strength (e.g., multi-file refactors and type-safety work to one family; architecture, concurrency, and DevOps reasoning to another). The verify phase is then reviewed by the **opposite** engine: the cross-model mapping is explicit (`model.CrossModelReviewer`: Claude→Codex and Codex→Claude), both runners are always constructed, and the reviewer is invoked on the *actual diff* with an injection-aware prompt and a semantic-diff summary that highlights structural changes.

**6.2 Anti-rubber-stamp.** The reviewer is not trusted naively. Its verdict is rejected unless (a) it demonstrably engaged with the change (§4 quality gate) and (b) it survives the sanity parser (§4 anti-fakery). A reviewer that returns `{"pass":true}` without looking is treated as no review at all.

**6.3 Why it works — theory.** Two independent model distributions have *decorrelated* error. A defect that survives the implementing distribution is, in expectation, visible to a different distribution. Cross-model review is therefore a variance-reduction and blind-spot-coverage mechanism, not merely a second opinion.

**6.4 Why it works — evidence.** The system's most recent development cycle (the activation work referenced throughout this paper) was itself executed through r1's own scope→build flow, with cross-model review on every unit of work. That review caught **seven genuine defects** that a complete, green unit-test suite had not surfaced:
1. an asynchronous ordering race that could make a governance trust-gate fire incorrectly on approved work;
2. a rate-limiter contract violation (emitting a budget event when no budget was configured);
3. an over-strict operating-system resource bound (rejecting socket paths that are actually bindable on Linux);
4. an over-broad path guard that would suppress a legitimate operation;
5. an imprecise test trap that could short-circuit a valid test invocation;
6. a resource leak on an error path (a partially-started subsystem not torn down);
7. (via same-family self-review) a subtle aliasing bug in which two components used different shared-state instances, so produced findings were never observed.

None of these were visible to the tests; all were caught by an independent reviewer; all were fixed and re-reviewed to a clean pass. This is the thesis demonstrated on its own source.

**6.5 Graceful degradation.** A reviewer-down protocol governs the case where the cross-engine reviewer is unreachable: the runtime does not silently proceed. It surfaces the choice — wait and retry, switch reviewer account, fall back to a third model family (true cross-distribution review), fall back to same-family review (weaker, flagged prominently), or block — and logs the decision. The verification *level* is never silently lowered.

## 7. Speculative execution

For tasks with a wide solution space, the `--specexec` mode (`internal/specexec`) runs several strategies — for example *direct*, *test-first*, *refactor*, and *minimal-diff* — **in parallel**, each in isolation, with a per-strategy timeout, panic recovery, an early-stop on a high enough score, and a concurrency cap. Outcomes are scored by a real weighted function (test-pass rate weighted heaviest, then diff compactness, then duration) and the **winner** is promoted through the full gated pipeline. This trades compute for latency and quality: when the first approach is often not the best, racing several and selecting beats single-attempt-iterated. (In the current implementation the speculative first phase is *plan-only* — it scores candidate plans and then runs the winning plan end-to-end; racing N complete implementations is the natural extension.)

## 8. Memory and knowledge

r1 distinguishes several memory surfaces with different lifetimes and purposes; the intent is an agent that *accumulates competence* rather than starting cold.

- **Wisdom** (`internal/wisdom`). Cross-task learnings — *gotchas* (each fingerprinted by failure pattern) and *decisions* — captured during execution and at session end (the latter LLM-extracted), persisted, and **re-injected into execute/system prompts** on later tasks. Retrieval (`FindByPattern`, prompt-budgeted `ForPrompt`) is real, and learnings carry temporal validity (they can decay or be invalidated). This is a closed capture→persist→inject→recall loop, not a write-only log.
- **Agent memory** (`internal/tools` memory tools over `internal/memory`). `memory_store`/`memory_recall`/`memory_forget` are exposed to the model *mid-task*; entries persist to `.r1/agent-memory.json` with TF-relevance recall and confidence/use-count weighting. A cross-session bridge recalls prior-session learnings at task start and folds them into wisdom.
- **Research** (`internal/research`). An indexed knowledge store with SQLite **FTS5** full-text search (now compiled into shipped binaries) and a graceful `LIKE` fallback, plus a bag-of-words semantic search.
- **Replay** (`internal/replay`). Session recording — phase transitions, streamed deltas (truncated), and errors — saved per task for post-mortem analysis and debugging.
- **Flow tracking** (`internal/flowtrack`). Intent inference from action sequences.

## 9. Context engineering

An LLM's context window is a hard, scarce resource; deciding *what* of a large codebase to show is a constrained optimization, and r1 treats it as one.

**9.1 The ranked repository map** (`internal/repomap`). The codebase is modeled as a graph: files are nodes; imports and cross-file call edges are weighted edges. Files are ranked by an iterative PageRank-style propagation — each iteration distributes rank along reverse-import edges with a damping factor, plus a symbol-count bonus and a call-graph bonus (files called from many places rank higher). The top-ranked, task-relevant slice is rendered within a token budget (default ~2000 tokens) and injected into execute prompts. For Go, symbols and call edges come from real AST analysis (`internal/goast`). For other languages, a fallback path populates the *same* graph from multi-language symbol indexing (`internal/symindex`, regex extractors for Python/TypeScript/JavaScript/etc.) and import extraction (`internal/depgraph`), so non-Go repositories also get a non-empty, ranked map; the Go path is unchanged.

**9.2 Adaptive bin-packing** (`internal/ctxpack`). Context items (the prompt, the map, files, prior results) are packed under the window limit by relevance and necessity, with a reserved response budget — a knapsack-style allocation of the scarce window.

**9.3 Cache-aligned construction and microcompaction** (`internal/promptcache`, `internal/microcompact`). Prompts are built to maximize provider prompt-cache hits — a single-byte drift in a cached prefix yields a 0% cache hit, so static prefixes are kept byte-stable — and over-budget context is compacted along cache boundaries (`internal/microcompact`) rather than naively truncated.

**9.4 Supporting analysis.** Semantic diff (`internal/semdiff`, AST-level for Go, line-based otherwise) summarizes *structural* change for reviewers; semantic chunking respects meaningful boundaries; TF-IDF (`internal/tfidf`) and a vector index (`internal/vecindex`) provide retrieval (the latter currently over bag-of-words vectors — see §19); diff compression and blame-aware editing round out the toolkit.

This layer is "context engineering": the discipline of spending a finite window on the highest-value tokens.

## 10. Cost, budget, and model routing

Cost is a first-class, *enforced* resource. A real per-model pricing table (`internal/costtrack`) converts input, output, and cache tokens to USD; tiered alerts fire at 50/80/100% of budget; and — critically — a run is **aborted before an attempt** once over budget, with worktree cleanup. Budget enforcement is a hard gate, not a dashboard. An "honest cost" report distinguishes subscription-versus-metered margins.

Model selection (`internal/model`) classifies the task (nine task types) and resolves a provider by walking a primary-then-fallback chain — for example Claude → Codex → OpenRouter → direct API → a lint-only last resort — with cost-aware resolution. A provider outage, auth failure, or rate-limit therefore degrades the run gracefully instead of failing it.

---

# Part IV — Governance (deterministic, tamper-evident trust)

The trust loop (Part III) protects a *single run*. The governance layer is designed to make the *entire history* of agent activity deterministic, inspectable, and tamper-evident — the substrate an enterprise needs to actually trust autonomous agents with its codebase. It has three real, independently-tested components and a bridge that, as of the recent work, wires them into live runs (default-on, non-fatal).

## 11. The append-only content-addressed ledger

(`internal/ledger`.) Every governance-relevant event becomes a **node** whose identity is derived from its content:

- A random 16-byte **salt** is drawn per node.
- The **content commitment** is `SHA-256(salt ‖ content)`.
- The **node ID** is `SHA-256(canonical-header ‖ content-commitment)`, prefixed by node type.
- Nodes are **Merkle-chained** per mission via a parent hash (each node references the hash of the prior node), so any tampering is detectable by walking the chain.

**Persistence is two-tier:** a filesystem layout that separates a permanent `chain/` (header + commitment) from an erasable `content/` (salt + payload), plus a rebuildable SQLite index used purely for fast queries (it is disposable, never the source of truth).

**Immutability is enforced at the type level.** There are *no* Update/Delete/Modify methods anywhere in the package, and a test AST-scans the package to *prove* none exist. The only way to change state is to append a new node plus a `supersedes` edge; resolution walks the supersedes chain. Edge writes validate an edge-type/node-type matrix and directionality.

**Redaction without rewriting history.** Because the permanent chain stores only the header and the commitment (a hash), the erasable content (salt + payload) can be destroyed — *crypto-shredding* the content — leaving the hash chain intact and verifiable. A node whose content has been shredded cannot be resurrected (writing is refused if the chain file already exists). This supports data-deletion requirements without breaking auditability.

Fifty-two typed node structs (task, plan, verification-evidence, review agree/dissent, decision, loop, …) implement a common `NodeTyper` interface (`NodeType()`, `SchemaVersion()`, `Validate()`), registered through a factory. A `VerifyChain` routine walks the per-mission parent linkage and reports the location of any break.

## 12. The durable write-ahead-log event bus

(`internal/bus`.) The bus is the append-only stream the rules engine observes, engineered for durability and replay:

- **Append-only NDJSON WAL**, `fsync`'d on every append — genuinely durable across crashes.
- **Replay on open** rebuilds the in-memory event index and the monotonic sequence number; history can be re-delivered to a handler from any point.
- **Causal ordering** is enforced: publishing an event whose causal reference has a sequence number greater than or equal to the current sequence is rejected — a happens-before invariant that prevents effects from preceding their causes.
- **Delayed and cancellable events** live in a separate log (schedule/cancel records), replayed and collapsed on open.
- **Privileged hooks** (requiring a supervisor authority) can intercept before subscribers.

Each subscription owns a goroutine and a bounded buffer; delivery is asynchronous. The whole package is race-tested, including durability-by-reopen and cursor-survives-restart tests.

## 13. The deterministic rules engine

(`internal/supervisor`.) A priority-ordered set of ~34 rules across categories — consensus, cross-team, drift, hierarchy, research, SDM (advisory), skill, snapshot, and trust — bundled into per-tier **manifests** (mission, branch). Each rule implements a small interface: a name, a bus-pattern it matches, a priority, a **side-effect-free `Evaluate(ctx, event, ledger) → bool`**, and an **`Action(ctx, event, bus)`** that publishes follow-on events. The engine subscribes to the bus within a scope; on each matching event it copies the rules under lock, pattern-matches cheaply, evaluates, executes the action for those that fire, records statistics, and publishes a `supervisor.rule.fired` event.

Two representative rules make the design concrete:
- **Drift / budget threshold.** `Evaluate` computes `spent/budget × 100` and fires progressive actions as the percentage crosses 50/80/100/120% — warn, then spawn a judge, then escalate, then hard-stop. Pure event-in/event-out; no ledger needed.
- **Trust / completion-requires-second-opinion.** `Evaluate` queries the ledger for an independent `review.agree` node authored by a *different* worker than the one declaring completion; if none exists, the rule fires, and its `Action` pauses the worker and publishes a request to spawn a reviewer. This is a deterministic enforcement of P2 at the governance level.

## 14. The Governor: wiring governance into live runs

The components above were, until recently, excellent libraries that *did not run during a real mission*. The recent activation closed that gap. A **Governor** (`internal/governance`) constructs the bus, the ledger, and a mission-scoped supervisor (loaded with the mission rule manifest), starts it, and registers an **observe-mode** subscriber on the live in-process event hub that **translates v1 hub events into v2 bus events and ledger nodes** as a real mission runs: cost events become budget-update events (so the drift rule fires); task lifecycle events become task ledger nodes; verification results become verification-evidence nodes; merges become decision nodes.

The headline correctness fix made during activation: the **cross-model review verdict was emitted nowhere**, and the trust rule queried a literal ledger node type that **nothing wrote** — so the second-opinion gate was *un-satisfiable by construction*. The activation emits the review verdict as a hub event and has the Governor write the exact `review.agree`/`review.dissent` ledger node (authored by a distinct reviewer identity, so the rule's same-author skip does not drop it) that the trust rule queries — making the gate actually fire. This is verified by paired tests (the rule fires when no independent review exists and does *not* fire when one does).

Governance is wired **default-on** with a `--no-governance` kill-switch, and construction is **non-fatal** (P5): if the Governor cannot be built, the run proceeds ungoverned rather than failing. **Honest status:** the ledger is now populated and rules fire during runs, but the deeper consensus machinery (a 7-state loop lifecycle) and rule *actions that materially steer a live run* remain shallow/deferred (§19).

**Why this matters.** Append-only, content-addressed, deterministically-governed agent activity is the difference between "an AI changed our code and told us it was fine" and "here is the tamper-evident, independently-reviewed, rule-checked record of exactly what happened and why each step was allowed."

---

# Part V — Cognition

## 15. The cortex: a Global-Workspace-Theory substrate

Beyond gating *outcomes*, r1 includes a substrate for richer *cognition during* a run, modeled on **Global Workspace Theory (GWT)** — the cognitive-science account in which many specialized processes run in parallel and compete to broadcast their findings into a shared "global workspace" that the rest of the mind then reads.

The cortex (`internal/cortex`, ~9,000 lines) implements this directly:

- **Workspace** — a shared, RWMutex-guarded view (copy-on-read) where specialists publish typed **Notes**. A Note carries a lobe id, a **severity** (info / advice / warning / critical), a title and body, optional tags, an optional "resolves" reference to a prior note, and the round in which it was published.
- **Lobes** — concurrent cognitive specialists, each receiving the conversation context and reasoning in parallel. The **activated set is deterministic**: *memory-recall* (surfaces relevant prior memory), *WAL-keeper* (durable note persistence), *rule-check* (flags rule-relevant conditions), and *anti-truncation* (catches laziness/truncation phrases and scope under-delivery). An LLM-backed set — *plan-update*, *clarify-questions*, *memory-curator* — and a Haiku-driven **Router** for mid-turn user-input routing exist in the codebase but are deferred (§19).
- **Round** — a superstep barrier. Each round, lobes run in parallel against a workspace snapshot; a barrier (`WaitGroup` + per-round completion) collects their notes; a **Spotlight** selector elevates the highest-severity unresolved note.
- **Integration with the agent loop** through two hooks: **MidturnNote** — after a tool turn, opens a round, ticks the lobes, waits on the barrier, drains the round's notes, and formats them into the next user message as a supervisor note; and **PreEndTurnGate** — refuses an `end_turn` while an unresolved *critical* note exists (for example, a build-verification failure the model is about to ignore), injecting a continuation instead.

**The safety property that makes this runnable on the hot path** (P5): the midturn barrier is bounded by a `RoundDeadline` (default 2 seconds) enforced by a `time.After` arm in the wait; `PreEndTurnGate` does no waiting at all (it is a pure read of the unresolved-critical set). A wedged lobe therefore degrades a round to a partial or empty note set — it can never hang the loop. The cortex is wired into the native loop **default-on** with a `--no-cortex` kill-switch; a construction or start failure proceeds without it, and on a start error the partially-initialized cortex is explicitly stopped to avoid leaking goroutines.

A subtle correctness detail fixed during activation illustrates the care required: the lobes capture a Workspace pointer at construction, while the agent-loop hook drains the *cortex's* Workspace via `MidturnNote`; if these are different Workspace instances (as a naïve "shell + live" construction produces), the lobes publish into a Workspace nothing reads and zero notes surface. The fix shares one Workspace between the lobes and the cortex.

The cortex is the architectural bet that *cognition is parallel and competitive*, not a single monolithic prompt — a more principled structure for "the agent should simultaneously recall relevant memory, check the rules, watch for truncation, and keep the plan updated" than concatenating all of that into one system prompt.

---

# Part VI — Evaluation, engineering, positioning

## 16. Evaluation and the truthful-completion benchmark

r1 is explicitly an *evaluation-grade* runtime, and it ships a benchmark for the very property it exists to enforce. The **TruthfulCompletion** benchmark (`internal/bench`, with a separate `cmd/r1-bench` harness and Docker reproduction kits) frames golden missions and measures whether an agent *actually* completes a scope versus *claims* to, with regression detection across runs. This closes the loop: the system that enforces truthful completion can also *measure* it, and across different agents.

The strongest evaluation evidence, though, is dogfooding (§6.4): the activation work was executed through r1's own scope→build flow with cross-model review on every unit, and that review caught seven real defects unit tests had missed. A runtime whose own development surfaces escaped bugs through its trust mechanism is making an empirical case, not merely an architectural one.

## 17. Implementation and engineering posture

- **Language and scale.** ~312,000 lines of first-party Go across 183 internal packages, ~956 test files; pinned to Go 1.25.5. CGO is required (SQLite); shipped binaries enable FTS5. Four binaries: `r1` (the runtime), `r1-server` (hosted mission API + admin), `r1-bench` (the benchmark), `r1-acp` (an Agent Client Protocol bridge).
- **Concurrency.** A small set of proven patterns recurs: RWMutex-with-copy-on-read for shared views; `WaitGroup` + buffered-channel fan-out; semaphore-via-buffered-channel for concurrency caps; one goroutine per subscriber with bounded buffers; atomic CAS for idempotent lifecycle (double-start collapses to one launch; double-stop is a no-op). The bus and cortex are race-tested.
- **Anti-stub culture.** In 312K lines, the only "not implemented" strings in first-party code live inside *anti-stub detection tooling*. Tests assert real behavior — a representative example AST-scans the ledger package to prove append-only at the type level; another reopens the bus and replays to prove durability.
- **The CI gate.** `go build ./...`, `go test ./...`, `go vet ./...` — with a `-race` variant in cloud CI (which also runs a web build, a vendor check, and an anti-truncation verification). These commands passing is the contract.
- **Self-hosting development.** The repository is itself developed by AI agents through an enforcement harness (shared playbooks; pre-tool-use and stop hooks; per-task-commit discipline) that operationalizes the same principles the product embodies — evidence-gated completion, cross-engine review, and no silent skips. (See Appendix D.)

## 18. Positioning relative to other approaches

r1 is defined less by any single feature than by the *combination*:

- **Versus autonomous-agent frameworks** (the AutoGPT/BabyAGI lineage). Those maximize autonomy and minimize verification — exactly the failure mode r1 targets. r1 is *autonomy with teeth*: every step is gated, and "done" is earned.
- **Versus single-model IDE copilots.** Those are tightly human-supervised and single-distribution. r1 is built for unsupervised or lightly-supervised runs and structurally breaks the single-distribution blind spot via cross-model review.
- **Versus offline evaluation harnesses** (SWE-bench-style). Those *measure* agents after the fact. r1 builds verification *into execution* — and ships a benchmark too.
- **Versus governance/observability add-ons.** r1's governance is not telemetry bolted on; it is a content-addressed, append-only, deterministically-ruled substrate intended to make agent activity tamper-evident by construction.

The novel claim is the *stack*: engine-agnostic execution + hard completion gates + adversarial cross-model review + deterministic tamper-evident governance + parallel cognition, integrated, with the trust mechanism demonstrably catching real bugs.

---

# Part VII — Status and future

## 19. Honest limitations

In keeping with the project's ethos, the gaps are stated plainly. None of these are stubs masquerading as features — they are real components with deliberately-bounded current scope.

- **Governance depth.** Lifecycle nodes are written and rules fire during runs, but the 7-state consensus-loop lifecycle is not driven, and rule *actions* do not yet materially steer a live run (pause/escalate are shallow).
- **Cognition depth.** Only the deterministic lobes run; the LLM-backed lobes and the mid-turn Router are deferred; per-lobe policy gating is currently all-on.
- **Semantic retrieval.** The "vector index" is cosine similarity over **bag-of-words** vectors, not neural embeddings; a real embedding provider is unwired (the code is honest about this). Non-Go repository mapping ranks by symbol count, with best-effort import edges and no call graph for regex languages.
- **Observability surfacing.** Telemetry, several metrics, and flow-tracking are recorded but rarely rendered back to the user.
- **Speculative execution** currently races plan-only first phases, not full implementations.
- **External orchestration.** The `--output stream-json` path is an intentional thin-client wire format for external orchestrators, not local execution.

## 20. Roadmap

The architecture's leverage points, in rough priority:
1. **Double down on cross-model review** — the proven differentiator. Make it cheaper and faster; add a third model family for genuine cross-distribution independence; expand the anti-rubber-stamp evidence requirements.
2. **Finish governance** — drive the consensus loops; make rule actions materially steer runs (real pause/spawn/escalate); surface the ledger as an audit UI. This is the enterprise-trust unlock.
3. **Activate full cognition** — wire the LLM lobes and the Router so mid-turn reasoning (not just deterministic notes) shapes runs; ~9k lines of engine await a provider wire.
4. **Real semantic retrieval and deeper mapping** — a neural embedding provider; per-language call graphs.
5. **Full speculative implementation racing** — race N complete implementations, not just plans.
6. **Productize** — the hosted runtime, admin, billing, and lifecycle are partly built.
7. **Observability** — render what is already collected.

The throughline is the founding thesis taken further: from "verify the outcome" toward "a fully auditable, multi-perspective, self-improving runtime you can trust to do real engineering unsupervised."

## 21. Conclusion

The bottleneck for autonomous coding agents is not capability; it is *trust* — specifically, the asymmetry between claiming and verifying completion, and the correlated blind spots of any single model. r1/Stoke is an architectural answer: make completion a gated, evidence-derived fact; review it adversarially across model families; record and govern it on a tamper-evident, deterministic substrate; and augment the run with bounded parallel cognition. The execution spine and the cross-model trust loop are mature and proven — the latter by catching real bugs in the system's own development. The governance and cognition layers are genuinely built and now wired in default-on, with their deeper capabilities a clearly-mapped frontier. The wager is simple and, on the evidence so far, sound: **an agent runtime that refuses to take its own word for it is the one you can actually let off the leash.**

---

# Appendices

## Appendix A — Key parameters and defaults
- **Cortex.** `RoundDeadline` (hard upper bound on the midturn barrier): 2 s. Cortex shutdown budget: 10 s. Pre-warm interval in MVP: suppressed (1 h). LLM-lobe concurrency cap: default 5, hard cap 8.
- **Context.** Repo-map injection budget: ~2000 tokens. Context window target: ~180k tokens with ~8k reserved for the response. Microcompaction triggers above ~80% of the window.
- **Model routing.** Fallback chain: Primary → Codex → OpenRouter → direct API → lint-only. Nine task types drive routing.
- **Governance.** Budget-rule thresholds: 50 / 80 / 100 / 120 %. Ledger node ID: `SHA-256(canonical-header ‖ SHA-256(salt ‖ content))`, type-prefixed, Merkle parent-chained per mission. ~34 supervisor rules across ~11 categories, 3 manifests; 52 ledger node types.
- **CI gate.** `go build ./...` · `go test ./...` (plus a `-race` variant in cloud CI) · `go vet ./...`.

## Appendix B — The command surface
The `r1` binary exposes ~60 subcommands. The load-bearing ones:
- `run` / `build` / `one-shot` — execute a task (free-text or via a Statement-of-Work file); `build` plans then executes with per-task review.
- `mission` — run a multi-task DAG via the mission runner.
- `simple-loop` — drive the `claude`/`codex` CLIs in a plan→build→review loop.
- `scan` / `scan-repair` — deterministic code scan and multi-phase repair.
- `serve` / `agent-serve` / `daemon` — the HTTP/daemon mission API.
- `mcp serve` — run as a stdio MCP server (38 tools); the one live cortex MCP surface.
- `verify` / `audit` / `inspect` / `watch` / `events` / `tasks` / `logs` / `cost` / `rules` / `policy` / `sessions` / `wizard` / `init` / `research` — operational and inspection commands.
- `honesty` / `artifact` / `receipt` — ledger-backed governance CLIs.
- Common flags: `--specexec`, `--governance` / `--no-governance`, `--cortex` / `--no-cortex`, `--cost-budget`, `--roi`, `--sqlite`, `--interactive`, `--output stream-json`.

## Appendix C — The package map (orientation)
```
cmd/
  r1/            the agent binary (~60 subcommands; main.go is the dispatcher)
  r1-server/     hosted mission API + admin panel
  r1-bench/      TruthfulCompletion benchmark harness
  r1-acp/        Agent Client Protocol bridge
internal/        183 packages, by concern:
  app workflow engine agentloop scheduler mission orchestrate   # live execution spine
  verify convergence critic taskstate hooks boulder failure     # completion enforcement
  supervisor ledger bus contentid stokerr bridge governance     # governance (V2)
  cortex cortex/lobes/* concern harness                          # parallel cognition
  repomap symindex depgraph goast semdiff chunker tfidf vecindex # code mapping / search
  memory wisdom research flowtrack replay                        # knowledge / memory
  model provider apiclient promptcache ctxpack microcompact      # LLM integration / context
  costtrack subscriptions pools rbac config consent scan         # cost / permissions / config
  server tui remote report viewport repl sessionctl              # UI / interfaces
  worktree atomicfs fileutil filewatcher patchapply tools        # files / edits
```

## Appendix D — A note on self-hosting development
This repository is developed *by* AI agents using a heavy enforcement harness that lives alongside the product (under `.claude/`, `.shared-playbooks/`, `.stoke/`, with `install-*.sh` installers). That harness defines agent slash-commands (scope a spec, build a spec with one subagent and one commit per checklist item, audit, verify) and pre-tool-use/stop hooks that enforce the same discipline the product embodies: evidence-gated completion (a `FIXED` claim must carry a real commit hash), cross-engine review, a forbidden-rebase / per-task-commit history convention, and no silent skips. This harness is *not* part of the shipped runtime; it is the development process, and it is itself an instance of the thesis — the people building the trust-enforcing runtime are held to the runtime's own standard of proof.
