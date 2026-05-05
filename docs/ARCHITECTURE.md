# Architecture

Trunk architecture view for r1 as of 2026-05-04 — after specs 1-9 merged + 9 Cloud Run SaaS surfaces went live.

## Audience

- Engineers maintaining the runtime, daemon, web, desktop, or agentic harness.
- Reviewers checking whether docs match shipped code.
- Operators standing up the SaaS surfaces or onboarding to ops scripts.
- Stakeholders who need the system shape without reading every package.

## Tech stack

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
cmd/r1-server/                     Mission API HTTP server (legacy; superseded by r1 serve).
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
bus/                               Durable WAL-backed event bus (hooks, delayed events, causality)
supervisor/                        Deterministic rules engine (30 rules, 10 categories, 3 manifests)
supervisor/rules/antitrunc/        Anti-truncation supervisor rules (3 rules)
concern/                           Per-stance context projection (10 sections, 9 role templates)
harness/                           Stance lifecycle: spawn/pause/resume/terminate (11 templates)
snapshot/                          Protected baseline manifest
wizard/                            First-time config presets
skillmfr/                          Skill manufacturing pipeline
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

--- CORE WORKFLOW ---
agentloop/                         Native agentic tool-use loop via Anthropic Messages API
app/                               Orchestrator: config + engines + worktree + verify + OnEvent
hub/                               Typed event hub with subscriber hooks
hub/builtin/                       Built-in hub subscribers (honesty gate, cost tracker)
mission/                           Mission lifecycle runner
workflow/                          Phase machine: plan → execute+verify → review → merge
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
services/cloudbuild-deploy.yaml    Auto-deploy pipeline (3 images, 3 deploys, smoke-check /livez).
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

### Hosted SaaS — 9 Cloud Run services
- 3 services × 3 envs (dev / staging / prod) on `relayone-488319` GCP project, region `us-central1`.
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
- **Cloud Run** services (us-central1): `r1-coord-api-{prod,staging,dev}`, `r1-docs-{prod,staging,dev}`, `r1-downloads-cdn-{prod,staging,dev}`. Min-instances=1, instance billing, distroless static.
- **Cloud SQL Postgres 16**: `r1-prod-pg` (db-g1-small, $10/mo), `r1-staging-pg` + `r1-dev-pg` (db-f1-micro, $7/mo each). All us-central1-c, ENTERPRISE edition.
- **Artifact Registry**: `us-central1-docker.pkg.dev/relayone-488319/r1` (3 images: r1-coord-api, r1-docs, r1-downloads-cdn).
- **Secret Manager**: 6 placeholders: `r1-{prod,staging,dev}-shared-{DATABASE_URL,ANTHROPIC_API_KEY}` (operator must populate real values).
- **GCS**: `gs://relayone-488319-r1-releases/{prod,staging,dev}/` (binary release channels; r1-downloads-cdn streams from here).
- **Cloud Build**: `r1-agent-pr` (PR gate) + `r1-agent-ci` (push to main). After PR #128 merges, `services/scripts/setup-cloudbuild-triggers.sh` adds 3 deploy triggers.
- **Domain mappings** (created, pending DNS): 9 subdomains under `r1.run` zone — `platform.{,staging.,dev.}r1.run`, `api.{,staging.,dev.}r1.run`, `downloads.{,staging.,dev.}r1.run`. Each maps to its Cloud Run service via CNAME → `ghs.googlehosted.com.`.

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

## Status

### Done
- Specs 1-9 merged + tested + deployed.
- 9 Cloud Run SaaS services live + Cloud SQL + Secret Manager + Artifact Registry.
- Branch hygiene: 20 archive tags, 3 active branches (main, claude/w521-…, archives).
- All Go tests + web typecheck + desktop tests green.
- Documentation: this doc + 6 sibling docs + 9 spec docs + decisions log + HANDOFF state file.

### In Progress
- DNS propagation for the 9 r1.run subdomains (operator action: add Cloudflare CNAMEs).
- Operator follow-ups: secret values, CLAUDE.md package map line, Cloud Build trigger creation.

### Scoped
- JWT login + RelayOne MSP SSO (Path A — Go reimpl of `@relayone/auth-core`).
- Admin panel at `admin.r1.run` (clone `*-admin` template, customize).
- PostHog + Customer.io + CodeRadar event integration.

### Scoping
- Cross-machine session migration.
- Encryption-at-rest for journals.
- Per-tool throttling policy.

### Potential — On Horizon
- Marketing site with affiliate / SEO / CRO / attribution / retention stack.
- BitBucket Pipelines adapter parity.
- Browser tool sandboxed under remote browser.
- Cross-product deterministic skill exchange.
