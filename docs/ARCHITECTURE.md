# Architecture

Trunk architecture view for r1 as of 2026-05-15 — after specs 1-9 merged, 12 public Cloud Run SaaS surfaces were verified live, and the final-sweep PRs #168 / #169 / #170 / #171 remained merged (sync to `main` in commit `242af4a8`) bringing skill-aware compaction, ed25519-signed redaction events, the v2 tracebundle export format, and the release-rehearsal CI lane.

## Audience

- Engineers maintaining the runtime, daemon, web, desktop, or agentic harness.
- Reviewers checking whether docs match shipped code.
- Operators standing up the SaaS surfaces or onboarding to ops scripts.
- Stakeholders who need the system shape without reading every package.

## Tech stack

R1 currently has five architectural planes that matter together:

1. Mission execution: planning, execution, verification, review
2. Governance and evidence: ledger, WAL, receipts, honesty, cost
3. Deterministic skills: compile, manufacture, register, select, run
4. Distribution and runtime extension: packs, registries, MCP-backed
   runtime functions
5. Anti-truncation enforcement: regex catalog, scope-completion gate,
   supervisor rules, agentloop wiring, post-commit git hook, and
   `r1 antitrunc verify` CLI / MCP tool — a layered, machine-
   mechanical defense against LLM self-reduction. Each layer is
   independently effective so the model cannot side-step one and
   pass.
| Layer | Technology | Pinned version |
|---|---|---|
| Core runtime | Go | 1.25.5 (1.26+ for cmd/r1 binaries via cloudbuild) |
| Daemon WS | `github.com/coder/websocket` | v1.8.13 |
| TUI | `charm.land/{bubbletea,bubbles,lipgloss}` v2 + `winder/bubblelayout` | v2.0.x / v2.1.x |
| Web UI | React + Vite + Tailwind + shadcn/ui | 18.3 / 6.0 / 3.4 / latest CLI |
| Web state | zustand | ^5.0.0 |
| Web router | react-router | ^7.0.0 |
| Web markdown | `vercel/streamdown` (anchor) | ~1.2.0 |
| Web AI hook | `@ai-sdk/react` (AI SDK 6) | ^3.0.176 |
| Web testing | Vitest + Playwright + axe-core/playwright | ^2.1 / ^1.49 / ^4.10 |
| Desktop | Tauri 2 + tauri-plugin-{websocket,store} | latest |
| SaaS services | Go (distroless static images) | 1.25 |
| SaaS DB | Cloud SQL Postgres 16 | db-g1-small (prod) / db-f1-micro (staging+dev) |
| Auth (planned) | `@relayone/auth-core` (Path A: Go port) | private package |

## Repository map (175 internal packages + 10 cmd binaries + services + web + desktop)

