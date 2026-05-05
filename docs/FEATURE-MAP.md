# Feature Map

Complete feature inventory for r1 as of 2026-05-04. Status reflects the merged state of specs 1-9 + the 9 deployed Cloud Run SaaS services.

## Mission Runtime

| Feature | Benefit | Status | Reference |
|---|---|---|---|
| Plan / execute / verify / review loop | One strong implementer + adversarial cross-model reviewer is more reliable than loose multi-agent consensus | Done | `internal/app/`, `internal/workflow/`, `internal/mission/`, `internal/verify/`, `internal/critic/`, `internal/convergence/` |
| Adversarial review posture | Refuses to call work "done" without evidence; gates on AC + git state + tool-call log | Done | `internal/critic/`, `internal/convergence/`, `internal/engine/` |
| Content-addressed evidence model | Every node has `sha256:<hex>` content ID; survives daemon restart via WAL replay | Done | `internal/ledger/`, `internal/bus/`, `internal/session/` |
| Five-provider model fallback | Provider-agnostic; degrades from Claude → Codex → OpenRouter → direct API → lint-only | Done | `internal/model/`, `internal/subscriptions/` |
| Cost-aware resolver + budget enforcement | Blocks turns when over-budget; per-task cost ticks journaled | Done | `internal/costtrack/`, `internal/model/CostAwareResolve` |
| Anti-truncation enforcement | Refuses end-turn while plan items unchecked or truncation phrases emitted; layered machine-mechanical defense against LLM self-reduction | Done | `internal/antitrunc/`, `internal/agentloop/antitrunc.go`, `internal/supervisor/rules/antitrunc/`, `cmd/r1/antitrunc_cmd.go`, `docs/ANTI-TRUNCATION.md` |

## Cortex — Parallel Cognition (specs 1, 2)

| Feature | Benefit | Status | Reference |
|---|---|---|---|
| Workspace + Lobe substrate | GWT-style shared mutable view; Lobes share full context with main thread (no subagent isolation) | Done | `internal/cortex/`, spec 1 |
| MemoryRecallLobe | Surfaces top-3 prior memory + wisdom hits as `info` Notes per round | Done | `internal/cortex/lobes/memoryrecall/` |
| WALKeeperLobe | Drains every hub event into durable WAL; survives daemon restart | Done | `internal/cortex/lobes/walkeeper/` |
| RuleCheckLobe | Maps supervisor-rule fires to Notes; `trust.*` and `consensus.dissent.*` are `critical` and refuse `end_turn` | Done | `internal/cortex/lobes/rulecheck/` |
| PlanUpdateLobe | Proposes `plan.json` deltas every 3rd turn or on action-verb input; auto-applies edits | Done | `internal/cortex/lobes/planupdate/` |
| ClarifyingQLobe | Drafts up to 3 clarifying questions when ambiguity is detected; surfaces at idle | Done | `internal/cortex/lobes/clarifyq/` |
| MemoryCuratorLobe | Extracts "should-remember" facts every 5th turn; privacy filter drops `private`-tagged messages | Done | `internal/cortex/lobes/memorycurator/` |
| AntiTruncLobe | Publishes `critical` Notes when truncation phrases or scope underdelivery detected | Done | `internal/cortex/lobes/antitrunc/`, spec 9 |
| Drop-partial interrupt | Cancellation atomic; never persist partial assistant messages | Done | `internal/cortex/interrupt.go` |
| Pre-warm cache pump | `max_tokens=1` warming request every 4 min; keeps Anthropic prompt-cache breakpoint hot (~50% prompt-cost savings) | Done | `internal/cortex/prewarm.go` |
| Workspace persistence + Replay | Notes written through to durable bus; restored on session resume | Done | `internal/cortex/persist.go` |
| Router (Haiku 4.5) | On mid-turn user input, picks between `interrupt`, `steer`, `queue_mission`, `just_chat` | Done | `internal/cortex/router.go` |

## Lanes — Cross-Surface UI Primitive (spec 3)

