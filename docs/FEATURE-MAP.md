# Feature Map

This cycle-close refresh keeps the feature inventory aligned with the shipping parity, deterministic-skills, and wizard/artifact lanes already present on `main`.

## Cycle 9 shipped additions

| Feature | Benefit | Status | Evidence |
|---------|---------|--------|----------|
| Beacon protocol foundation | Gives R1 a concrete runtime protocol instead of ad-hoc task-only coordination. | Done | PR `#45`, commit `6eba269`. |
| Hub trust layer | Adds explicit trust semantics to cross-surface runtime actions. | Done | PR `#46`, commit `35d4fc7`. |
| Missing beacon primitives | Closes the protocol gaps that blocked end-to-end beacon operation. | Done | PR `#47`, commit `10f00cf`. |
| Wave D expansion | Extends the operator/runtime surface beyond parity into higher-order workflows. | Done | PR `#48`, commit `f2d30d6`. |
| Wave D post-merge command set | Lands the follow-on command work that completes the wave. | Done | PR `#49`, commit `57906b9`. |
| Beacon-era canonical docs | Refreshes the seven canonical docs so evaluation and onboarding match trunk reality. | Done | PR `#50`, commit `60e38a6`; PR `#52`, commit `00a34b5`. |
| Replay-safe deterministic cache attribution | Keeps deterministic replay cache entries and honest-cost reporting tied to the exact compiled skill/input boundary instead of a looser mission bucket. | Done | PR `#63`, commit `2b037a3`. |

## W36 parity, deterministic-skills, and wizard/artifact status

### Done

- Live parity matrix.
- Evaluation-agent skill.
- Manifest-enforced skill manufacturing pipeline.
- Deterministic skills substrate: compile, analyze, interpreter, registry, and proof-emitting CLI.
- Shell preprocessing and path-scoped skill activation.
- Skill wizard flow: `stoke wizard run`, `migrate`, `register`, and `query`.
- `ask_user` primitive and decision-ledger capture inside the wizard lane.
- Beacon protocol foundation: identity, pairing, session, token, and ledger node coverage.
- Bulk migration adapters for Markdown, OpenAPI, Zapier, and TOML skill sources.
- Artifact ledger nodes plus Antigravity import/export wire format.
- Artifact storage and `stoke artifact` CLI for import/export and inspection workflows.
- Ledger-native plan artifact and plan approval emission from `stoke plan --approve`.
- Wave B receipts, honesty decisions, honest-cost reports, and replay-cache-key hardening.
- Wave D counterfactual replay, decision-bisector narratives, and self-tune recommendations.
- Bundled pack install, update, and recursive skill-pack composition plus shared-library distribution.

### In Progress

- Parity-to-superiority execution wave.
- Deterministic-skills adoption across more execution and packaging surfaces.

### Scoping

- More explicit superiority reporting, artifact publishing, and marketplace loops.

## Wave 3 (2026-04-30) — Deterministic Skills + Wizard + Artifact Ledger

