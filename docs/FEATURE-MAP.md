# Feature Map

Complete feature inventory for r1 as of 2026-05-06. Status reflects the merged state of specs 1-9 + the 9 deployed Cloud Run SaaS services + the four final-sweep PRs (#168/#169/#170/#171, sync to `main` in commit `242af4a8`).

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
| Make targets | `make agent-features[-update,-drift-check]`, `make lint-views`, `make docs-agentic` | Done | `Makefile` |
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

## Final Sweep — Skill Lifecycle, Signed Redaction, Tracebundle v2, Release-Rehearsal CI (PRs #168 / #169 / #170 / #171)

| Feature | Benefit | Status | Reference |
|---|---|---|---|
| `concern.SkillCompactor` with `EvictionPolicy` | A turn never inherits stale skill text from a previous task; LRU drop frees only as much budget as needed; every eviction lands as a `SkillUnloaded` ledger node so the audit chain stays complete | Done | `internal/concern/skill_compactor.go`, `internal/skilltracker/tracker.go` (`EvictByCompactor`) |
| `concern.LRUPolicy` (default) | Drops oldest-loaded skills first; stops as soon as freed tokens cover the budget overrun; tokens=0 entries are last-resort | Done | `internal/concern/skill_compactor.go` |
| `workflow.SkillScopeCloser.OnPhaseExit` | Phase boundary — normal completion *or* abort — drops every skill loaded into the (stance, task) scope and emits `SkillUnloaded(reason="scope_exit")` per drop; idempotent | Done | `internal/workflow/skill_scope_closer.go`, `internal/skilltracker/tracker.go` (`CloseScope`) |
| Signed redaction events (ed25519) | Every redaction logged to the ledger carries a tamper-evident signature; `Store.RedactionsForVerified` returns a `Verified` bool so the dashboard side panel renders "tampered" / "legacy unsigned" overlays distinctly; signer-swap attacks fail because the public-key fingerprint is part of the canonical signing form | Done | `internal/ledger/redact_sign.go`, `internal/ledger/redact_log.go` (`SignedRedactionEvent`) |
| `LoadOrGenerateSigningKey` | First call generates the keypair under `<store-root>/redactions/sign-{priv,pub}.pem` (modes 0600 / 0644); subsequent calls reuse the persisted private; pub auto-restored from priv if missing | Done | `internal/ledger/redact_sign.go` |
| `SignRecord` / `VerifyRecord` / `ErrSignatureMismatch` / `ErrUnsigned` | Distinct errors for the dashboard so "signature mismatch" renders red and "legacy unsigned entry" renders gray | Done | `internal/ledger/redact_sign.go` |
| Tracebundle v2 — per-session filtering | `Store.ListNodesForSession(sid)` filters by `MissionID`; `Store.ListEdgesForSession(sid)` filters by `Edge.Metadata["session_id"]`; bundle exports become single-session-scoped instead of dumping the entire ledger | Done | `internal/ledger/store_session.go` |
| Tracebundle v2 — chain-root hash | `Store.ChainRootHashForSession(sid)` returns a deterministic SHA256 chain over `(prev_hash || node_id || content_commitment)` sorted by `(CreatedAt, ID)`; downstream verifiers can recompute without reloading the ledger | Done | `internal/ledger/store_session.go` |
| Tracebundle v2 — canonical manifest signing body | `ledger.CanonicalManifestSignBody(format, version, sessionID, chainRootHash, generatedAt, signer)` returns deterministic bytes; cmd/r1-server's sign + verify paths and out-of-tree auditors share the same canonical input | Done | `internal/ledger/store_session.go` |
| Production tracebundle adapter | `cmd/r1-server/tracebundle_source.go` is the production source for `GET /api/session/{id}/export.tracebundle`. Reads `LedgerDir` from the DB session row, opens the store, returns a `serveTracebundle` writer. (Spec D — D-UI2-7 — removed the prior `R1_SERVER_UI_V2` envelope gate.) | Done | `cmd/r1-server/tracebundle_source.go` |
| Release-rehearsal Cloud Build trigger (push-to-main) | `r1-agent-e2e-rehearsal-main` fires on every push to `main` and runs the full Playwright + axe-core E2E flow against a freshly-built `r1-server`; red blocks any release that gates on this check | Done | `services/cloudbuild-e2e-trigger.yaml`, `services/cloudbuild-e2e.yaml` |
| Release-rehearsal Cloud Build trigger (tag) | `r1-agent-e2e-rehearsal-tag` fires on `^v.*$` tag pushes; same flow; blocks tag promotion when red | Done | `services/cloudbuild-e2e-trigger.yaml` |
| Manual GitHub Actions rehearsal | `.github/workflows/e2e-rehearsal-manual.yml` lets an operator dispatch the rehearsal from the Actions UI; calls `gcloud builds triggers run` against the main-branch trigger; workflow summary links to the Cloud Build console | Done | `.github/workflows/e2e-rehearsal-manual.yml` |
| One-time trigger setup script | `scripts/setup-cloudbuild-e2e-trigger.sh` is idempotent — re-running updates triggers in place; requires `roles/cloudbuild.builds.editor` on `relayone-488319` | Done | `scripts/setup-cloudbuild-e2e-trigger.sh` |

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
| Federated v2 pack format (C7) | `manifest.v2.json` with `compat` matrix, runtime_assertions, consumer_hooks. v1 packs auto-upgrade to v2 at load time | Done | `internal/skill/manifest_v2.go` |
| Cross-product runtime adapters (C7) | Adapt v2 manifest to CloudSwarm/Heroa/Veritize/R1 wrappers | Done | `internal/skill/compat/` |
| `r1 skills pack adopt --pack <id> --for <product>` (C7) | Writes target-product wrapper + signed `pack.adopted` ledger event | Done | `cmd/r1/pack_adopt_cmd.go` |
| Federated ed25519 trust root (C7) | Per-publisher kids with not_before/not_after/scopes; signed document via root operator key | Done | `internal/skill/trustroot.go` |
| `/v2/packs`, `/v2/trust-root`, `X-R1-Registry-Sig` (C7) | HTTPS + per-IP rate limit + response signing | Done | `cmd/r1/skills_pack_server_v2.go` |

## Agentic Test Harness

| Feature | Benefit | Status | Reference |
|---|---|---|---|
| `r1.*` MCP catalog (38 tools across 10 categories) | One namespace; every UI action reachable through MCP | Done (catalog only; back-end pending specs 1-7) | `internal/mcp/r1_server_catalog.go` |
| Slack-style envelope + stokerr/ taxonomy | Predictable wire shape; no raw Go errors leak | Done | `internal/mcp/envelope.go`, `internal/mcp/stokerr_map.go` |
| `r1 mcp serve --print-tools [--markdown]` | Lint + docs generator have a stable input | Done | `cmd/r1/mcp.go` |
| `internal/tui/teatest_shim.go` | Bubble Tea drivable through MCP without a terminal emulator | Done (in-process driver; teatest swap pending dep) | `internal/tui/teatest_shim.go` |
| `A11yEmitter` + JSONPath evaluator | Synthetic a11y trees + structural assertions | Done | `internal/tui/a11y.go`, `internal/tui/jsonpath.go` |
| `*.agent.feature.md` parser + dispatcher | Gherkin-shaped tests dispatched to MCP catalog | Done | `tools/agent-feature-runner/` |
| 8 seed feature fixtures across all 10 categories | Coverage gate per spec 8 §10 | Done | `tests/agent/{tui,web,cli,mission,worktree}/` |
| `lint-view-without-api` + allowlist | UI without API is a build break | Done (Go scanner active; React + Tauri scanners blocked on specs 6/7 merge) | `tools/lint-view-without-api/` |
| `make agent-features[-update,-drift-check]`, `make lint-views`, `make docs-agentic` | One-line CI/local recipes | Done | `Makefile` |
| `docs/AGENTIC-API.md` + D-A1..D-A5 acceptance | External-agent contract + decisions log | Done | `docs/AGENTIC-API.md`, `docs/decisions/index.md` |

## Status

### Done

- Specs 1-9 — all 171/172 items merged + tested + deployed
- 9 Cloud Run SaaS services live + Cloud SQL + Secret Manager + Artifact Registry + domain mappings created
- Anti-truncation 7-layer defense + 1M-iter soak (0 FP / 0 FN)
- Branch hygiene: 20 archive tags, repo cleaned to 2 active branches
- Documentation: this doc + 6 sibling docs + 9 spec docs + decisions log
- All Go tests + web typecheck + desktop tests green
- governed mission runtime
- deterministic skill substrate
- full pack lifecycle including signing, verification, and HTTP serving
- runtime metrics/audit/timeout/cancel/cost helper surfaces
- agentic test harness wire surface (38 r1.* tools, parser/dispatcher,
  TUI shim, lint scanner, 8 seed fixtures, AGENTIC-API.md, D-A1..D-A5)
- **Final-sweep PRs #168 / #169 / #170 / #171** (sync to `main` in commit `242af4a8`):
  - Skill-aware compactor (`SkillCompactor` + `LRUPolicy`) and `SkillScopeCloser.OnPhaseExit` wire skilltracker's `EvictByCompactor` + `CloseScope` into production callers; `SkillUnloaded` ledger nodes emitted for both reasons (`compactor` and `scope_exit`).
  - ed25519-signed redaction events; `LoadOrGenerateSigningKey` persists keys under `<root>/redactions/`; `Store.RedactionsForVerified` flags tampered + legacy-unsigned entries distinctly.
  - Tracebundle v2: per-session filtering (`ListNodesForSession`, `ListEdgesForSession`), chain-root hashing (`ChainRootHashForSession`), canonical manifest signing body (`CanonicalManifestSignBody`); `cmd/r1-server/tracebundle_source.go` wired as the production source for `GET /api/session/{id}/export.tracebundle`. (Spec D — D-UI2-7 — removed the originally-paired `R1_SERVER_UI_V2` envelope gate.)
  - Release-rehearsal CI: Cloud Build triggers (push-to-`main` + `^v.*$` tag) + manual GitHub Actions workflow (`e2e-rehearsal-manual.yml`) firing the full Playwright + axe-core E2E lane; idempotent setup via `scripts/setup-cloudbuild-e2e-trigger.sh`.
- **r1-server UI v2 retrofit — production default** (Spec D / D-UI2-7, 2026-05-06): the legacy vanilla-JS SPA was deleted, the v2 htmx + Go-templates surface promoted from `cmd/r1-server/ui/web/` to `cmd/r1-server/ui/`, and the `R1_SERVER_UI_V2` envelope toggle removed. `Renderable()` / `v2Enabled()` / `traceV2Enabled()` always return true; v2 is the only surface. Five sub-specs landed during the retrofit:
  - **Foundation** (`r1-server-ui-v2-foundation.md`): vendored htmx 2.0.4 + htmx-ext-sse 2.2.4 + three.js 0.170.0 ESM + d3-force-3d 3.0.5 + import map; SRI hashes verified at vendor time + at every CI run; `base.html` htmx layout that all v2 pages extend; `data-hx-*` attribute convention pinned. Air-gapped r1-server build with no CDN dependencies; ≤250 KB gzipped chrome.
  - **3D perf** (`r1-server-ui-v2-3d-perf.md`): InstancedMesh refactor + Web Worker for d3-force-3d + frozen-position time scrubber. The ledger 3D viewer scales from ~500 to ~3000 nodes at ≥30 FPS without freezing the page during simulation.
  - **Event rendering** (`r1-server-ui-v2-event-rendering.md`): typed `IsRedacted` / `SkillEventMap` Go helpers + waterfall lock + side-panel `[content redacted]` + skill row icons + 3D desaturation/opacity transitions; emission paths for `skill_loaded` (skill_injector) + `skill_unloaded` (compactor + scope-exit).
  - **Handlers + routes** (`r1-server-ui-v2-handlers-and-routes.md`): centralised `V2Config` flag (replaces ad-hoc `os.Getenv` calls); `index.html` + `session.html` + `session-stream.html` + `memories.html` + `share.html` + `diff.html` page templates; memory-side-panel + memory-graph view; `.tracebundle` export route; SSE `last_event_id` URL-query fallback + `event: resync` frame on cursor pruning.
  - **Tests** (`r1-server-ui-v2-tests.md`): golden test suite for every page template (auto-update via `-update`); 3D worker fixture test (vitest); Playwright + axe-core E2E in a separate Go submodule; vendor freshness CI guard.

### In Progress

- broader runtime-wide adoption of deterministic skills
- agentic test harness back-end wiring (depends on specs 1-7 merging
  the cortex/lanes/TUI/r1d/web/desktop sources)
- DNS propagation for the 9 r1.run subdomains (operator action: add Cloudflare CNAMEs)
- Operator follow-ups: secret values, CLAUDE.md package map line, Cloud Build trigger creation

### Scoped — release-blocking (Tier A, scoped 2026-05-11)

| Feature | Outcome | Status | Reference |
|---|---|---|---|
| Prompt-guard hardening (A1) | Customers running r1 against untrusted repos cannot have their diff hijacked by a `Ignore previous instructions`-style payload embedded in a README, a tool result, or a sub-agent response. The guard runs at plan + execute + verify boundaries, per-tool input validation lives on the MCP wire, every turn carries an ed25519 system-prompt fingerprint so a tampered system block fails verification, an adversarial reviewer evaluates against the CL4R1T4S corpus, and a per-session injection budget circuit-breaks a session that crosses a configurable attempt threshold. | Scoped (ready, BUILD_ORDER 34) | `specs/promptguard-hardening.md` |
| P0 platform hardening — foundation (A2) | r1's agent platform survives the failure modes that take down agent runtimes in production: panics on background goroutines become structured failures, a SIGTERM lets the daemon drain in-flight tool calls cleanly, in-flight tool calls replay-or-reject on restart, per-session resource limits prevent one runaway session from starving the others, observability hooks fire at every state transition, and a preflight gate refuses to start when host permissions are misconfigured. Spec flagged DRAFT because the source PORTFOLIO-INDEX referenced encryption-at-rest tasks already shipped; the surviving scope is the agent-platform P0 list. | Scoped (DRAFT — source-doc mismatch noted) | `specs/p0-hardening-s0-foundation.md` |
| One-shot production hardening (A3) | The `r1 --one-shot` integration surface — the one RelayGate Phase K-3 wires inline — becomes production-ready: memory bounds, per-call timeout enforcement that fails closed on stalls, deterministic shutdown ordering so a SIGTERM never leaves a half-written ledger node, a remote audit-ledger publishing path so the operator's ledger of record can be off-host, and a 1000-concurrent integration test that proves the path holds under realistic load. RelayGate gets a documented integration contract instead of a moving target. | Scoped (ready, BUILD_ORDER 35) | `specs/oneshot-production-hardening.md` |
| RelayOne SSO (A4) | Customers stop holding long-lived API keys; they log in with their RelayOne identity. Go reimpl of `@relayone/auth-core`'s `JwtService` + `RelayOneSsoClient`, OIDC + PKCE against the RelayOne IdP, per-tenant token isolation, JWKs rotation, middleware that gates the admin panel and every future enterprise route. Unblocks A5 and every Tier-B billing-aware path. | Scoped (ready, BUILD_ORDER 36) | `specs/relayone-sso.md` |
| Admin panel at admin.r1.run (A5) | Internal operators answer "what is this customer's session doing right now" without raw SQL. Mounted on the existing `r1-server` process; five read-only routes (sessions, tenants, billing, audit, anti-trunc events) auth-gated through the A4 SSO middleware. Regulators verify chain-of-custody from a browser; support answers tickets without ops escalation. | Scoped (ready, BUILD_ORDER 37) | `specs/admin-panel.md` |
| Prompt-guard hardening (A1) | Customers running r1 against untrusted repos cannot have their diff hijacked by a `Ignore previous instructions`-style payload embedded in a README, a tool result, or a sub-agent response. The guard runs at plan + execute + convergence boundaries, per-tool input validation lives on the MCP wire + agentloop dispatch, every turn carries an ed25519 system-prompt fingerprint (`r1 promptguard verify-system-prompt`) so a tampered system block fails verification, the cross-model reviewer evaluates against the CL4R1T4S corpus signature set (`TestCL4R1T4SDetectionRate` ≥85% gate, current 92%), and a per-session injection budget kills a session within 100ms when severity-weighted detections cross threshold. | Done (2026-05-12) | `specs/promptguard-hardening.md` |
| P0 platform hardening — foundation (A2) | r1's agent platform survives the failure modes that take down agent runtimes in production: panics on background goroutines become structured failures, a SIGTERM lets the daemon drain in-flight tool calls cleanly, in-flight tool calls replay-or-reject on restart, per-session resource limits prevent one runaway session from starving the others, observability hooks fire at every state transition, and a preflight gate refuses to start when host permissions are misconfigured. Spec flagged DRAFT because the source PORTFOLIO-INDEX referenced encryption-at-rest tasks already shipped; the surviving scope is the agent-platform P0 list. | Scoped (DRAFT — source-doc mismatch noted) | `specs/p0-hardening-s0-foundation.md` |
| One-shot production hardening (A3) | The `r1 --one-shot` integration surface — the one RelayGate Phase K-3 wires inline — is production-ready: `--max-mem` (default 256 MiB) enforced via `debug.SetMemoryLimit` + Linux `RLIMIT_AS`, `--timeout` with drop-partial pattern (exit 4 on timeout), deterministic SIGTERM/SIGINT shutdown (exit 130/143) so a half-written ledger node is impossible, `--audit-endpoint` HMAC-SHA256-signed POST with 3× exponential retry and fire-and-forget worker, a 1000-concurrent integration test under `-tags integration` proving the path holds (per-process RSS ≤256 MiB ±10%, p50 cold-start <500ms), and `docs/integrations/relaygate-r1-stage.md` operator runbook. | Done (2026-05-12) | `specs/oneshot-production-hardening.md` |
| RelayOne SSO (A4) | Customers stop holding long-lived API keys; they log in with their RelayOne identity. Go port of `@relayone/auth-core`'s `JwtService` (HS256 + RS256, kid rotation, RFC 7519 claims) and `RelayOneSsoClient` (OIDC + PKCE S256 per RFC 6749/7636), per-tenant token isolation via HKDF-SHA256, `__Host-` cookies, `/auth/sso/{start,callback}` + `/auth/refresh` + `/auth/logout` handlers, full TS↔Go round-trip interop test against `auth-core/test/` fixtures. Middleware gates the admin panel + every future enterprise route. Coverage: auth 76%, sessionhub 80%, bus 84%. | Done (2026-05-12) | `specs/relayone-sso.md` |
| Admin panel at admin.r1.run (A5) | Internal operators answer "what is this customer's session doing right now" without raw SQL. Mounted on the existing `r1-server` process; five read-only routes (sessions, tenants, billing, audit, anti-trunc events) auth-gated through the A4 SSO middleware. Regulators verify chain-of-custody from a browser; support answers tickets without ops escalation. | Done (2026-05-12) | `specs/admin-panel.md` |

### Scoped — commercial readiness (Tier B, scoped 2026-05-11)

| Feature | Outcome | Status | Reference |
|---|---|---|---|
| PostHog analytics (B1) | The product team answers "what's the activation rate" with one query. Twenty-four events instrumented end-to-end (signup → daemon-start → first mission → first verified completion → first anti-trunc fire → first paid event), per-tenant Group Analytics so the dashboard slices by enterprise account, three product funnels (activation, mission-success, anti-trunc-fire-recovered) and four cohorts (free-active, paid-active, churn-risk, regretted-activation). | Scoped (ready, BUILD_ORDER 38) | `specs/posthog-analytics.md` |
| Customer.io lifecycle email (B2) | Retention email becomes a product surface, not a manual ops task. Six lifecycle triggers — signup, activation, first mission, first completion, anti-trunc fired, budget alert — each backed by a transactional template marketing edits without a deploy. A GDPR DSAR flow lets a customer request export-or-delete; the flagstore tables that record consent are part of the spec. | Scoped (ready, BUILD_ORDER 39) | `specs/customerio-lifecycle.md` |
| CodeRadar dogfood (B3) | r1 finally eats its own dogfood. Eighteen canonical events emitted from the nine Cloud Run services (daemon, coord-api, docs, downloads, admin, plus the three new B-tier services), per-environment wiring with sampling so prod stays cheap, and the CodeRadar dashboard becomes the on-call surface for r1 itself. | Scoped (ready, BUILD_ORDER 40) | `specs/coderadar-dogfood.md` |

### Scoped — frontier extensions (Tier C, scoped 2026-05-11)

| Feature | Outcome | Status | Reference |
|---|---|---|---|
| Cross-machine session migration (C1) | A customer's session is not stuck to a host. A portable `.r1session` bundle format plus `r1 session export / import / migrate` commands let a session that started on a laptop resume on a cloud sandbox; chain-root-hash continuity verification carries a tamper-evident provenance chain across both machines. The audit story survives the move. | Scoped (ready, BUILD_ORDER 41) | `specs/cross-machine-session-migration.md` |
| Per-tool throttling (C3) | A runaway agent cannot burn an entire monthly quota on `Bash`. Two-tier token-bucket — per-session and per-tenant — enforced at the MCP boundary and the native agentloop boundary; declarative YAML policy the operator edits without a code change; `daemon.reload_config` hot-reloads without dropping in-flight tokens. Closes the cost-runaway hole every multi-tenant agent platform eventually finds. | Done (2026-05-12) | `specs/per-tool-throttling.md`, `docs/operations/throttling.md` |
| MCP IDE bundles (C4) | The customer installs r1 once and every IDE on their machine sees it. Single spec covering Cursor, Windsurf, VS Code, and JetBrains with one `r1 ide install / uninstall / verify` command that writes the right config in the right place for each IDE plus a JetBrains-side plugin shim. No more per-IDE walkthrough in the docs. | Scoped (ready, BUILD_ORDER 43) | `specs/mcp-ide-bundles.md` |
| BitBucket Pipelines adapter (C5) | Customers on BitBucket stop being a third-class platform. Parity with the existing GitHub Actions + GitLab CI adapters: OIDC-based authentication, PR commenting with diff-aware annotations, and the same `r1 run --ci` flag set across all three providers. | Scoped (ready, BUILD_ORDER 44) | `specs/bitbucket-pipelines-adapter.md` |
| Browser tool — remote sandbox (C6) | The hosted r1.run finally has a browser the agent can drive without compromising the underlying host. Two interchangeable providers (Browserless managed + an in-house Cloud Run provider), tenant-isolated sandbox model, deny-by-default egress policy. Unlocks every "scrape this site / fill this form" agent workflow on the hosted tier. | Scoped (ready, BUILD_ORDER 45) | `specs/browser-remote-sandbox.md` |
| Cross-product skill exchange (C7) | Skills become portable assets across the RelayOne portfolio instead of per-product silos. Pack-format v2 with an explicit compatibility matrix, federated trust root so a skill signed by one product is verifiable by another, runtime adapters for CloudSwarm, Heroa, and Veritize. A skill written for r1 runs in Heroa with no manual port; a skill from CloudSwarm runs in r1. | Scoped (ready, BUILD_ORDER 46) | `specs/cross-product-skill-exchange.md` |
| Per-tool throttling (C3) | A runaway agent cannot burn an entire monthly quota on `Bash`. Two-tier token-bucket — per-session and per-tenant — enforced at the MCP boundary; declarative YAML policy the operator edits without a code change; bucket state journaled so daemon restart honors the in-flight throttle window. Closes the cost-runaway hole every multi-tenant agent platform eventually finds. | Scoped (ready, BUILD_ORDER 42) | `specs/per-tool-throttling.md` |
| MCP IDE bundles (C4) | The customer installs r1 once and every IDE on their machine sees it. Single spec covering Cursor, Windsurf, VS Code, and JetBrains with one `r1 ide install / uninstall / verify` command that writes the right config in the right place for each IDE plus a JetBrains-side plugin shim. No more per-IDE walkthrough in the docs. | Scoped (ready, BUILD_ORDER 43) | `specs/mcp-ide-bundles.md` |
| BitBucket Pipelines adapter (C5) | Customers on BitBucket stop being a third-class platform. Parity with the existing GitHub Actions + GitLab CI adapters: OIDC-based authentication, PR commenting with diff-aware annotations, and the same `r1 run --ci` flag set across all three providers. | Scoped (ready, BUILD_ORDER 44) | `specs/bitbucket-pipelines-adapter.md` |
| Browser tool — remote sandbox (C6) | The hosted r1.run finally has a browser the agent can drive without compromising the underlying host. Two interchangeable providers (Browserless managed + an in-house Cloud Run provider), tenant-isolated sandbox model, deny-by-default egress policy. Unlocks every "scrape this site / fill this form" agent workflow on the hosted tier. | Scoped (ready, BUILD_ORDER 45) | `specs/browser-remote-sandbox.md` |
| Cross-product skill exchange (C7) | Skills become portable assets across the RelayOne portfolio instead of per-product silos. Pack-format v2 with an explicit compatibility matrix, federated trust root so a skill signed by one product is verifiable by another, runtime adapters for CloudSwarm, Heroa, and Veritize. A skill written for r1 runs in Heroa with no manual port; a skill from CloudSwarm runs in r1. | Done (R1-side substrate, 2026-05-12) | `specs/cross-product-skill-exchange.md`, `internal/skill/manifest_v2.go`, `internal/skill/compat/`, `internal/skill/trustroot.go`, `cmd/r1/pack_adopt_cmd.go` |

### Scoped — legacy (pre-2026-05-11)
- JWT login + RelayOne MSP SSO (Path A — Go reimpl of @relayone/auth-core JwtService + RelayOneSsoClient) — superseded by A4 above with a fuller scope.
- Admin panel at admin.r1.run (clone *-admin template + customize for r1 routes) — superseded by A5 above with explicit route inventory.
- PostHog product analytics integration — superseded by B1 above with funnel + cohort spec.
- Customer.io retention + lifecycle email integration — superseded by B2 above with DSAR flow.
- CodeRadar dogfood event streaming (already in-house; just turn on per env) — superseded by B3 above with explicit 18-event canonical inventory.
- (UI v2 retrofit moved to Done — see "r1-server UI v2 retrofit — production default" above. Spec D / D-UI2-7 closed the parallel-deploy window 2026-05-06.)
- **Node 22 LTS CI bump** (precursor to UI v2 retrofit): Node 20 went EOL 2026-04-30; bump unblocks jsdom 29, vitest 4, vite 7. Single small CI-only PR (`cloudbuild.yaml` + `desktop-augmentation.yml`).

### Scoping
- Encryption-at-rest for journals — separate spec drafted at `specs/encryption-at-rest.md`; remains the scoping target for the journals path.

### Potential — On Horizon
- Marketing site with affiliate / SEO / CRO / attribution / retention stack — multi-week marketing-engineering effort; deferred until Tier A+B land.