```
cmd/r1/                            CLI entrypoint — 30+ subcommands. Anti-truncation, lanes, missions, MCP serve, etc.
cmd/r1-mcp/                        Standalone MCP-over-stdio server.
cmd/r1-server/                     Mission API HTTP server. Hosts GET /api/session/{id}/export.tracebundle (always-on post-Spec-D; the prior R1_SERVER_UI_V2 envelope was removed). tracebundle_source.go is the production ledger-backed source; calls Store.ListNodesForSession / ListEdgesForSession / ChainRootHashForSession / CanonicalManifestSignBody.
cmd/r1-acp/                        Agent Client Protocol adapter.
cmd/r1-a2a/                        Agent-to-Agent transport.
cmd/r1-gateway/                    Reverse-proxy gateway for distributed pools.
cmd/r1-skill-compile/              Deterministic skill compiler.
cmd/chat-probe/                    Chat session probe utility.
cmd/critique-compare/              Cross-model critique comparison.
cmd/heroa-e2e/                     Heroa-platform end-to-end harness.

--- V2 GOVERNANCE ---
contentid/                         Content-addressed ID generation (SHA256, 16 prefixes)
stokerr/                           Structured error taxonomy (10 error codes)
ledger/                            Append-only content-addressed graph (nodes, edges, filesystem + SQLite)
ledger/nodes/                      22 node type structs with NodeTyper interface
ledger/loops/                      7-state consensus loop tracker
ledger/redact_log.go               SignedRedactionEvent + RecordRedaction / RedactionsFor / RedactAndLog
ledger/redact_sign.go              ed25519 signing — LoadOrGenerateSigningKey, SignRecord, VerifyRecord, RedactionsForVerified
ledger/store_session.go            Per-session filtering (ListNodesForSession / ListEdgesForSession), ChainRootHashForSession, CanonicalManifestSignBody — tracebundle-v2 backbone
bus/                               Durable WAL-backed event bus (hooks, delayed events, causality)
supervisor/                        Deterministic rules engine (30 rules, 10 categories, 3 manifests)
supervisor/rules/antitrunc/        Anti-truncation supervisor rules (3 rules)
concern/                           Per-stance context projection (10 sections, 9 role templates)
concern/skill_compactor.go         SkillCompactor + EvictionPolicy (default LRUPolicy) — per-stance LRU skill eviction under budget pressure; calls skilltracker.EvictByCompactor
harness/                           Stance lifecycle: spawn/pause/resume/terminate (11 templates)
skilltracker/                      Per-stance loaded-skill table (Note / Drop / CloseScope / EvictByCompactor); emits SkillLoaded / SkillUnloaded ledger nodes
snapshot/                          Protected baseline manifest
wizard/                            First-time config presets
skillmfr/                          Skill manufacturing pipeline
skill/                             v1 + v2 manifest, registry, integrations, federated trust root (C7)
skill/compat/                      Runtime adapters: r1, cloudswarm, heroa, veritize (C7)
bench/                             Golden mission benchmarking
bridge/                            V1→V2 bridge adapters

--- CORTEX (specs 1-2) ---
cortex/                            Workspace, Lobe, Round, Spotlight, Router (parallel cognition substrate)
cortex/lobes/llm/                  LobePromptBuilder (cache-aligned 1h TTL), Escalator
cortex/lobes/memoryrecall/         TF-IDF + memory + wisdom indexing
cortex/lobes/walkeeper/            Drains hub events to durable WAL
cortex/lobes/rulecheck/            supervisor.rule.fired → severity-mapped Notes
cortex/lobes/planupdate/           Every-3rd-turn or verb-scan plan delta proposer
cortex/lobes/clarifyq/             Turn-after-user clarifying-question drafter
cortex/lobes/memorycurator/        Every-5-turn fact extractor with privacy filter
cortex/lobes/antitrunc/            AntiTruncLobe (publishes critical Workspace Notes)

--- LANES (spec 3) ---
streamjson/                        Lane wire-format adapter
mcp/lanes_server.go                LanesServer (5 r1.lanes.* tools)

--- DAEMON (spec 5) ---
server/                            HTTP+WS server with sessionhub, ws, sse, jsonrpc
server/sessionhub/                 Session lifecycle + workdir validation + chdir sentinel
server/ws/                         WebSocket handler with subprotocol-token auth
server/ipc/                        unix socket / named pipe transports
server/jsonrpc/                    JSON-RPC 2.0 envelope + 22 RPC methods
server/static/                     Embedded //go:embed dist (web UI bundle target)
daemonlock/                        gofrs/flock single-instance enforcement
daemondisco/                       ~/.r1/daemon.json discovery + token rotation
serviceunit/                       kardianos/service per-OS service unit installer

--- ANTI-TRUNCATION (spec 9) ---
antitrunc/                         Phrase catalog, gate, scopecheck, soak driver
agentloop/antitrunc.go             Gate composition wiring (composes BEFORE all other end-turn hooks)
cmd/r1/antitrunc_cmd.go --hook-mode  Claude Code Stop-hook adapter (writes JSON envelope, exit code 2 on findings)

--- TRUTHFULCOMPLETION BENCHMARK (spec truthful-completion-benchmark) ---
bench/MissionConfig + RunResult    Schema extensions for plan items + completion verdict bits
bench/verdict.go                   VerdictScorer combines plan satisfaction + delivery ratio + LLM judge
bench/judge.go                     apiJudge wraps apiclient.Client; cross-vendor constraint enforced upstream
bench/stats.go                     WilsonCI 95% confidence interval, no continuity correction
bench/leaderboard.go               BuildLeaderboard + RenderMarkdown over []RunResult
bench/permission_render.go         BuildPerMissionTable for drill-down view
bench/agents/                      Dispatcher interface + 8 implementations (R1, claude-code-{default,stop-hook}, cline, aider, codex-cli, cursor, tether)
bench/golden/truthful-completion/  5 seed missions (95 SWE-bench Pro missions deferred to operator curation)
cmd/r1-bench/                      Runner CLI: drives Dispatcher → VerdictScorer → JSON RunResult
cmd/r1-bench/vendor.go             Agent/model → vendor mapping; refuses same-vendor judge
cmd/r1-bench/container.go          Hermetic-run Dockerfile template generator
cmd/r1-bench/reproduction-kit/     docker-compose + per-agent Dockerfiles + run.sh
services/cloudbuild-bench-truthful-completion-{monthly,pr}.yaml  CI cadence
docs/truthful-completion-methodology.md  Published methodology (v1, 2026-05-14)

--- CORE WORKFLOW ---
agentloop/                         Native agentic tool-use loop via Anthropic Messages API
app/                               Orchestrator: config + engines + worktree + verify + OnEvent
hub/                               Typed event hub with subscriber hooks
hub/builtin/                       Built-in hub subscribers (honesty gate, cost tracker)
mission/                           Mission lifecycle runner
workflow/                          Phase machine: plan → execute+verify → review → merge
workflow/skill_scope_closer.go     SkillScopeCloser.OnPhaseExit — fires skilltracker.CloseScope at phase boundaries; one SkillUnloaded(reason="scope_exit") per dropped skill
engine/                            Claude/Codex CLI runners
orchestrate/                       Mission execution pipeline integrator
scheduler/                         GRPW priority + file-scope conflict + resume + WithSpecExec
plan/                              Load/Save/Validate plans
taskstate/                         Anti-deception task state

--- PLANNING & DECOMPOSITION ---
interview/                         Socratic clarification phase
intent/                            Intent classification + verbalization gate
conversation/                      Multi-turn conversation state management
skillselect/                       Tech-stack auto-detection + skill mapping

--- CODE ANALYSIS ---
goast/                             Go AST analysis + extraction
repomap/                           Repository map with PageRank
symindex/                          Symbol indexing
depgraph/                          Import/dependency graph
chunker/                           Semantic code chunking
tfidf/                             TF-IDF semantic search
vecindex/                          Vector/embedding-based search
semdiff/                           Semantic diff with structural changes
diffcomp/                          Diff compression
gitblame/                          Git blame integration

--- FILE & WORKSPACE ---
atomicfs/                          Multi-file atomic edits
fileutil/                          Shared filesystem operations
filewatcher/                       File-system monitoring
worktree/                          Git worktree create/merge/cleanup
branch/                            Conversation branching
hashline/                          Hash-anchored line verification

--- TESTING & VERIFICATION ---
baseline/                          Build/test/lint state capture
verify/                            Build/test/lint pipeline + CheckProtectedFiles + CheckScope
convergence/                       Adversarial self-audit
testgen/                           Test scaffold generation
testselect/                        Dependency-aware test selection
critic/                            Adversarial pre-commit critic
scan/                              18 deterministic security rules

--- ERROR HANDLING & RECOVERY ---
failure/                           10 failure classes + fingerprint dedup + ShouldRetry
errtaxonomy/                       Structured error taxonomy
checkpoint/                        Synchronous checkpointing

## Anti-Truncation Plane

The anti-truncation plane addresses a documented LLM behaviour: under
long-running multi-task work the model self-reduces scope to fit
imagined token / time / Anthropic load-balance budgets. The plane is
seven layers, each independently effective:

- regex catalog — `internal/antitrunc/phrases.go`
- scope-completion gate — `internal/antitrunc/gate.go`
- cortex Lobe (Detector) — `internal/cortex/lobes/antitrunc/`
- supervisor rules — `internal/supervisor/rules/antitrunc/`
- agentloop wiring — `internal/agentloop/antitrunc.go`
- post-commit git hook — `scripts/git-hooks/post-commit-antitrunc.sh`
- CLI + MCP tool — `cmd/r1/antitrunc_cmd.go`,
  `internal/mcp/r1_server.go`

The gate composes BEFORE any other end-turn hook, so a model that
says "skip the gate this once" is ignored at the host process layer.
Operator override (`--no-antitrunc-enforce`) is real but has no
LLM-visible toggle. Full details: [`ANTI-TRUNCATION.md`](ANTI-TRUNCATION.md).

## Runtime Extension Plane
--- CODE GENERATION ---
patchapply/                        Unified-diff parsing/application
extract/                           Structured content parsing
autofix/                           Auto-lint-and-fix loop
conflictres/                       Smart merge-conflict resolution
tools/                             Cascading str_replace algorithm

--- AGENT BEHAVIOR ---
boulder/                           Idle detection + continuation enforcement
specexec/                          Speculative parallel execution (4 strategies)
handoff/                           Agent-to-agent context transfer

--- KNOWLEDGE & LEARNING ---
memory/                            Persistent cross-session knowledge
wisdom/                            Cross-task learnings + FindByPattern
research/                          Persistent indexed research with FTS5
flowtrack/                         Flow-aware intent tracking
replay/                            Session recording for post-mortem

--- LLM INTEGRATION ---
apiclient/                         Multi-provider SSE streaming
provider/                          Direct AI model API clients
mcp/                               Model Context Protocol — 38-tool catalog
model/                             9 task types, 5-provider fallback
prompt/                            Prompt engineering utilities
prompts/                           BuildPlanPrompt, BuildExecutePrompt, BuildReviewPrompt
promptcache/                       Cache-aligned prompt construction
microcompact/                      Cache-aligned context compaction
ctxpack/                           Adaptive context bin-packing
tokenest/                          Token count estimation
costtrack/                         Real-time cost tracking + budget alerts

--- PERMISSIONS & SECURITY ---
consent/                           Human-in-the-loop approval
rbac/                              Role-Based Access Control
hooks/                             Anti-deception PreToolUse/PostToolUse guards
throttle/                          C3 per-tool, two-tier (session+tenant) token-bucket rate limiter
throttle/policy/                   Leaf YAML schema + validator (avoids config<->throttle cycle)

--- CONFIG & SESSION ---
config/                            YAML policy parser
session/                           SessionStore interface (JSON + SQLite WAL)
subscriptions/                     Pool Acquire/Release + circuit breaker + usage poller
pools/                             Worker pool management
context/                           Three-tier context budget + progressive compaction

--- INFRASTRUCTURE ---
agentmsg/                          Inter-agent communication protocol
dispatch/                          Three-tier message dispatch queue
logging/                           Structured leveled logging
metrics/                           Thread-safe counters
telemetry/                         Structured metrics collection
notify/                            Event notification
stream/                            NDJSON parser (6 event types)
jsonutil/                          JSON parsing from mixed-format LLM outputs
schemaval/                         Structured-output validation
validation/                        Input validation at API boundaries

--- UI & INTERFACES ---
tui/                               Headless runner + Bubble Tea TUI
tui/lanes/                         Lane panel: Model, Transport, runProducer (250 ms coalesce)
tui/teatest_shim.go                MCP-driveable TUI shim
viewport/                          Constrained file viewport
repl/                              Interactive REPL
server/                            Mission API HTTP endpoints
remote/                            Build session progress reporting
report/                            BuildReport + per-task TaskReport
progress/                          Plan-aware progress estimation
audit/                             17 review personas

--- LIFECYCLE ---
skill/                             Reusable workflow patterns
plugins/                           Plugin manifest + loading
preflight/                         Pre-flight workspace assertions

--- HOSTED SAAS (services/) ---
services/r1-coord-api/             Go service: /healthz, /v1/version, /v1/license/verify, /v1/telemetry/opt-in. Cloud Run.
services/r1-docs/                  Go service: embeds docs/*.md, renders to HTML. Cloud Run.
services/r1-downloads-cdn/         Go service: streams gs://relayone-488319-r1-releases/{env}/<asset>. Cloud Run.
services/cloudbuild-deploy.yaml    Auto-deploy pipeline (4 images, 4 deploys, smoke-check /livez).
services/deploy.sh                 ./services/deploy.sh {dev|staging|prod|all} — manual deploy.
services/scripts/setup-cloudbuild-triggers.sh   Operator script: 3 Cloud Build triggers (per env).

--- WEB UI (web/) — spec 6 ---
web/src/components/                React component tree: layout, session, chat, lanes, settings, workdir
web/src/lib/api/                   r1d.ts public surface (HTTP + WS), zod schemas, ResilientSocket, AuthClient
web/src/lib/store/                 zustand factory: one store per daemon connection
web/src/hooks/                     useDaemonSocket, useChat, useLanes, useSession, useWorkdir, useKeybindings
web/src/lib/render/markdown.tsx    Streamdown wrapper with shared Shiki + KaTeX config
web/src/test/                      vitest setup, coverage manifest, stories manifest, e2e (Playwright)
web/eslint-rules/                  Custom rule: require-data-testid on every interactive JSX element
web/scripts/verify-build-output.mjs   Verifies dist/ at internal/server/static/dist/

--- DESKTOP (desktop/) — spec 7 ---
desktop/src-tauri/                 Rust host: discovery, transport, lanes, popout, menu, ipc, sidecar
desktop/src/                       React webview wrapping the same web/ components
packages/web-components/           npm workspace: shared React components (LaneCard, LaneSidebar, LaneDetail, PoppedLaneApp)

--- AGENTIC HARNESS (spec 8) ---
internal/mcp/r1_server.go          Consolidated 38-tool catalog
internal/mcp/r1_server_catalog.go  Per-category tool definitions
internal/mcp/lanes_server.go       5 r1.lanes.* tools
internal/mcp/cortex_server.go      r1.cortex.* tools
internal/tui/teatest_shim.go       MCP-driveable TUI shim
tools/agent-feature-runner/        Gherkin-flavored markdown parser + dispatcher
tools/lint-view-without-api/       UI-without-API CI lint
tests/agent/                       8 seed feature fixtures across 10 categories
docs/AGENTIC-API.md                External-agent contract

--- CROSS-MACHINE SESSION MIGRATION (spec C1) ---
internal/migration/bundle.go              .r1session format (manifest + Ed25519 sign/verify)
internal/migration/importer.go            Destination-side Import pipeline + chain-root verify
internal/migration/idempotency.go         migration_imports SQLite table + memory store
internal/migration/replay.go              Bus WAL replay with incremental partial-root checks
internal/migration/events.go              session.migrate.{exported,imported,divergent} emitter
internal/migration/source.go              LedgerBundleSource — production BundleSource
internal/server/sessionhub/migrate.go     BeginMigrateOut/EndMigrateOut + IsMigrating latch
cmd/r1-server/migrate_out.go              POST /api/session/{id}/migrate-out streaming handler
cmd/r1-server/migrate_in.go               POST /api/session/migrate-in ingestion handler
cmd/r1/session_export_cmd.go              r1 session export <id> [-o file] [--force] [--park]
cmd/r1/session_import_cmd.go              r1 session import <file>
cmd/r1/session_migrate_cmd.go             r1 session migrate <id> --to <dest-url>
docs/operations/session-migration.md      Operator runbook (export / transfer / import / verify)
```