| Feature | Benefit | Status | Evidence |
|---------|---------|--------|----------|
| Deterministic skill IR (`internal/r1skill`) | Skill execution can move from prompt-only interpretation to typed, inspectable, replayable programs. | Done | PR #34, commit `1492ab5`. |
| `r1-skill-compile` CLI | Operators can compile or `--check` a deterministic skill before shipping it into a library. | Done | PR #34, commit `1492ab5`. |
| Analyzer + compile proof output | Every deterministic skill can emit a proof artifact showing what the compiler accepted. | Done | PR #34, commit `1492ab5`. |
| Registry-backed deterministic execution | `useIR=true` manifests route through the deterministic runtime instead of the prompt-only path. | Done | PR #34, commit `1492ab5`. |
| Deterministic echo example skill | The substrate ships with a concrete example and proof file operators can inspect end-to-end. | Done | PR #34, commit `1492ab5`. |
| `stoke wizard run` | A guided operator flow can create or refine skill configurations without hand-authoring every manifest field. | Done | PR #36, commit `98203a7`. |
| `stoke wizard migrate` | Existing skill sources can be bulk-migrated into the new deterministic substrate. | Done | PR #36, commit `98203a7`. |
| `stoke wizard register` | Reviewed skill IR and compile proofs can be copied into the deterministic registry in a stable on-disk layout. | Done | PR #44, commit `80f721f`. |
| `stoke wizard query` | Operators can interrogate wizard state and migration outputs from the CLI, including ledger-backed sessions. | Done | PR #44, commit `80f721f`. |
| `ask_user` primitive | Wizard flows can pause for operator judgment instead of guessing through trust-boundary decisions. | Done | PR #36, commit `98203a7`. |
| Decision ledger for wizard runs | Wizard choices become durable governance data instead of disposable terminal interaction, with linked source / IR / proof refs when persisted to the ledger. | Done | PR #44, commit `80f721f`. |
| Wizard migration adapters | Markdown, OpenAPI, Zapier, and TOML sources can be normalized into the deterministic skill lane. | Done | PR #36, commit `98203a7`. |
| `stoke skills pack install` | Bundled packs like `actium-studio` can be activated without hand-made symlinks; canonical and legacy skill dirs are linked together in one command. | Done | PR #67, commit `fc55a0d`. |
| Recursive pack composition + shared-library resolution | Pack dependencies declared in `pack.yaml` now install transitively, and pack lookup reads both repo-local and user-level `.r1/.stoke` libraries. | Done | PR #68, commit `bf45191`. |
| `stoke skills pack list` | Operators can audit which packs are currently installed from the merged `.r1` / `.stoke` skill-link view without inspecting symlinks by hand. | Done | `cmd/stoke/skills_pack_cmd.go`; `cmd/stoke/skills_pack_cmd_test.go` |
| `stoke skills pack update` | Operators can refresh an installed pack from its current source, safely skip repo-local bundled checkouts, and fast-forward external git-backed pack repos before relinking new dependencies. | Done | `cmd/stoke/skills_pack_cmd.go`; `cmd/stoke/skills_pack_cmd_test.go` |
| Beacon protocol foundation | Identity material, pairing claims, session state, tokens, and ledger-native beacon records are now first-class runtime surfaces instead of external glue. | Done | PR #45, commit `6eba269`. |
| Artifact storage backend | Plans, proofs, approvals, and converted skill assets can be stored and replayed as first-class artifacts. | Done | PR #37, commit `e8608b1`. |
| `stoke artifact` CLI | Artifact inspection, import, and export become a supported operator path instead of an internal-only primitive. | Done | PR #37, commit `e8608b1`. |
| Antigravity converter | External artifact formats can be converted into the R1 artifact model without ad hoc glue scripts. | Done | PR #37, commit `e8608b1`. |
| Plan approval ledger nodes | `stoke plan --approve` now emits explicit plan and approval nodes into the governance graph. | Done | PR #37, commit `e8608b1`. |
| Beacon trust validation layer | Inbound beacon traffic is checked against pinned roots, signed signal frames, freshness windows, nonce replay defense, and hardcoded signal kinds before it is trusted. | Done | PR #46, commit `35d4fc7`. |
| Beacon review and notify primitives | Offline review envelopes and beacon-targeted notify metadata complete the first practical handoff surfaces around the beacon lane. | Done | PR #47, commit `10f00cf`. |

### Potential-On Horizon

- Portfolio-wide deterministic skill exchange and marketplace surfaces.

Every feature R1 has or will have, grouped by user-visible outcome.
For each: what it does, the benefit to the operator, current build
status, and the spec it traces to.

Status legend:

- **Done** — shipped in the current trunk, passing build/test/vet,
  race-clean, and integrated into the default execution path.
- **Done (flagged)** — shipped but behind an env var or CLI flag.
- **Ready** — spec file in `specs/` at STATUS: ready, awaiting build.
- **Scoped** — spec in flight or under review.
- **Horizon** — on the roadmap, not yet scoped.

## Wave 2 (2026-04-26) — R1-Parity Sprint Additions

| Feature | Benefit | Status | Evidence |
|---------|---------|--------|----------|
| Browser tools `wait_for`, `get_html` | Tasks that need real DOM state can wait for elements and pull rendered HTML — flaky timing issues disappear. | Done | T-R1P-001/002; PR #12, commit `7144b6f`. |
| Manus-style autonomous operator | A single agent can drive a multi-step browser flow without per-step prompts. | Done | T-R1P-002; PR #15, commit `f8d8d1c`. |
| LSP server adapter (`stoke-lsp`) | Any LSP-enabled editor (Neovim, Helix, Sublime) drives R1 with zero plugin work. | Done | T-R1P-009; PR #13, commit `3cc1b6f`. |
| Multi-language LSP client | LSP support spans the full set of languages R1 already lints, not just Go. | Done | T-R1P-020; PR #17, commit `4042692`. |
| VS Code IDE plugin | Native panel inside VS Code drives R1 missions with one click. | Done | T-R1P-003; PR #16, commit `e6393c8`. |
| JetBrains IDE plugin | Native panel inside any JetBrains IDE drives R1 missions. | Done | T-R1P-003; PR #16, commit `e6393c8`. |
| GitHub Actions adapter | Drop R1 into a GitHub workflow with a single `uses:` step. | Done | T-R1P-021; PR #14, commit `f8d8d1c`. |
| GitLab CI adapter | Drop R1 into a GitLab pipeline with one `include:` line. | Done | T-R1P-022; PR #14, commit `f8d8d1c`; PR #17, commit `4042692`. |
| CircleCI adapter | Drop R1 into a CircleCI orb. | Done | T-R1P-020; PR #14, commit `f8d8d1c`. |
| Tauri subprocess launcher | Cross-platform desktop GUI launches the orchestrator without a separate install. | Done | R1D-1.1/1.2/1.3/1.4; commit `693e241`. |
| Real robotgo backend on the desktop GUI | Desktop GUI drives real input/output instead of a stub. | Done | T-R1P-009 follow-up; PR #19, commit `841a494`. |
| Tool surface wired into `Handle()` | The full Wave 2 tool kit (image_read, notebook_read/cell_run, powershell, gh_pr/run) is selectable by the executor. | Done | T-R1P-004/005/015/016; PR #9, commit `cbe0ae1`. |
| `web_fetch` / `web_search` / `cron` / `pdf_read` tools | The model can pull external context, schedule, and parse PDFs without a separate agent. | Done | T-R1P-007/008/006/023; commit `20228bf`. |
| Shell injection preprocessing | Skill activations can carry shell snippets without injection risk. | Done | T-R1P-018; commit `13afd78`. |
| Path-scoped skill activation | Skills can be scoped to a path glob; reduces cross-mission interference. | Done | T-R1P-019; commit `13afd78`. |
| Veritize-Verity dual-send headers | Outbound HTTP includes both `X-Veritize-*` and `X-Verity-*` during the rename window. | Done | PR #8, commit `6ed5bb8`. |
| Cloud Build CI cutover + local pre-push hook | CI runs in our own GCP project; developers get fast pre-push feedback. | Done | PR #11, commit `a883825`. |
| CI/CD + desktop alternate-path test | Removed runtime alternate-path flag; added negative-path test. | Done | PRs #18-21; commits `bd6de28`, `2607578`. |

