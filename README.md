# r1

**A parallel-cognition agent runtime with a multi-instance chat UI, a per-user daemon, and an MCP-equivalent for every interaction — now live as a hosted SaaS at r1.run.**

r1 is the open agent runtime that refuses to lie about completion. It plans, executes, verifies, and reviews coding work the same way a careful engineer does: one strong implementer, an adversarial cross-model reviewer, content-addressed evidence, and a layered machine-mechanical guard that *refuses to call work "done" while plan items are unchecked*. It thinks in parallel via a Global Workspace–style Cortex. It surfaces every cognitive thread, tool call, and mission task as a cross-platform UI primitive called a Lane — rendered identically in a Bubble Tea TUI, a Cursor-3-Glass React web app, and a Tauri 2 desktop shell. Every UI action has an idempotent, schema-validated MCP equivalent so external agents drive r1 the same way humans do.

The "refuses to lie about completion" claim is now measurable. The **TruthfulCompletion benchmark** ([`docs/truthful-completion-methodology.md`](docs/truthful-completion-methodology.md)) scores AI coding agents on a single axis that no other benchmark covers: *when the agent claimed to be done, was the agent actually done?* The shipped runner (`cmd/r1-bench/`) drives an 8-dispatcher matrix — R1, R1 with anti-truncation enforce, Claude Code (with and without R1's Stop-hook template), Cline, Aider, Codex CLI, Cursor, plus a Tether middleware that wraps any of the above in R1's anti-truncation engine — and reports a Wilson 95% CI on each agent's truthful-completion rate. The engineering scope plus 5 seed missions ships in this branch; the checked-in monthly + PR TruthfulCompletion Cloud Build configs are not the live GCP automation yet, and the 95-mission SWE-bench Pro–derived corpus is still deferred to operator curation per [`plans/corpus-100.md`](plans/corpus-100.md).

Current repo + infra truth-state is tracked in [`plans/TRUTH-STATE-2026-05-15.md`](plans/TRUTH-STATE-2026-05-15.md). Use that file, not older deploy handoffs, for the confirmed live state.

## Completion SOW — shipped 2026-05-14

Fourteen specs took r1 from "technically honest agent runtime" to "operationally honest hosted product." All fourteen are merged. The original Tier A/B/C/D enumeration with duplicate per-spec descriptions has been collapsed into the canonical list below; full per-spec acceptance criteria, dependencies, and BUILD_ORDER live in [`specs/`](specs/). The companion entries in [`docs/FEATURE-MAP.md`](docs/FEATURE-MAP.md), [`docs/BUSINESS-VALUE.md`](docs/BUSINESS-VALUE.md), [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md), and [`docs/HOW-IT-WORKS.md`](docs/HOW-IT-WORKS.md) cover the operator-facing detail, the marketing rationale, the new internal package layout, and the runtime integration points.

| Tier | Spec | What | Reference |
|---|---|---|---|
| A1 | Prompt-guard hardening | Threads prompt-guard through plan/execute/verify, per-tool MCP input validation, ed25519 system-prompt fingerprint, adversarial review over the CL4R1T4S corpus, per-session injection budget | [`specs/promptguard-hardening.md`](specs/promptguard-hardening.md) |
| A2 | P0 hardening + S-0 foundation | Superseded by [`specs/encryption-at-rest.md`](specs/encryption-at-rest.md) + [`specs/retention-policies.md`](specs/retention-policies.md) — the cited "WORK-r1 Tasks 8-10" resolved to that work | [`specs/p0-hardening-s0-foundation.md`](specs/p0-hardening-s0-foundation.md) |
| A3 | One-shot production hardening | `--max-mem` (RLIMIT_AS + GOMEMLIMIT), `--timeout` with drop-partial, deterministic SIGINT/SIGTERM (exits 130/143), HMAC-signed audit POST, 1000-concurrent integration test | [`specs/oneshot-production-hardening.md`](specs/oneshot-production-hardening.md) · [runbook](docs/integrations/relaygate-r1-stage.md) |
| A4 | RelayOne SSO | OIDC + PKCE-S256, per-tenant token isolation (HKDF-SHA256, per-tenant `kid`), HS256 + RS256 rotation, `__Host-r1_at` + `__Host-r1_rt` cookies, 4 routes on the daemon mux | [`specs/relayone-sso.md`](specs/relayone-sso.md) · [runbook](docs/integrations/relayone-sso.md) |
| A5 | Admin panel (Phase 1, read-only) | Read-only admin plan and hosted scaffold shipped; hosted `r1-admin` data/auth wiring is still partial | [`specs/admin-panel.md`](specs/admin-panel.md) · [runbook](docs/operations/admin-panel.md) |
| B1 | PostHog analytics | Client + subscriber code shipped; product-wide hosted wiring is still partial and not bound in the public Cloud Run deploys | [`specs/posthog-analytics.md`](specs/posthog-analytics.md) · [runbook](docs/integrations/posthog.md) |
| B2 | Customer.io lifecycle email | Client + debounce store shipped; production lifecycle-event wiring remains partial | [`specs/customerio-lifecycle.md`](specs/customerio-lifecycle.md) · [runbook](docs/integrations/customerio.md) |
| B3 | CodeRadar dogfood | Error/observability capture shipped; canonical bus subscriber is wired in `cmd/r1`; hosted `coord-api` telemetry now emits real CodeRadar `/v1/track` events with browser-attribution properties when `CODERADAR_DSN` is present, but broader GTM/browser rollout remains partial | [`specs/coderadar-dogfood.md`](specs/coderadar-dogfood.md) |
| C1 | Cross-machine session migration | `.r1session` bundle format, `r1 session export/import/migrate`, chain-root-hash continuity | [`specs/cross-machine-session-migration.md`](specs/cross-machine-session-migration.md) · [runbook](docs/operations/session-migration.md) |
| C3 | Per-tool throttling | Two-tier (per-session + per-tenant) token bucket at MCP + agentloop boundaries, declarative `r1.policy.yaml`, `daemon.reload_config` hot-reloads, p99 < 100µs per `Allow` | [`specs/per-tool-throttling.md`](specs/per-tool-throttling.md) · [runbook](docs/operations/throttling.md) |
| C4 | MCP IDE bundles | `r1 ide install/uninstall/verify` for Cursor, Windsurf, VS Code, JetBrains | [`specs/mcp-ide-bundles.md`](specs/mcp-ide-bundles.md) |
| C5 | BitBucket Pipelines adapter | Strict parity with GitHub Actions + GitLab CI: OIDC via `BITBUCKET_STEP_OIDC_TOKEN`, inline PR comments, `R1 Verify` commit-status writer, 4 per-language templates | [`specs/bitbucket-pipelines-adapter.md`](specs/bitbucket-pipelines-adapter.md) · [runbook](docs/integrations/bitbucket-pipelines.md) |
| C6 | Browser tool — remote sandbox | Browserless + in-house Cloud Run providers, tenant-isolated sandbox, deny-by-default egress | [`specs/browser-remote-sandbox.md`](specs/browser-remote-sandbox.md) · [runbook](docs/integrations/remote-browser.md) · [ops](docs/operations/r1-browser-service.md) |
| C7 | Cross-product skill exchange | Pack-format v2 with `compat` matrix, federated ed25519 trust root, runtime adapters for CloudSwarm/Heroa/Veritize | [`specs/cross-product-skill-exchange.md`](specs/cross-product-skill-exchange.md) |
| D1 | Anti-truncation hook-mode flag | `r1 antitrunc verify --hook-mode --plan` emits the JSON envelope Claude Code's Stop hook expects; exit 2 on findings | [`specs/antitrunc-hook-mode-flag.md`](specs/antitrunc-hook-mode-flag.md) |
| D2 | TruthfulCompletion benchmark | 8-dispatcher matrix, cross-vendor LLM judge, Wilson 95% CI, leaderboard renderer, 5 seed missions, and checked-in monthly + PR configs; live GCP automation is still the legacy nightly benchmark pair while the 95-mission corpus remains deferred per [`plans/corpus-100.md`](plans/corpus-100.md) | [`specs/truthful-completion-benchmark.md`](specs/truthful-completion-benchmark.md) · [methodology](docs/truthful-completion-methodology.md) |

The canonical build-spec queue is closed, but meaningful backlog remains outside that queue: the deferred 95-mission TruthfulCompletion corpus, large unfinished desktop-runtime surfaces, marketing / GTM work, Cloud Build trigger creation, and real CodeRadar product-analytics token wiring if R1 moves GTM reporting off third-party tools. DNS is no longer pending: the `r1.run` Cloudflare records and 12 public domain mappings are already live.

## Cross-product skill distribution (preview)

C7 ships the R1-side substrate for federated skill packs. A v2
manifest schema with an explicit `compat` list, a federated ed25519
trust root, and runtime adapters for CloudSwarm, Heroa, and Veritize
let a pack be authored once against R1 and adopted into the sibling
products via `r1 skills pack adopt --pack <id> --for <product>`. The
existing v1 pack format remains supported unchanged. See
[`docs/skills/cross-product-distribution.md`](docs/skills/cross-product-distribution.md)
for pack-author docs and
[`docs/skills/federated-trust.md`](docs/skills/federated-trust.md)
for the operator runbook.

## What's new — final-sweep features (2026-05-05)

- core mission loop with planning, execution, verification, and review
- content-addressed ledger and WAL-backed runtime evidence
- benchmark and parity evidence under `evaluation/`
- deterministic skill manufacturing, registry, and selection surfaces
- skill-pack lifecycle commands including `init`, `info`, `install`,
  `list`, `publish`, `search`, `sign`, `verify`, `update`, and `serve`
- seeded repo/user skill-pack registries and signed-pack runtime
  verification
- new runtime helpers for ledger audit, execution audit, metrics
  collection, timeout/cancel behavior, oneshot runtime cost metadata,
  and flagship deterministic runtimes
- **anti-truncation enforcement** — a layered, machine-mechanical
  defense against LLM self-truncation. Refuses end-turn while plan
  items are unchecked or truncation phrases are emitted. See
  [`docs/ANTI-TRUNCATION.md`](docs/ANTI-TRUNCATION.md).
Four small, load-bearing additions merged via PRs #168 / #169 / #170 / #171 (sync to `main` in commit `242af4a8`). They close out the trust + audit story and harden the release pipeline:

- **Skill-aware compaction** — `internal/concern/SkillCompactor` evicts least-recently-used skills under context-budget pressure and `internal/workflow/SkillScopeCloser` drops every skill loaded into a phase scope on phase exit. A turn never has to inherit unused skill text from a previous task; the audit trail still has every load and every unload as a ledger node. — `internal/concern/skill_compactor.go`, `internal/workflow/skill_scope_closer.go`, `internal/skilltracker/`. **Status: Done.**
- **Signed redaction events** — every redaction logged to the ledger is signed with an ed25519 keypair persisted under `<store-root>/redactions/sign-{priv,pub}.pem`. The dashboard side panel reads via `Store.RedactionsForVerified` and renders a `Verified` flag (or a `legacy unsigned` / `tampered` warning) on each entry. The signature is over a canonical form that excludes the signature itself but includes the public-key fingerprint, so swapping the signer can't reattribute a record. — `internal/ledger/redact_sign.go`, `internal/ledger/redact_log.go`. **Status: Done.**
- **Release-rehearsal CI** — a Cloud Build E2E lane runs the full Playwright + axe-core flow against a freshly-built `r1-server` on every push to `main` and on every `v*` tag. A manual GitHub Actions workflow (`.github/workflows/e2e-rehearsal-manual.yml`) calls `gcloud builds triggers run` so an operator can fire a rehearsal from the Actions UI without local `gcloud`. Red blocks the release. — `services/cloudbuild-e2e-trigger.yaml`, `services/cloudbuild-e2e.yaml`, `scripts/setup-cloudbuild-e2e-trigger.sh`. **Status: Done.**
- **Tracebundle v2 export format** — `GET /api/session/{id}/export.tracebundle` ships per-session-filtered chain nodes + edges + a deterministic `chain_root_hash`. Three new ledger surfaces back this: `Store.ListNodesForSession`, `Store.ListEdgesForSession`, `Store.ChainRootHashForSession`, plus `ledger.CanonicalManifestSignBody` for downstream verifiers. The bundle becomes the on-disk audit artifact for offline / compliance review. (Spec D — D-UI2-7 — removed the originally-paired `R1_SERVER_UI_V2` envelope gate.) — `internal/ledger/store_session.go`, `cmd/r1-server/tracebundle_source.go`. **Status: Done.**

Together these mean: less prompt budget burned on stale skills, a cryptographic chain-of-custody on every redaction, an automated release gate that fails *before* a bad tag promotes, and a portable per-session export every auditor can verify without access to the live ledger.

## Why r1?

Most coding agents *say* they verify. r1 makes verification load-bearing:

- **The harness is the product, not the model.** r1 is provider-agnostic; automatic fallback today resolves Claude → Codex, with OpenRouter / direct API / lint-only tiers router-defined but not yet wired as execution runners. The differentiator is the loop: descent verification, honesty gates, content-addressed ledger, anti-truncation enforcement.
- **Refuses to truncate itself.** When the model says "good enough" or "I'll defer this to a follow-up," seven independent layers of regex + scope-completion gates + supervisor rules + post-commit hooks refuse `end_turn` while real plan items are unchecked. The model can't override this from inside; only the operator can, with an explicit flag.
- **Parallel cognition without subagent isolation.** Six specialist Lobes run alongside the main thread, share the same context window via a 1-hour cache breakpoint, and publish typed Notes into a shared Workspace. Mid-turn user input invokes a Haiku-driven Router that decides whether to interrupt, steer, queue, or just chat.
- **One wire, many surfaces.** The same per-user `r1 serve` daemon (Watchman pattern; one process, N session goroutines) backs the TUI, the web app at `platform.r1.run`, the Tauri desktop, and any external MCP-aware agent.

## Quick start

### Install (CLI / daemon)

```bash
# From source (Go 1.25+, CGO for SQLite)
git clone https://github.com/RelayOne/r1-agent && cd r1-agent
go build ./cmd/r1
sudo mv r1 /usr/local/bin/

# Or hosted binary CDN (prod channel; populated by the publish-r1-channel
# CI step in cloudbuild.yaml on push to main)
curl -fsSL https://downloads.r1.run/prod/r1-$(uname -s | tr A-Z a-z)-$(uname -m | sed s/x86_64/amd64/) -o r1
chmod +x r1 && sudo mv r1 /usr/local/bin/

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
                                      # mission, worktree, bus, verify, TUI, web, cli)
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
- **Model fallback + subscription pool.** Automatic fallback today resolves Claude → Codex (the wired execution runners); OpenRouter / direct API / Ember / lint-only are defined in `internal/model/` routing but not yet wired as workflow runners (`isAvailable` hardcodes them unavailable). Subscription pool, circuit breaker, OAuth poller, cost-aware resolver are live. — `internal/model/`, `internal/subscriptions/`. **Status: Partial — Claude/Codex Done; remaining tiers Scoped.**

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
- **Web** (`web/` — React 18 + Vite 6 + Tailwind 3 + shadcn/ui + zustand 5 + react-router 7 + Streamdown + `@ai-sdk/react`): Cursor 3 "Glass" three-column shell. Streaming markdown via `vercel/streamdown`. Tool / reasoning / plan / diff cards. Tile mode pins 2-4 lanes into the center pane with HTML5 drag-reorder + `Cmd+Shift+←/→` keyboard alternative. WS subprotocol-token auth; reconnect with `Last-Event-ID`; 10-attempt hard cap then `<ConnectionLostBanner>`. CSP locked to loopback. Coverage manifest enforces sibling-tests on every component. **Status: Partial.** The client is complete (55/55 spec items) and the daemon now serves its wire contract — `/ws` typed-frame bridge (`{type:chat|interrupt|subscribe|…}` ↔ `session.send` / `session.interrupt` / journal-replay subscriptions), `POST /auth/ws-ticket`, and the read-only `GET /v1/sessions/{id}/sse` bridge (audits A008/A069). Remaining gap: `r1 serve` does not yet drive a session agent loop, so chat turns are only accepted while a session `Run` is active — until that execution glue lands, sends outside a live run return an honest error envelope. — `web/`, `internal/server/ws/webbridge.go`, `internal/server/sse/`.
- **Desktop** (`desktop/` — Tauri 2 augmentation): keeps the existing 12-phase R1D shell. Adds discovery-or-spawn (probes `~/.r1/daemon.json`, falls back to bundled `r1` via `ShellExt::sidecar`). Per-session workdir via `tauri-plugin-store`. Per-session `tauri::ipc::Channel<LaneEvent>` at 10 Hz. `Cmd+\` pops a lane into its own `WebviewWindow`. Component sharing via npm workspace `packages/web-components/`. **Status: Partial.** The shell/UI scaffolding is present, but streamed session execution is still simulated in places and several advertised IPC verbs remain UI-only. — `desktop/`, `packages/web-components/`.

### r1d daemon — one process, N sessions (spec 5)
- **Watchman pattern**: per-user singleton, spawn-on-demand. `gofrs/flock` on `~/.r1/daemon.lock` enforces single-instance.
- **Discovery**: `~/.r1/daemon.json` (mode 0600, 32-byte hex token rotated on every start).
- **IPC**: unix socket / Windows named pipe for CLI (peer-cred check; no token); loopback HTTP+WS for browsers and desktop (Origin pin + Host pin + WS subprotocol token + 256-bit Bearer).
- **Multi-session**: N concurrent sessions as goroutines, each bound to a workdir via `cmd.Dir`. **Pre-multisession `os.Chdir` audit lint** is the mandatory gate (one stray `os.Chdir` would silently leak workdir across goroutines).
- **Journal-first**: each session writes `journal.ndjson` under `<workdir>/.r1/sessions/<id>/`; daemon restart replays the journal and emits `daemon.reloaded` to reconnecting clients.
- **`r1 serve --install`** opts into a per-OS service unit (launchd / systemd-user / Windows SCM).
- **Status: Done.** — `internal/server/`, `internal/daemonlock/`, `internal/daemondisco/`, `internal/serviceunit/`.

### Agentic test harness — every UI action is a tool (spec 8)
- **38-tool MCP catalog** across 10 categories (sessions, lanes, cortex, mission, worktree, bus, verify, TUI, web, cli). **Status: Done.**
- **Slack-style envelope** + `internal/stokerr/` 10-code error taxonomy at every wire boundary. No raw Go errors leak.
- **TUI shim** (`internal/tui/teatest_shim.go`) drives Bubble Tea via MCP without a terminal emulator. Synthetic `A11yEmitter` + JSONPath evaluator for structural assertions.
- **Gherkin-flavored markdown** (`*.agent.feature.md`) parsed + dispatched by `tools/agent-feature-runner/`. 8 seed feature fixtures across all 10 categories.
- **`lint-view-without-api`** scanner — UI without an MCP equivalent is a build break. — `tools/lint-view-without-api/`.
- **`make agent-features`, `make lint-views`, `make docs-agentic`** — one-line CI/local recipes.
- See [`docs/AGENTIC-API.md`](docs/AGENTIC-API.md) for the full external-agent contract.

### Anti-truncation enforcement (spec 9)
- **Seven independently-effective layers**, each enough to refuse self-truncation on its own. The model cannot bypass them from inside the conversation; only the operator can, via `--no-antitrunc-enforce`.
- **Layer 1**: regex catalog (`internal/antitrunc/phrases.go`) — 14 truncation + false-completion patterns.
- **Layer 2**: scope-completion gate (`internal/antitrunc/gate.go`) — refuses `end_turn` while plan or spec items are unchecked.
- **Layer 3**: cortex Lobe (`internal/cortex/lobes/antitrunc/`) — publishes `critical` Workspace Notes that block `end_turn`.
- **Layer 4**: supervisor rules (`internal/supervisor/rules/antitrunc/`) — `truncation_phrase_detected`, `scope_underdelivery`, `subagent_summary_truncation`.
- **Layer 5**: agentloop wiring (`internal/agentloop/antitrunc.go`) — gate composes BEFORE all other end-turn hooks.
- **Layer 6**: post-commit git hook (`scripts/git-hooks/post-commit-antitrunc.sh`) — observes false-completion phrases in commit bodies.
- **Layer 7**: CLI + MCP tool (`r1 antitrunc verify`, MCP tool `stoke_antitrunc_verify` / canonical alias `r1_antitrunc_verify`) — cross-checks recent commit "task N done" claims against the actual checklist; exits non-zero on `lying_count > 0`.
- **Soak**: 1,000,000-iteration soak run shows 0 false positives, 0 false negatives, 499K true positives at 16,891 iter/sec.
- **Status: Done.** Full guide: [`docs/ANTI-TRUNCATION.md`](docs/ANTI-TRUNCATION.md).

### Skill lifecycle + compaction (final sweep)
- **`SkillCompactor` with pluggable `EvictionPolicy`** (default `LRUPolicy`): when current-tokens > budget, picks oldest-loaded skills until the freed token count covers the overrun. Calls `skilltracker.Tracker.EvictByCompactor`, which emits one `SkillUnloaded` ledger node per evicted skill with `Reason="compactor"`. — `internal/concern/skill_compactor.go`.
- **`SkillScopeCloser.OnPhaseExit`** is the workflow phase-machine hook that calls `skilltracker.Tracker.CloseScope` for `(stanceID, taskScope)` when a phase exits — normal completion or abort. Each closed skill emits `SkillUnloaded` with `Reason="scope_exit"`. Idempotent: re-firing on the same scope drops zero. — `internal/workflow/skill_scope_closer.go`.
- **Status: Done** — `internal/concern/skill_compactor_test.go` + `internal/workflow/skill_scope_closer_test.go` cover budget no-op, LRU ordering, scope no-op, and ledger-emission paths.

### Signed redaction events (final sweep)
- **ed25519 signing on every `Store.RedactAndLog`**: the keypair lives at `<store-root>/redactions/sign-priv.pem` (mode 0600) + `sign-pub.pem` (0644), generated on first call via `LoadOrGenerateSigningKey`. The signer field is a 12-char hex prefix of `sha256(pub)` so multiple keys can co-exist across rotations. — `internal/ledger/redact_sign.go`.
- **Canonical form** signs over `{node_id, redacted_at, reason, signer}` — excludes the signature itself, includes the signer fingerprint. Tamper attempts that swap any field or rewrite the signer fail `VerifyRecord` with `ErrSignatureMismatch`. Legacy entries (pre-spec) return `ErrUnsigned` instead, distinguishing "tampered" from "old".
- **`Store.RedactionsForVerified`** returns a `[]VerifiedRedactionEvent` carrying a `Verified` bool + a `VerifyErr` string for the dashboard side panel.
- **Status: Done** — `internal/ledger/redact_sign_test.go` covers fresh-key generation, persistence-survives-restart, sign-then-verify roundtrip, tampered-record detection, signer-swap detection, and missing-key fallback.

### Tracebundle v2 export (final sweep)
- **Per-session filtering** (`internal/ledger/store_session.go`): `Store.ListNodesForSession(sessionID)` filters by `Node.MissionID`; `Store.ListEdgesForSession` filters by `Edge.Metadata["session_id"]` (edges without that key are conservatively kept). Empty `sessionID` falls back to the unfiltered listing for backward-compatible callers.
- **Chain-root hash** (`Store.ChainRootHashForSession`) computes `sha256(prev || node_id || content_commitment)` over the session's nodes sorted by `(CreatedAt, ID)`. The final hex is the bundle's tamper-evident root; downstream verifiers can recompute without reloading the ledger.
- **Canonical manifest** (`ledger.CanonicalManifestSignBody`) returns the deterministic byte-body the manifest signs over (everything except `signature_hex`), so cmd/r1-server's sign + verify paths and out-of-tree verifiers produce the same input.
- **Production source** (`cmd/r1-server/tracebundle_source.go`) wires the per-session API into `GET /api/session/{id}/export.tracebundle` and serves the full bundle (chain + edges + content + manifest). Spec D — D-UI2-7 — removed the originally-paired `R1_SERVER_UI_V2` envelope gate; the route is always reachable.
- **Status: Done** — `internal/ledger/store_session_test.go` covers per-session filter, chain-root determinism, empty-session edge cases, and canonical-manifest stability.

### Release-rehearsal E2E lane (final sweep)
- **Cloud Build trigger pair** (`services/cloudbuild-e2e-trigger.yaml`): `r1-agent-e2e-rehearsal-main` fires on every push to `main` (post-deploy verification); `r1-agent-e2e-rehearsal-tag` fires on `^v.*$` tags (release gate — red blocks tag promotion). Both call `services/cloudbuild-e2e.yaml`, which builds `r1-server`, installs Playwright + chromium, runs `go test -tags=e2e ./cmd/r1-server/e2e/...` with `R1_SERVER_UI_V2=1 R1_SERVER_SHARE_ENABLED=1` (the server ignores `R1_SERVER_UI_V2` post-Spec-D, but the e2e harness still uses it as its opt-in run/skip gate — without it the suite skips and the pipeline would post false green), and posts the green/red commit status.
- **Manual GitHub Actions workflow** (`.github/workflows/e2e-rehearsal-manual.yml`): operator clicks Run-workflow, picks a branch, the runner authenticates to GCP via `secrets.GCP_SA_JSON` and calls `gcloud builds triggers run r1-agent-e2e-rehearsal-main --branch=$BRANCH`. The workflow summary links straight to the Cloud Build console.
- **One-time setup** is `scripts/setup-cloudbuild-e2e-trigger.sh` (idempotent — re-running updates triggers in place).
- **Status: Done.** Full operations details: [`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md) §Release-rehearsal lane.