| Feature | Benefit | Status | Reference |
|---|---|---|---|
| Six lane event types | Universal wire format for every cognitive thread, tool call, mission task | Done | `internal/streamjson/lane.go` |
| Six-state FSM | `pending → running → blocked → done | errored | cancelled` + orthogonal `pinned` flag | Done | `internal/streamjson/lane.go` |
| Monotonic per-session `seq` | Single-writer goroutine; `seq=0` reserved for `session.bound` | Done | `internal/streamjson/lane.go` |
| ULID lane_id / event_id | Time-ordered + globally unique | Done | `oklog/ulid/v2` |
| 5 MCP tools | `r1.lanes.list/.subscribe/.get/.kill/.pin` make every lane action agent-driveable | Done | `internal/mcp/lanes_server.go` |
| HTTP+SSE endpoint `/v1/lanes/events` | Server-Sent Events with `Last-Event-ID` replay | Done | `internal/server/sse/` |
| WS upgrade `/v1/lanes/ws` | `Sec-WebSocket-Protocol: r1.lanes.v1, <token>` + Origin pinning | Done | `internal/server/ws/` |
| JSON-RPC 2.0 `session.subscribe` | WAL replay; ordered before live events | Done | `internal/server/jsonrpc/` |
| Backward-compat dual emit | `session.delta` co-emitted with `lane.delta` for the main lane during compat window | Done | spec 3 §Out of scope |
| Performance | 3 µs/event end-to-end; 2.3 µs/event with 5 subscribers (target 50/100 µs) | Done | `bench/lanes_bench_test.go` |

## TUI — Bubble Tea v2 (spec 4)

| Feature | Benefit | Status | Reference |
|---|---|---|---|
| Adaptive lane columns | Columns when `width >= n*32`, vertical stack otherwise | Done | `internal/tui/lanes/` |
| Focus mode 65/35 split | Primary lane + peers visible | Done | `internal/tui/lanes/` |
| 250 ms coalesce | Single fan-in `chan laneTickMsg`; ≤10 Hz visible rerender | Done | `internal/tui/lanes/runProducer` |
| Render-string cache | Diff-only repaint per lane | Done | `internal/tui/lanes/Model` |
| Keybindings | `1`–`9` jump-to-lane, `tab`/`shift-tab` cycle, `j`/`k` move, `enter` focus, `esc` exit, `x`+`y` kill, `K` kill-all, `?` help | Done | `internal/tui/lanes/keymap` |
| `--lanes` flag | Wired into `r1 chat-interactive` | Done | `cmd/r1/chat_interactive.go` |
| 72 tests `-race` clean | Catches lane FSM regressions | Done | `internal/tui/lanes/*_test.go` |

## Web UI — Cursor 3 Glass (spec 6)

