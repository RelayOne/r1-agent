# r1

**A parallel-cognition agent runtime with a multi-instance chat UI, a per-user daemon, and an MCP-equivalent for every interaction — now live as a hosted SaaS at r1.run.**

r1 is the open agent runtime that refuses to lie about completion. It plans, executes, verifies, and reviews coding work the same way a careful engineer does: one strong implementer, an adversarial cross-model reviewer, content-addressed evidence, and a layered machine-mechanical guard that *refuses to call work "done" while plan items are unchecked*. It thinks in parallel via a Global Workspace–style Cortex. It surfaces every cognitive thread, tool call, and mission task as a cross-platform UI primitive called a Lane — rendered identically in a Bubble Tea TUI, a Cursor-3-Glass React web app, and a Tauri 2 desktop shell. Every UI action has an idempotent, schema-validated MCP equivalent so external agents drive r1 the same way humans do.

## Why r1?

Most coding agents *say* they verify. r1 makes verification load-bearing:

- **The harness is the product, not the model.** r1 is provider-agnostic with a five-tier fallback (Claude → Codex → OpenRouter → direct API → lint-only). The differentiator is the loop: descent verification, honesty gates, content-addressed ledger, anti-truncation enforcement.
- **Refuses to truncate itself.** When the model says "good enough" or "I'll defer this to a follow-up," seven independent layers of regex + scope-completion gates + supervisor rules + post-commit hooks refuse `end_turn` while real plan items are unchecked. The model can't override this from inside; only the operator can, with an explicit flag.
- **Parallel cognition without subagent isolation.** Six specialist Lobes run alongside the main thread, share the same context window via a 1-hour cache breakpoint, and publish typed Notes into a shared Workspace. Mid-turn user input invokes a Haiku-driven Router that decides whether to interrupt, steer, queue, or just chat.
- **One wire, many surfaces.** The same per-user `r1 serve` daemon (Watchman pattern; one process, N session goroutines) backs the TUI, the web app at `platform.r1.run`, the Tauri desktop, and any external MCP-aware agent.

## Quick start

### Install (CLI / daemon)

```bash
# Hosted binary CDN (production channel)
curl -fsSL https://downloads.r1.run/prod/r1-$(uname -s | tr A-Z a-z)-$(uname -m | sed s/x86_64/amd64/) -o r1
chmod +x r1 && sudo mv r1 /usr/local/bin/

# Or from source (Go 1.25+, CGO for SQLite)
git clone https://github.com/RelayOne/r1-agent && cd r1-agent
go build ./cmd/r1
sudo mv r1 /usr/local/bin/

# One-line installer (legacy; verifies cosign signature when cosign is on PATH)
curl -fsSL https://raw.githubusercontent.com/RelayOne/r1-agent/main/install.sh | bash
```

### Run a single mission

```bash
r1 run --task "Add request ID middleware" --dry-run
r1 build --plan stoke-plan.json --workers 4 --dry-run
r1 task "Fix the flaky integration test in server/handler"
```

### Start the daemon + UIs

```bash
r1 serve                              # spawn-on-demand singleton; one process, N sessions
r1 chat                               # connects to the local daemon over unix-socket / named-pipe
open http://127.0.0.1:7777/           # web UI (loopback by default; CSP locked)
r1 serve --install                    # install per-OS service unit (launchd / systemd-user / Windows SCM)
```

### Drive r1 from another agent (MCP)

```bash
r1 mcp serve --print-tools            # 38-tool catalog across 10 categories (sessions, lanes, cortex,
                                      # mission, worktree, bus, verify, TUI, web, anti-trunc)
r1 mcp serve --markdown               # docs/AGENTIC-API.md generator
```

### Verify the anti-truncation gate fired correctly

```bash
r1 antitrunc verify -n 20             # cross-checks last 20 commit "spec N done" claims against
                                      # the actual plan/spec checklist; exits non-zero on lying
r1 antitrunc tail                     # streams audit/antitrunc/ in real time
```

## How it works (60-second tour)

