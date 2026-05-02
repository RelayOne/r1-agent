// Package cortex implements r1's parallel-cognition substrate.
//
// Background: GWT (Global Workspace Theory) and Theater-of-Mind
// --------------------------------------------------------------
// Inspired by Global Workspace Theory and the Theater-of-Mind metaphor:
// many specialist threads operate concurrently with full shared context,
// publishing findings to a shared Workspace. The main "action thread"
// (the agentloop) reads broadcast Notes at turn boundaries.
//
// The package replaces the "isolated subagent" pattern (each subagent has
// its own context, cannot see what its peers discovered) with a "shared
// brain" pattern: one context, many parallel cognitive threads called
// Lobes, all writing to a shared Workspace that the main thread drains
// per round.
//
// Architecture
// ------------
//
//	+-----------------------------------------------------------------+
//	|                          CORTEX                                 |
//	|                                                                 |
//	|   +-------+ +-------+ +-------+ +-------+ +-------+             |
//	|   | Lobe1 | | Lobe2 | | Lobe3 | | Lobe4 | | Lobe5 |   (LLM &    |
//	|   +---+---+ +---+---+ +---+---+ +---+---+ +---+---+   determ.)  |
//	|       | Publish |         |         |         |                 |
//	|       +---------+---------+---------+---------+                 |
//	|                          |                                      |
//	|                  +-------v--------+                             |
//	|                  |   Workspace    |   (RWMutex, hub.Bus,        |
//	|                  |  (notes, seq,  |    durable bus.Bus WAL)     |
//	|                  |   spotlight)   |                             |
//	|                  +-------+--------+                             |
//	|                          |                                      |
//	|      +-------------------+-------------------+                  |
//	|      |                                       |                  |
//	|   +--v--+                                +---v---+              |
//	|   |Round|  (superstep barrier per        |Spotlight| (severity- |
//	|   +-----+   MidturnNote drain)           +--------+   ranked    |
//	|                                                       Note)     |
//	|                                                                 |
//	|   +----------+                                                  |
//	|   |  Router  | -- Haiku 4.5 + 4 tools (interrupt/steer/queue/   |
//	|   +----------+    just_chat) decides merge-back on user input   |
//	|                                                                 |
//	+-----------------------------------------------------------------+
//
//	  agentloop.Loop  --CortexHook-- Cortex.MidturnNote/PreEndTurnGate
//
// Key invariants (see specs/cortex-core.md "Risks & Gotchas"):
//
//   - Notes are immutable once Published.
//   - Workspace.Publish persists to durable bus BEFORE releasing the
//     mutex; subscribers fire AFTER lock release. Reverse this and you
//     either emit ghosts or deadlock.
//   - PreEndTurnGate short-circuits operator gates when CortexHook
//     returns non-empty — a cortex-tagged critical Note refuses
//     end_turn even if the operator's gate would allow it.
//   - Slow Lobes that miss a Round deadline are NOT cancelled; their
//     Notes land on the next round. Cancelling a partway-LLM-call
//     wastes API spend.
//   - Pre-warm cache: warming request must produce byte-identical
//     system blocks AND tool ordering as the main thread, or the
//     0.1x cache-hit price is lost and the system silently pays 10x.
//   - Drop-partial interrupt: never persist a partial assistant
//     message; on cancel, drain SSE goroutine and discard the
//     in-flight assistant turn entirely (RT-CANCEL-INTERRUPT pattern).
//   - bus.Bus.Publish vs hub.Bus.Emit: durable WAL vs in-process
//     pub-sub. Cortex uses BOTH; mixing them up silently drops
//     persistence.
//
// References:
//
//   - specs/cortex-core.md (the contract)
//   - specs/research/synthesized/cortex.md (design rationale)
//   - specs/research/raw/RT-PARALLEL-COGNITION.md (prior art)
//   - specs/research/raw/RT-CANCEL-INTERRUPT.md (drop-partial pattern)
//   - specs/research/raw/RT-CONCURRENT-CLAUDE-API.md (cache pre-warm)
//   - specs/cortex-concerns.md (BUILD_ORDER 2 -- the 6 v1 Lobes)
package cortex
