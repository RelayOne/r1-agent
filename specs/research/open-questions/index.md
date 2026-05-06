# Open Questions — Cortex / Lanes / Surfaces Scope

These are decisions that surfaced during research. Default answers proposed; user can override.

| # | Question | Default answer | Spec |
|---|----------|---------------|------|
| OQ-1 | Lobe naming — `Lobe` vs `Specialist` vs `Concern` | **Lobe** (avoids collision with existing `concern/`; matches user's brain metaphor) | 1 |
| OQ-2 | Workspace persistence — in-memory only or write-through to bus/ WAL | **Write-through to bus/ WAL** so daemon restart preserves Notes | 1 |
| OQ-3 | Router model when user input arrives mid-turn | **Haiku 4.5** with 4 tools: interrupt / steer / queue_mission / just_chat | 1 |
| OQ-4 | Default Lobe concurrency cap | **5 LLM Lobes + unlimited deterministic** at Tier 4. Tunable up to 8. | 1 |
| OQ-5 | Cache pre-warm cadence | **Every 4 minutes** (Anthropic 5-min TTL minus margin) | 1 |
| OQ-6 | Critical-Note escalation model for Lobes | **Default Haiku, allow Sonnet escalation on rule-check failure or operator-tagged critical** | 1, 2 |
| OQ-7 | Memory-curator privacy | **No auto-write of user messages tagged "private"; only project-facts get auto-curated** | 2 |
| OQ-8 | Lane stable ordering | **Creation timestamp + lane_id tiebreak**. Re-rank by activity is opt-in. | 3, 4 |
| OQ-9 | TUI keybinding for kill | **`k` then `y` to confirm**. Plain `k` jumps to "kill" focus row to prevent accidents. | 4 |
| OQ-10 | r1d single-instance lock location | **`~/.r1/daemon.lock` via gofrs/flock** + socket exclusivity | 5 |
| OQ-11 | Token storage | **`~/.r1/daemon.json` mode 0600**. OS keychain is opt-in (`r1 serve --use-keychain`) | 5 |
| OQ-12 | Web UI hosting model | **Daemon serves it from `internal/server/static/` (embedded via `embed.FS`)** | 5, 6 |
| OQ-13 | Desktop sidecar vs always-external daemon | **External daemon primary, sidecar fallback on first run** | 7 |
| OQ-14 | MCP tool granularity | **Goal-shaped (e.g., `r1.lanes.kill`) not RPC-shaped (`r1.fn.cortex.lobeCancel`)** | 8 |
| OQ-15 | os.Chdir audit + CI lint | **Required gate before turning on multi-session in spec 5** | 5 |

If user accepts defaults, defaults become decisions and feed `docs/decisions/index.md`. Spec writers cite OQ-N when they apply.