## System components

### r1d daemon process (spec 5)
- Single per-user singleton (Watchman pattern). One process holds N concurrent sessions as goroutines.
- Bound to working directories via `cmd.Dir` per session — never `os.Chdir`. CI gate `make lint-chdir` prevents regressions.
- IPC: unix socket (`$XDG_RUNTIME_DIR/r1/r1.sock`) / Windows named pipe with current-SID + LocalSystem SDDL.
- HTTP+WS: loopback `127.0.0.1:0` (port captured at start).
- Auth: subprotocol-token on WS upgrade; 256-bit Bearer on HTTP; peer-cred check on unix socket.
- Discovery: atomic write `~/.r1/daemon.json` mode 0600 with 32-byte hex token rotated on every start.

### Cortex Workspace (specs 1, 2)
- 6 v1 Lobes share full context (read-only message history, the same model breakpoint, the same tool ordering — pre-warmed via `max_tokens=1` cache request every 4 minutes).
- Workspace persists Notes through write-through to durable bus + Replay on session resume.
- Default 5 concurrent LLM Lobes, hard cap 8. Per-turn budget caps Lobe output at 30% of main-thread tokens.
- Drop-partial interrupt protocol: cancel turn context, drain SSE, never persist partial assistant message, append synthetic user message describing the interrupt.

### Lanes wire format (spec 3)
- Six event types streamed over JSON-RPC 2.0; monotonic per-session `seq` allocated by single-writer goroutine; `seq=0` reserved for `session.bound`.
- Replay: `Last-Event-ID` (SSE) or `since_seq` (JSON-RPC).
- WebSocket subprotocol `r1.lanes.v1` + `<token>` for auth. Origin pinning + Host pinning.
- Per-lane `kind` enum: `main | lobe | tool | mission_task | router`.
- Six-state FSM with critical transitions emitted top-level: `lane.killed` is always critical; `lane.note` is critical when `note_severity="critical"`; `lane.status` is critical when `status="errored"`.

### Anti-truncation enforcement (spec 9)
- Seven independently-effective layers; each can refuse `end_turn` on its own. Operator-only override (`--no-antitrunc-enforce`).
- Layer order: phrase regex → scope completion → cortex Lobe Note → supervisor rules → agentloop gate → post-commit hook → CLI/MCP verifier.
- Soak-tested 1M iterations; 0 FP, 0 FN, 499K TP at 16,891 iter/sec.

### Skill lifecycle + compaction (final-sweep PR #168)

The skill lifecycle is the chain `skill_loaded → (use) → skill_unloaded`, where "unloaded" can fire via three distinct paths:

1. **Explicit drop** — `skilltracker.Tracker.Drop(ctx, stanceID, skillRef, reason)` — the model itself unloads a skill it no longer needs. Reason is free-form.
2. **Phase scope exit** — `workflow.SkillScopeCloser.OnPhaseExit(ctx, stanceID, taskScope)` calls `Tracker.CloseScope`, which iterates every skill loaded into that scope and emits `SkillUnloaded` with `Reason="scope_exit"` per drop. Triggered by `workflow.PhaseRunner` on completion *or* abort.
3. **Compactor eviction** — `concern.SkillCompactor.EvictForBudget(ctx, stanceID, currentTokens)` — when context-budget pressure rises, the compactor's `EvictionPolicy` (default `LRUPolicy`) picks oldest-loaded skills until the freed token total covers the overrun, then calls `Tracker.EvictByCompactor` which emits `SkillUnloaded` with `Reason="compactor"` per drop.