1. **You write a task or load a plan.** r1 either generates a plan from your prompt or accepts a JSON plan file with explicit tasks, AC, dependencies.
2. **The mission runtime takes over.** For each task: a strong-model implementer drafts the change, the verification descent engine cross-checks against git state, AC, and the tool-call log, and an adversarial cross-model reviewer (Codex by default when Claude implements) runs the gauntlet again before approval.
3. **The Cortex thinks in parallel.** While the main thread works, six Lobes run in parallel rounds — pulling memory, watching the plan, drafting clarifying questions, gating end-of-turn on critical findings, drainging events to the WAL, curating "should-remember" facts.
4. **Lanes surface every thread.** The main agent thread is a lane. Each Lobe is a lane. Each long-running tool is a lane. Lanes have a 6-state machine and stream over JSON-RPC 2.0 with monotonic per-session `seq`. The TUI, web, desktop, and MCP all see the same lanes.
5. **Anti-truncation refuses false completion.** When the model emits a phrase from the catalog ("good enough", "deferring to follow-up", "Anthropic load balance limits") or tries to `end_turn` while plan items are unchecked, the layered gate refuses and forces continuation. Verifiable in `r1 antitrunc verify`.
6. **Evidence persists.** Every node lands in a content-addressed ledger; every event hits the WAL-backed durable bus; every cost tick is journaled. `daemon restart` replays from `journal.ndjson` and emits `daemon.reloaded` to reconnecting clients.

Full narrative: [`docs/HOW-IT-WORKS.md`](docs/HOW-IT-WORKS.md).

## Features

### Mission runtime
- **Plan / execute / verify / review loop** with cross-model reviewer gating. One strong implementer per task plus an adversarial reviewer is more reliable than loose multi-agent consensus. — `internal/app/`, `internal/workflow/`, `internal/mission/`, `internal/verify/`, `internal/critic/`, `internal/convergence/`. **Status: Done.**
- **Content-addressed ledger + WAL-backed event bus + STOKE envelope.** Every node has a `sha256:<hex>` content ID; every event survives daemon restart. — `internal/ledger/`, `internal/bus/`. **Status: Done.**
- **Five-provider model fallback** (Claude → Codex → OpenRouter → direct API → lint-only) with subscription pool, circuit breaker, OAuth poller, cost-aware resolver. — `internal/model/`, `internal/subscriptions/`. **Status: Done.**

### Cortex — parallel cognition (specs 1, 2)
- **MemoryRecallLobe** (deterministic) surfaces top-3 prior memory + wisdom hits as `info` Notes per round.
- **WALKeeperLobe** (deterministic) drains every hub event to durable WAL with backpressure-shed; cortex Notes survive daemon restart.
- **RuleCheckLobe** (deterministic) maps supervisor-rule fires to Notes; `trust.*` and `consensus.dissent.*` are `critical` and refuse `end_turn`.
- **PlanUpdateLobe** (Haiku) proposes `plan.json` deltas every third turn or on action-verb input; auto-applies edits, queues adds and removes for confirmation.
- **ClarifyingQLobe** (Haiku) detects actionable ambiguity and drafts up to 3 clarifying questions at idle.
- **MemoryCuratorLobe** (Haiku) extracts "should-remember" facts every fifth turn; auto-writes only the `fact` category, queues others; privacy filter drops `private`-tagged source messages and writes every auto-curate to `~/.r1/cortex/curator-audit.jsonl`.
- **Defaults:** 5 concurrent LLM Lobes, hard cap 8. Per-turn budget caps Lobe output at 30% of main-thread tokens. Sonnet escalation only on tagged-critical paths.
- **Status: Done.** — `internal/cortex/`, `internal/cortex/lobes/`.

### Lanes — the cross-surface UI primitive (spec 3)
- Six event types (`lane.created`, `lane.status`, `lane.delta`, `lane.cost`, `lane.note`, `lane.killed`); JSON-RPC 2.0 envelope; monotonic per-session `seq`; replay via `Last-Event-ID` (SSE) or `since_seq` (JSON-RPC).
- Six-state FSM (`pending → running → blocked → done | errored | cancelled`) with orthogonal `pinned` flag.
- **Five MCP tools**: `r1.lanes.list`, `r1.lanes.subscribe`, `r1.lanes.get`, `r1.lanes.kill`, `r1.lanes.pin` — every lane action agent-driveable.
- WebSocket subprotocol `r1.lanes.v1` + `Sec-WebSocket-Protocol: <token>` auth.
- 3 µs/event end-to-end; 2.3 µs/event with 5 subscribers — well under the 50/100 µs spec targets.
- **Status: Done.** — `internal/streamjson/lane.go`, `internal/mcp/lanes_server.go`, `internal/server/`.