| Feature | Benefit | Status | Reference |
|---|---|---|---|
| `<ThreeColumnShell>` | Sessions / Chat / Lanes layout with collapsible per-daemon rails | Done | `web/src/components/layout/ThreeColumnShell.tsx` |
| `<SessionList>` + `<SessionItem>` | Per-daemon session sidebar; status dots; relative-time | Done | `web/src/components/session/` |
| `<NewSessionDialog>` | zod-validated form for r1d.session.create | Done | `web/src/components/session/NewSessionDialog.tsx` |
| `<ChatPane>` | Swaps message column ↔ tile grid by pin state | Done | `web/src/components/chat/ChatPane.tsx` |
| `<MessageLog>` | `react-virtual`; sticky-bottom scroll; aria-live polite on streaming bubble | Done | `web/src/components/chat/MessageLog.tsx` |
| `<MessageBubble>` | Routes text/tool/reasoning/plan parts to specific cards | Done | `web/src/components/chat/MessageBubble.tsx` |
| `<ToolCard>` | Collapsible (default-collapsed once `output-available`); copy button | Done | `web/src/components/chat/ToolCard.tsx` |
| `<ReasoningCard>` | Dim collapsible; reduced-motion shimmer while streaming | Done | `web/src/components/chat/ReasoningCard.tsx` |
| `<PlanCard>` | Live-updating from PlanUpdateLobe; per-item testids | Done | `web/src/components/chat/PlanCard.tsx` |
| `<DiffCard>` | `react-diff-view`; consolidated per-lane diff | Done | `web/src/components/chat/DiffCard.tsx` |
| `<Composer>` | Cmd/Ctrl+Enter send; streaming-aware disable | Done | `web/src/components/chat/Composer.tsx` |
| `<StopButton>` | Swap with Send during streaming; sends `interrupt` envelope | Done | `web/src/components/chat/StopButton.tsx` |
| `<LanesSidebar>` + `<LaneRow>` | Right-rail lane index; Pin / Kill per lane | Done | `web/src/components/lanes/LanesSidebar.tsx` |
| `<LaneTile>` | Live render-string with sticky-bottom diff-only update | Done | `web/src/components/lanes/LaneTile.tsx` |
| `<TileGrid>` | 1 / 1×2 / 1×3 / 2×2 auto-layout; HTML5 drag + Cmd+Shift+Arrow keyboard reorder; per-tile collapse | Done | `web/src/components/lanes/TileGrid.tsx` |
| `<WorkdirBadge>` + `<WorkdirPickerDialog>` | FSA `showDirectoryPicker()` + IndexedDB persistence + manual fallback | Done | `web/src/components/workdir/` |
| `<StatusBar>` | Live connection / latency / cost / lane counts | Done | `web/src/components/StatusBar.tsx` |
| `<HighContrastToggle>` + Settings page | Theme + lane filters + keybindings cheat-sheet | Done | `web/src/components/settings/` |
| `<GlobalKeybindings>` | Cmd+1..9 daemon switch, Cmd+Shift+S toggle rail, `?` help, `/` focus, Esc stop | Done | `web/src/components/GlobalKeybindings.tsx` |
| `<ConnectionLostBanner>` | Hard-cap reconnect alert (10-attempt cap) | Done | `web/src/components/ConnectionLostBanner.tsx` |
| react-router v7 nested routes | `daemon → session → lane` deep-linkable URLs | Done | `web/src/routes/index.tsx` |
| ResilientSocket | 250 ms→8 s exponential backoff, jitter ±20%, 10-attempt cap, Last-Event-ID replay | Done | `web/src/lib/api/ws.ts` |
| AuthClient mintWsTicket | Cached ticket with skew-based refresh | Done | `web/src/lib/api/auth.ts` |
| zustand per-daemon store | One store instance per daemon connection; envelope coalescer at rAF | Done | `web/src/lib/store/daemonStore.ts` |
| Streamdown markdown | Partial-markdown handling; Shiki syntax highlighting; KaTeX math; Mermaid diagrams | Done | `web/src/lib/render/markdown.tsx` |
| Coverage manifest | Walks src/ at test time; fails if any source lacks sibling `.test.tsx` | Done | `web/src/test/coverage-manifest.test.ts` |
| Stories manifest | Same for `.stories.tsx` (component-only) | Done | `web/src/test/stories-manifest.test.ts` |
| Custom eslint rule require-data-testid | Every interactive JSX element must have `data-testid`; build-break otherwise | Done | `web/eslint-rules/require-data-testid.js` |
| CSP zero-violation enforcement | Playwright + axe-core gate on every route across chromium + firefox + webkit | Done | `web/src/test/e2e/csp-axe.spec.ts` |
| 9 `*.agent.feature.md` Playwright MCP flows | Spec 8 dependency: happy-path-chat, multi-instance-switch, lane-pin-tile-mode, interrupt-mid-stream, reconnect-replay, workdir-picker-fsa, deep-link-lane, a11y-keyboard-only, csp-no-violations | Done | `web/src/test/e2e/*.agent.feature.md` |

## Desktop — Tauri 2 Augmentation (spec 7)