All three paths land on `EmitSkillUnloaded` — the same builtin hub event — so the ledger gets an unambiguous chain of `(load_at, unload_at, reason)` triples per `(stanceID, skillRef)` pair. The 3D ledger viewer desaturates the chain segment when `unload_reason="compactor"` (skill came back later) and renders it gone-for-good when `unload_reason="scope_exit"` (phase boundary closed it).

The compactor is one-way: it doesn't mutate prompt content directly. A future caller (a skill-aware section shrinker or an explicit budget guard) calls `EvictForBudget` when it needs to free tokens; the prompt rebuild happens on the next round, *after* the load table is current.

### Signed redaction events (final-sweep PR #169)

Every redaction logged to the ledger is signed with an ed25519 keypair persisted under `<store-root>/redactions/`:

- **`sign-priv.pem`** mode 0600 — PEM-encoded ed25519 private key, generated on first call to `LoadOrGenerateSigningKey`.
- **`sign-pub.pem`** mode 0644 — PEM-encoded public; auto-restored from the private if missing.
- **Signer fingerprint** — first 6 bytes of `sha256(pub)` rendered as 12-char hex; stamped into `SignedRedactionEvent.Signer`. Multiple keys can co-exist in the same audit trail across rotations.

`SignRecord(rec, priv)` canonicalizes over `{node_id, redacted_at, reason, signer}` (excludes the signature itself but includes the signer so swapping the signer fails verification) and stamps `rec.SignatureHex`. `VerifyRecord(rec, pub)` returns `nil` on match, `ErrUnsigned` for legacy entries pre-this-spec, `ErrSignatureMismatch` for tampered entries. The dashboard side panel uses the distinction to render a gray "legacy unsigned" tooltip vs a red "signature mismatch" warning.

`Store.RedactionsForVerified(nodeID)` returns `[]VerifiedRedactionEvent` with a `Verified` bool + a `VerifyErr` string. The signing key is loaded once per process root via `sync.Once` cached in a per-store-root `sync.Map`.

### Tracebundle v2 export (final-sweep PR #171)

The `GET /api/session/{id}/export.tracebundle` endpoint produces a portable per-session audit artifact. V2 introduces three pieces:

1. **Per-session filtering**:
   - `Store.ListNodesForSession(sid)` filters by `Node.MissionID`. Empty `sid` falls back to the unfiltered `ListNodes` for backward compat.
   - `Store.ListEdgesForSession(sid)` filters by `Edge.Metadata["session_id"]`. Edges without the key are conservatively included.
2. **Chain-root hash** — `Store.ChainRootHashForSession(sid)` computes `prev_hash = sha256(prev_hash || node_id || content_commitment)` over nodes sorted by `(CreatedAt, ID)`. Final hex is the root. Empty session → "" + nil. Single node → hash of that node's metadata.
3. **Canonical manifest signing body** — `ledger.CanonicalManifestSignBody(format, version, sessionID, chainRootHash, generatedAt, signer)` returns the deterministic byte-body the manifest signs over, sharing the same canonical layout cmd/r1-server's sign + verify and out-of-tree verifiers use.

`cmd/r1-server/tracebundle_source.go` is the production `TracebundleSource`. The handler `serveTracebundleAdapter` resolves the session's `LedgerDir` from the DB row, opens the store, and delegates to `serveTracebundle(src)`. The `Chain()` projection emits `TracebundleNode` entries with the chain-tier metadata pre-projected into the `Header` field so consumers don't need to re-derive it. (Spec D — D-UI2-7 — removed the prior `R1_SERVER_UI_V2` envelope gate; the route is always reachable.)

### Cross-machine session migration (spec C1, 2026-05-12)

The `.r1session` bundle is the live-half complement to the `.tracebundle`: it carries every byte required to resume a session on a different daemon, not just the read-only forensic export. Architecture:

1. **`internal/migration` package** — bundle format + sign/verify + import pipeline. Stdlib only (`archive/tar`, `compress/gzip`, `encoding/json`, `crypto/ed25519`, `crypto/sha256`). The `BundleSource` interface keeps the package decoupled from `internal/memory`, `internal/skill`, `internal/cortex`, and the daemon's session registry. Concrete `LedgerBundleSource` adapts a `ledger.Store` + a handful of callback fields into the interface for the production export path.

2. **Manifest layout** — `manifest.json` is emitted LAST so every count + sha256 (chain-root hash, WAL sha256, memory sha256) is known before signing. The canonical body is `ledger.CanonicalManifestSignBody("r1session", 1, sessionID, chainRootHash, exportedAt, signer)` — the same canonical form the tracebundle path uses, so downstream verifiers wired for tracebundle signatures verify migration manifests with zero new code.

3. **Bundle archive paths (deterministic order)**: `ledger/chain.ndjson` → `ledger/edges.ndjson` → `ledger/content/<id>.json` blobs → `ledger/content/redacted.json` → `bus/wal.ndjson` → `bus/wal.index.json` (with partial-chain-root checkpoints every 100 events) → `memory/rows.ndjson` → `memory/index.json` → `skills/pack-refs.json` (refs only; bodies are pre-staged on the destination via `r1 skills pack install`) → `lobe-state/<id>.json` → `lanes/snapshot.json` → `checkpoint/pre-export.json` → `manifest.json` → `signature.ed25519`. Tar entries use zero-mtime headers so two exports of the same input are byte-identical.

4. **Import pipeline** (`migration.Importer.Import`): verify signature → tenant claim → idempotency (SQLite `migration_imports` table, manifest-sha256-keyed) → schema-version → skill-pack pre-flight (PackChecker interface; missing packs → 422 with the list) → allocate destination session (state=`migrating-in`) → hydrate ledger (nodes + edges + content; MissionID re-mapped from source-id to dest-id) → hydrate memory (encryption envelopes preserved byte-for-byte) → WAL replay with **incremental partial-chain-root checks** every 100 events → restore lobes + lanes → **final chain-root verify** → record idempotency row → flip state to `idle` → emit `session.migrate.imported` bus event. Any failure between allocation and the final verify flips the destination session to `migrated-failed` for operator inspection; the idempotency table is NOT updated so a retry succeeds.

5. **Active-session safety** — `SessionHub.BeginMigrateOut(id, force)` (`internal/server/sessionhub/migrate.go`) refuses to latch a `running` / `tool-in-flight` session unless `force=true`. The agent loop's mid-turn observer consults `SessionHub.IsMigrating(id)` and yields after the current `assistant`/`tool_result` pair completes (RT-CANCEL-INTERRUPT-safe; no orphaned `tool_use`). Successful export pairs with `EndMigrateOut(id, parked)` to either flip back to live (`parked=false`) or park the source read-only (`parked=true`).

6. **Three new bus events** — `session.migrate.exported`, `session.migrate.imported`, `session.migrate.divergent` (declared in `internal/bus/bus.go`; emitted via `migration.BusEventEmitter`). The divergent event carries `{expected, actual, divergent_at_seq}` for audit replay.

7. **HTTP surface** — `POST /api/session/{id}/migrate-out` (`cmd/r1-server/migrate_out.go`) streams a gzip-tar response; `POST /api/session/migrate-in` (`cmd/r1-server/migrate_in.go`) ingests one. Both run inside the daemon's bearer-protected mux. Cross-tenant migration is rejected with 403 (v1 requires both daemons to hold the same master key, per encryption-at-rest spec).

8. **CLI surface** — `r1 session export <id> [-o file] [--force] [--park]`, `r1 session import <file>`, `r1 session migrate <id> --to <dest-url>`. Singular form (`session`) distinguishes the migration verbs from the existing read-only `r1 sessions` checkpoint browser.