### Surfaces — TUI, Web, Desktop (specs 4, 6, 7)
- **TUI** (Bubble Tea v2): adaptive lane columns, focus mode at 65/35 split, single fan-in `chan laneTickMsg` coalesced to ≤10 Hz, render-string cache, diff-only repaint. Keys `1`–`9` jump-to-lane, `tab` cycles, `enter` focuses, `x` kills, `K` kills-all. **Status: Done (72 tests `-race`).** — `internal/tui/lanes/`.
- **Web** (`web/` — React 18 + Vite 6 + Tailwind 3 + shadcn/ui + zustand 5 + react-router 7 + Streamdown + `@ai-sdk/react`): Cursor 3 "Glass" three-column shell. Streaming markdown via `vercel/streamdown`. Tool / reasoning / plan / diff cards. Tile mode pins 2-4 lanes into the center pane with HTML5 drag-reorder + `Cmd+Shift+←/→` keyboard alternative. WS subprotocol-token auth; reconnect with `Last-Event-ID`; 10-attempt hard cap then `<ConnectionLostBanner>`. CSP locked to loopback. Coverage manifest enforces sibling-tests on every component. **Status: Done (55/55 spec items).** — `web/`.
- **Desktop** (`desktop/` — Tauri 2 augmentation): keeps the existing 12-phase R1D shell. Adds discovery-or-spawn (probes `~/.r1/daemon.json`, falls back to bundled `r1` via `ShellExt::sidecar`). Per-session workdir via `tauri-plugin-store`. Per-session `tauri::ipc::Channel<LaneEvent>` at 10 Hz. `Cmd+\` pops a lane into its own `WebviewWindow`. Component sharing via npm workspace `packages/web-components/`. **Status: Done (40/40 + 3 post-flight).** — `desktop/`, `packages/web-components/`.

### r1d daemon — one process, N sessions (spec 5)
- **Watchman pattern**: per-user singleton, spawn-on-demand. `gofrs/flock` on `~/.r1/daemon.lock` enforces single-instance.
- **Discovery**: `~/.r1/daemon.json` (mode 0600, 32-byte hex token rotated on every start).
- **IPC**: unix socket / Windows named pipe for CLI (peer-cred check; no token); loopback HTTP+WS for browsers and desktop (Origin pin + Host pin + WS subprotocol token + 256-bit Bearer).
- **Multi-session**: N concurrent sessions as goroutines, each bound to a workdir via `cmd.Dir`. **Pre-multisession `os.Chdir` audit lint** is the mandatory gate (one stray `os.Chdir` would silently leak workdir across goroutines).
- **Journal-first**: each session writes `journal.ndjson` under `<workdir>/.r1/sessions/<id>/`; daemon restart replays the journal and emits `daemon.reloaded` to reconnecting clients.
- **`r1 serve --install`** opts into a per-OS service unit (launchd / systemd-user / Windows SCM).
- **Status: Done.** — `internal/server/`, `internal/daemonlock/`, `internal/daemondisco/`, `internal/serviceunit/`.

### Agentic test harness — every UI action is a tool (spec 8)
- **38-tool MCP catalog** across 10 categories (sessions, lanes, cortex, mission, worktree, bus, verify, TUI, web, anti-trunc). **Status: Done.**
- **Slack-style envelope** + `internal/stokerr/` 10-code error taxonomy at every wire boundary. No raw Go errors leak.
- **TUI shim** (`internal/tui/teatest_shim.go`) drives Bubble Tea via MCP without a terminal emulator. Synthetic `A11yEmitter` + JSONPath evaluator for structural assertions.
- **Gherkin-flavored markdown** (`*.agent.feature.md`) parsed + dispatched by `tools/agent-feature-runner/`. 8 seed feature fixtures across all 10 categories.
- **`lint-view-without-api`** scanner — UI without an MCP equivalent is a build break. — `tools/lint-view-without-api/`.
- **`make agent-features`, `make lint-views`, `make docs-agentic`, `make storybook-mcp-validate`** — one-line CI/local recipes.
- See [`docs/AGENTIC-API.md`](docs/AGENTIC-API.md) for the full external-agent contract.

### Anti-truncation enforcement (spec 9)
- **Seven independently-effective layers**, each enough to refuse self-truncation on its own. The model cannot bypass them from inside the conversation; only the operator can, via `--no-antitrunc-enforce`.
- **Layer 1**: regex catalog (`internal/antitrunc/phrases.go`) — 14 truncation + false-completion patterns.
- **Layer 2**: scope-completion gate (`internal/antitrunc/gate.go`) — refuses `end_turn` while plan or spec items are unchecked.
- **Layer 3**: cortex Lobe (`internal/cortex/lobes/antitrunc/`) — publishes `critical` Workspace Notes that block `end_turn`.
- **Layer 4**: supervisor rules (`internal/supervisor/rules/antitrunc/`) — `truncation_phrase_detected`, `scope_underdelivery`, `subagent_summary_truncation`.
- **Layer 5**: agentloop wiring (`internal/agentloop/antitrunc.go`) — gate composes BEFORE all other end-turn hooks.
- **Layer 6**: post-commit git hook (`scripts/git-hooks/post-commit-antitrunc.sh`) — observes false-completion phrases in commit bodies.
- **Layer 7**: CLI + MCP tool (`r1 antitrunc verify`, `r1.antitrunc.verify`) — cross-checks recent commit "task N done" claims against the actual checklist; exits non-zero on `lying_count > 0`.
- **Soak**: 1,000,000-iteration soak run shows 0 false positives, 0 false negatives, 499K true positives at 16,891 iter/sec.
- **Status: Done.** Full guide: [`docs/ANTI-TRUNCATION.md`](docs/ANTI-TRUNCATION.md).

### Hosted SaaS surfaces on r1.run
- **`platform.{,staging.,dev.}r1.run`** — docs site rendered from `docs/` via `r1-docs` Cloud Run service.
- **`api.{,staging.,dev.}r1.run`** — `r1-coord-api` Cloud Run service: `/healthz`, `/v1/version`, `/v1/license/verify`, `/v1/telemetry/opt-in`. Backed by Cloud SQL `r1-{prod,staging,dev}-pg`.
- **`downloads.{,staging.,dev.}r1.run`** — `r1-downloads-cdn` Cloud Run service streaming binaries from `gs://relayone-488319-r1-releases/{prod,staging,dev}/<asset>`.
- **All 9 services live on Cloud Run us-central1** with min-instances=1, instance-based billing, distroless static images, /livez + /readyz endpoints.
- **Auto-deploy** via `services/cloudbuild-deploy.yaml` — push to `main` rebuilds + redeploys prod; `staging` and `dev` branches deploy to their respective envs.
- **Status: Live (DNS pending Cloudflare CNAME records).** Operations runbook: [`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md).

## Project status

| Area | Status | What it means |
|---|---|---|
| Specs 1-9 (cortex / lanes / multi-surface / agentic / anti-trunc) | **Done — 171/172 items + 1 BLOCKED on operator action** | Merged to working branch, pushed to GitHub PR #128. |
| 9 Cloud Run SaaS services | **Live — 200 OK on /livez** | dev/staging/prod for r1-coord-api, r1-docs, r1-downloads-cdn. DNS pending Cloudflare CNAMEs. |
| Cloud SQL (r1-{prod,staging,dev}-pg, POSTGRES_16) | **Live** | All RUNNABLE; secrets bound via Secret Manager. |
| `go build`/`vet`/`test` | **All green** | 100% of test suite passes; 2 pre-existing failures fixed. |
| JWT login + RelayOne MSP SSO | **Scoped (Path A — Go reimpl)** | Uses `@relayone/auth-core` contract; Go port pending. |
| Admin panel (admin.r1.run) | **Scoped** | Will clone an existing `*-admin` template + customize for r1 routes. |
| PostHog + Customer.io + CodeRadar tracking | **Scoped** | CodeRadar already in-house; PostHog + Customer.io vendor integrations pending. |
| Marketing site + affiliate / SEO / CRO / attribution | **On horizon** | Multi-week marketing-engineering effort; deferred. |

## Documentation

| Doc | Audience | What it covers |
|---|---|---|
| [`docs/README.md`](docs/README.md) | Everyone | Mirror of this file. |
| [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) | Engineers | Tech stack, repo map (175 packages), system components, data models, API surface, infrastructure, testing architecture. |
| [`docs/HOW-IT-WORKS.md`](docs/HOW-IT-WORKS.md) | Anyone | User journey, technical walkthrough, key technical decisions. |
| [`docs/FEATURE-MAP.md`](docs/FEATURE-MAP.md) | PMs / decision-makers | Feature inventory grouped by area, status (Done / In-Progress / Scoped / Scoping / Potential). |
| [`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md) | DevOps | Prerequisites, env vars, build, deploy, infrastructure, monitoring, rollback. |
| [`docs/BUSINESS-VALUE.md`](docs/BUSINESS-VALUE.md) | Marketing / investors | Pitch narrative; zero jargon. |
| [`docs/AGENTIC-API.md`](docs/AGENTIC-API.md) | External-agent integrators | The full MCP tool surface + decisions log. |
| [`docs/ANTI-TRUNCATION.md`](docs/ANTI-TRUNCATION.md) | Operators | The 7-layer machine-mechanical defense against LLM self-truncation. |
| [`docs/decisions/index.md`](docs/decisions/index.md) | Engineers / reviewers | Architectural decisions log (D-A1..D-A5, D-2026-05-04-01..08, etc.). |

