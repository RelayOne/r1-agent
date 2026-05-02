# r1

**A parallel-cognition runtime with a Claude-chat-style multi-instance UI.**

r1 is a governed coding agent that thinks in parallel. The main action thread
drives the work. A shared **Cortex** workspace runs a half-dozen specialist
"Lobes" alongside it — pulling memory, watching the plan, drafting clarifying
questions, gating end-of-turn on critical findings — and feeds findings back
into the main thread at safe checkpoints. When you type something mid-turn,
an LLM-driven Router decides whether to interrupt, steer, queue, or just chat.
Every cognitive thread, every tool call, and every mission task becomes a
**Lane** — a first-class UI primitive rendered identically across a Bubble Tea
TUI, a React + Vite web app, and a Tauri 2 desktop shell. All three surfaces
talk to a single per-user **r1d daemon** that hosts N concurrent sessions
bound to working directories.

`r1` is the CLI name and the primary entrypoint under `cmd/r1`.

## What r1 actually does for you

- **Plans and executes coding work** through a deterministic plan → execute →
  verify → review loop, one strong implementer per task plus an adversarial
  cross-model reviewer. The harness is the product.
- **Refuses to believe "done"** without evidence. The verification descent
  engine cross-checks against git state, AC, and the tool-call log. Honeypot
  gates abort end-of-turn on canary leaks, markdown-image exfil, and
  destructive-without-consent shell.
- **Runs N cognitions per turn instead of one.** Memory recall, plan-update,
  rule-check, clarifying-Q, memory-curator, and WAL-keeper Lobes share full
  context with the main thread (no subagent isolation), publish typed Notes
  into a shared Workspace, and surface as live lanes in your UI.
- **Ships a real chat UI**, not a streaming-text terminal. The web app is
  styled after Cursor 3 "Glass": a session list on the left, a streaming
  chat in the center with tool/reasoning/plan cards, a lanes sidebar on the
  right, and a tile mode that pins 2-4 lanes into the main pane for parallel
  watching.
- **Speaks every surface through one wire.** Every UI action has an idempotent
  schema-validated MCP equivalent. The UI is a view over the API; never the
  reverse. External agents (Claude, Codex, Stagehand) drive r1 the same way
  you do.

## Status — What ships on `main` today

The cortex / lanes / multi-surface scope below is **scoped, not yet built**.
What's running on `main` right now:

- The governed plan / execute / verify / review mission loop with cross-model
  reviewer gating.
- The append-only content-addressed ledger, WAL-backed event bus, and the
  STOKE event envelope.
- The deterministic skill substrate: manufacture, registry, selection,
  signed-pack distribution, and an HTTP pack registry served by `r1 skills
  pack serve`.
- A 30+ subcommand `r1` CLI plus eight purpose-built satellite binaries
  (`stoke-acp`, `stoke-a2a`, `stoke-mcp`, `stoke-server`, `stoke-gateway`,
  `r1-server`, `chat-probe`, `critique-compare`).
- Wave 2 R1-parity: browser tools, Manus-style autonomous operator,
  multi-language LSP client, VS Code + JetBrains plugins, multi-CI parity
  (GitHub Actions, GitLab CI, CircleCI), a Tauri 2 desktop shell with the
  R1D-1..R1D-12 phases (scaffold, session view, SOW tree, descent ladder,
  skill catalog, ledger, memory, settings, MCP servers, observability,
  multi-session, signing).
- Prompt-injection hardening across four ingest paths plus tool-output
  sanitization, honeypot pre-end-turn gates, and a 58-sample red-team corpus.
- A 5-provider model resolver (Claude → Codex → OpenRouter → direct API →
  lint-only) with subscription pool, circuit breaker, and OAuth poller.

## Roadmap — Cortex / Lanes / Multi-Surface

Eight specs in `specs/` define the next slice. Build order is a strict DAG:

```
1. cortex-core              ──► foundation: Workspace, Lobes, Round, Spotlight,
                                Router, drop-partial interrupt, pre-warm pump
                                ┌───────────────────┐
                                ▼                   ▼
2. cortex-concerns          3. lanes-protocol
   six v1 Lobes                 wire format + 5 MCP tools
                                ┌───────────────────┐
                                ▼                   ▼
4. tui-lanes                5. r1d-server
   Bubble Tea v2 panel         per-user singleton daemon
                                ┌───────────────────┐
                                ▼                   ▼
6. web-chat-ui              7. desktop-cortex-augmentation
   React + Vite + Tailwind     Tauri 2 augmentation
                                └─────────┬─────────┘
                                          ▼
                                8. agentic-test-harness
                                   every UI action has an MCP tool
```

### Cortex — parallel cognition (specs 1, 2)

A new `internal/cortex/` package introduces a Global Workspace Theory-style
shared mutable view. The main `agentloop.Loop` keeps doing what it does;
alongside it run six concurrent specialist **Lobes**:

- **MemoryRecallLobe** (deterministic) — TF-IDF over the memory + wisdom
  store, surfaces top-3 prior findings as `info` Notes per round.
- **WALKeeperLobe** (deterministic) — drains every hub event into the durable
  bus WAL with backpressure-shed semantics, so cortex Notes survive daemon
  restart.
- **RuleCheckLobe** (deterministic) — converts supervisor-rule fires into
  Notes; `trust.*` and `consensus.dissent.*` are tagged `critical` and refuse
  `end_turn` until acknowledged.
- **PlanUpdateLobe** (Haiku 4.5) — every third turn or on action-verb input,
  proposes `plan.json` deltas; auto-applies edits, queues adds and removes
  for user confirmation.
- **ClarifyingQLobe** (Haiku 4.5) — detects actionable ambiguity, drafts up
  to 3 clarifying questions, surfaces them at idle.
- **MemoryCuratorLobe** (Haiku 4.5) — every fifth turn, extracts
  "should-remember" facts; auto-writes only the `fact` category, queues
  others. Privacy filter drops `private`-tagged source messages and writes
  every auto-curate to `~/.r1/cortex/curator-audit.jsonl`.

Lobes share full context (read-only message history, the same model breakpoint,
the same tool ordering — pre-warmed via a `max_tokens=1` cache request every
4 minutes). They publish typed `Notes` into the `Workspace`; the main thread
drains them at `MidturnCheckFn`, gates end-of-turn at `PreEndTurnCheckFn`, and
on mid-turn user input invokes the **Router**: a Haiku 4.5 call with four
tools — `interrupt`, `steer`, `queue_mission`, `just_chat` — that decides how
your message is merged. Interrupts use the drop-partial protocol (cancel the
turn context, drain SSE, never persist the partial assistant message, append
a synthetic user message describing the interrupt).

Defaults: 5 concurrent LLM Lobes, hard cap 8. Per-turn budget caps Lobe output
at 30% of main-thread output tokens. Sonnet escalation only on tagged-critical
paths or operator-flagged Lobes.

### Lanes — the cross-surface UI primitive (spec 3)

A **Lane** is the per-surface visible thread of activity inside a single r1d
session. The main agent thread is a lane. Each Lobe is a lane. A long-running
tool call gets promoted to its own lane. Lanes have a six-state machine
(`pending → running → blocked → done | errored | cancelled`) plus an orthogonal
`pinned` flag. Six event types stream over JSON-RPC 2.0 with monotonic per-
session `seq`: `lane.created`, `lane.status`, `lane.delta`, `lane.cost`,
`lane.note`, `lane.killed`. Replay uses `Last-Event-ID` (SSE) or `since_seq`
(JSON-RPC). The WebSocket subprotocol is `r1.lanes.v1`. Five MCP tools
(`r1.lanes.list`, `subscribe`, `get`, `kill`, `pin`) make every lane action
agent-driveable.

### Surfaces — TUI, Web, Desktop (specs 4, 6, 7)

- **TUI** (`internal/tui/lanes/` — Bubble Tea v2): adaptive lane columns when
  the terminal is wide, vertical stack when narrow, focus mode at 65/35
  main+peers. Single fan-in `chan laneTickMsg` coalesced to 5 Hz. Keys
  `1`–`9` jump-to-lane, `tab` cycles, `enter` focuses, `x` kills, `K`
  kills-all, `?` opens help. Per-lane render-string cache; diff-only repaint.