9. **Integration test** — `internal/migration/integration_roundtrip_test.go` builds with `-tags integration_session_migrate`. Seeds 1000 turns into a source `ledger.Store`, exports, imports into a fresh destination store, and asserts (a) destination chain root byte-equals source's, (b) bundle ≤100MB, (c) wall-clock <60s.

References: spec [`specs/cross-machine-session-migration.md`](../specs/cross-machine-session-migration.md), runbook [`docs/operations/session-migration.md`](operations/session-migration.md).

### Release-rehearsal CI lane (final-sweep PR #170)

Cloud Build trigger pair (`services/cloudbuild-e2e-trigger.yaml`) firing `services/cloudbuild-e2e.yaml`:

| Trigger | Fires on | Purpose |
|---|---|---|
| `r1-agent-e2e-rehearsal-main` | push to `^main$` | Post-deploy verification — confirms the just-shipped main is e2e-clean. |
| `r1-agent-e2e-rehearsal-tag` | push to `^v.*$` tag | Release gate — red blocks tag promotion to staging / main / production rollouts. |

Pipeline: `go build -mod=vendor` → `npm install + npx playwright install --with-deps chromium` → `go test -tags=e2e ./cmd/r1-server/e2e/...` with `R1_SERVER_SHARE_ENABLED=1` (Spec D — D-UI2-7 — removed the prior paired `R1_SERVER_UI_V2=1`) → publish green/red commit-status to GitHub.

Manual escape hatch: `.github/workflows/e2e-rehearsal-manual.yml` lets an operator dispatch from the Actions UI without local `gcloud`. The runner authenticates to GCP via `secrets.GCP_SA_JSON` and calls `gcloud builds triggers run r1-agent-e2e-rehearsal-main --branch=$BRANCH`. Workflow summary links to the Cloud Build console for live logs.

One-time setup: `scripts/setup-cloudbuild-e2e-trigger.sh` is idempotent (re-running updates triggers in place). Requires `roles/cloudbuild.builds.editor` on `relayone-488319`. Both triggers run under the BYOSA service account `cloud-build-byosa@relayone-488319.iam.gserviceaccount.com`.

### Hosted SaaS — 12 public Cloud Run services
- 4 services × 3 envs (dev / staging / prod) on `relayone-488319` GCP project, region `us-central1`: `r1-coord-api`, `r1-docs`, `r1-downloads-cdn`, `r1-admin`.
- All services: distroless static images, min-instances=1, instance billing (no CPU throttling), `--allow-unauthenticated`, port 8080.
- Cloud Run org policy intercepts `/healthz`; r1 services use `/livez` + `/readyz` + `/v1/version` + `/`.
- Auto-deploy on push to `main` / `staging` / `dev` via `services/cloudbuild-deploy.yaml`.

## Data models

### `ledger.Node` (content-addressed)
```go
type Node struct {
  ID        string         // sha256:<hex> content ID
  Type      string         // 22 node types (mission, task, plan, exec, verify, …)
  Payload   json.RawMessage
  Refs      []string       // outgoing edges (other node IDs)
  CreatedAt time.Time
}
```

### `streamjson.LaneEvent`
```go
type LaneEvent struct {
  Type      string          // lane.created | lane.status | lane.delta | lane.cost | lane.note | lane.killed
  SessionID string
  LaneID    string
  Seq       uint64
  Payload   json.RawMessage // shape per Type
  Critical  bool
}
```

### `cortex.Note`
```go
type Note struct {
  ID         string
  Severity   Severity   // info | advice | warning | critical
  Content    string
  PublishedBy string    // lobe id
  TTL        time.Duration
  CausedBy   string    // event/round id
}
```

### `antitrunc.Finding`
```go
type Finding struct {
  Source   string   // phrase | scope | commit
  Phrase   string
  Position int
  Reason   string
  Action   string   // "refuse" | "advise"
}
```

### `LaneSnapshot` (web/desktop)
```ts
{ id, sessionId, label, state: LaneState,
  createdAt, updatedAt, progress: number|null,
  lastRender: string|null, lastSeq: number }
```

## API surface

### MCP catalog — 38 tools across 10 categories
- **sessions** (5): `r1.session.list`, `.get`, `.create`, `.pause`, `.cancel`
- **lanes** (5): `r1.lanes.list`, `.subscribe`, `.get`, `.kill`, `.pin`
- **cortex** (4): `r1.cortex.notes`, `.workspace`, `.lobes`, `.round`
- **mission** (4): `r1.mission.start`, `.status`, `.abort`, `.report`
- **worktree** (3): `r1.worktree.create`, `.merge`, `.cleanup`
- **bus** (1): `r1.bus.tail`
- **verify** (4): `r1.verify.build`, `.test`, `.vet`, `.lint`
- **TUI** (5): `r1.tui.snapshot`, `.dispatch`, `.assertA11y`, `.cycle`, `.quit`
- **anti-truncation** (1): `r1.antitrunc.verify`
- **plus** legacy `stoke_*` aliases (deprecated; removal scheduled v2.0.0).

### r1d JSON-RPC (loopback HTTP+WS)
- `session.start | pause | resume | cancel | send | subscribe | unsubscribe`
- `lanes.list | kill`
- `cortex.notes`
- `daemon.info | shutdown | reload_config`

### `cmd/r1-server` HTTP (per-session export)
- `GET /api/session/{id}/export.tracebundle` — returns the per-session tracebundle (chain nodes + edges + content + canonical-signed manifest with `chain_root_hash`). Always reachable post-Spec-D — the prior `R1_SERVER_UI_V2` envelope gate was removed (D-UI2-7). Backed by `ledgerTracebundleSource` over `ledger.Store`.

### Hosted SaaS HTTP

**`api.r1.run` (r1-coord-api)**
- `GET /` — service metadata
- `GET /livez` `GET /readyz` `GET /v1/version` — health + version
- `POST /v1/license/verify` — `{key} → {valid}`
- `POST /v1/telemetry/opt-in` — accept opt-in record

**`platform.r1.run` (r1-docs)**
- `GET /` — docs index
- `GET /<doc>.html` — rendered markdown
- `GET /raw/<doc>.md` — raw source

**`downloads.r1.run` (r1-downloads-cdn)**
- `GET /` — channel + asset index (JSON)
- `GET /<channel>/<asset>` — stream binary
- `GET /<channel>/<asset>/sha256` — content metadata

## Execution flow — single mission end-to-end

1. **Entry**: `r1 run --task "..."` or `r1 build --plan plan.json` (or via web/desktop UI through `r1 serve`).
2. **Plan**: `internal/plan/` validates the plan (cycle DFS, deps); `internal/scheduler/` orders tasks via GRPW priority.
3. **Cortex pre-warm**: `internal/cortex/prewarm.go` fires a `max_tokens=1` warming request 4 min before the round.
4. **Round dispatch**: `internal/cortex/Workspace.Run()` starts main thread + N Lobes in parallel.
5. **Execute**: main thread runs `internal/agentloop.Loop` against Anthropic Messages API; tool calls dispatched through `internal/hooks/` PreToolUse + PostToolUse guards.
6. **Verify**: `internal/verify/` runs build + test + vet; `internal/critic/` runs adversarial pre-commit critic; `internal/convergence/` runs adversarial self-audit.
7. **Anti-trunc gate**: `internal/agentloop/antitrunc.go` composes the gate BEFORE every other end-turn hook. If the model emits a truncation phrase or unchecked plan items remain, the gate refuses `end_turn` and forces continuation.
8. **Review**: `model.CrossModelReviewer()` runs Codex (or whichever non-implementer model is in the resolver chain).
9. **Persist**: every event hits `internal/bus/` WAL; every node lands in `internal/ledger/`; every cost tick is journaled.
10. **Surface**: lanes stream over WS to TUI/web/desktop/MCP subscribers via per-session `seq`.