## Wave 2 Status Summary

- **Done:** every Wave 2 row above is on `main`.
- **In Progress:** Manus operator hardening behind a per-mission toggle;
  LSP feature coverage beyond hover/definition/diagnostics.
- **Scoped:** IDE plugin marketplace publishing; headless desktop GUI for
  CI screenshot tests.
- **Scoping:** cross-machine session migration; per-tool throttling
  policy in `.stoke/`.
- **Potential-On Horizon:** BitBucket Pipelines adapter parity; native
  MCP bundle in IDE plugins; remote-browser sandboxing for browser tools.

## Wave B (2026-04-29) — Receipts And Honesty

| Feature | Benefit | Status | Evidence |
|---------|---------|--------|----------|
| `B1` Mission receipts index | Operators can persist, list, export, and sign task-level receipts instead of treating raw anchors as the only audit surface. | Done | `internal/receipts/`, `cmd/stoke/receipt_cmd.go` |
| Replay-backed receipt generation | Replays can be promoted into durable receipts with task linkage and provenance. | Done | `internal/receipts/store.go`, `internal/receipts/store_test.go` |
| `B17` Refuse-to-Lie decisions | R1 can refuse unsupported claims and preserve that refusal in the ledger. | Done | `internal/honesty/`, `cmd/stoke/honesty_cmd.go` |
| `B18` Why-Not decisions | Skipped, deferred, and downgraded actions become queryable records instead of loose prose. | Done | `internal/honesty/`, `cmd/stoke/honesty_cmd.go` |
| `B19` Honest cost rollups | Cost reports now break down provider groups, metered-equivalent spend, subscription-vs-metered margin, and human-minute equivalents. | Done | PR #63, commit `2b037a3`; `internal/costtrack/honest_cost.go`, `cmd/stoke/ops_cost.go` |
| Deterministic replay cache-key namespacing | Replay cache entries are keyed by IR hash plus canonicalized cache-key inputs, so equivalent JSON shapes replay bit-exactly and unrelated skills do not share cache entries. | Done | PR #63, commit `2b037a3`; `internal/r1skill/interp/interp.go` |

## Wave D (2026-04-30) — Expansion Features

| Feature | Benefit | Status | Evidence |
|---------|---------|--------|----------|
| `stoke cf` counterfactual replay | Operators can replay a mission snapshot with deterministic config changes and inspect divergence from the original outcome. | Done | PRs #48 and #49. |
| Knob application + deterministic run IDs | The same mission snapshot plus the same knob set yields the same counterfactual run identity. | Done | PR #48. |
| `stoke why-broken` decision bisector | Regressions can be explained as a step-by-step decision narrative with an auto-generated gotcha learning. | Done | PRs #48 and #49. |
| `stoke self-tune` recommendation engine | Operators can compare harness trials against a baseline and emit a non-regressing tuning recommendation. | Done | PRs #48 and #49. |

## The trust layer — verification descent

Verification descent refuses to believe a model when it says "done."
It runs the anti-deception contract, parses actual completion
evidence, catches ghost writes, caps repair loops, and plugs
non-code executors into the same ladder.