### MCP IDE bundles — Cursor / Windsurf / VS Code / JetBrains (C4)
- **`r1 ide install <cursor|windsurf|vscode|jetbrains>`** auto-registers R1's stdio MCP server with the target IDE. Per-IDE installers under `internal/ideinstall/` resolve the right config file on the right platform, then atomically merge the R1 stanza (backup-before-write, restore-on-uninstall).
- **VS Code** uses root key `servers` (not `mcpServers`); R1 stanza adds `"type": "stdio"` because Copilot Agent requires explicit transport. Reminder line tells operators to switch the Copilot panel to "Agent" mode.
- **JetBrains** receives a bundled `r1-mcp-bridge.jar` (built from `ide/jetbrains/`) — the plugin spawns `r1 mcp serve` and proxies MCP traffic to the JetBrains AI Assistant. Signing flow + CI keys documented in `docs/integrations/jetbrains-plugin-signing.md`.
- **`r1 chat` first-run prompt** asks once per user account whether to install R1 into the detected unregistered IDE; respects non-TTY stdin; ack stored in `~/.r1/ide-prompt-acked` so it never re-prompts.
- **`r1 ide verify`** prints a stable pipe-aligned table covering all four IDEs in cursor / windsurf / vscode / jetbrains order. Exit 0 always — verify is a report.
- Spec: [`specs/mcp-ide-bundles.md`](specs/mcp-ide-bundles.md). Quickstart: [`docs/integrations/ide-bundles.md`](docs/integrations/ide-bundles.md).
- **Status: Done.** — `cmd/r1/ide_install_cmd.go`, `internal/ideinstall/`, `ide/jetbrains/`.

