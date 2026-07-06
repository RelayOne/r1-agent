# r1 / Stoke — Total-Takeover Handoff

> **Audience:** an engineer inheriting this project with **zero prior context**. This document assumes you have never seen the code and explains what it is, why it exists, how to run it, how it's built, the (unusual) development workflow, what's done vs. not, and where to start. Read §0–§3 first; the rest is reference.

**Repo:** `github.com/RelayOne/r1` (the working dir is `r1-agent/`) · **Language:** Go 1.25.5 · **Scale:** ~312K first-party Go LOC, 183 internal packages, ~956 test files · **Primary binary:** `cmd/r1`.
**Current point:** `main` @ `68fa2b45` (the "audit-activation" merge — see §6).

---

## 0. The 60-second orientation

**What is it?** r1 (internal codename **"Stoke"**) is a **runtime that drives AI models to write code for you and refuses to let them lie about being done.** You give it a task ("implement X", "fix this bug"); it plans, edits code in an isolated git workspace, runs the build/tests/lint, has a *second* AI model independently review the diff, and only "merges" the change if hard checks pass. If the model says "done" without evidence, the runtime rejects it.

**Why does it exist?** The founding belief is that coding agents get lazy and claim completion they didn't achieve. So instead of trusting the model, r1 wraps every step in deterministic gates, evidence requirements, and **cross-model adversarial review** (Model A writes, Model B reviews).

**Two mental anchors:**
1. It can run an LLM **two different ways**: (a) a *native* loop that calls the Anthropic API directly, or (b) by **shelling out to the `claude` / `codex` / `gemini` command-line tools** inside throwaway git worktrees. Both are real and used.
2. The whole thing is a **pipeline of gates**: `plan → execute → verify(build/test/lint) → cross-model review → self-audit → MERGE (only if all gates pass)`.

**One honest caveat up front:** the codebase has two layers. The **execution + review + gates** layer is mature and load-bearing. A second **"governance / cognition" layer** (a deterministic rules engine, an append-only audit ledger, and a 9k-line "cortex" cognition engine) was built as solid libraries that *until very recently didn't actually run in a real task* — they were just wired in (default-on) during the last work cycle, with some depth still deliberately deferred. §5 is brutally honest about what's real vs. aspirational.

---

## 1. Heads-up: this repo is *developed by AI agents*, and it shows

Before you're confused by the hundreds of files that aren't Go: **this project is itself built using a heavy AI-agent development harness.** You'll see:
- `.claude/`, `.claude/hooks/`, `.claude/skills/`, `.shared-playbooks/`, `.stoke/`, `AGENTS.md`, `CODEOWNERS`, `install-*.sh` — these are **tooling for the agent-driven development process**, not part of the product. They define slash-commands (`/scope`, `/build`, `/audit`…) and **PreToolUse/Stop hooks** that enforce process rules on the *agents* writing the code.
- The Go product itself lives under `cmd/` and `internal/`. **You can develop it with plain Go tooling** (`go build`, `go test`) and ignore the agent harness if you're working as a human.
- Caveat: some of those hooks will still fire if you run things through the agent tooling — e.g. they **forbid `git rebase`** (to preserve per-task commit history), **block editing `CLAUDE.md`** via tools, and enforce a per-task-commit cadence. As a human with a normal shell you're mostly unaffected, but **don't rebase** — this repo's history convention is per-task commits + merge commits.

**`CLAUDE.md` is the single most important file** — it's the canonical package map, the list of 28+ key design decisions, and the build/test gate. Read it. (It's permission-locked from the agents, so humans edit it.)

---

## 2. Getting it running locally

### Prerequisites
- **Go 1.25.5** (the module pins it; `go.mod` line 3).
- **CGO enabled** — the project uses `mattn/go-sqlite3`; builds need a C toolchain. The shipped binary is built with `-tags sqlite_fts5` (enables SQLite full-text search).
- For **real runs** (not just tests): the `claude` and/or `codex` CLIs installed and authenticated, **and/or** an Anthropic API key for the native loop. Tests do **not** need these (they use fakes).
- A short `TMPDIR` (e.g. `export TMPDIR=/tmp`) — some integration tests build unix-socket paths that overflow the 108-byte `sun_path` limit if `TMPDIR` is deep.