## Infrastructure

### GCP project: `relayone-488319`
- **Cloud Run** public services (us-central1): `r1-coord-api-{prod,staging,dev}`, `r1-docs-{prod,staging,dev}`, `r1-downloads-cdn-{prod,staging,dev}`, `r1-admin-{prod,staging,dev}`. Min-instances=1, instance billing, distroless static.
- **Cloud SQL Postgres 16**: `r1-prod-pg` (db-g1-small, $10/mo), `r1-staging-pg` + `r1-dev-pg` (db-f1-micro, $7/mo each). All us-central1-c, ENTERPRISE edition.
- **Artifact Registry**: `us-central1-docker.pkg.dev/relayone-488319/r1` (4 public-service images: r1-coord-api, r1-docs, r1-downloads-cdn, r1-admin).
- **Secret Manager**: core visible env set during the 2026-05-15 audit was `r1-{prod,staging,dev}-shared-{DATABASE_URL,ANTHROPIC_API_KEY,AUTH_JWT_SECRET}`. Treat broader GTM/observability secrets as operator-verified state, not assumed from docs.
- **GCS**: `gs://relayone-488319-r1-releases/{prod,staging,dev}/` (binary release channels; r1-downloads-cdn streams from here).
- **Cloud Build**: `r1-agent-pr` (PR gate) + `r1-agent-ci` (push to main). After PR #128 merges, `services/scripts/setup-cloudbuild-triggers.sh` adds 3 deploy triggers.
- **Domain mappings** (live): 12 subdomains under `r1.run` — `platform|api|downloads|admin` across `prod|staging|dev`. Each maps to its Cloud Run service via CNAME → `ghs.googlehosted.com.`.

### DNS — Cloudflare zone for `r1.run`
- 9 CNAME records (operator action; see `plans/HANDOFF-deploy-state.md`).
- Proxy mode MUST be **off** (gray cloud). Cloud Run provisions its own Google-managed TLS cert.

## Testing architecture

- **Go**: `go test ./...` runs all 175 packages; race-clean across the suite.
- **Web** (vitest + jsdom + MSW): unit tests as sibling `<Component>.test.tsx`. Coverage threshold 80% statements / 70% branches enforced via `vitest.config.ts`. Coverage manifest test (`web/src/test/coverage-manifest.test.ts`) walks the source tree and fails if any component lacks a sibling `.test.tsx`.
- **Web e2e** (Playwright + axe-core): `web/src/test/e2e/csp-axe.spec.ts` enforces zero CSP errors + zero serious/critical axe findings on every route across chromium + firefox + webkit. 9 `*.agent.feature.md` Gherkin-flavored flows for the spec 8 MCP harness.
- **Storybook MCP**: every web component has a sibling `.stories.tsx` (CSF 3). Stories manifest test enforces this.
- **Desktop** (cargo test + vitest): 110 Rust tests + 19 TS tests + 4 Playwright e2e (multi-session, lanes-streaming, popout-lane, daemon-discovery).
- **Anti-truncation soak**: build-tagged `soak` test runs 1M iterations against a corpus of 40 legitimate phrasings; 0 FP / 0 FN / 499K TP at 16,891 iter/sec.

## CI gates

| Gate | Command | When | Status |
|---|---|---|---|
| Build | `go build ./...` | every PR | required |
| Vet | `go vet ./...` | every PR | required |
| Test | `go test ./... -count=1 -timeout=300s` | every PR | required |
| Race | `go test -race ./... -count=1` | every PR | required (advisory on flake) |
| Lint-chdir | `make lint-chdir` | every PR | required |
| Lint-views | `make lint-views` | every PR | required (after spec 8 merge) |
| Web build | `cd web && npm run build` | every PR | required |
| Web tests | `cd web && npm run test` | every PR | required |
| Anti-trunc verify | `r1 antitrunc verify -n 20` | every PR + post-commit hook | required |
| Release-rehearsal E2E (`r1-agent-e2e-rehearsal-main`) | `services/cloudbuild-e2e.yaml` (Cloud Build) | every push to `main` | required for any release gating on it; manual escape via `.github/workflows/e2e-rehearsal-manual.yml` |
| Release-rehearsal E2E (`r1-agent-e2e-rehearsal-tag`) | `services/cloudbuild-e2e.yaml` (Cloud Build) | every `^v.*$` tag push | required — red blocks tag promotion |

## Status

### Done
- Specs 1-9 merged + tested + deployed.
- 12 public Cloud Run SaaS services live + Cloud SQL + Secret Manager + Artifact Registry.
- Branch hygiene: 20 archive tags, 3 active branches (main, claude/w521-…, archives).
- All Go tests + web typecheck + desktop tests green.
- Documentation: this doc + 6 sibling docs + 9 spec docs + decisions log + HANDOFF state file.
- **Final-sweep PRs #168 / #169 / #170 / #171** (sync to `main` in commit `242af4a8`):
  - Skill lifecycle hooks: `concern.SkillCompactor` (LRU eviction under budget) + `workflow.SkillScopeCloser` (phase-exit drop) wired through `internal/skilltracker.Tracker`.
  - ed25519-signed redaction events: `internal/ledger/redact_sign.go` + `Store.RedactionsForVerified`.
  - Tracebundle v2: per-session ledger filtering + chain-root hashing + canonical manifest body; production source at `cmd/r1-server/tracebundle_source.go` v2-flag-gated.
  - Release-rehearsal CI: Cloud Build trigger pair (push-to-main + tag) + manual GitHub Actions workflow.

### In Progress
- Operator follow-ups: secret values, CodeRadar analytics-token wiring if GTM moves there, and Cloud Build trigger creation beyond the base PR/CI pair.

### Partial (post-2026-05-11)
- PostHog product analytics (B1, BUILD_ORDER 38) — 24-event taxonomy, bus subscriber, funnel + cohort + dashboard JSON are checked in, and `cmd/r1` now registers the subscriber in the real binary. Hosted public-service deployment remains partial; do not treat this as proven end-to-end GTM wiring. See `internal/analytics/`, `internal/hub/builtin/analytics_subscriber.go`, `docs/integrations/posthog.md`.

### Scoped
- JWT login + RelayOne MSP SSO (Path A — Go reimpl of `@relayone/auth-core`).
- Desktop runtime completion.
- Lifecycle / GTM completion beyond the shipped client/subscriber code.

### Partial
- CodeRadar dogfood event integration (B3, 2026-05-12). Canonical event
  schemas and the bus subscriber exist, and `cmd/r1` now registers the
  subscriber in the main binary. Hosted product-analytics adoption still
  needs project-token wiring; the current public deploy proves
  observability paths more strongly than GTM replacement.

### Scoping
- Encryption-at-rest for journals.

### Potential — On Horizon
- Marketing site with affiliate / SEO / CRO / attribution / retention stack.

## Planned components (scoped, not yet built)

Fourteen specs scoped on 2026-05-11 introduce a new set of internal packages. Each entry below names the package, its role, and the spec that drives it. None of these are merged yet; this section is the forward-looking map.

