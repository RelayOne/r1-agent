# Feature Map

Every feature Stoke has or will have, grouped by user-visible outcome. For each: what it does, the benefit to the operator, current build status, and the spec it traces to.

Status legend:

- **Done** — shipped in the current trunk (`feat/smart-chat-mode`), passing build/vet/test.
- **Scoped** — spec file in `specs/`, STATUS: ready, awaiting `/build`.
- **Scoping** — spec in progress.
- **Horizon** — on the roadmap, not yet scoped.

## Verification Descent — the trust layer

| Feature | Benefit | Status | Spec |
|---|---|---|---|
| Anti-deception contract in worker prompts | Workers cannot silently fake completion — truthfulness block injected at dispatch | Done (tier1) | [descent-hardening](../specs/descent-hardening.md) |
| Forced self-check before turn end | Model signals tangible completion evidence; parser cross-checks against git + AC state | Done (tier1) | [descent-hardening](../specs/descent-hardening.md) |
| Bootstrap per descent cycle | Manifest-touching repairs re-install deps before next AC — no stale-workspace false failures | Done (tier1) | [descent-hardening](../specs/descent-hardening.md) |
| Per-file repair cap | 3-attempt cap per file (Cursor 2.0 parity) prevents infinite repair loops | Done (tier1) | [descent-hardening](../specs/descent-hardening.md) |
| Ghost-write detector | Post-tool supervisor hook catches "tool reported success but file is empty" fakes | Done (tier1) | [descent-hardening](../specs/descent-hardening.md) |
| Env-issue worker tool | Worker self-reports environment blockers; descent skips multi-analyst — saves ~$0.10/AC | Done (tier1) | [descent-hardening](../specs/descent-hardening.md) |
| VerifyFunc on acceptance criteria | Non-code executors (research/browser/deploy) plug into the same 8-tier descent ladder | Done (Task 11) | [executor-foundation](../specs/executor-foundation.md) |

## Prompt-injection hardening

| Feature | Benefit | Status | Spec |
|---|---|---|---|
| Promptguard wired into 4 ingest paths | Every file-to-prompt flow is scanned (skills, failure analysis, feasibility gate, convergence judge) | Done (Track A 1) | Portfolio WORK-stoke.md Track A 1 |
| Tool-output sanitization | 200KB cap + chat-template-token scrub + injection-shape annotation on every tool_result | Done (Track A 2) | Portfolio Track A 2 |
| Honeypot pre-end-turn gate | Canary + markdown-exfil + role-injection + destructive-without-consent; turn aborts on fire | Done (Track A 3) | Portfolio Track A 3 |
| Websearch domain allowlist + body cap | Operator-configurable glob allowlist; 100KB body cap on every fetch | Done (Track A 4) | Portfolio Track A 4 |
| MCP sanitization audit | Per-CallTool marker asserts LLM vs code classification; grep-able maintenance check | Done (Track A 5) | Portfolio Track A 5 |
| Red-team corpus | 58-sample regression suite across 4 categories (OWASP LLM01, CL4R1T4S, SpAIware); 100% detection on active set | Done (Track A 6) | Portfolio Track A 6 |
| SECURITY.md + known-miss advancement | Disclosure policy at repo root; 1 known-miss sample being advanced to detection | Scoped | [finishing-touches](../specs/finishing-touches.md) |

## Executor architecture — multi-task agent

| Feature | Benefit | Status | Spec |
|---|---|---|---|
| Executor interface + router | Uniform Execute/BuildCriteria/BuildRepairFunc/BuildEnvFixFunc surface; natural-language task routing | Done (Task 19) | [executor-foundation](../specs/executor-foundation.md) |
| `stoke task` CLI | Free-text entry point — router classifies + dispatches | Done (Task 19) | [executor-foundation](../specs/executor-foundation.md) |
| CodeExecutor (SOW-backed) | Existing SOW pipeline wrapped behind the executor interface | Done (wrapper) | [executor-foundation](../specs/executor-foundation.md) |
| ResearchExecutor MVP | Single-agent, stdlib-only; keyword-overlap claim verification | Done (Task 20 MVP) | [browser-research-executors](../specs/browser-research-executors.md) Part 2 |
| Research lead+subagent orchestrator | Anthropic-style orchestrator-worker; lead decomposes, N subagents fan out in parallel | Scoped | [research-orchestrator](../specs/research-orchestrator.md) |
| BrowserExecutor Part 1 (http) | Fetch + HTML strip + VerifyContains/VerifyRegex; `stoke browse` CLI | Done (Task 21 part 1) | [browser-research-executors](../specs/browser-research-executors.md) Part 1 |
| BrowserExecutor Part 2 (go-rod) | Interactive headless browser: click, type, wait, screenshot, vision diff | Scoped | [browser-interactive](../specs/browser-interactive.md) |
| DeployExecutor (Fly.io) | `stoke deploy` with dry-run + health-check ACs; subprocess via `flyctl` | Done (Task 22) | [deploy-executor](../specs/deploy-executor.md) |
| DeployExecutor Phase 2 (Vercel + Cloudflare) | Additional provider adapters | Scoped | [deploy-phase2](../specs/deploy-phase2.md) |
| DelegationExecutor MVP (verify-settle) | Hired-agent deliverable verification + settlement via TrustPlane | Done (S-10 sliver) | [delegation-a2a](../specs/delegation-a2a.md) |
| DelegationExecutor full (A2A protocol) | HMAC tokens + trust clamp + x402 micropayments + signed cards + JWKS + saga compensators | Scoped | [delegation-a2a](../specs/delegation-a2a.md) |