- **Web** (`web/` — React 18 + Vite 6 + Tailwind 3 + shadcn/ui): three-column
  Cursor 3 "Glass" layout. Streaming markdown via `vercel/streamdown`. Tool /
  reasoning / plan / diff cards via `@ai-sdk/elements`. Tile mode pins 2-4
  lanes into the center pane. WS subprotocol-token auth; reconnect with
  `Last-Event-ID`. Keyboard-only flows; high-contrast mode; CSP locked to
  loopback.
- **Desktop** (`desktop/` — Tauri 2 augmentation): keeps the existing 12-phase
  R1D shell intact. Adds discovery-or-spawn (probes `~/.r1/daemon.json`,
  falls back to bundled-binary sidecar via `ShellExt::sidecar`). Per-session
  workdir via `tauri-plugin-store`. Per-session `tauri::ipc::Channel<LaneEvent>`
  at 10 Hz. `Cmd+\` pops a lane into its own `WebviewWindow`. Lane components
  shared with web via a `packages/web-components/` workspace package.

### r1d daemon — one process, N sessions (spec 5)

`r1 serve` becomes a per-user singleton on-demand daemon (the Watchman
pattern). One process holds N concurrent sessions as goroutines, each bound
to a working directory carried via `cmd.Dir`. Single-instance enforcement via
`gofrs/flock` on `~/.r1/daemon.lock`. Discovery via `~/.r1/daemon.json`
(mode 0600, token rotated on every start). IPC: unix socket / Windows named
pipe for CLI (peer-cred check; no token), loopback HTTP+WS for browsers and
desktop (Origin pin + Host pin + WS subprotocol token + 256-bit Bearer).
Each session writes a `journal.ndjson` under `<workdir>/.r1/sessions/<id>/`;
daemon restart replays the journal and emits `daemon.reloaded` to reconnecting
clients. A pre-multisession **`os.Chdir` audit + CI lint** is the gate before
multi-session is enabled — one stray `os.Chdir` would silently leak workdir
across goroutines.

### Agentic test harness — every UI action is a tool (spec 8)

The governing principle: every action a human can take through a UI MUST
have a documented, idempotent, schema-validated agent equivalent reachable
through MCP. A consolidated `internal/mcp/r1_server.go` publishes the full
catalog (`r1.session.*`, `r1.lanes.*`, `r1.cortex.*`, `r1.mission.*`,
`r1.worktree.*`, `r1.bus.tail`, `r1.verify.*`, `r1.tui.*`). A
`teatest_shim.go` exposes the TUI under MCP without a real terminal. Web is
covered by Playwright MCP; component contracts by Storybook MCP. A CI lint
(`tools/lint-view-without-api/`) scans React, Bubble Tea, and Tauri sources
for interactive elements that lack MCP counterparts. Test scenarios live as
Gherkin-flavored markdown in `*.agent.feature.md` files.

## Where to start

- **Architecture**: [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md)
- **Workflow narrative**: [`docs/HOW-IT-WORKS.md`](docs/HOW-IT-WORKS.md)
- **Feature inventory**: [`docs/FEATURE-MAP.md`](docs/FEATURE-MAP.md)
- **Deployment posture**: [`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md)
- **Commercial framing**: [`docs/BUSINESS-VALUE.md`](docs/BUSINESS-VALUE.md)
- **Decisions log**: [`docs/decisions/index.md`](docs/decisions/index.md)
- **Specs (1–8)**: [`specs/`](specs/) — `cortex-core.md`, `cortex-concerns.md`,
  `lanes-protocol.md`, `tui-lanes.md`, `r1d-server.md`, `web-chat-ui.md`,
  `desktop-cortex-augmentation.md`, `agentic-test-harness.md`
- **Synthesized research**: [`specs/research/synthesized/`](specs/research/synthesized/)
- **Main evaluation artifact**:
  [`evaluation/r1-vs-reference-runtimes-matrix.md`](evaluation/r1-vs-reference-runtimes-matrix.md)

## Install (today, on `main`)