Specs 1-9: [`specs/cortex-core.md`](specs/cortex-core.md), [`specs/cortex-concerns.md`](specs/cortex-concerns.md), [`specs/lanes-protocol.md`](specs/lanes-protocol.md), [`specs/tui-lanes.md`](specs/tui-lanes.md), [`specs/r1d-server.md`](specs/r1d-server.md), [`specs/web-chat-ui.md`](specs/web-chat-ui.md), [`specs/desktop-cortex-augmentation.md`](specs/desktop-cortex-augmentation.md), [`specs/agentic-test-harness.md`](specs/agentic-test-harness.md), [`specs/anti-truncation.md`](specs/anti-truncation.md).

## Build, test, vet — the CI gate

```bash
go build ./...
go test ./...
go vet ./...
cd web && npm run build && npm run test     # web/ side
cd ../desktop && cargo test                 # desktop/ side
make lint-chdir                             # r1d-server Phase A audit gate
make lint-views                             # spec 8 UI-without-API gate
r1 antitrunc verify -n 20                   # spec 9 false-completion gate
```

These commands are the gate. They must be green on every PR. CI also runs `-race`, `golangci-lint` (advisory), `govulncheck`, `gosec`, and `make check-pkg-count`.

## Operations

After PR #128 merges:

```bash
# 1. Create dev + staging branches and apply branch protection
./scripts/setup-branch-protection.sh

# 2. Wire 3 Cloud Build triggers (one per env)
./services/scripts/setup-cloudbuild-triggers.sh

# 3. Add the 9 CNAMEs to Cloudflare (see plans/HANDOFF-deploy-state.md)
# 4. Set real values on the 6 r1-{env}-shared-{DATABASE_URL,ANTHROPIC_API_KEY} secrets
# 5. Smoke-check live: curl https://platform.r1.run/livez ; curl https://api.r1.run/v1/version
```

## Governance

- [GOVERNANCE.md](GOVERNANCE.md) — Roles (Contributor / Maintainer / BDFL), decision process.
- [STEWARDSHIP.md](STEWARDSHIP.md) — Core commitment: no functional feature migrates from self-hosted to cloud-only, ever.
- [CONTRIBUTING.md](CONTRIBUTING.md) — How to contribute, branch naming, PR template, DCO signoff.
- [SECURITY.md](SECURITY.md) — Disclosure policy, threat-model scope.

## License

MIT.
