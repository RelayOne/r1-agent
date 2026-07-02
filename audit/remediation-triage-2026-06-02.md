# Audit Remediation — Triage & Status

**Date:** 2026-06-02
**Branch:** `fix/audit-remediation-2026-06-02`
**Source audit:** `audit/deep-audit-eval-runtime-2026-06-02.md`
**Instruction:** "fix all and complete"

Honest scope statement: of the 10 findings, 3 are mechanical and verifiable in one
session; the other 7 are genuine multi-day features or require operator decisions.
This repo's own iron law forbids claiming completion without fresh verification, so
the large items are marked **BLOCKED** with concrete implementation specs rather than
faked. Present to user for triage.

---

## UPDATE — same session (after user triage)

User directed: tackle B1 next; apply doc fixes (B4 relabel + B6 counts); explain B5/B7 before touching. Resulting status:

- F1 FTS5 — FIXED, commit 72053d5f.
- F2 memory bridge — FIXED, commit 72053d5f.
- B4 embeddings — FIXED (honest relabel), commit 301068d6. vecindex package doc now states results are lexical bag-of-words, not neural; names the upgrade path.
- B1 governance — FIRST SLICE DONE + verified, commit c5e5e787. New internal/governance.Governor constructs durable bus + ledger + supervisor (all MissionRules, Start()), plus a hub->bus bridge so cost events fire the budget rule and task events write ledger nodes. Default-OFF via RunConfig.GovernanceEnabled / policy.Governance.Enabled. 4/4 governance tests pass; config + all supervisor packages still green. Remaining B1 (next sessions): more event mappings, ledger/loops transitions, trust/second-opinion rule (needs review nodes), --governance CLI flag, default-on rollout + live-run integration test.
- B5 CloudSwarm — RESOLVED as intentional (no code change). --output stream-json is an external-orchestrator wire contract: main.go:1731 says it is "Consumed by Multica, OpenACP, and other orchestrators"; internal/skill/compat/cloudswarm.go and provider/correlation_wire.go shape output for CloudSwarm's loader. The local binary is a thin emitter by design; the external fleet dispatches. Audit "SCAFFOLD" label corrected to "intentional thin-client wire format."
- B7 shell hooks — RESOLVED as operator tooling (no code change). The untracked install-hook-precision-refinements.sh / install-override-protocol.sh are the installers that populate .claude/hooks/ (+ .checksums). The missing hooks are "not installed in this checkout," not bit-rotted product. Fix = run the installers (operator action). Go-level merge gates are intact independent of these.
- B6 doc counts — BLOCKED at the harness permission layer: CLAUDE.md edits return "directory denied by permission settings", which conversational approval cannot lift. Exact diff handed to user (183 internal / 52 node types / 11 categories / 34 rules / 10 bench).
- B2 cortex / B3 mapping — still BLOCKED (multi-day); specs above unchanged.
- I1 test isolation — confirmed live this session: go test ./cmd/r1 leaked ~120 r1.test serve daemons that ran git in the repo root and raced .git/index.lock. Still open.

---

## STATUS: FIXED (commit: b456ddd8)

### F1 — FTS5 compiled out of every shipped binary → activated
- **Was:** No build path set the `sqlite_fts5` tag, so `research`, `wisdom/sqlite`,
  and `membus` "full-text search" silently ran the `LIKE` fallback in all shipped
  binaries; tests never asserted `hasFTS` so it passed regardless.
- **Fix:** Added `-tags sqlite_fts5` to the `r1` build in `Makefile:16` and
  `install.sh:56`; added `internal/research/fts5_active_test.go` (tag-guarded) that
  fails if `hasFTS` is false under the tag. Untagged build still compiles (graceful
  fallback preserved).
- **Verified:** `go build ./...` exit 0; `go build -tags sqlite_fts5 ./cmd/r1` exit 0;
  `go test -tags sqlite_fts5 ./internal/research` ok.

### F2 — `app.RunConfig.Memory` cross-session recall bridge was dead → wired
- **Was:** `app.go:230` recalls cross-session memory into wisdom→prompt, but
  `cfg.Memory` was never assigned in the CLI, so the path never ran.
- **Fix:** Added `openCrossSessionMemory()` (`cmd/r1/main.go:6555`) loading the same
  `.r1/agent-memory.json` the in-task `memory_store` tool writes; wired at both
  `app.RunConfig` construction sites (`main.go:431` runBuild, `:6582` buildRunConfig).
  Regression test `cmd/r1/cross_session_memory_test.go` proves the round-trip and the
  fresh-repo-safe case.
- **Verified:** `go vet ./cmd/r1` exit 0; `go test -run TestOpenCrossSessionMemory ./cmd/r1` ok.

---

## STATUS: BLOCKED — multi-day features (need user go-ahead + multiple sessions)

### B1 — V2 governance not wired into the mission path  *(highest value)*
- **Finding:** `supervisor` (34-rule engine) has **zero non-test callers**; `ledger`
  and `bus` are constructed only by CLI/audit subcommands, never by the mission/
  workflow executor. The "deterministic governance" is real as a library but governs
  nothing at runtime.
- **Spec to fix (real, not token):**
  1. In `app.New`/`orchestrator.Run` (or `mission.Runner`), construct a `bus.Bus`
     (durable WAL under `.r1/bus/`) and a `ledger.Store` (`.r1/ledger/`) once per run.
  2. Write `ledger` nodes at each real lifecycle edge already emitted on the hub:
     task.start, plan.ready, execute.done, verify.{pass,fail}, review.verdict,
     merge.done — map each hub event to the existing node type in `ledger/nodes/`.
  3. Construct `supervisor.New(manifests.Mission(...))`, subscribe it to the bus, and
     publish those same lifecycle events to the bus so rules (budget_threshold,
     completion_requires_second_opinion, …) actually fire and can pause/escalate.
  4. Drive `ledger/loops` transitions from the review/convergence states.