| Feature | Benefit | Status | Spec |
|---|---|---|---|
| Anti-deception contract in worker prompts | Workers cannot silently fake completion — truthfulness block injected at dispatch | Done | [descent-hardening](../specs/descent-hardening.md) |
| Forced self-check before turn end | Model signals tangible completion evidence; parser cross-checks against git + AC state | Done | [descent-hardening](../specs/descent-hardening.md) |
| Bootstrap per descent cycle | Manifest-touching repairs re-install deps before next AC — no stale-workspace false failures | Done | [descent-hardening](../specs/descent-hardening.md) |
| Per-file repair cap (3 attempts, Cursor 2.0 parity) | Infinite repair loops end | Done | [descent-hardening](../specs/descent-hardening.md) |
| Ghost-write detector | Post-tool supervisor hook catches "tool reported success but file is empty" fakes | Done | [descent-hardening](../specs/descent-hardening.md) |
| Env-issue worker tool | Worker self-reports environment blockers; descent skips multi-analyst — saves ~$0.10/AC | Done | [descent-hardening](../specs/descent-hardening.md) |
| VerifyFunc on acceptance criteria | Non-code executors (research/browse/deploy/delegate) plug into the 8-tier ladder | Done | [executor-foundation](../specs/executor-foundation.md) |
| Soft-pass AC after 2× `ac_bug` verdicts | Reviewers blaming the AC can't spin forever | Done | [descent-hardening](../specs/descent-hardening.md) |
| T8 soft-pass → session verdict | Top-tier soft-pass propagates to the session-level verdict (H-91b) | Done | [descent-hardening](../specs/descent-hardening.md) |
| JSONL tool-call log + reviewer injection | Reviewer sees what the worker actually did, not what it claimed | Done | [descent-hardening](../specs/descent-hardening.md) |
| Correlation IDs + SOW snapshot | Every worker turn is traceable back to the SOW snapshot it was given | Done (H-91d) | [descent-hardening](../specs/descent-hardening.md) |
| Attempt history for T4 | Retry context carries forward per-AC attempt history (H-91g) | Done | [descent-hardening](../specs/descent-hardening.md) |
| `STOKE_DESCENT=1` opt-in flag | Ship the engine behind a flag; flip to default after bake-in | Done (flagged) | [descent-hardening](../specs/descent-hardening.md) |

## Prompt-injection hardening

Every file-to-prompt ingest path is scanned, every tool output is
sanitized, every end-of-turn is gated against honeypots.

| Feature | Benefit | Status | Spec |
|---|---|---|---|
| Promptguard wired into 4 ingest paths | Every file-to-prompt flow is scanned (skills, failure analysis, feasibility gate, convergence judge) | Done | Portfolio WORK-stoke.md Track A 1 |
| Tool-output sanitization | 200KB cap + chat-template-token scrub + injection-shape annotation on every tool_result | Done | Portfolio Track A 2 |
| Honeypot pre-end-turn gate | Canary + markdown-exfil + role-injection + destructive-without-consent; turn aborts on fire | Done | Portfolio Track A 3 |
| Websearch domain allowlist + body cap | Operator-configurable glob allowlist; 100KB body cap on every fetch | Done | Portfolio Track A 4 |
| MCP sanitization audit marker | Per-CallTool marker asserts LLM vs code classification; grep-able maintenance check | Done | Portfolio Track A 5 |
| Red-team corpus | 58-sample regression suite across 4 categories (OWASP LLM01, CL4R1T4S, SpAIware, Willison); ≥60% detection per category | Done | Portfolio Track A 6 |
| SECURITY.md + disclosure policy | GitHub Security Advisories preferred channel; honor list for responsible disclosure | Done | [SECURITY.md](../SECURITY.md) |
| Known-miss advancement | 1 known-miss corpus sample being advanced into detection | Scoped | [finishing-touches](../specs/finishing-touches.md) |

## Executor architecture — multi-task agent

One `Executor` interface, many backends. `stoke task "<free text>"`
routes via a classifier.

| Feature | Benefit | Status | Spec |
|---|---|---|---|
| Executor interface + router | Uniform Execute/BuildCriteria/BuildRepairFunc/BuildEnvFixFunc surface; natural-language task routing | Done | [executor-foundation](../specs/executor-foundation.md) |
| `stoke task` CLI | Free-text entry point — router classifies + dispatches | Done | [executor-foundation](../specs/executor-foundation.md) |
| CodeExecutor (SOW-backed) | Existing SOW pipeline wrapped behind the executor interface | Done | [executor-foundation](../specs/executor-foundation.md) |
| ResearchExecutor MVP | Single-agent, stdlib-only; keyword-overlap claim verification | Done | [browser-research-executors](../specs/browser-research-executors.md) Part 2 |
| Research lead+subagent orchestrator | Anthropic-style orchestrator-worker; lead decomposes, N subagents fan out in parallel | Scoped | [research-orchestrator](../specs/research-orchestrator.md) |
| BrowserExecutor Part 1 (http) | Fetch + HTML strip + VerifyContains/VerifyRegex; `stoke browse` CLI | Done | [browser-research-executors](../specs/browser-research-executors.md) Part 1 |
| BrowserExecutor Part 2 (go-rod) | Interactive headless browser: click, type, wait, screenshot, vision diff | Scoped | [browser-interactive](../specs/browser-interactive.md) |
| DeployExecutor (Fly.io) | `stoke deploy` with dry-run + health-check ACs; subprocess via `flyctl` | Done | [deploy-executor](../specs/deploy-executor.md) |
| DeployExecutor Phase 2 (Vercel + Cloudflare) | Additional provider adapters | Scoped | [deploy-phase2](../specs/deploy-phase2.md) |
| DelegationExecutor MVP (verify-settle) | Hired-agent deliverable verification + settlement via TrustPlane | Done | [delegation-a2a](../specs/delegation-a2a.md) |
| DelegationExecutor full (A2A protocol) | HMAC tokens + trust clamp + x402 micropayments + signed cards + JWKS + saga compensators | Scoped | [delegation-a2a](../specs/delegation-a2a.md) |