## Protocol surfaces — external integrations

| Feature | Benefit | Status | Spec |
|---|---|---|---|
| CloudSwarm NDJSON + HITL | Two-lane emitter; `hitl_required` gate on stdin; `stoke run` subcommand | Done (tier1) | [cloudswarm-protocol](../specs/cloudswarm-protocol.md) |
| STOKE envelope (v1.0) | Every event carries `stoke_version`, `instance_id`, `trace_parent`, optional `ledger_node_id` | Done (RS-6) | [docs/stoke-protocol.md](stoke-protocol.md) |
| r1-server binary | Separate daemon ingesting r1.session.json + event log + ledger DAG from running Stoke instances | Done (RS-1..RS-6) | [r1-server](../specs/r1-server.md) |
| r1-server web dashboard | Instance list + live stream view + 3D force-directed ledger visualizer (Three.js + time scrubber) | Done (RS-4) | [r1-server](../specs/r1-server.md) |
| `stoke agent-serve` HTTP | Hireable-agent facade — POST /api/task + GET /api/task/{id}; X-Stoke-Bearer auth | Done (Task 24 MVP) | [agent-serve-async](../specs/agent-serve-async.md) |
| Agent-serve async mode | Worker pool + SSE events + webhook callbacks + crash recovery | Scoped | [agent-serve-async](../specs/agent-serve-async.md) |
| MCP client | Consume external MCP servers (github, linear, slack) for worker tool access | Scoped | [mcp-client](../specs/mcp-client.md) |
| Policy engine | Cedar/OPA-style governance; `CLOUDSWARM_POLICY_ENDPOINT` no-op today | Scoped | [policy-engine](../specs/policy-engine.md) |

## Operator ergonomics

| Feature | Benefit | Status | Spec |
|---|---|---|---|
| TUI progress renderer | Live multi-line dashboard on stderr; event-bus Observe subscriber | Done (S-1) | [tui-renderer](../specs/tui-renderer.md) |
| Multi-provider pool (`STOKE_PROVIDERS`) | Mix providers by role — Anthropic for reasoning + Ollama for workers + Gemini for review | Done (S-6) | [provider-pool](../specs/provider-pool.md) |
| HITL soft-pass approval | `SoftPassApprovalFunc` on descent config; enterprise-tier HITL gate | Done (S-2 via tier1) | [cloudswarm-protocol](../specs/cloudswarm-protocol.md) + [operator-ux-memory](../specs/operator-ux-memory.md) Part B |
| `stoke plan` CLI | Structured plan review separate from `stoke ship`; produces resumable `plan.json` | Scoped | [operator-ux-commands](../specs/operator-ux-commands.md) Part A |
| Nested TUI panes (execute-phase) | Per-session/task/AC drill-down navigation; active-focus with tab/j/k/enter | Scoped | [operator-ux-commands](../specs/operator-ux-commands.md) Part C |
| Live inter-session meta-reasoner | Consolidates episodic memory between sessions into semantic/procedural for the next | Scoped | [operator-ux-commands](../specs/operator-ux-commands.md) Part E |
| `progress.md` writer | Human-readable live Markdown file at repo root — H1/H2/H3/checkbox format | Scoped | [operator-ux-commands](../specs/operator-ux-commands.md) Part F |
| Intent Gate (plan vs diagnose) | Verb-scan + Haiku fallback; DIAGNOSE masks write tools | Scoped | [operator-ux-commands](../specs/operator-ux-commands.md) |
| `stoke attach` + `stoke replay` | Client to sessionctl socket + read-only event-log timeline with follow mode | Scoped | [finishing-touches](../specs/finishing-touches.md) |
| Chat descent control + sessionctl | Chat mini-descent gate; Unix socket control plane with 8 verbs | Scoped | [chat-descent-control](../specs/chat-descent-control.md) |

## Durability + replay

| Feature | Benefit | Status | Spec |
|---|---|---|---|
| Event log (bus WAL) | Append-only NDJSON at `.stoke/bus/events.log`; `stoke resume` reporting | Done (Task 18 MVP) | [executor-foundation](../specs/executor-foundation.md) §eventlog |
| Event log proper (SQLite + hash chain) | ULID-indexed events table with parent-hash chain; `stoke sow --resume-from=<seq>` restart hook | Scoped | [event-log-proper](../specs/event-log-proper.md) |
| Fan-out generalization | Extract session-scheduler parallelism into reusable `internal/fanout/` for research + delegation consumers | Scoped | [fanout-generalization](../specs/fanout-generalization.md) |