| Feature | Benefit | Status | Reference |
|---|---|---|---|
| Discovery-or-spawn daemon transport | External `r1 serve` is primary; bundled-binary sidecar fallback on first run | Done | `desktop/src-tauri/src/discovery.rs` |
| `tauri-plugin-websocket` | Sidesteps Windows mixed-content block | Done | `desktop/src-tauri/Cargo.toml` |
| `tauri-plugin-store` | Per-session workdir (NOT localStorage) | Done | `desktop/src-tauri/src/sessionstore.rs` |
| `tauri::ipc::Channel<LaneEvent>` per session | High-frequency lane stream at 10 Hz; sidesteps global event bus | Done | `desktop/src-tauri/src/lanes.rs` |
| Lane pop-out via `Cmd+\` | Pops a lane into its own `WebviewWindow` | Done | `desktop/src-tauri/src/popout.rs` |
| Native menu bar | Per-OS native menu structure | Done | `desktop/src-tauri/src/menu.rs` |
| Auto-start option per OS | `tauri-plugin-autostart` | Done | `desktop/src-tauri/src/autostart.rs` |
| Component sharing | npm workspace `packages/web-components/` (shared with web) | Done | `packages/web-components/` |
| 110 cargo tests `-race` clean | Validates Rust host code | Done | `desktop/src-tauri/src/*_test.rs` |
| 4 Playwright e2e | multi-session, lanes-streaming, popout-lane, daemon-discovery | Done | `desktop/tests/agent/*.spec.ts` |

## r1d Daemon — One Process, N Sessions (spec 5)

| Feature | Benefit | Status | Reference |
|---|---|---|---|
| Watchman pattern (singleton on-demand) | Zero idle resource cost; spawn-on-demand from CLI / browser / desktop | Done | `internal/server/`, `cmd/r1/serve_cmd.go` |
| Single-instance via `gofrs/flock` | `~/.r1/daemon.lock` advisory lock | Done | `internal/daemonlock/` |
| Discovery file `~/.r1/daemon.json` | Atomic mode-0600 write + 32-byte hex token rotated on every start | Done | `internal/daemondisco/` |
| Unix socket / Windows named pipe | `$XDG_RUNTIME_DIR/r1/r1.sock` mode 0600 + peer-cred OR Windows SDDL granting current SID + LocalSystem | Done | `internal/server/ipc/` |
| Loopback HTTP+WS | `127.0.0.1:0` + Origin pin + Host pin + WS subprotocol token + 256-bit Bearer | Done | `internal/server/{http,ws}/` |
| Per-OS service unit | `kardianos/service` — launchd / systemd-user / Windows SCM | Done | `internal/serviceunit/` |
| `os.Chdir` audit + CI lint | Mandatory gate before multi-session enabled; one stray `os.Chdir` would silently leak workdir | Done | `tools/cmd/chdir-lint/`, `make lint-chdir` |
| Per-session journal | `<workdir>/.r1/sessions/<id>/journal.ndjson` (fsync on terminal events) | Done | `internal/journal/` |
| Sessions index | `~/.r1/sessions-index.json` atomic + fsync | Done | `internal/server/sessionhub/` |
| Daemon-restart replay | Replays journal → emits `daemon.reloaded` to reconnecting clients | Done | `internal/server/sessionhub/` |
| 22 JSON-RPC methods | session.start/pause/resume/cancel/send/subscribe/unsubscribe, lanes.list/kill, cortex.notes, daemon.info/shutdown/reload_config | Done | `internal/server/jsonrpc/` |
| Per-subscription monotonic seq | Replay-before-live ordering | Done | `internal/server/sessionhub/` |
| 22 tests `-race` clean | Multi-session × multi-workdir validation | Done | `internal/server/sessionhub/*_test.go` |
| Soak test | 50 sessions × 100 messages; 262 MB/s journal throughput; 852 µs p99 dispatch latency | Done | `bench/r1d_serve_bench_test.go` |
| `r1 serve --install/--uninstall/--status` | Opts into always-on operation | Done | `cmd/r1/serve_cmd.go` |

## Agentic Test Harness (spec 8)

| Feature | Benefit | Status | Reference |
|---|---|---|---|
| 38-tool MCP catalog across 10 categories | Every UI action reachable through MCP | Done | `internal/mcp/r1_server.go`, `r1_server_catalog.go` |
| Slack-style envelope | Predictable wire shape `{ok, data?, error_code?, error_message?, links?}` | Done | `internal/mcp/envelope.go` |
| `internal/stokerr/` 10-code taxonomy | No raw Go errors leak at the wire | Done | `internal/mcp/stokerr_map.go` |
| `r1 mcp serve --print-tools [--markdown]` | Lint + docs generator have a stable input | Done | `cmd/r1/mcp.go` |
| `internal/tui/teatest_shim.go` | Bubble Tea drivable through MCP without a terminal emulator | Done | `internal/tui/teatest_shim.go` |
| `A11yEmitter` + JSONPath evaluator | Synthetic a11y trees + structural assertions; `lipgloss.SetColorProfile(termenv.Ascii)` for byte determinism | Done | `internal/tui/a11y.go`, `internal/tui/jsonpath.go` |
| `*.agent.feature.md` parser + dispatcher | Gherkin-shaped tests dispatched via heuristics + per-file `## Tool mapping` blocks | Done | `tools/agent-feature-runner/` |
| 8 seed feature fixtures across 10 categories | Coverage gate per spec 8 §10 | Done | `tests/agent/{tui,web,cli,mission,worktree}/` |
| `lint-view-without-api` scanner | UI without API is a build break | Done | `tools/lint-view-without-api/` |
| Make targets | `make agent-features[-update,-drift-check]`, `make lint-views`, `make docs-agentic`, `make storybook-mcp-validate` | Done | `Makefile` |
| `docs/AGENTIC-API.md` + D-A1..D-A5 | External-agent contract + acceptance decisions | Done | `docs/AGENTIC-API.md`, `docs/decisions/index.md` |
| Auto-snapshot mitigation | Lint-drift mitigation per audit | Done | `tools/lint-view-without-api/snapshot.go` |

## Anti-Truncation Enforcement (spec 9)

| Feature | Benefit | Status | Reference |
|---|---|---|---|
| Layer 1: Phrase regex catalog | 14 truncation + false-completion patterns, hand-tuned against legitimate-text corpus | Done | `internal/antitrunc/phrases.go` |
| Layer 2: Scope-completion gate | Refuses end_turn while plan or in-progress spec items unchecked | Done | `internal/antitrunc/gate.go`, `internal/antitrunc/scopecheck.go` |
| Layer 3: AntiTruncLobe | Publishes `critical` Workspace Notes that block end_turn | Done | `internal/cortex/lobes/antitrunc/` |
| Layer 4: Supervisor rules | `truncation_phrase_detected`, `scope_underdelivery`, `subagent_summary_truncation` | Done | `internal/supervisor/rules/antitrunc/` |
| Layer 5: agentloop wiring | Gate composes BEFORE all other end-turn hooks | Done | `internal/agentloop/antitrunc.go` |
| Layer 6: post-commit git hook | Observes false-completion phrases in commit bodies; writes `audit/antitrunc/post-commit-<sha>.md` | Done | `scripts/git-hooks/post-commit-antitrunc.sh` |
| Layer 7: CLI + MCP tool | `r1 antitrunc verify -n N` + `r1.antitrunc.verify` MCP tool; classifies commits Verified / Unverified / Lying; exits non-zero on lying | Done | `cmd/r1/antitrunc_cmd.go`, `internal/mcp/r1_server.go` |
| `r1 antitrunc tail` | Streams audit/antitrunc/ in real time | Done | `cmd/r1/antitrunc_cmd.go` |
| 1M-iteration soak | 0 FP / 0 FN / 499K TP at 16,891 iter/sec | Done | `internal/antitrunc/soak_extended_test.go` (build tag `soak`) |
| Cortex-mission integration test | `TestMissionIntegration_GateRefusesAndForcesContinuation` end-to-end | Done | `internal/cortex/lobes/antitrunc/integration_test.go` |
| `docs/ANTI-TRUNCATION.md` | Operator guide; documents override path | Done | `docs/ANTI-TRUNCATION.md` |

## Hosted SaaS — `r1.run` (this session)

| Feature | Benefit | Status | Reference |
|---|---|---|---|
| `services/r1-coord-api/` Go service | License-verify + telemetry-opt-in scaffold; Cloud SQL backed | Done (stubs; real auth pending Path-A Go port) | `services/r1-coord-api/main.go` |
| `services/r1-docs/` Go service | Embeds docs/*.md; renders to HTML; CSP-locked | Done | `services/r1-docs/main.go` |
| `services/r1-downloads-cdn/` Go service | Streams gs://relayone-488319-r1-releases/{env}/ via service account | Done | `services/r1-downloads-cdn/main.go` |
| 9 Cloud Run services | dev/staging/prod for each of the 3 services; min-instances=1; instance billing; distroless static | Live | gcloud run services list |
| 3 Cloud SQL Postgres 16 instances | r1-{prod,staging,dev}-pg, all RUNNABLE | Live | gcloud sql instances list |
| Artifact Registry repo | us-central1-docker.pkg.dev/relayone-488319/r1 | Live | gcloud artifacts repositories list |
| 6 Secret Manager placeholders | r1-{prod,staging,dev}-shared-{DATABASE_URL,ANTHROPIC_API_KEY} (operator must populate) | Pending real values | gcloud secrets list |
| 9 domain mappings | platform/api/downloads × dev/staging/prod under r1.run | Created (DNS pending) | gcloud beta run domain-mappings list |
| `services/cloudbuild-deploy.yaml` auto-deploy | Build + push + deploy + smoke /livez on push to main/staging/dev | Done | services/cloudbuild-deploy.yaml |
| `services/scripts/setup-cloudbuild-triggers.sh` | Operator script to create the 3 deploy triggers | Done | services/scripts/setup-cloudbuild-triggers.sh |
| `services/deploy.sh` | Manual deploy: `./services/deploy.sh {dev|staging|prod|all}` | Done | services/deploy.sh |
| `scripts/setup-branch-protection.sh` | Operator script: dev + staging branch creation + protection rules | Done | scripts/setup-branch-protection.sh |

## Deterministic Skills

| Feature | Benefit | Status | Reference |
|---|---|---|---|
| Skill manufacturing pipeline | Turns reusable workflows into governed artifacts | Done | `internal/skillmfr/` |
| Registry + selection | Maps runtime behavior to explicit skill assets | Done | `internal/skill/`, `internal/skillselect/` |
| `r1 skills pack init/info/install/list/publish/search/sign/verify/update/serve` | Full pack lifecycle | Done | `cmd/r1/skills_pack_cmd.go` |
| HTTP pack registry | `r1 skills pack serve` exposes published packs | Done | `cmd/r1/skills_pack_server.go` |
| Signed-pack runtime verification | Prevents runtime registration from ignoring pack integrity | Done | `internal/skill/verify.go` |

## Status

### Done
- Specs 1-9 — all 171/172 items merged + tested + deployed
- 9 Cloud Run SaaS services live + Cloud SQL + Secret Manager + Artifact Registry + domain mappings created
- Anti-truncation 7-layer defense + 1M-iter soak (0 FP / 0 FN)
- Branch hygiene: 20 archive tags, repo cleaned to 2 active branches
- Documentation: this doc + 6 sibling docs + 9 spec docs + decisions log
- All Go tests + web typecheck + desktop tests green

### In Progress
- DNS propagation for the 9 r1.run subdomains (operator action: add Cloudflare CNAMEs)
- Operator follow-ups: secret values, CLAUDE.md package map line, Cloud Build trigger creation

### Scoped
- JWT login + RelayOne MSP SSO (Path A — Go reimpl of @relayone/auth-core JwtService + RelayOneSsoClient)
- Admin panel at admin.r1.run (clone *-admin template + customize for r1 routes)
- PostHog product analytics integration
- Customer.io retention + lifecycle email integration
- CodeRadar dogfood event streaming (already in-house; just turn on per env)

### Scoping
- Cross-machine session migration
- Encryption-at-rest for journals
- Per-tool throttling policy

### Potential — On Horizon
- Marketing site with affiliate / SEO / CRO / attribution / retention stack
- BitBucket Pipelines adapter parity with GitLab CI / GitHub Actions
- Browser tool sandboxed under remote browser
- Cross-product deterministic skill exchange
- Native MCP server bundle for popular IDEs without separate install