## Protocol surfaces — external integrations

| Feature | Benefit | Status | Spec |
|---|---|---|---|
| CloudSwarm NDJSON + HITL | Two-lane emitter; `hitl_required` gate on stdin; `stoke run` subcommand | Done | [cloudswarm-protocol](../specs/cloudswarm-protocol.md) |
| STOKE envelope (v1.0) | Every event carries `stoke_version`, `instance_id`, `trace_parent`, optional `ledger_node_id` | Done | [stoke-protocol](stoke-protocol.md) |
| r1-server binary | Separate daemon ingesting r1.session.json + event log + ledger DAG from running R1 instances | Done | [r1-server](../specs/r1-server.md) |
| r1-server web dashboard (MVP) | Instance list + live-tailing stream view + event-type filter + auto-scroll | Done | [r1-server](../specs/r1-server.md) |
| r1-server UI v2 (waterfall + 3D) | Waterfall + tree view, LLM I/O bubbles, 3D force-directed ledger viz | Scoped | [r1-server-ui-v2](../specs/r1-server-ui-v2.md) |
| `stoke agent-serve` HTTP MVP | Hireable-agent facade — POST /api/task + GET /api/task/{id}; X-Stoke-Bearer auth | Done | [agent-serve-async](../specs/agent-serve-async.md) |
| Agent-serve async mode | Worker pool + SSE events + webhook callbacks + crash recovery | Scoped | [agent-serve-async](../specs/agent-serve-async.md) |
| MCP client | Consume external MCP servers (github, linear, slack, postgres, custom) for worker tool access | Done (flagged) | [mcp-client](../specs/mcp-client.md) |
| MCP trust gating + circuit breaker | Per-server trust label, concurrency cap, auth redactor, closed/open/half-open breaker | Done | [mcp-security](mcp-security.md) |
| Policy engine | Cedar/OPA-style governance; `CLOUDSWARM_POLICY_ENDPOINT` no-op today | Scoped | [policy-engine](../specs/policy-engine.md) |
| TrustPlane real client (DPoP + RFC 9449) | Stdlib-only Ed25519 DPoP; no go-jose dep; env-driven key loading | Done | [trustplane-integration](trustplane-integration.md) |
| A2A Agent Card v1.0.0 + canonical path | `/.well-known/agent-card.json` canonical; legacy `agent.json` 308-redirects with Deprecation + Sunset headers | Done | [CHANGELOG.md](../CHANGELOG.md) T22 |
| ACP adapter (`stoke-acp`) | Agent Client Protocol adapter exposes R1 to ACP-aware editors | Done | S-U-002 |

## Operator ergonomics

| Feature | Benefit | Status | Spec |
|---|---|---|---|
| TUI progress renderer | Live multi-line dashboard on stderr; event-bus Observe subscriber | Done | [tui-renderer](../specs/tui-renderer.md) |
| Bubble Tea interactive TUI | Full-screen Dashboard / Focus / Detail panes | Done | [tui-renderer](../specs/tui-renderer.md) |
| Multi-provider pool (`STOKE_PROVIDERS`) | Mix providers by role — Anthropic for reasoning + Ollama for workers + Gemini for review | Done | [provider-pool](../specs/provider-pool.md) |
| HITL soft-pass approval | `SoftPassApprovalFunc` on descent config; enterprise-tier HITL gate | Done | [cloudswarm-protocol](../specs/cloudswarm-protocol.md) + [operator-ux-memory](../specs/operator-ux-memory.md) Part B |
| `stoke plan` CLI | Structured plan review separate from `stoke ship`; produces resumable `plan.json` | Scoped | [operator-ux-commands](../specs/operator-ux-commands.md) Part A |
| Nested TUI panes (execute-phase) | Per-session/task/AC drill-down navigation; active-focus with tab/j/k/enter | Scoped | [operator-ux-commands](../specs/operator-ux-commands.md) Part C |
| Live inter-session meta-reasoner | Consolidates episodic memory between sessions into semantic/procedural for the next | Scoped | [operator-ux-commands](../specs/operator-ux-commands.md) Part E |
| `progress.md` writer | Human-readable live Markdown file at repo root — H1/H2/H3/checkbox format | Scoped | [operator-ux-commands](../specs/operator-ux-commands.md) Part F |
| Intent Gate (plan vs diagnose) | Verb-scan + Haiku fallback; DIAGNOSE masks write tools | Scoped | [operator-ux-commands](../specs/operator-ux-commands.md) |
| `stoke attach` + `stoke replay` | Client to sessionctl socket + read-only event-log timeline with follow mode | Scoped | [finishing-touches](../specs/finishing-touches.md) |
| Chat descent control + sessionctl | Chat mini-descent gate; Unix socket control plane with 8 verbs | Scoped | [chat-descent-control](../specs/chat-descent-control.md) |
| `stoke doctor` | Tool-dependency check across all 5 fallback chain providers | Done | [QUICKSTART](../specs/QUICKSTART.md) |
| `stoke repair` | Auto-fix common configuration issues | Done | [QUICKSTART](../specs/QUICKSTART.md) |
| `stoke memory` CLI (6 verbs) | add, list, get, promote, delete, search over persistent cross-session memory | Done (flagged) | [memory-full-stack](../specs/memory-full-stack.md) |