## Memory — persistent cross-session knowledge

| Feature | Benefit | Status | Spec |
|---|---|---|---|
| Memory store MVP | `stoke_memories` table + FTS5 + triggers in the existing wisdom SQLite DB | Done (S-9 MVP) | [operator-ux-memory](../specs/operator-ux-memory.md) Part D |
| Memory full stack | sqlite-vec + 3-way embedder fallback + consolidation + 4 auto-retrieval hooks + `stoke memory` CLI (6 verbs) | Scoped | [memory-full-stack](../specs/memory-full-stack.md) |
| Scoped memory bus (worker-to-worker) | Live intra-session comms with 6 visibility scopes (ScopeSession / ScopeSessionStep / ScopeWorker / ScopeAllSessions / ScopeGlobal / ScopeAlways); writer-goroutine pattern + batched INSERTs for 10k+ ops/sec | Scoped | [memory-bus](../specs/memory-bus.md) |

## Durability + governance

| Feature | Benefit | Status | Spec |
|---|---|---|---|
| Two-level Merkle commitment | Ledger IDs depend on header+commitment, not content; content wipe preserves chain integrity forever | Scoped | [ledger-redaction](../specs/ledger-redaction.md) |
| Encryption at rest | SQLCipher (chacha20) + per-line XChaCha20-Poly1305 JSONL + OS keyring with FileBackend fallback | Scoped | [encryption-at-rest](../specs/encryption-at-rest.md) |
| Retention policies | Per-surface configurable retention; on-session-end + hourly sweep; crypto-shreds content tier without breaking Merkle chain; compliance-ready (HIPAA / GDPR / EU AI Act Art. 12) | Scoped | [retention-policies](../specs/retention-policies.md) |

## r1-server UI v2 — visual execution trace

| Feature | Benefit | Status | Spec |
|---|---|---|---|
| Waterfall + indented-tree default view | Familiar observability UX; LLM I/O as message bubbles; cost/token badges inline | Scoped | [r1-server-ui-v2](../specs/r1-server-ui-v2.md) |
| htmx + SSE + vendored ESM | Works offline; no CDN dependency; ~250KB vendored JS inside the binary | Scoped | [r1-server-ui-v2](../specs/r1-server-ui-v2.md) |
| 3D ledger graph (perf retrofit) | InstancedMesh + Web Worker simulation + aggregation time scrubber; 3000-node smooth on mid-range laptop | Scoped | [r1-server-ui-v2](../specs/r1-server-ui-v2.md) |
| Memory explorer (grouped-list default) | Scope-grouped cards with inline backlinks, FTS5 search, [Promote]/[Delete]/[+Add] | Scoped | [r1-server-ui-v2](../specs/r1-server-ui-v2.md) |
| Skill load/unload events | `skill_loaded` / `skill_unloaded` ledger nodes emitted at SkillInjector hook; 3D viz shows hexagonal prisms with opacity transitions | Scoped | [r1-server-ui-v2](../specs/r1-server-ui-v2.md) |
| Cryptographic verification UI | Every node shows ✔ verified / ⚠ unsigned / ✗ tampered based on `ledger.Verify()` hash check | Scoped | [r1-server-ui-v2](../specs/r1-server-ui-v2.md) |
| `.tracebundle` export | Content-addressed portable trace archive — other r1-servers can import | Scoped | [r1-server-ui-v2](../specs/r1-server-ui-v2.md) |
| Run diff view | Git-like tree-diff of two session runs | Scoped | [r1-server-ui-v2](../specs/r1-server-ui-v2.md) |

## Build pipeline — all scope cycles

### Scope cycle 1 (2026-04-21)

1. **research-orchestrator** (16) — 35 items
2. **browser-interactive** (17) — 47 items
3. **event-log-proper** (18) — 35 items
4. **agent-serve-async** (19) — 50 items
5. **memory-full-stack** (20) — 64 items
6. **operator-ux-commands** (21) — 68 items
7. **finishing-touches** (22) — 62 items

### Scope cycle 2 (2026-04-21) — r1-server RS-7…RS-11 + research corrections

8. **memory-bus** (23) — 58 items
9. **ledger-redaction** (24) — 48 items
10. **encryption-at-rest** (25) — 61 items
11. **retention-policies** (26) — 48 items
12. **r1-server-ui-v2** (27) — 91 items

### Parallel track — specs awaiting `/build` from prior cycles

mcp-client, policy-engine, fanout-generalization, deploy-phase2, chat-descent-control, delegation-a2a (full A2A).

---

*Last updated: 2026-04-21 (scope cycle 2)*
