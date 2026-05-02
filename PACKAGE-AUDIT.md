# PACKAGE-AUDIT.md — Stoke Internal Package Audit

Generated: 2026-04-06

## Full Package Table

| Package | Files | ~LOC | Callers | Class | Note |
|---------|-------|------|---------|-------|------|
| agentloop | 2 | 592 | 1 | CORE | Native agent loop used by engine/native_runner |
| agentmsg | 1 | 263 | 1 | HELPFUL | Inter-agent messaging; only used by scheduler |
| apiclient | 1 | 530 | 1 | CORE | SSE streaming API client; used by engine/api_runner |
| app | 1 | 324 | 1 | CORE | Top-level orchestrator wiring; called from cmd/r1 |
| atomicfs | 1 | 332 | 1 | HELPFUL | Atomic file edits; only used by worktree/helpers |
| audit | 1 | 245 | 1 | HELPFUL | 17 review personas; called from cmd/r1 |
| autofix | 1 | 217 | 1 | HELPFUL | Auto-lint-fix loop; called from workflow |
| baseline | 1 | 318 | 2 | CORE | Build/test state capture; used by verify + workflow |
| boulder | 1 | 315 | 3 | CORE | Idle detection + continuation enforcement; 3 callers |
| bench | 4 | 1200 | 1 | CORE | Golden mission benchmarking with regression detection |
| branch | 1 | 259 | 1 | HELPFUL | Conversation branching; only used by scheduler |
| bridge | 2 | 400 | 4 | CORE | V1→V2 bridge adapters (cost, verify, wisdom, audit → bus+ledger) |
| bus | 2 | 600 | 5 | CORE | Durable WAL-backed event bus; hooks, delayed events, causality |
| checkpoint | 1 | 177 | 1 | HELPFUL | Synchronous checkpointing; only used by workflow |
| chunker | 1 | 408 | 1 | HELPFUL | Semantic code chunking; only used by tfidf |
| concern | 2 | 500 | 3 | CORE | Per-stance context projection; 10 sections, 9 role templates |
| config | 4 | 906 | 8 | CORE | YAML policy parser; most-imported package |
| conflictres | 1 | 320 | 1 | HELPFUL | Merge conflict resolution; only used by worktree |
| consent | 1 | 244 | 2 | CORE | Human-in-the-loop approval; used by hooks + workflow |
| contentid | 1 | 150 | 6 | CORE | Content-addressed ID generation (SHA256, 16 prefixes) |
| context | 2 | 473 | 1 | CORE | 3-tier context budget; called from cmd/r1 |
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
| harness | 4 | 800 | 3 | CORE | Stance lifecycle: spawn/pause/resume/terminate (11 templates) |
| hashline | 1 | 203 | 1 | HELPFUL | Hash-anchored line verification; only used by patchapply |
| hooks | 1 | 436 | 3 | CORE | Anti-deception enforcement layer; 3 callers |
| hub | 12 | 1989 | 7 | CORE | Tool dispatch hub; 7 callers — central to native agent loop |
| intent | 1 | 249 | 1 | HELPFUL | Intent classification; only used by workflow |
| interview | 1 | 405 | 1 | HELPFUL | Socratic clarification; only used by repl |
| jsonutil | 1 | 64 | 1 | HELPFUL | JSON parsing from LLM output; only used by workflow |
| ledger | 3 | 900 | 6 | CORE | Append-only content-addressed graph (nodes, edges, filesystem + SQLite) |
| ledger/loops | 1 | 300 | 2 | CORE | 7-state consensus loop tracker |
| ledger/nodes | 1 | 500 | 4 | CORE | 22 node type structs with NodeTyper interface |
| logging | 1 | 111 | 2 | CORE | Structured logging; used by workflow + cmd |
| mcp | 2 | 1316 | 2 | CORE | MCP codebase tool server; 2 callers |
| memory | 1 | 388 | 1 | HELPFUL | Cross-session knowledge; only used by app |
| metrics | 1 | 221 | 1 | HELPFUL | Thread-safe counters; only used from cmd/r1 |
| microcompact | 1 | 312 | 1 | HELPFUL | Cache-aligned compaction; only used by workflow |
| mission | 8 | 5458 | 5 | CORE | Mission execution pipeline; 5 callers |
| model | 3 | 511 | 4 | CORE | Model resolution + fallback chain; 4 callers |
| notify | 2 | 134 | 1 | HELPFUL | Event notification; only used from cmd/r1 |
| orchestrate | 2 | 1336 | 3 | CORE | Mission pipeline integrator; 3 callers |
| patchapply | 1 | 495 | 1 | HELPFUL | Unified diff parsing; only used by workflow |
| plan | 3 | 545 | 5 | CORE | Plan load/save/validate; 5 callers |
| plugins | 1 | 95 | 1 | HELPFUL | Plugin manifest/loading; only used by app |
| pools | 1 | 437 | 1 | HELPFUL | Worker pool management; only used from cmd/r1 |
| preflight | 1 | 226 | 1 | HELPFUL | Pre-flight assertions; only used by app |
| progress | 1 | 398 | 1 | HELPFUL | ETA estimation; only used from cmd/r1 |
| prompt | 1 | 177 | 1 | HELPFUL | Prompt fingerprinting; only used by prompts/ |
| promptcache | 1 | 264 | 1 | HELPFUL | Cache-aligned prompt construction; only used by workflow |
| prompts | 2 | 1986 | 3 | CORE | Build plan/execute/review prompts; 3 callers |
| provider | 1 | 459 | 4 | CORE | AI model API clients; 4 callers |
| rbac | 1 | 199 | 1 | HELPFUL | RBAC enforcement; only used by app |
| remote | 1 | 170 | 1 | HELPFUL | Dashboard progress reporting; only from cmd/r1 |
| repl | 1 | 286 | 1 | HELPFUL | Interactive REPL; only from cmd/r1 |
| replay | 1 | 258 | 3 | HELPFUL | Session recording; 3 callers |
| repomap | 1 | 430 | 3 | CORE | PageRank-based repo map; 3 callers |
| report | 1 | 96 | 1 | HELPFUL | BuildReport output; only from cmd/r1 |
| research | 1 | 615 | 2 | HELPFUL | Persistent research storage; 2 callers |
| scan | 2 | 354 | 3 | CORE | 18 deterministic security rules; 3 callers |
| scheduler | 2 | 675 | 1 | CORE | GRPW priority ordering; called from cmd/r1 |
| schemaval | 1 | 254 | 1 | HELPFUL | Structured output validation; only used by workflow |
| semdiff | 1 | 782 | 1 | HELPFUL | Semantic diff analysis; only used by workflow |
| server | 2 | 507 | 1 | HELPFUL | HTTP API endpoints; only from cmd/r1 |
| session | 4 | 839 | 1 | CORE | JSON + SQLite session storage; critical persistence layer |
| skill | 3 | 905 | 6 | CORE | Reusable workflow patterns; 6 callers |
| skillmfr | 2 | 400 | 2 | CORE | Skill manufacturing pipeline (4 workflows, confidence ladder) |
| skillselect | 3 | 1176 | 3 | HELPFUL | Skill selection logic; 3 callers |
| snapshot | 1 | 224 | 2 | CORE | Protected baseline manifest (file paths + content hashes) |
| stokerr | 1 | 200 | 8 | CORE | Structured error taxonomy (10 error codes) |
| supervisor | 3 | 800 | 3 | CORE | Deterministic rules engine (30 rules, 10 categories, 3 manifests) |
| specexec | 1 | 262 | 2 | HELPFUL | Speculative parallel execution; 2 callers |
| stream | 3 | 651 | 11 | CORE | NDJSON parser; most-depended-on after config (11 callers) |
| subscriptions | 3 | 585 | 4 | CORE | Pool acquire/release + circuit breaker; 4 callers |
| symindex | 1 | 555 | 2 | HELPFUL | Symbol indexing; 2 callers |
| taskstate | 2 | 434 | 4 | CORE | Anti-deception task state; 4 callers |
| telemetry | 1 | 244 | 1 | HELPFUL | Metrics collection; only used by app |
| testgen | 1 | 460 | 1 | HELPFUL | Test scaffold generation; only used by workflow |
| testselect | 1 | 303 | 3 | CORE | Dependency-aware test selection; 3 callers |
| tfidf | 1 | 329 | 2 | HELPFUL | TF-IDF search; 2 callers |
| tokenest | 1 | 241 | 1 | HELPFUL | Token count estimation; only used by workflow |
| tools | 2 | 613 | 1 | CORE | Tool execution layer for native agent loop; used by engine |
| tui | 2 | 600 | 1 | HELPFUL | Bubble Tea TUI; only from cmd/r1 |
| validation | 1 | 100 | 1 | HELPFUL | Input validation; only used by app |
| vecindex | 1 | 299 | 2 | HELPFUL | Vector search; 2 callers |
| verify | 3 | 494 | 4 | CORE | Build/test/lint pipeline; 4 callers |
| viewport | 1 | 239 | 2 | HELPFUL | File viewport; 2 callers |
| wisdom | 2 | 305 | 4 | CORE | Cross-task learnings; 4 callers |
| wizard | 8 | 2107 | 1 | CORE | First-time config with presets (minimal/balanced/strict) |
| workflow | 1 | 1877 | 2 | CORE | Phase machine; central workflow engine; 2 callers |
| worktree | 3 | 894 | 4 | CORE | Git worktree management; 4 callers |

---

## Summary

- **Total internal packages**: 180 (includes sub-packages under concern/, env/, harness/, hub/, ledger/, supervisor/; grew from 132 as portfolio-alignment + OSS-hub + work-stoke seams landed)
- **Additional packages**: 9 cmd + 10 bench = 199 total
- **CORE**: 47 packages (32 v1 + 15 v2)
- **HELPFUL**: 47 packages
- **DEPRECATED**: 0 packages (13 removed in v2 cleanup)
- **Highest-traffic** (by caller count): `stream` (11), `config` (8), `stokerr` (8), `hub` (7), `contentid` (6), `convergence` (6), `costtrack` (6), `engine` (6), `ledger` (6), `skill` (6)
- **Note**: Table above lists top-level internal packages. Sub-packages (e.g., `concern/sections`, `supervisor/rules/*`, `env/*`) are grouped under their parent. Run `make check-pkg-count` to verify the count hasn't drifted.

## Recommendations

1. **Watch list**: 47 HELPFUL packages with only 1 caller each — candidates for inlining if the caller package is small enough