### Hosted SaaS surfaces on r1.run
- **`platform.{,staging.,dev.}r1.run`** — docs site rendered from `docs/` via `r1-docs` Cloud Run service.
- **`api.{,staging.,dev.}r1.run`** — `r1-coord-api` Cloud Run service: `/healthz`, `/v1/version`, `/v1/license/verify`, `/v1/telemetry/opt-in`. Backed by Cloud SQL `r1-{prod,staging,dev}-pg`.
- **`downloads.{,staging.,dev.}r1.run`** — `r1-downloads-cdn` Cloud Run service streaming binaries from `gs://relayone-488319-r1-releases/{prod,staging,dev}/<asset>`.
- **`admin.{,staging.,dev.}r1.run`** — `r1-admin` Cloud Run service. The public surface is live; local JWT operator-role verification and runtime/coord-api summary are real, but broader business/session/user data surfaces are still partial.
- **All 12 public services are live on Cloud Run us-central1** with min-instances=1, instance-based billing, distroless static images, and `/livez` endpoints.
- **`r1-browser`** is documented and buildable, but it was not present as a live Cloud Run service during the 2026-05-15 audit.
- **Auto-deploy** via `services/cloudbuild-deploy.yaml` — push to `main` rebuilds + redeploys prod; `staging` and `dev` branches deploy to their respective envs.
- **Status: Live.** DNS and domain mappings are already in place. Operations runbook: [`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md).

## Project status

| Area | Status | What it means |
|---|---|---|
| Final-sweep PRs #168 / #169 / #170 / #171 | **Done — merged to main (commit `242af4a8`)** | Skill-aware compactor + signed-redaction + release-rehearsal CI + tracebundle v2 export. |
| Specs 1-9 (cortex / lanes / multi-surface / agentic / anti-trunc) | **Done — merged to main** | Specs 6/7/8/9 + r1.run SaaS shipped via PRs #128 / #143 / #150 / #151. |
| 12 Cloud Run SaaS surfaces (4 services × 3 envs) | **Live — 12/12 HTTPS-200 on /livez** | dev/staging/prod for r1-coord-api, r1-docs, r1-downloads-cdn, r1-admin. All r1.run subdomains resolve. |
| Cloud SQL (r1-{prod,staging,dev}-pg, POSTGRES_16) | **Live** | All RUNNABLE; DSN secrets in Secret Manager + bound via `--add-cloudsql-instances`. |
| `go build`/`vet`/`test` | **All green** | Full `go list ./...` set green (281 packages at last count); sequential test suite passes; race-detector clean on the bus. |
| Web + desktop + web-components vitest | **All green** | 295 tests pass (web 212 + components 19 + desktop 64). React 19 + jsdom 26 stack. |
| JWT login + RelayOne MSP SSO | **Done — Path A Go reimpl** | `services/r1-coord-api/internal/auth/{jwt,sso,middleware}.go`; HS256 + RS256; OIDC code flow. |
| Admin panel (admin.r1.run) | **Partial** | `services/r1-admin/main.go` — live hosted surface; operator JWT verification and runtime summary are real, but major business/session/user data sections are still partial. |
| PostHog + Customer.io + CodeRadar tracking | **Partial** | Client/subscriber code exists, and hosted `coord-api` now emits CodeRadar telemetry opt-in + attribution events when `CODERADAR_DSN` is present, but the broader GTM, lifecycle, and marketing-site rollout is not fully live. |
| Nightly benchmark cron | **Done** | `services/cloudbuild-bench-nightly.yaml` + Cloud Scheduler `r1-bench-nightly-cron` (04:00 UTC daily); reports upload to `gs://relayone-488319-r1-bench-reports/<date>/`. |
| Spec 7 desktop daemon discovery wiring | **Done** | `desktop/src-tauri/src/discovery_state.rs` — Tauri-managed state + `app.discovery_status` IPC verb. |
| `r1-server-ui-v2` retrofit (61 items) | **Scoped — 5 sub-specs ready for /build** | foundation + 3d-perf + event-rendering + handlers-and-routes + tests. Build order: foundation → (3d-perf, event-rendering, handlers-and-routes parallel) → tests. |
| Node 22 LTS CI bump | **Scoped (precursor to UI v2 retrofit)** | Node 20 LTS EOL 2026-04-30; bump unblocks jsdom 29 + vitest 4 + vite 7. Single small CI-only PR. |
| Marketing site + affiliate / SEO / CRO / attribution | **On horizon** | Multi-week marketing-engineering effort; deferred. |

## Documentation

| Doc | Audience | What it covers |
|---|---|---|
| [`docs/README.md`](docs/README.md) | Everyone | Mirror of this file. |
| [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) | Engineers | Tech stack, repo map (251 internal packages), system components, data models, API surface, infrastructure, testing architecture. |
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

These commands are the gate. They must be green on every PR. CI also runs `-race`, `golangci-lint` (advisory), `govulncheck`, and `gosec`. `make check-pkg-count` is a local drift guard (not wired into CI); run it when adding or removing internal packages.

## Environments

This repo follows the portfolio env-branch convention:

| Branch    | Environment | Service / target                                    |
|-----------|-------------|-----------------------------------------------------|
| `dev`     | dev         | `r1-{coord-api,docs,downloads-cdn,admin}-dev` (auto-deploy on push to `dev`) |
| `staging` | staging     | `r1-{coord-api,docs,downloads-cdn,admin}-staging` (auto-deploy on push to `staging`) |
| `main`    | prod        | `r1-{coord-api,docs,downloads-cdn,admin}-prod` (auto-deploy on push to `main`) |

**Branch protection on `main` and `staging`:** PR-required, no force-push, no delete, required check is `r1-agent-pr (relayone-488319)`. `dev` allows direct commits.

**Forward-merge flow:** `dev` → `staging` → `main`. Direct pushes to `main`/`staging` are blocked by branch protection.

**Per-env data isolation:** Each env has its own Secret Manager entries (`r1-<env>-shared-<VAR>`), Cloud SQL Postgres instance (`r1-<env>-pg`), and Cloud Run service revisions. See [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md) for the full operator runbook.

## Operations

After PR #128 merges:

```bash
# 1. Create dev + staging branches and apply branch protection
./scripts/setup-branch-protection.sh

# 2. Wire 3 Cloud Build triggers (one per env)
./services/scripts/setup-cloudbuild-triggers.sh

# 3. Verify the 12 CNAMEs in Cloudflare (already live; see the CNAME table in docs/DEPLOYMENT.md)
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