### Build & test (this is the CI gate — memorize it)
```bash
go build ./...          # everything compiles
go test ./...           # all tests (CI also runs the -race variant)
go vet ./...            # static checks
# These three green = CI green. (cmd/r1/CLAUDE.md states this explicitly.)

make build              # produces bin/r1 and bin/r1-acp (uses -tags sqlite_fts5)
go build ./cmd/r1       # quick build of just the agent binary
```

### Run it
```bash
./bin/r1 --help                       # ~60 subcommands
./bin/r1 build --task "..." ...       # plan+execute a task (classic workflow)
./bin/r1 run --sow path/to/spec.md    # run a Statement-of-Work (multi-task)
./bin/r1 mission ...                  # multi-task DAG mission
./bin/r1 mcp serve                    # run as an MCP server (38 tools)
```
Useful flags: `--specexec` (speculative parallel strategies), `--governance`/`--no-governance`, `--cortex`/`--no-cortex`, `--cost-budget`, `--roi`, `--sqlite`, `--interactive`.

### If something fails
- **Tests leak processes / touch git?** That suite was non-hermetic historically; it's fixed now. Use the `newTempGitRepo` / `shortCtlDir` / `TestMain`-trap helpers in `cmd/r1` for any new `serve`/socket/git test.
- **`-race` flake in `internal/throttle`?** Known: a time-based rate-limiter test; the numeric bound is asserted only in non-race runs now.
- **Build fails on sqlite/FTS5?** Ensure CGO + a C compiler; the `sqlite_fts5` tag needs them.

---

## 3. The mental model of a "run" (read this once, slowly)

When you ask r1 to do a task, here is the real control flow (file pointers so you can read along):

```
cmd/r1/main.go  (runBuild)                  # parse flags -> BuildConfig -> per-task app.RunConfig
  → internal/app/app.go  (Orchestrator.Run) # load policy; build engines + worktree + verifier;
                                             #   construct the Governor (governance, default-on);
                                             #   build workflow.Engine; run it
    → internal/workflow/workflow.go          # THE PHASE MACHINE (the heart):
        1. plan        (LLM produces a plan)
        2. execute     (LLM edits code in an isolated git worktree; retries on failure)
        3. verify      (runs build/test/lint via internal/verify)
        4. cross-model review   (the OTHER engine reviews the diff; verdict must pass)
        5. convergence (adversarial self-audit)
        6. MERGE GATE  (~workflow.go:1115): merges ONLY if every gate passed
```

- **Execution engine** is chosen per task: `internal/engine/native_runner.go` (Anthropic API via `internal/agentloop`) **or** `internal/engine/claude.go` / `codex.go` / `gemini.go` (spawn the CLI as a subprocess, in a git worktree, with OS process-group isolation).
- **Isolation:** every task runs in its own **git worktree** (`internal/worktree`) so concurrent tasks don't collide; merges to the base branch are serialized.
- **The gates are real:** verify failure → retry/escalate, never reaches merge. Review verdict `pass=false` → cleanup + fail. There's even an *anti-fakery* verdict parser that rejects an empty `{pass:true}` from a reviewer that didn't actually look.
- **The cross-model review is the standout feature.** Claude implements, Codex reviews (or vice-versa). In the most recent build cycle, this loop caught **7 real bugs** that all the passing unit tests had missed. If you keep one thing, keep this.

---

## 4. Glossary (the project's private vocabulary)

