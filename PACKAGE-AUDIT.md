# PACKAGE-AUDIT.md — Stoke Internal Package Audit

Generated: 2026-04-06

## DEPRECATED Packages (13 — zero external callers, dead code)

| Package | Files | ~LOC | Callers | Class | Note |
|---------|-------|------|---------|-------|------|
| **compute** | 3 | 401 | 0 | DEPRECATED | Ember/Flare cloud burst backend — never integrated; not imported anywhere |
| **lifecycle** | 1 | 335 | 0 | DEPRECATED | 5-tier hook registry, overlaps with `hooks/` which is actually wired in (3 callers); lifecycle is the dead version |
| **managed** | 1 | 255 | 0 | DEPRECATED | Proxy client for "Ember managed AI endpoint" — never wired in; cloud feature never shipped |
| **permissions** | 1 | 270 | 0 | DEPRECATED | Composable auth pipeline for tools; overlaps with `rbac/` (1 caller) and `hooks/` (3 callers) which do the real enforcement |
| **phaserole** | 1 | 216 | 0 | DEPRECATED | Phase-to-role mapping; never integrated — `model.Resolve()` handles model selection instead |
| **prompttpl** | 1 | 338 | 0 | DEPRECATED | Template engine (conditionals, iteration); `prompts/` uses `prompt/` fingerprinting directly instead |
| **ralph** | 1 | 374 | 0 | DEPRECATED | Persistent execution discipline enforcer; functionality absorbed by `workflow/` and `boulder/` |
| **ratelimit** | 1 | 285 | 0 | DEPRECATED | Token bucket rate limiter; `subscriptions/` circuit breaker handles rate management instead |
| **sandattr** | 1 | 215 | 0 | DEPRECATED | Sandbox failure attribution; never wired in — `failure/` handles all failure analysis |
| **sandbox** | 1 | 155 | 0 | DEPRECATED | Container detection/enforcement; settings.json sandbox is used instead (design decision #6) |
| **sandguard** | 1 | 279 | 0 | DEPRECATED | Sandbox escape detection; never wired in — `hooks/` + `scan/` do the actual security checks |
| **team** | 1 | 249 | 0 | DEPRECATED | Multi-agent parallel review; never integrated — `specexec/` handles parallel execution |
| **toolcache** | 1 | 274 | 0 | DEPRECATED | Tool output caching across turns; never wired in to any runner |

**Total dead code: ~3,636 LOC across 13 packages.**

---

## Full Package Table

| Package | Files | ~LOC | Callers | Class | Note |
|---------|-------|------|---------|-------|------|
| agentloop | 2 | 592 | 1 | CORE | Native agent loop used by engine/native_runner |
| agentmsg | 1 | 263 | 1 | HELPFUL | Inter-agent messaging; only used by scheduler |
| apiclient | 1 | 530 | 1 | CORE | SSE streaming API client; used by engine/api_runner |
| app | 1 | 324 | 1 | CORE | Top-level orchestrator wiring; called from cmd/stoke |
| atomicfs | 1 | 332 | 1 | HELPFUL | Atomic file edits; only used by worktree/helpers |
| audit | 1 | 245 | 1 | HELPFUL | 17 review personas; called from cmd/stoke |
| autofix | 1 | 217 | 1 | HELPFUL | Auto-lint-fix loop; called from workflow |
| baseline | 1 | 318 | 2 | CORE | Build/test state capture; used by verify + workflow |
| boulder | 1 | 315 | 3 | CORE | Idle detection + continuation enforcement; 3 callers |
| branch | 1 | 259 | 1 | HELPFUL | Conversation branching; only used by scheduler |
| checkpoint | 1 | 177 | 1 | HELPFUL | Synchronous checkpointing; only used by workflow |
| chunker | 1 | 408 | 1 | HELPFUL | Semantic code chunking; only used by tfidf |
| compute | 3 | 401 | 0 | DEPRECATED | Cloud burst backend; never integrated |
| config | 4 | 906 | 8 | CORE | YAML policy parser; most-imported package |
| conflictres | 1 | 320 | 1 | HELPFUL | Merge conflict resolution; only used by worktree |
| consent | 1 | 244 | 2 | CORE | Human-in-the-loop approval; used by hooks + workflow |
| context | 2 | 473 | 1 | CORE | 3-tier context budget; called from cmd/stoke |
| convergence | 7 | 4794 | 6 | CORE | Adversarial self-audit; heavily used (6 callers) |
| conversation | 1 | 266 | 1 | HELPFUL | Multi-turn state; only used by repl |
| costtrack | 1 | 296 | 6 | CORE | Real-time cost tracking; 6 callers across system |
| critic | 1 | 537 | 1 | HELPFUL | Pre-commit critic; only used by workflow |
| ctxpack | 1 | 200 | 1 | HELPFUL | Adaptive bin-packing; only used by workflow |
| depgraph | 1 | 367 | 2 | HELPFUL | Import graph extraction; used by testselect + workflow |
| diffcomp | 1 | 355 | 1 | HELPFUL | Diff compression; only used by workflow |
| dispatch | 1 | 278 | 1 | HELPFUL | 3-tier message queue; only used by scheduler |
| engine | 7 | 1100 | 6 | CORE | Claude/Codex CLI runners; central execution layer |
| errtaxonomy | 1 | 206 | 1 | HELPFUL | Error taxonomy; only used by failure/analyzer |
| extract | 1 | 280 | 1 | HELPFUL | LLM output parsing; only used by workflow |
| failure | 2 | 619 | 2 | CORE | Failure analysis + fingerprint dedup; used by workflow + scheduler |
| fileutil | 1 | 87 | 1 | HELPFUL | Shared file ops; only used by workflow |
| filewatcher | 1 | 424 | 1 | HELPFUL | FS monitoring; only used by workflow |
| flowtrack | 1 | 393 | 2 | HELPFUL | Flow-aware intent tracking; 2 callers |
| gitblame | 1 | 227 | 1 | HELPFUL | Git blame integration; only used by workflow |
| goast | 1 | 653 | 4 | CORE | Go AST analysis; 4 callers (repomap, symindex, testgen, testselect) |
| handoff | 1 | 276 | 2 | HELPFUL | Agent-to-agent context transfer; 2 callers |
| hashline | 1 | 203 | 1 | HELPFUL | Hash-anchored line verification; only used by patchapply |
| hooks | 1 | 436 | 3 | CORE | Anti-deception enforcement layer; 3 callers |
| hub | 12 | 1989 | 7 | CORE | Tool dispatch hub; 7 callers — central to native agent loop |
| intent | 1 | 249 | 1 | HELPFUL | Intent classification; only used by workflow |
| interview | 1 | 405 | 1 | HELPFUL | Socratic clarification; only used by repl |
| jsonutil | 1 | 64 | 1 | HELPFUL | JSON parsing from LLM output; only used by workflow |
| lifecycle | 1 | 335 | 0 | DEPRECATED | Dead hook registry; superseded by hooks/ |
| logging | 1 | 111 | 2 | CORE | Structured logging; used by workflow + cmd |
| managed | 1 | 255 | 0 | DEPRECATED | Unused proxy client for cloud endpoint |
| mcp | 2 | 1316 | 2 | CORE | MCP codebase tool server; 2 callers |
| memory | 1 | 388 | 1 | HELPFUL | Cross-session knowledge; only used by app |
| metrics | 1 | 221 | 1 | HELPFUL | Thread-safe counters; only used from cmd/stoke |
| microcompact | 1 | 312 | 1 | HELPFUL | Cache-aligned compaction; only used by workflow |
| mission | 8 | 5458 | 5 | CORE | Mission execution pipeline; 5 callers |
| model | 3 | 511 | 4 | CORE | Model resolution + fallback chain; 4 callers |
| notify | 2 | 134 | 1 | HELPFUL | Event notification; only used from cmd/stoke |
| orchestrate | 2 | 1336 | 3 | CORE | Mission pipeline integrator; 3 callers |
| patchapply | 1 | 495 | 1 | HELPFUL | Unified diff parsing; only used by workflow |
| permissions | 1 | 270 | 0 | DEPRECATED | Unused auth pipeline; overlaps with rbac + hooks |
| phaserole | 1 | 216 | 0 | DEPRECATED | Unused phase-role mapping |
| plan | 3 | 545 | 5 | CORE | Plan load/save/validate; 5 callers |
| plugins | 1 | 95 | 1 | HELPFUL | Plugin manifest/loading; only used by app |
| pools | 1 | 437 | 1 | HELPFUL | Worker pool management; only used from cmd/stoke |
| preflight | 1 | 226 | 1 | HELPFUL | Pre-flight assertions; only used by app |
| progress | 1 | 398 | 1 | HELPFUL | ETA estimation; only used from cmd/stoke |
| prompt | 1 | 177 | 1 | HELPFUL | Prompt fingerprinting; only used by prompts/ |
| promptcache | 1 | 264 | 1 | HELPFUL | Cache-aligned prompt construction; only used by workflow |
| prompts | 2 | 1986 | 3 | CORE | Build plan/execute/review prompts; 3 callers |
| prompttpl | 1 | 338 | 0 | DEPRECATED | Unused template engine |
| provider | 1 | 459 | 4 | CORE | AI model API clients; 4 callers |
| ralph | 1 | 374 | 0 | DEPRECATED | Unused execution discipline enforcer |
| ratelimit | 1 | 285 | 0 | DEPRECATED | Unused token bucket limiter |
| rbac | 1 | 199 | 1 | HELPFUL | RBAC enforcement; only used by app |
| remote | 1 | 170 | 1 | HELPFUL | Dashboard progress reporting; only from cmd/stoke |
| repl | 1 | 286 | 1 | HELPFUL | Interactive REPL; only from cmd/stoke |
| replay | 1 | 258 | 3 | HELPFUL | Session recording; 3 callers |
| repomap | 1 | 430 | 3 | CORE | PageRank-based repo map; 3 callers |
| report | 1 | 96 | 1 | HELPFUL | BuildReport output; only from cmd/stoke |
| research | 1 | 615 | 2 | HELPFUL | Persistent research storage; 2 callers |
| sandattr | 1 | 215 | 0 | DEPRECATED | Unused sandbox failure attribution |
| sandbox | 1 | 155 | 0 | DEPRECATED | Unused container detection |
| sandguard | 1 | 279 | 0 | DEPRECATED | Unused sandbox escape detection |
| scan | 2 | 354 | 3 | CORE | 18 deterministic security rules; 3 callers |
| scheduler | 2 | 675 | 1 | CORE | GRPW priority ordering; called from cmd/stoke |
| schemaval | 1 | 254 | 1 | HELPFUL | Structured output validation; only used by workflow |
| semdiff | 1 | 782 | 1 | HELPFUL | Semantic diff analysis; only used by workflow |
| server | 2 | 507 | 1 | HELPFUL | HTTP API endpoints; only from cmd/stoke |
| session | 4 | 839 | 1 | CORE | JSON + SQLite session storage; critical persistence layer |
| skill | 3 | 905 | 6 | CORE | Reusable workflow patterns; 6 callers |
| skillselect | 3 | 1176 | 3 | HELPFUL | Skill selection logic; 3 callers |
| snapshot | 1 | 224 | 1 | HELPFUL | Pre-merge workspace snapshots; only used by workflow |
| specexec | 1 | 262 | 2 | HELPFUL | Speculative parallel execution; 2 callers |
| stream | 3 | 651 | 11 | CORE | NDJSON parser; most-depended-on after config (11 callers) |
| subscriptions | 3 | 585 | 4 | CORE | Pool acquire/release + circuit breaker; 4 callers |
| symindex | 1 | 555 | 2 | HELPFUL | Symbol indexing; 2 callers |
| taskstate | 2 | 434 | 4 | CORE | Anti-deception task state; 4 callers |
| team | 1 | 249 | 0 | DEPRECATED | Unused multi-agent review |
| telemetry | 1 | 244 | 1 | HELPFUL | Metrics collection; only used by app |
| testgen | 1 | 460 | 1 | HELPFUL | Test scaffold generation; only used by workflow |
| testselect | 1 | 303 | 3 | CORE | Dependency-aware test selection; 3 callers |
| tfidf | 1 | 329 | 2 | HELPFUL | TF-IDF search; 2 callers |
| tokenest | 1 | 241 | 1 | HELPFUL | Token count estimation; only used by workflow |
| toolcache | 1 | 274 | 0 | DEPRECATED | Unused tool output cache |
| tools | 2 | 613 | 1 | CORE | Tool execution layer for native agent loop; used by engine |
| tui | 2 | 600 | 1 | HELPFUL | Bubble Tea TUI; only from cmd/stoke |
| validation | 1 | 100 | 1 | HELPFUL | Input validation; only used by app |
| vecindex | 1 | 299 | 2 | HELPFUL | Vector search; 2 callers |
| verify | 3 | 494 | 4 | CORE | Build/test/lint pipeline; 4 callers |
| viewport | 1 | 239 | 2 | HELPFUL | File viewport; 2 callers |
| wisdom | 2 | 305 | 4 | CORE | Cross-task learnings; 4 callers |
| wizard | 8 | 2107 | 1 | HELPFUL | `stoke init` config wizard; only from cmd/stoke |
| workflow | 1 | 1877 | 2 | CORE | Phase machine; central workflow engine; 2 callers |
| worktree | 3 | 894 | 4 | CORE | Git worktree management; 4 callers |

---

## Summary

- **Total packages**: 107 (103 directories + hub/builtin subpackage)
- **CORE**: 32 packages
- **HELPFUL**: 49 packages
- **DEPRECATED**: 13 packages (~3,636 LOC of dead code)
- **Highest-traffic** (by caller count): `stream` (11), `config` (8), `hub` (7), `convergence` (6), `costtrack` (6), `engine` (6), `skill` (6)
- **Not in CLAUDE.md**: `agentloop`, `compute`, `hub/builtin`, `mission`, `skillselect`, `tools`, `wizard` (of these, `compute` is dead; the rest are actively used)

## Recommendations

1. **Delete 13 DEPRECATED packages** to remove ~3,636 LOC of dead code
2. **Add missing packages to CLAUDE.md**: agentloop, mission, skillselect, tools, wizard, hub/builtin
3. **Watch list**: 49 HELPFUL packages with only 1 caller each — candidates for inlining if the caller package is small enough