## Durability + replay

| Feature | Benefit | Status | Spec |
|---|---|---|---|
| Event log (bus WAL) | Append-only NDJSON at `.stoke/bus/events.log`; `stoke resume` reporting | Done | [executor-foundation](../specs/executor-foundation.md) §eventlog |
| Event log proper (SQLite + hash chain) | ULID-indexed events table with parent-hash chain; `stoke sow --resume-from=<seq>` restart hook | Scoped | [event-log-proper](../specs/event-log-proper.md) |
| Fan-out generalization | Extract session-scheduler parallelism into reusable `internal/fanout/` for research + delegation consumers | Done | [fanout-generalization](../specs/fanout-generalization.md) |
| `.tracebundle` export | Content-addressed portable trace archive — other r1-servers can import | Scoped | [r1-server-ui-v2](../specs/r1-server-ui-v2.md) |
| Session snapshot + restore | Pre-merge snapshot of protected baseline; restore on merge failure | Done | [ARCHITECTURE](ARCHITECTURE.md) |

## Memory — persistent cross-session knowledge

| Feature | Benefit | Status | Spec |
|---|---|---|---|
| Memory store MVP | `stoke_memories` table + FTS5 + triggers in the existing wisdom SQLite DB | Done | [operator-ux-memory](../specs/operator-ux-memory.md) Part D |
| Memory full stack | sqlite-vec + 3-way embedder fallback + consolidation + 4 auto-retrieval hooks + `stoke memory` CLI (6 verbs) | Scoped | [memory-full-stack](../specs/memory-full-stack.md) |
| Scoped memory bus (worker-to-worker) | Live intra-session comms with 6 visibility scopes (ScopeSession / ScopeSessionStep / ScopeWorker / ScopeAllSessions / ScopeGlobal / ScopeAlways); writer-goroutine pattern + batched INSERTs for 10k+ ops/sec | Scoped | [memory-bus](../specs/memory-bus.md) |
| Wisdom temporal validity | ValidFrom/ValidUntil, AsOf() query, Invalidate() | Done | [ARCHITECTURE](ARCHITECTURE.md) |

## Durability + governance

| Feature | Benefit | Status | Spec |
|---|---|---|---|
| Two-level Merkle commitment | Ledger IDs depend on header+commitment, not content; content wipe preserves chain integrity forever | Scoped | [ledger-redaction](../specs/ledger-redaction.md) |
| Encryption at rest | SQLCipher (chacha20) + per-line XChaCha20-Poly1305 JSONL + OS keyring with FileBackend fallback | Scoped | [encryption-at-rest](../specs/encryption-at-rest.md) |
| Retention policies | Per-surface configurable retention; on-session-end + hourly sweep; crypto-shreds content tier without breaking Merkle chain; compliance-ready (HIPAA / GDPR / EU AI Act Art. 12) | Scoped | [retention-policies](../specs/retention-policies.md) |

## V2 governance layer

R1 v2 wraps the execution engine in a multi-role consensus layer.

| Feature | Benefit | Status | Spec |
|---|---|---|---|
| Append-only content-addressed ledger | 22 node types, 7 edge types, SHA256 + 16 prefixes, filesystem + SQLite backends | Done | [v2-overview](architecture/v2-overview.md) |
| Durable WAL-backed event bus | 30+ event types, hooks, delayed events, parent-hash causality | Done | [bus](architecture/bus.md) |
| 30 deterministic supervisor rules | 10 categories (consensus, drift, hierarchy, research, skill, snapshot, SDM, cross-team, trust, lifecycle); 3 per-tier manifests | Done | [supervisor-rules](architecture/supervisor-rules.md) |
| 7-state consensus loop tracker | PRD → SOW → ticket → PR → landed lifecycle | Done | [v2-overview](architecture/v2-overview.md) |
| 11 stance roles | PO, CTO, QA Lead, Reviewer, Dev, Researcher, SDM, Deployer, Harness + per-role tool authorization | Done | [harness-stances](architecture/harness-stances.md) |
| Concern field projection | 10 sections × 9 role templates render role-specific system prompts from ledger state | Done | [v2-overview](architecture/v2-overview.md) |
| Skill manufacturing pipeline | 4 workflows + confidence ladder produces reusable playbooks | Done | [skill-pipeline](architecture/skill-pipeline.md) |
| Deterministic skills substrate | Typed IR, 8-stage analyzer, compile proofs, opt-in deterministic execution for `useIR=true` manifests | In Progress | [SKILLS-DETERMINISTIC](SKILLS-DETERMINISTIC.md) |
| V1-to-V2 bridge adapters | cost/verify/wisdom/audit emit bus events + ledger nodes automatically | Done | [bridge](architecture/bridge.md) |
| Content-addressed ID generation | SHA256, 16 node-type prefixes, collision-safe across backends | Done | `internal/contentid/` |
| Structured error taxonomy (10 codes) | Uniform `stokerr.Error` with code + context; `errors.As` everywhere | Done | `internal/stokerr/` |
| Snapshot protection | Baseline manifest (file paths + content hashes); pre-merge + restore-on-failure | Done | `internal/snapshot/` |
| First-time config wizard (presets) | minimal / balanced / strict presets | Done | [wizard](architecture/wizard.md) |
| 12 MCP memory tools | Expose ledger, wisdom, research, skill stores as MCP | Done | [ARCHITECTURE](ARCHITECTURE.md) |