| Term | Meaning |
|---|---|
| **Stoke** | The internal codename for r1 (you'll see `.stoke/`, `stoke` in code/tests). Same thing. |
| **SOW** | "Statement of Work" — a multi-task spec file the runtime executes (`r1 run --sow`, `r1 sow`). |
| **Mission** | A DAG of tasks executed with promotion gates and an evidence trail (`internal/mission`, `r1 mission`). |
| **Engine / runner** | A way to execute an LLM: the *native* runner (API) or a *CLI* runner (claude/codex/gemini). |
| **Cross-model review** | One engine reviews the other's diff; merge blocks on the verdict. The core trust mechanism. |
| **Phase machine** | The plan→execute→verify→review→merge state machine in `internal/workflow`. |
| **Worktree** | A throwaway git worktree per task for isolation (`internal/worktree`). |
| **Governance / "V2"** | The deterministic-rules + append-only-ledger + durable-bus layer (`internal/supervisor`, `ledger`, `bus`), now driven by `internal/governance.Governor`. |
| **Supervisor** | The deterministic rules engine (34 rules) — fires actions (pause, escalate, spawn reviewer) on bus events. NOT the same as the v1 "boulder" idle-detector also called "supervisor" in places. |
| **Ledger** | Append-only, content-addressed, Merkle-chained audit graph (`internal/ledger`); 52 node types; no Update/Delete methods. |
| **Cortex** | A Global-Workspace-Theory parallel-cognition engine (`internal/cortex`): N "Lobes" run concurrently and publish "Notes" the main loop reads at checkpoints. |
| **Lobe** | A cortex specialist (memoryrecall, walkeeper, rulecheck, antitrunc, planupdate, …). 4 "deterministic" lobes are active; LLM lobes are deferred. |
| **Wisdom** | Cross-task learnings (gotchas/decisions) captured and injected into future prompts (`internal/wisdom`). |
| **specexec** | Speculative execution: run N strategies in parallel, score, pick the winner (`internal/specexec`, `--specexec`). |
| **GRPW** | The scheduler's priority ordering ("greatest remaining priority weight") — tasks with the most downstream work dispatch first. |
| **antitrunc** | "Anti-truncation" — detection of laziness/truncation phrases + scope under-delivery. |
| **boulder** | Idle-detection / continuation enforcement (`internal/boulder`). |
| **CloudSwarm / Multica / OpenACP** | **External** orchestrators that consume r1's `--output stream-json` wire format. r1 acts as a thin client to them; it does not implement them. (Confirm specifics with the original author.) |
| **RelayOne / RelayGate / CodeRadar / BetBuddies** | Sister products / the parent org's ecosystem (seen in commit history and `services/`). Context lives outside this repo. |

---

## 5. What's actually done vs. not (honest)

Ratings: **LIVE** = wired and exercised on the real path · **LIBRARY** = real & tested but used narrowly · **DEFERRED** = built but capability not fully turned on.

### Solid and load-bearing (trust these)
- **Dual-engine execution + cross-model review + gated merge** — LIVE. `internal/{engine,app,workflow,scheduler,model}`. The merge gate genuinely blocks (`workflow.go:~1115`).
- **Completion enforcement** — LIVE. `internal/{taskstate,verify,convergence,critic}`. Anti-fakery, scope/protected-file re-scan, evidence gates.
- **Cost → USD + budget halt** — LIVE. `internal/costtrack` (a run aborts when over budget).
- **Wisdom memory loop** + agent-tool memory (`memory_store`/`recall`) + cross-session bridge — LIVE.
- **Code mapping** injected into prompts — LIVE. `internal/{repomap,symindex,depgraph,semdiff,goast}` (now multi-language).
- **MCP server** (`r1 mcp serve`, 38 tools), **TUI** (`internal/tui`), **mission API server** (`cmd/r1-server`) — LIVE.

### Recently activated (default-on, but shallow — verify before relying)
- **Governance** (`internal/governance.Governor`) — bridges the live event hub into the durable bus + ledger + supervisor; the "second-opinion" trust gate now fires. **DEFERRED:** the `ledger/loops` 7-state consensus machine isn't driven; supervisor pause/escalate actions fire but don't deeply steer a run yet.
- **Cortex** — 4 **deterministic** lobes run in the native loop, produce midturn notes, can block `end_turn` on a critical note; bounded so it can't hang. **DEFERRED:** the **LLM lobes** (planupdate/clarifyq/memorycurator), the **Router** (mid-turn input routing), per-lobe policy gating, and the durable-bus/pre-warm features are off by design.

### Not built / known-shallow
- **Neural embeddings** — `vecindex` is **bag-of-words** cosine, not real embeddings (honestly relabeled). Wiring a real `EmbedFunc` is open.
- **Non-Go repo map depth** — ranks by symbol count; import-edge matching is best-effort; no call graph for regex languages.
- **Observability surfacing** — `telemetry`/`metrics`/`flowtrack` are recorded but rarely rendered back.
- **CloudSwarm `--output stream-json`** — intentional thin-client wire format for an external orchestrator, not local execution (not a bug — by design).
- **Shell-hook enforcement layer** (`.claude/hooks/enforce-*.sh`) — operator-installed; the Go-level gates are the real enforcement.

### Gotchas that will bite you
- `cmd/r1` tests were historically non-hermetic (leaked `serve` daemons, ran git in the real repo) — fixed; keep new tests hermetic.
- `internal/throttle.TestConcurrentSafety` flakes under `-race` (time-based; bound now non-race-only).
- Deep `TMPDIR` → unix-socket `sun_path` overflow in `sessionctl` tests → use a short `TMPDIR`.
- **Local `main` is often stale** vs `origin/main` (the team promotes via PRs); always `git fetch` and branch off `origin/main`.
- **`CLAUDE.md` is permission-locked** from agent tools; humans edit it directly.

---

## 6. Repo structure (where to change things)

```
cmd/
  r1/            THE agent binary (~60 subcommands; main.go is the dispatcher, ~7k lines)
  r1-server/     hosted mission API + admin panel (r1.run, admin.r1.run)
  r1-bench/      TruthfulCompletion benchmark harness (Docker reproduction kits)
  r1-acp/        Agent Client Protocol bridge
internal/        183 packages. The big ones, by concern:
  app workflow engine agentloop scheduler mission orchestrate   # the live execution spine
  verify convergence critic taskstate hooks boulder             # completion enforcement
  supervisor ledger bus contentid stokerr bridge governance     # V2 governance
  cortex cortex/lobes/* concern harness                         # parallel cognition
  repomap symindex depgraph goast semdiff chunker tfidf vecindex# code mapping/search
  memory wisdom research flowtrack replay                       # knowledge/memory
  model provider apiclient promptcache ctxpack microcompact     # LLM integration
  costtrack subscriptions pools rbac config consent scan        # cost / permissions / config
  server tui remote report viewport repl sessionctl             # UI / interfaces
  worktree atomicfs fileutil filewatcher patchapply tools       # files / edits
specs/           Build specs (the unit of planned work). Recent: governance-activation,
                 cortex-activation, repomap-multilang, cmd-r1-test-isolation (all STATUS: done)
audit/           Audit reports + per-spec reviews (start with deep-audit-eval-runtime-2026-06-02.md)
docs/            README, ARCHITECTURE, HOW-IT-WORKS, FEATURE-MAP, DEPLOYMENT, BUSINESS-VALUE, this file
plans/           HANDOFF.md (session checkpoints) + plan files
services/        Cloud Build configs, deploy yaml, cron setup
.claude/ .shared-playbooks/ .stoke/ AGENTS.md   # the agent-DEVELOPMENT harness (not the product)
```
**"I want to change X" →**
- the gates / what blocks a merge → `internal/workflow/workflow.go`
- how an LLM is executed → `internal/engine/` (`native_runner.go`, `claude.go`, `codex.go`)
- model selection / fallback → `internal/model/router.go`
- governance behavior → `internal/governance/governance.go` + `internal/supervisor/rules/`
- cortex behavior → `internal/cortex/` + `internal/cortex/lobes/`
- the repo map fed to prompts → `internal/repomap/repomap.go`
- a CLI flag / subcommand → `cmd/r1/main.go`

---

## 7. The development & release workflow (important & non-obvious)

- **Branch model:** `dev → staging → main`, promoted via PRs (you'll see `sync: staging → main` commits). **`main` is protected and triggers deploys** (`.github/workflows/{r1d-server,desktop-augmentation,web}.yml`). Don't push straight to main.
- **Local `main` drifts behind `origin/main`** — always `git fetch origin` and base work on `origin/main`.
- **No rebase** — repo convention (and an agent hook) preserve **per-task commit history**; integrate via cherry-pick or merge, not rebase.
- **Commit style:** `feat(...)`, `fix(...)`, `test(...)`, `docs(...)`; FIXED claims carry a commit hash. Co-authored-by trailers are expected from the agent flow.
- **CI:** GitHub Actions (`go vet`, `lint-chdir`) + **GCP Cloud Build `r1-agent-pr`** (project `resolute-parity-484218-g1`) which runs web-build, vendor-check, test, **race**, antitrunc-verify. The `race` step is the strictest (catches timing flakes).
- **The agent workflow** (if you use it): `/scope` (write a spec) → `/build` (execute the spec, one subagent + one commit per checklist item, with cross-model review) → merge. Specs live in `specs/`, reviews in `audit/`.

---

## 8. External dependencies & ecosystem (so the names aren't a mystery)
- **Anthropic API + `claude` CLI** and **OpenAI/`codex` CLI** (and optionally `gemini`) — the actual models/engines. Real runs need at least one authenticated.
- **GCP project `resolute-parity-484218-g1`** — Cloud Build CI + deploys. `gcloud` access needed to inspect builds.
- **go-sqlite3 (CGO), golang.org/x/time/rate, Bubble Tea, yaml.v3** — key libraries.
- **RelayOne ecosystem** (parent org): sister products and external orchestrators referenced in code/history — **CloudSwarm / Multica / OpenACP** (consume r1's stream-json), **CodeRadar, RelayGate, BetBuddies**, the hosted **`r1.run` / `admin.r1.run`**. Their details live outside this repo — **ask the original author**; treat them as integration endpoints, not things you own.

---

## 9. Your first week
1. **Day 1:** Read `CLAUDE.md` end to end. Build + test (`go build ./... && go test ./... && go vet ./...`). Run `./bin/r1 --help`.
2. **Day 2:** Trace one real run with the code open: `main.go (runBuild)` → `app.go (Orchestrator.Run)` → `workflow.go` (find the merge gate). Read `engine/native_runner.go` and `engine/claude.go`.
3. **Day 3:** Read the audit (`audit/deep-audit-eval-runtime-2026-06-02.md`) — it's a subsystem-by-subsystem reality check. Then skim the 4 activation specs in `specs/` and their `audit/spec-review-*.md`.
4. **Day 4:** Make a tiny, safe change (e.g., a log line or a new unit test in a leaf package like `repomap` or `wisdom`), get it through `go test`, open a PR to `dev`, watch CI.
5. **Day 5:** Pick one **DEFERRED** item from §5 (smallest = "render the telemetry that's already collected", or "deepen non-Go repomap import edges") and scope it.

---

## 10. Open questions only the original author can answer
- The exact contracts of the external orchestrators (**CloudSwarm / Multica / OpenACP**) and which are live.
- Deploy topology & secrets for `cmd/r1-server` / `r1.run` (partly in `services/` but org-specific).
- Whether governance/cortex should be pushed from "wired" to "deeply driving runs" (loops + LLM lobes) as a near-term priority.
- The intended productization path (SaaS billing/admin are partly built).
- The status of the agent-development harness (`.shared-playbooks/`, the `install-*.sh` hook installers) — operator tooling that isn't fully captured in-repo.

---

## 11. One-paragraph summary
**r1/Stoke is a mature, engine-agnostic AI coding-agent runtime whose differentiated, *proven* core is: execute a task with either the Anthropic API or the claude/codex/gemini CLIs in isolated git worktrees, then gate "done" behind real build/test/lint verification and an independent cross-model review that actually blocks merges (it has caught real bugs unit tests missed).** On top of that solid spine sits a governance layer (deterministic rules + tamper-evident append-only ledger + durable bus) and a parallel-cognition "cortex" — both genuinely built and now wired in default-on, with their deeper capabilities (consensus loops, LLM lobes, the router) deliberately deferred and clearly documented. The honest backlog is in the specs' deferred sections; the strongest investment is doubling down on the cross-model-review trust mechanism and finishing the governance/cognition depth.