- **Risk:** High — touches the hot execution path; the `supervisor` actions can
  pause/halt a worker, so they must be gated behind a config flag
  (`policy.Governance.Enabled`, default off) until proven.
- **Effort:** ~2–4 sessions. **Verify:** new integration test driving a fake mission
  through the bus and asserting ledger nodes + a rule firing; cannot be validated by
  a live run here (needs claude/codex CLIs + API keys).

### B2 — Cortex parallel-cognition engine is never started
- **Finding:** `cfg.Cortex` (the `agentloop.CortexHook`) is never assigned in any live
  path; the MCP backend wires lobes but never calls `cortex.Start()` ("Lobe runners
  are inert until Start"). ~9k lines of cognition code never executes.
- **Spec:** Assign `cfg.Cortex` in `engine.NativeRunner` config build with the 4
  deterministic lobes only (no LLM provider needed); call `Start()`/`Stop()` around the
  agent loop; verify the `MidturnNote` barrier deadline can't stall the loop (cap the
  wait, already present). Leave LLM lobes (`"all"` mode) behind the existing
  `stubProvider` until a provider is wired.
- **Risk:** Medium-high — adds a midturn barrier to the hot loop (latency/hang risk).
- **Effort:** ~1–2 sessions. **Verify:** a native-loop test asserting cortex Notes are
  produced and `PreEndTurnGate` blocks on a critical Note.

### B3 — Mapping is Go-only (undercuts "complete agnostic")
- **Finding:** `repomap.Build` runs entirely on `goast.AnalyzeDir` (`go/parser`); the
  ranked map injected into execute prompts (`workflow.go:2049`) is empty for non-Go
  repos. `symindex` is multi-language but regex-only; `semdiff` is AST for Go, line for
  others.
- **Spec:** Add a language-agnostic fallback ranker: feed `symindex` (already
  Python/TS/JS/Go via regex) + import heuristics into the same PageRank graph when
  `goast` yields nothing, so `RenderRelevant` produces a real map for non-Go repos.
- **Risk:** Medium. **Effort:** ~2 sessions (per-language import extraction).
- **Alternative (cheap, honest):** if multi-language mapping is out of near-term scope,
  correct the "complete agnostic" marketing instead — but that's a doc decision (B6).

### B4 — "Embeddings" are bag-of-words, not neural
- **Finding:** `vecindex` cosine math is real but every production caller uses
  `BagOfWordsEmbed`; no neural embedding provider is wired. Code comments are honest;
  the package name / docs imply more.
- **Spec (choose one):** (a) wire a real `EmbedFunc` via an embedding provider
  (OpenAI/Voyage/local) behind config + API key, or (b) relabel as lexical/TF search in
  docs and package comment. (a) is a feature (~1 session + keys); (b) is a doc edit.
- **Decision needed from user:** real embeddings vs honest relabel.

### B5 — CloudSwarm `r1 run --output stream-json` is announce-only
- **Finding:** `dispatchCloudSwarmSOW` "intentionally does NOT fork a worker";
  `dispatchCloudSwarmFreeText` emits `"status":"announce_only"`. The newest streaming
  entrypoint does no execution.
- **Spec:** Either implement worker dispatch (fork the native runner per session and
  stream lifecycle spans for real) or document it explicitly as an orchestrator-only
  wire format. **Decision needed:** is this meant to execute, or is it a contract for an
  external orchestrator?
- **Effort:** ~1–2 sessions if it should execute.

---

## STATUS: BLOCKED — needs operator decision (not a code fix I should make unilaterally)

### B6 — Doc drift in `CLAUDE.md` (permission-protected)
- Correct counts: **183** internal packages (not 132), **52** ledger node types
  (not 22), **11** rule categories (not 10), **34** rules (not 30), **10** bench pkgs.
- `CLAUDE.md` is permission-protected (editing project instructions should be explicit).
  **Needs user approval** to apply, or user edits it. Note: counts are *understated* —
  the code exceeds the docs — but the docs imply governance is active when it isn't (B1).

### B7 — Bit-rotted shell-hook layer
- ~40 `enforce-*.sh` / `detect-*.sh` hooks referenced by the SessionStart machinery are
  missing (`FAILED open or read` for all of them this session). These live in
  `.claude/hooks/` (operator tooling), not the Go tree. The Go-level merge gates
  (`workflow.go`) are the real enforcement and are intact. **Needs user decision:**
  restore the shell hooks (from where?) or remove the stale references.

---

## Incidental finding (surfaced during remediation)

### I1 — `cmd/r1` test suite has an isolation bug
- Several `cmd/r1` tests (`serve`/`agent-serve`/sessionctl, skill-pack-pull) run real
  `git` commands in the repo root and leak `r1.test serve` daemon processes that don't
  get reaped. Running `go test ./cmd/r1` spawned ~120 orphaned `serve` daemons and
  repeated `git commit`/`pull`/`status` in the working repo, racing `.git/index.lock`.
- Also `TestStartSessionCtlServer_DefaultStatus_ModeMatches` is flaky: it binds a unix
  socket under `t.TempDir()`, whose path can exceed the 108-char `sun_path` limit →
  `bind: invalid argument`. Should use a short socket dir.
- **Not caused by the F1/F2 changes** (additive, don't touch sessionctl). Logged as a
  finding for triage.