## Execution engine

| Feature | Benefit | Status | Spec |
|---|---|---|---|
| Native agentic tool-use loop (Anthropic Messages API) | Parallel tools, prompt caching, direct-API path — no CLI dependency | Done | [agentloop](architecture/agentloop.md) |
| 5-provider fallback chain | Claude → Codex → OpenRouter → Direct API → lint-only | Done | [providers](architecture/providers.md) |
| Cross-model review gate | Claude implements → Codex reviews (or vice versa); dissent blocks merge | Done | `internal/model/` |
| GRPW priority scheduling | Tasks with most downstream work dispatch first; file-scope conflict detection | Done | `internal/scheduler/` |
| Speculative parallel execution (`--specexec`) | 4 strategies in parallel, pick winner by verification | Done | `internal/specexec/` |
| 10 failure classes with language-specific parsers | TS/Go/Python/Rust/Clippy parsers; fingerprint dedup; same-error-twice escalation | Done | `internal/failure/` |
| Dependency-aware test selection | `testselect.BuildGraph()` narrows `go test` to affected packages | Done | `internal/testselect/` |
| Adversarial self-audit convergence checks | Mission-level convergence validation | Done | `internal/convergence/` |
| Pre-commit AST-aware critic | Secrets, injection, debug prints, empty catches | Done | `internal/critic/` |
| 17 review personas | Security, performance, a11y, DX, testing, doc, ... auto-selected per task | Done | `internal/audit/` |
| Ember integration (Phases 1-3) | Managed AI routing, burst compute, remote progress | Done | [ROADMAP](ROADMAP.md) |
| Ranked repomap injection | PageRank over import graph, token-budgeted `RenderRelevant()` | Done | `internal/repomap/` |
| L0-L3 context budget framing | Identity, Critical, Topical, Deep tiers with progressive compaction | Done | [context-budget](architecture/context-budget.md) |
| Auto-infer task dependencies from file scope overlap | Missing deps caught at plan validation time | Done | `internal/plan/` |
| Shared dependency symlinks | node_modules, vendor, .venv shared across worktrees | Done | `internal/worktree/` |

## CI / release / productionization

| Feature | Benefit | Status | Spec |
|---|---|---|---|
| CI gate: build + test + vet | Three commands pin every PR | Done | `.github/workflows/ci.yml` |
| Race detector green across repo | Any new race fails CI, not advisory | Done | streamjson TwoLane fix |
| `golangci-lint` advisory | Findings surface as warnings; 30-PR cleanup campaign closed 600+ findings | Done | `.github/workflows/ci.yml` |
| `govulncheck` + `gosec` | Findings surface as warnings; stdlib vulns → Go upgrade | Done | `.github/workflows/ci.yml` |
| Nightly bench workflow | HTML report artifacts, regression detection | Done | `.github/workflows/bench-nightly.yml` |
| CLA Assistant workflow | Contributor CLA signature enforced at PR time | Done | `.github/workflows/cla.yml` |
| goreleaser + cosign keyless OIDC signing | Signed cross-platform releases (linux/darwin × amd64/arm64) | Done | `.github/workflows/release.yml` |
| Homebrew tap (`RelayOne/homebrew-r1-agent`, legacy `ericmacdougall/homebrew-stoke`) | `brew install` via goreleaser formula | Done | [install.sh](../install.sh) |
| Docker image (`ghcr.io/RelayOne/r1`, legacy `ghcr.io/ericmacdougall/stoke`) | Multi-stage distroless runtime | Done | [Dockerfile](../Dockerfile) |
| Dockerfile.pool (worker image) | macOS Keychain isolation workaround via Docker volumes | Done | [Dockerfile.pool](../Dockerfile.pool) |
| Package-count drift check | `make check-pkg-count` asserts 180 internal packages | Done | [Makefile](../Makefile) |
| Self-scan dogfooding | 18+ rules over 1,010 Go source files; zero blocking | Done | `internal/scan/` |
| Negative hook tests (12 attack payloads) | All blocked | Done | `internal/hooks/` |
| OAuth endpoint contract test | Forward-compatibility validation | Done | `internal/subscriptions/` |
| Bench corpus (20 tasks × 5 categories) | Security, correctness, refactoring, features, testing | Done | `bench/` |
| SWE-bench Pro evaluation path | Published; separate methodology | Done | [benchmark-stance](benchmark-stance.md) |
| Productionization docs | SECURITY.md, CONTRIBUTING.md, CHANGELOG.md, Makefile, GOVERNANCE.md, CLA.md, CODE_OF_CONDUCT.md, STEWARDSHIP.md | Done | Repo root |
| Architecture sub-docs | 19 files under docs/architecture/ | Done | [docs/architecture/](architecture/) |
| Historical design docs preserved | Original design rationale retained in docs/history/ | Done | [docs/history/](history/) |