```bash
# Homebrew (macOS + Linux) — published by goreleaser on each tag.
brew install RelayOne/r1-agent/r1

# One-line installer — detects platform, verifies cosign signature
# (keyless OIDC via sigstore) when cosign is on PATH.
curl -fsSL https://raw.githubusercontent.com/RelayOne/r1-agent/main/install.sh | bash

# From source (Go 1.26+, CGO enabled for SQLite).
go build ./cmd/r1
sudo mv r1 /usr/local/bin/
```

Once spec 5 (r1d-server) lands, `r1 serve` becomes the canonical entrypoint
for long-running daemons; `r1 chat` connects to it; `r1 serve --install`
opts into a per-OS service unit (launchd / systemd-user / Windows SCM) for
always-on operation.

## Quick start

```bash
# Single task end-to-end: plan, execute, verify, commit
r1 run --task "Add request ID middleware" --dry-run

# Multi-task plan with parallel agents, resume, ROI filter
r1 build --plan stoke-plan.json --workers 4 --dry-run

# Generate a task plan from codebase analysis
r1 plan --task "Add JWT auth" --dry-run

# Free-text task entry — the executor router picks the right backend
r1 task "Fix the flaky integration test in server/handler"

# Deterministic security scan (secrets, eval, injection, debug)
r1 scan --security

# 17-persona adversarial audit
r1 audit --dry-run

# Interactive Bubble Tea TUI (dashboard, focus, detail panes)
r1 build --plan stoke-plan.json --interactive

# Subscription pool utilization + circuit breaker state
r1 pool --claude-config-dir /pool/claude-1
```

## Build, test, vet — the CI gate

```bash
go build ./cmd/r1
go test ./...
go vet ./...
```

These three commands are the gate. They must be green on every PR. CI also
runs the race detector, `golangci-lint` (advisory), `govulncheck`, and
`gosec`.

## Governance

- [GOVERNANCE.md](GOVERNANCE.md) — Roles (Contributor / Maintainer / BDFL),
  decision process, maintainer path.
- [STEWARDSHIP.md](STEWARDSHIP.md) — Core commitment: no functional feature
  migrates from self-hosted to cloud-only, ever.
- [CONTRIBUTING.md](CONTRIBUTING.md) — How to contribute, branch naming, PR
  template, DCO signoff.
- [SECURITY.md](SECURITY.md) — Disclosure policy, threat-model scope.

## Status — sectioned

### Done
- Governed plan/execute/verify mission runtime with cross-model reviewer gate.
- Content-addressed ledger, WAL-backed event bus, STOKE envelope.
- Deterministic skill substrate + signed pack distribution + HTTP registry.
- Wave 2 R1-parity: browser tools, Manus operator, LSP client, VS Code +
  JetBrains plugins, multi-CI parity, Tauri desktop R1D-1..R1D-12.
- Prompt-injection hardening across ingest paths, honeypot gates, red-team
  corpus.

### In Progress
- Hardening of the Manus-style autonomous operator (per-mission toggle).
- LSP feature coverage beyond hover/definition/diagnostics.
- Race-clean regression sweep across `internal/`.

### Scoped
- **cortex-core** (spec 1) — Workspace + Lobe interface + Round + Router +
  drop-partial interrupt + pre-warm pump.
- **cortex-concerns** (spec 2) — six v1 Lobes.
- **lanes-protocol** (spec 3) — six event types, JSON-RPC envelope, five MCP
  tools.
- **tui-lanes** (spec 4) — Bubble Tea v2 lanes panel.
- **r1d-server** (spec 5) — `r1 serve` per-user singleton daemon.
- **web-chat-ui** (spec 6) — React + Vite + Tailwind + shadcn web app.
- **desktop-cortex-augmentation** (spec 7) — Tauri 2 augmentation.
- **agentic-test-harness** (spec 8) — every UI action reachable via MCP.

### Scoping
- Cross-machine session migration (current daemon is one-host).
- Per-tool throttling policy in `.stoke/`.
- Encryption-at-rest for journals (separate `specs/encryption-at-rest.md`).

### Potential — On Horizon
- BitBucket Pipelines adapter parity with GitLab CI / GitHub Actions.
- Native MCP server bundle for popular IDEs without a separate install step.
- Browser tool sandboxed under a remote browser.
- Cross-product deterministic skill exchange and marketplace dynamics.

## License

MIT.