- **`internal/promptguard/` (extended)** — gains three new submodules: `toolinput` (per-tool input validation runs at the MCP wire, rejecting payloads that violate the tool's declared schema or carry known-injection markers), `fingerprint` (ed25519 signs every system-prompt block and verifies it at each plan / execute / verify boundary so a tampered system block fails closed), and `budget` (per-session injection-attempt counter with circuit-break semantics). Adversarial reviewer hooks into the existing audit chain and evaluates against the CL4R1T4S corpus on a CI cadence. Driven by `specs/promptguard-hardening.md` (A1).
- **`internal/auth/` (new)** — JWT verification + RelayOne SSO client + middleware. Subdivides into `jwt` (HS256 + RS256 verify, JWKs rotation, claims extraction), `sso` (OIDC + PKCE flow against the RelayOne IdP, per-tenant token isolation, refresh-token handling), and `middleware` (gates admin-panel routes and every future enterprise route; pluggable via `http.Handler` chain). Driven by `specs/relayone-sso.md` (A4) and consumed by `specs/admin-panel.md` (A5) and `specs/oneshot-production-hardening.md` (A3).
- **`internal/analytics/` (B1, done 2026-05-12)** — PostHog client live. The DSN-aware client at `internal/analytics/analytics.go` mirrors the `internal/coderadar/coderadar.go` shape: `FromEnv()` constructor, no-op when `POSTHOG_API_KEY` is empty, `Enabled()` predicate. The canonical 24-event taxonomy and per-event property adapter table sit at `internal/analytics/taxonomy.go`; the bus subscriber at `internal/hub/builtin/analytics_subscriber.go` registers an `ModeObserve` hook on the bus and forwards captures through a bounded 8192-deep channel so the hot path never blocks. Per-tenant Group Analytics rides through the `correlation.IDs.TenantID` field (populated by A4 RelayOne SSO once merged) via the shim at `internal/analytics/tenant_id.go`. Funnels and cohorts are versioned at `docs/analytics/funnels.md` and `docs/analytics/cohorts.md`; the overview dashboard JSON lives at `docs/analytics/dashboards/r1-overview.json`. Driven by `specs/posthog-analytics.md` (B1).
- **`internal/auth/` (live, A4 done 2026-05-12)** — Go port of `@relayone/auth-core`. `jwt.go` implements `JwtService` (HS256 + RS256 sign/verify/refresh via `lestrrat-go/jwx/v2`; 15-min access / 30-day refresh defaults; reserved-claim collision guard; `JwtServiceFromEnv` mirrors the TS env contract). `sso_client.go` implements the OIDC RP (`coreos/go-oidc/v3` for discovery + JWKS, `golang.org/x/oauth2` for authorization-code-with-PKCE-S256; pinned endpoints with lazy discovery fallback; `NormalizeProfile` lifts MSP claims `msp_org_id` + `msp_managed_orgs`). `sso_handlers.go` exposes four HTTP routes (`/auth/sso/start`, `/auth/sso/callback`, `/auth/refresh`, `/auth/logout`) with `__Host-r1_at` / `__Host-r1_rt` / `__Host-r1_sso_state` cookies (Secure, HttpOnly, SameSite=Lax). `state_store.go` is an in-memory one-shot PKCE state store with TTL sweeper + client IP/UA binding. `middleware.go` is the `RequireBearer` HTTP middleware (header or `__Host-r1_at` cookie). `keys.go` cascades SecretManager → env → file sources, generating an RSA-2048 keypair on first use with 0600/0644 mode discipline mirroring `internal/ledger/redact_sign.go`. `keys_tenant.go` derives per-tenant HMAC secrets via HKDF-SHA256 (info `r1-jwt-tenant-v1:<tenantID>`). `wire.go` is the daemon mount helper gated by `R1_AUTH_MODE=anonymous|sso|both`. Driven by `specs/relayone-sso.md` (A4) and consumed by `specs/admin-panel.md` (A5).
- **`internal/analytics/` (new)** — PostHog client. Holds the canonical event catalog (24 events), the per-tenant Group Analytics wiring, the funnel / cohort definitions as code so they survive a redeploy, and a non-blocking emit path (drop-on-overflow) so analytics emission never blocks a mission round. Driven by `specs/posthog-analytics.md` (B1).
- **`internal/lifecycle/` (new)** — Customer.io client plus the flagstore that records per-user consent. Six lifecycle triggers fire from existing hub events (no new emit points needed in the runtime; lifecycle subscribes). DSAR flow lives in a sub-handler that gates on the consent flagstore. Driven by `specs/customerio-lifecycle.md` (B2).
- **`internal/throttle/` (new)** — token-bucket primitive plus a per-tool policy loader. Two-tier model (session + tenant) is realized as two nested buckets per call; the policy file is YAML, loaded at startup and live-reloadable via `daemon.reload_config`. Bucket state journals through the existing WAL so a restart honors the in-flight throttle window. MCP boundary enforces the call. Driven by `specs/per-tool-throttling.md` (C3).
- **`internal/ideinstall/` (new)** — IDE config writers plus a JetBrains-side plugin shim. Subdivides into `cursor`, `windsurf`, `vscode`, and `jetbrains` writers, each owning the right config file in the right place per IDE. The `r1 ide` command dispatches to the right writer based on the `--ide` flag (auto-detected from `$PATH` when omitted). Driven by `specs/mcp-ide-bundles.md` (C4).
- **`internal/cicd/bitbucket/` (new)** — BitBucket Pipelines adapter parallel to the existing `internal/cicd/github/` and `internal/cicd/gitlab/` adapters. OIDC-based authentication, PR commenting with diff-aware annotations, `r1 run --ci` integration. Driven by `specs/bitbucket-pipelines-adapter.md` (C5).
- **`internal/browser/{browserless,inhouse}/` (new)** — two remote-browser providers behind a common `Provider` interface. `browserless/` wraps the managed Browserless service; `inhouse/` runs a tenant-isolated Cloud Run service with a deny-by-default egress policy and a per-tenant browser pool. The browser tool selects a provider at session start based on tenant config. Driven by `specs/browser-remote-sandbox.md` (C6).
- **`internal/skill/manifest_v2.go` + `internal/skill/compat/{cloudswarm,heroa,veritize}/` (new)** — pack-format v2 with explicit compatibility matrix and a federated trust root so a skill signed by one RelayOne portfolio product is verifiable by another. Per-product runtime adapters bridge the differences between r1's harness and the consuming product's harness. Driven by `specs/cross-product-skill-exchange.md` (C7).
- **`internal/auth/` (new)** — JWT verification + RelayOne SSO client + middleware. Subdivides into `jwt` (HS256 + RS256 verify, JWKs rotation, claims extraction), `sso` (OIDC + PKCE flow against the RelayOne IdP, per-tenant token isolation, refresh-token handling), and `middleware` (gates admin-panel routes and every future enterprise route; pluggable via `http.Handler` chain). Driven by `specs/relayone-sso.md` (A4) and consumed by `specs/admin-panel.md` (A5) and `specs/oneshot-production-hardening.md` (A3).
- **`internal/analytics/` (new)** — PostHog client. Holds the canonical event catalog (24 events), the per-tenant Group Analytics wiring, the funnel / cohort definitions as code so they survive a redeploy, and a non-blocking emit path (drop-on-overflow) so analytics emission never blocks a mission round. Driven by `specs/posthog-analytics.md` (B1).
- **`internal/lifecycle/` (new)** — Customer.io client plus the flagstore that records per-user consent. Six lifecycle triggers fire from existing hub events (no new emit points needed in the runtime; lifecycle subscribes). DSAR flow lives in a sub-handler that gates on the consent flagstore. Driven by `specs/customerio-lifecycle.md` (B2).
- **`internal/throttle/` (new)** — token-bucket primitive plus a per-tool policy loader. Two-tier model (session + tenant) is realized as two nested buckets per call; the policy file is YAML, loaded at startup and live-reloadable via `daemon.reload_config`. Bucket state journals through the existing WAL so a restart honors the in-flight throttle window. MCP boundary enforces the call. Driven by `specs/per-tool-throttling.md` (C3).
- **`internal/ideinstall/` (done — C4)** — IDE config writers plus the bundled-jar copier for JetBrains. Subdivides into `cursor.go`, `windsurf.go`, `vscode.go`, and `jetbrains.go` per-IDE installers atop a shared `config.go` merge primitive (read → backup-to-`.r1-backup` → atomic-write). Detection probes canonical config dirs, not `$PATH`. `cmd/r1/ide_install_cmd.go` exposes `r1 ide install <ide>`, `r1 ide uninstall <ide>`, and `r1 ide verify`. The first-run helper `MaybePromptIDEInstall` lives in the same CLI file and is wired into `r1 chat` startup; ack persists in `~/.r1/ide-prompt-acked`. The JetBrains plugin source lives under `ide/jetbrains/` (Kotlin + Gradle, IntelliJ Platform 2026.1+); CI builds and signs `r1-mcp-bridge.jar`. Spec: `specs/mcp-ide-bundles.md`. Operator quickstart: `docs/integrations/ide-bundles.md`. Signing setup: `docs/integrations/jetbrains-plugin-signing.md`.
- **`internal/ideinstall/` (new)** — IDE config writers plus a JetBrains-side plugin shim. Subdivides into `cursor`, `windsurf`, `vscode`, and `jetbrains` writers, each owning the right config file in the right place per IDE. The `r1 ide` command dispatches to the right writer based on the `--ide` flag (auto-detected from `$PATH` when omitted). Driven by `specs/mcp-ide-bundles.md` (C4).
- **`internal/cicd/bitbucket/` (shipped 2026-05-12)** — BitBucket Pipelines adapter parallel to the existing `internal/cicd/github/` and `internal/cicd/gitlab/` adapters. OIDC-based authentication (auth.go), inline PR commenting with diff-aware annotations (reviewer.go + comment.go), commit-status row writer (`PostCommitStatus`, shared name `"R1 Verify"`), in-step runner (runner.go) and template generator (`cicd_bitbucket.go`). Promoted `Finding`/`ParseFindings`/`LLMFunc`/`RenderCommentBody`/`DefaultReviewPrompt` to `internal/cicd/shared/` so all three adapters share the auto-reviewer primitives. A parity-audit test (`internal/cicd/parity_test.go`) reads `docs/integrations/bitbucket-pipelines-parity.md` and fails the build on any required-capability drift. Operator runbook at `docs/integrations/bitbucket-pipelines.md`. Driven by `specs/bitbucket-pipelines-adapter.md` (C5).
- **`internal/cicd/bitbucket/` (new)** — BitBucket Pipelines adapter parallel to the existing `internal/cicd/github/` and `internal/cicd/gitlab/` adapters. OIDC-based authentication, PR commenting with diff-aware annotations, `r1 run --ci` integration. Driven by `specs/bitbucket-pipelines-adapter.md` (C5).
- **`internal/browser/{browserless,inhouse}/` (Done — C6, 2026-05-12)** — two remote-browser providers behind a common `Provider` interface in `internal/browser/provider.go`. `browserless/` wraps the managed Browserless service via CDP-over-WebSocket with per-tenant incognito contexts + token-resolution chain + 30-second-Unit cost rollup; `inhouse/` is the in-house Provider client (Bearer ID-token auth + cached metadata-server tokens) wired to the `services/r1-browser/` Cloud Run service (`Dockerfile.r1-browser` + `services/cloudbuild-r1-browser.yaml`, operator-driven deploy). `internal/browser/fallback.go` provides a primary→secondary `FallbackProvider` with permanent-vs-transient error classification; `internal/browser/session_bound.go` enforces idle + hard timeouts; `internal/browser/conformance.go` is the byte-identical contract test every provider passes; `internal/browser/lint_cookies_test.go` enforces the no-cookies rule at CI gate. Driven by `specs/browser-remote-sandbox.md` (C6).
- **`internal/browser/{browserless,inhouse}/` (new)** — two remote-browser providers behind a common `Provider` interface. `browserless/` wraps the managed Browserless service; `inhouse/` runs a tenant-isolated Cloud Run service with a deny-by-default egress policy and a per-tenant browser pool. The browser tool selects a provider at session start based on tenant config. Driven by `specs/browser-remote-sandbox.md` (C6).
- **`internal/skill/manifest_v2.go` + `internal/skill/compat/{cloudswarm,heroa,veritize}/` (new)** — pack-format v2 with explicit compatibility matrix and a federated trust root so a skill signed by one RelayOne portfolio product is verifiable by another. Per-product runtime adapters bridge the differences between r1's harness and the consuming product's harness. Driven by `specs/cross-product-skill-exchange.md` (C7).
- **`internal/oneshot/` (extended, **A3 landed 2026-05-12**)** — the existing `oneshot` runtime now ships memory bounds, per-call timeout enforcement, deterministic shutdown ordering, an in-package audit submitter, and the 1000-concurrent integration benchmark. Added files:
  - `events.go` / `exit_codes.go` — exported event-name and exit-code constants pinned to the operator runbook via a doctest so the names never drift from the docs.
  - `memlimit.go` + `memlimit_linux.go` + `memlimit_other.go` — cross-platform helpers that pin `RLIMIT_AS` on Linux (via `prlimit`) and stamp Go's `debug.SetMemoryLimit` at 87% of the hard cap on every platform (13% GC headroom).
  - `audit.go` — `AuditClient` with 64-slot non-blocking queue, 3-retry exponential backoff (200 ms / 800 ms / 3.2 s), HMAC-SHA256 signing, and a `DrainOrDrop(ctx)` exit path so a wedged endpoint never blocks process exit.
  - `concurrency_test.go` — the 1000-concurrent benchmark under build tag `integration`; runs via `make test-oneshot-concurrent` on a 16-core / 32-GiB host.
  - `cmd/mockaudit/main.go` — minimal HMAC-verifying audit sink for the operator runbook.
  CLI plumbing lives in `cmd/r1/oneshot_cmd.go` + `oneshot_memlimit{,_linux,_other}.go`. Driven by `specs/oneshot-production-hardening.md` (A3).
- **`internal/oneshot/` (extended) + `internal/oneshot/audit/` (new)** — the existing `oneshot` runtime gets memory bounds, per-call timeout enforcement, deterministic shutdown ordering, and a new `audit` subpackage that publishes per-call audit events to a remote ledger of record (the operator's chosen sink, not the local SQLite ledger). The 1000-concurrent integration test lives under `internal/oneshot/loadtest_test.go`. Driven by `specs/oneshot-production-hardening.md` (A3).
- **`internal/sessionhub/` (extended) + migration module** — the existing session hub gains a `migration` submodule that owns `.r1session` bundle serialization, chain-root-hash continuity verification, and the import / export / migrate CLI verbs. The bundle format is the same canonical-manifest layout the tracebundle already uses, extended with replay state so a re-imported session resumes mid-conversation. Driven by `specs/cross-machine-session-migration.md` (C1).
- **`internal/admin/` (new)** — server-rendered Go admin panel mounted on the existing `r1-server` process. Five read-only routes (sessions, tenants, billing, audit, anti-trunc events), each backed by a query on the existing data stores; no new persistence layer is introduced. Auth gate is the `internal/auth/middleware` wired with the operator role check. Driven by `specs/admin-panel.md` (A5).
- **`internal/preflight/` (extended) + `internal/recovery/` (new)** — P0 platform hardening adds a `recovery` package that wraps every long-running goroutine with `recover()` + structured-failure emit, a graceful-shutdown coordinator that drains in-flight tool calls on SIGTERM, and per-session resource limits (memory + open-FD + goroutine cap). `preflight` gains host-permission checks that refuse to start the daemon when the runtime dirs are misconfigured. Driven by `specs/p0-hardening-s0-foundation.md` (A2).

When these specs are built, the package map in this document and the one in [CLAUDE.md](../CLAUDE.md) get updated as part of each spec's done-criteria. The forward-looking map above is the contract between the scope and the implementation.