## r1-server UI v2 — visual execution trace

| Feature | Benefit | Status | Spec |
|---|---|---|---|
| Waterfall + indented-tree default view | Familiar observability UX; LLM I/O as message bubbles; cost/token badges inline | Scoped | [r1-server-ui-v2](../specs/r1-server-ui-v2.md) |
| htmx + SSE + vendored ESM | Works offline; no CDN dependency; ~250KB vendored JS inside the binary | Scoped | [r1-server-ui-v2](../specs/r1-server-ui-v2.md) |
| 3D ledger graph (perf retrofit) | InstancedMesh + Web Worker simulation + aggregation time scrubber; 3000-node smooth on mid-range laptop | Scoped | [r1-server-ui-v2](../specs/r1-server-ui-v2.md) |
| Memory explorer (grouped-list default) | Scope-grouped cards with inline backlinks, FTS5 search, Promote/Delete/+Add | Scoped | [r1-server-ui-v2](../specs/r1-server-ui-v2.md) |
| Skill load/unload events | `skill_loaded` / `skill_unloaded` ledger nodes; 3D viz shows hexagonal prisms with opacity transitions | Scoped | [r1-server-ui-v2](../specs/r1-server-ui-v2.md) |
| Cryptographic verification UI | Every node shows verified / unsigned / tampered based on `ledger.Verify()` hash check | Scoped | [r1-server-ui-v2](../specs/r1-server-ui-v2.md) |
| Run diff view | Git-like tree-diff of two session runs | Scoped | [r1-server-ui-v2](../specs/r1-server-ui-v2.md) |

## 30-PR cleanup campaign — "put the repo on a race-clean footing"

Shipped this session. Each line is a landed PR on `main`.

| PR | Area | Findings closed |
|---|---|---|
| #5 | OSS-hub governance | GOVERNANCE + CLA + goreleaser brews + cosign keyless OIDC |
| #6 | Tests | four pre-existing test failures on `main` |
| #7 | CI | Go-version drift on lint + security jobs |
| #8 | Race | streamjson TwoLane stop-channel race + .site/ gitignore |
| #9 | Lint | unconvert + ineffassign + wastedassign (batch 1) |
| #10 | Lint | makezero + ineffassign + wastedassign (batch 2) |
| #11 | Lint | forcetypeassert on production paths |
| #12 | Lint | noctx context-aware HTTP probes in cmd/stoke |
| #13 | Lint | staticcheck SA4000/SA4006/SA4010/SA1019/SA1024/SA9003 |
| #14 | Lint | gosec-security + govet-nilness + errorlint on production paths |
| #15 | Lint | errorlint %w + errors.As on production paths |
| #16 | Lint | goconst — extract repeated string literals to package consts |
| #17 | Lint | prealloc — pre-allocate slices in bounded loops |
| #18 | Lint | test-file forcetypeassert + G306 (0644 → 0600) |
| #19 | Lint | predeclared — rename variables shadowing Go builtins |
| #20 | Lint | unused — remove dead code across 21 files (~340 lines) |
| #21 | Lint | exhaustive — close switch coverage gaps across 21 files |
| #22 | Lint | gosimple — 13 staticcheck findings |
| #23 | Lint | gocritic — 22 findings across exitAfterDefer + misc |
| #24 | Lint | errname — rename identifiers to match Go error conventions |
| #25 | Lint | nilerr — 37 findings across 22 files |
| #26 | Lint | prealloc + goconst — round 2 |
| #28 | Lint | revive — indent-error-flow in sow_convert |
| #29 | Lint | errorlint — close remaining 35 findings |

Race-clean gate in place. Advisory-lint posture documented. OSS-hub
governance shipped: `GOVERNANCE.md`, `CONTRIBUTING.md`, `CLA.md`,
`CODE_OF_CONDUCT.md`, `STEWARDSHIP.md`, `SECURITY.md`, goreleaser
Homebrew publishing, cosign keyless OIDC signing.

---

*Last updated: 2026-04-23 (holistic refresh after 30-PR lint + race + OSS-hub campaign).*
| Deterministic skill wizard | `stoke wizard run|migrate|register|query` with decision ledger, registry install, and compile proof output | Done | [SKILL-WIZARD.md](SKILL-WIZARD.md) |

---

## Cycle 29 Status Refresh

### Done

- Deterministic skills moved from a partial import path to a fuller pack lane on trunk: PR #67 (`fc55a0d`) bundled installer, PR #68 (`bf45191`) recursive install, PR #69 (`4a19231`) uninstall, and PR #71 (`92b6f47`) list.
- PR #70 (`d15bee8`) refreshed docs for the first half of that work; this cycle updates the feature map so the shipped surface also includes uninstall and list.

### In Progress

- Pack-management ergonomics are still improving around the deterministic lane.

### Scoped

- Publish/update flows for skill packs remain scoped beyond the current install/list/uninstall feature set.
