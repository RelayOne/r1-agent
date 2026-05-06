# Research Index — r1 Cortex / Lanes / Multi-Surface Scope (2026-05-02)

Scope-set: parallel-cognition foundation (cortex + concerns + lanes) and the surfaces that consume it (TUI, r1d daemon, web UI, desktop, agentic test harness).

## Raw research files

| File | Topic | Key recommendation | Primary spec consumer |
|------|-------|--------------------|-----------------------|
| RT-EXISTING-CONCURRENCY.md | Inventory of existing parallelism in r1 | WaitGroup tool exec (agentloop) + winner-take-all specexec + scheduler fan-out exist; **no shared-state workspace, no live cross-thread comms** — that's the gap | spec 1, 2 |
| RT-PARALLEL-COGNITION.md | SoM, GWT, CoALA, Hearsay-II, LangGraph, AutoGen, Swarm, Theater-of-Mind | GWT broadcast cycle on top of CoALA memory tiers, scheduled in LangGraph-style supersteps with Hearsay-II agenda. Closest analog: "Theater of Mind for LLMs" (arXiv 2604.08206). Anthropic multi-agent is the *contrast* (parallel but isolated). Avoid AutoGen GroupChat (turn-based) and Swarm (sequential handoff) | spec 1 |
| RT-CONCURRENT-CLAUDE-API.md | Concurrent Anthropic API + cache + rate limits | Pre-warm cache before launching concerns; Tier 4 supports 5–6 concurrent Haiku threads + main; mid-stream cancel does NOT refund tokens; serialize tool writes, parallelize reads; expected cache-hit ~40–60% with pre-warm | spec 1, 2 |
| RT-CANCEL-INTERRUPT.md | Mid-stream cancel + replay safety | Drop-partial pattern: never persist partial assistant message. `context.WithCancel` per turn + drain SSE goroutine on interrupt. 30s ping watchdog. Synthetic tool_result repair only as fallback. Orphan tool_use is the #1 break (issue #3003: 2.4–14% rate) | spec 1 |
| RT-TUI-LANES.md | TUI lane layout patterns | Adaptive hybrid: columns when width ≥ N×32, vertical list otherwise. Bubble Tea v2 + bubblelayout for grid. lipgloss AdaptiveColor + glyphs (▸⏸✓✗·). Cache per-lane render strings; coalesce upstream events at 200–300 ms. Anti-pattern: repaint every lane on every tick | spec 4 |
| RT-R1D-DAEMON.md | Multi-instance daemon architecture | Watchman pattern: per-user singleton, on-demand spawn, tmux-style detachable sessions as goroutines (each carries SessionRoot via cmd.Dir). Unix socket + loopback HTTP+WS, token auth, Origin pinning. JSON-RPC 2.0 over WS, monotonic seqnos, replay from bus/ WAL on reconnect. **Risk: os.Chdir is process-global — must audit all 132 packages before turning on multi-session** | spec 5 |
| RT-DESKTOP-TAURI.md | Tauri 2 multi-session + sidecar | External `r1 serve` daemon as primary, Tauri sidecar as first-run fallback. tauri-plugin-websocket sidesteps `https://tauri.localhost` → `ws://` mixed-content block. Single primary window + session sidebar + drag-and-drop panes. Pop-out into `WebviewWindow` for power users. tauri-plugin-store for per-session workdir persistence | spec 7 |
| RT-WEB-UX.md | Web chat UX + WS auth + library refs | Copy Cursor 3 "Glass" (Apr 2026): right-sidebar Agents Window with status dots, "Agent Tabs" tile mode in main pane. WS auth via `Sec-WebSocket-Protocol` subprotocol token. streamdown for streaming markdown, @ai-sdk/elements for tool/reasoning/plan cards. Strict Origin/Host allowlist for CSWSH | spec 6 |
| RT-AGENTIC-TEST.md | Agent-driveable UIs | MCP-primary. One r1 MCP server exposing lanes/cortex/sessions/missions/worktrees/bus/verify/TUI as goal-shaped tools. Playwright MCP for web. teatest+JSON-RPC shim for TUI. Storybook MCP for component contracts. Gherkin markdown (`*.agent.feature.md`) as test DSL. **Governing principle: every UI action has an idempotent schema-validated MCP equivalent — UI is a view over the API, never the reverse.** CI lint enforces | spec 8 (cross-cutting) |

## Synthesized clusters

- `synthesized/cortex.md` — cognitive architecture decisions (RT-PARALLEL-COGNITION + RT-EXISTING-CONCURRENCY + RT-CONCURRENT-CLAUDE-API + RT-CANCEL-INTERRUPT)
- `synthesized/surfaces.md` — UI surface decisions (RT-TUI-LANES + RT-WEB-UX + RT-DESKTOP-TAURI)
- `synthesized/transport.md` — daemon + transport decisions (RT-R1D-DAEMON)
- `synthesized/agentic.md` — agentic test harness (RT-AGENTIC-TEST)

## Open questions

See `open-questions/index.md`.
