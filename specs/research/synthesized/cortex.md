# Synthesized — Cortex / Cognitive Architecture

Combines: RT-PARALLEL-COGNITION, RT-EXISTING-CONCURRENCY, RT-CONCURRENT-CLAUDE-API, RT-CANCEL-INTERRUPT.

## Architectural decisions

### A. Substrate: GWT-style Workspace, not Blackboard
A shared mutable workspace where N concurrent specialists publish hypotheses and one arbiter selects the broadcast item per "round". Maps cleanly to Hearsay-II/Global Workspace Theory.

- New package: `internal/cortex/` exposing `Workspace`, `Lobe`, `Note`, `Round`, `Spotlight`.
- `Workspace` = RWMutex-protected typed-block store with per-block versioning + pub-sub via existing `hub.Bus`.
- Existing `internal/concern/` is **stance-context projection** (different concept) — leave alone. Do not collide.

### B. Lobe model — "specialists with full context"
Each Lobe is a goroutine with read-only access to the current message history and write access to the Workspace via `Workspace.Publish(Note)`. Every Lobe runs in the same process (in-memory shared state — not subprocess-isolated).

- Lobe types in v1 (defined fully in spec 2):
  - `MemoryRecallLobe` (deterministic — TF-IDF over memory/wisdom)
  - `WALKeeperLobe` (deterministic — drains hub events to journal)
  - `RuleCheckLobe` (deterministic — runs supervisor/rules in the background)
  - `PlanUpdateLobe` (Haiku 4.5 LLM — keeps plan.json current)
  - `ClarifyingQLobe` (Haiku 4.5 LLM — drafts async user questions)
  - `MemoryCuratorLobe` (Haiku 4.5 LLM — detects "should-remember" moments)

### C. Merge model — agent-decides
Lobes don't write to the conversation history directly. Notes accumulate in the Workspace. The main action thread (the existing `agentloop.Loop`) consults the Workspace at three checkpoints:

1. **Pre-turn** (replaces existing `MidturnCheckFn` injection): build a "side-notes" prompt block from unresolved Notes. Inject as supervisor note.
2. **Pre-end-turn** (uses existing `PreEndTurnCheckFn`): if any unresolved critical Note exists, refuse `end_turn`.
3. **On new user input mid-turn** (NEW): a `Router` LLM call (Haiku) decides one of {interrupt, steer, queue, just-chat}. Router has tools to act on each. This is the agent-decides merge model the user asked for.

### D. Interrupt safety — drop-partial pattern (RT-CANCEL-INTERRUPT)
The Loop owns a `partial *Message` field outside committed history. Streaming events accumulate into `partial`. On normal completion, commit. On interrupt:
- `cancel()` the per-turn context.
- Drain the SSE-reader goroutine via `<-streamDone` (mandatory — prevents leak).
- Discard `partial` entirely (Anthropic gives no recovery handle for incomplete tool_use blocks).
- Append a synthetic user message: "Interrupted by [reason]. New direction: [steer]."
- Resume.

Add a 30 s ping-based idle watchdog that auto-cancels on connection stalls.

### E. Concurrency cap — Tier-4 budget (RT-CONCURRENT-CLAUDE-API)
- 1 main thread (Sonnet/Opus).
- ≤5 concurrent Lobes by default. Tunable up to 6 with cap=8 hard ceiling.
- Goroutine pool sized to `runtime.NumCPU()`; deterministic Lobes run free, LLM Lobes acquire a semaphore (capacity = configured concurrency).

### F. Pre-warm cache before fan-out (RT-CONCURRENT-CLAUDE-API)
- On Loop start: send one `max_tokens=0` warming request with the system prompt + tool list.
- Subsequent main + Lobe calls hit the warm cache (~90% cost reduction).
- Shared-suffix detection: tool definitions sorted alphabetically (already in `agentloop`); system prompt block is a stable cache-control breakpoint.
- Warm rebuild scheduled every 4 minutes (5-minute TTL minus a margin).

### G. Token-cost discipline
- Lobes default to Haiku 4.5; configurable per Lobe.
- 1-hour cache extension for Lobe system prompts → input tokens at 10% baseline.
- Per-turn budget: Lobes collectively capped at 30% of main thread output tokens. CostTracker enforces.
- Smart-not-cheap default (per user): Haiku is the floor, but on rule-check failures or critical findings the Lobe may escalate to Sonnet. Operator can disable escalation per Lobe.

### H. Reuse existing primitives
| Existing | Use as |
|----------|--------|
| `internal/hub/Bus` | Workspace event substrate (Lobes publish + subscribe) |
| `internal/bus/` durable WAL | Replay log for Notes (survive daemon restart) |
| `internal/agentloop.Loop.MidturnCheckFn` | Pre-turn Workspace drain |
| `internal/agentloop.Loop.PreEndTurnCheckFn` | Critical-Note gate |
| `internal/specexec/` | Reused — speculative parallel execution still has its place for "try N approaches" tasks. Cortex is for "think in parallel about ONE approach" |
| `internal/scheduler/` | Mission-task parallelism, unchanged |
| `internal/conversation.Runtime` | RWMutex-backed history (already exists; perfect for shared read-access by Lobes) |

## Open issues that need decision

- **What does the Router (mid-turn user input handler) look like as a tool?** Tools: `interrupt`, `steer`, `queue_mission`, `just_chat`, `update_workspace_note`. Router is a separate Haiku call invoked when stdin/WS gets a user message during an in-flight turn.
- **Naming**: `Lobe` vs `Specialist` vs `Concern`. Going with `Lobe` because (a) `concern` is taken, (b) `Specialist` is too generic, (c) `Lobe` matches the user's brain metaphor.
- **Workspace persistence**: in-memory only? Or write-through to bus/ WAL? — Going write-through so a daemon restart preserves Notes for in-flight sessions.

