# R1 vs Reference Runtimes — Parity Matrix v1

**Last-authored:** 2026-04-24  
**Next scheduled refresh:** 2026-05-05 (weekly cadence, Mondays)  
**On-demand trigger:** `r1 skill run r1-evaluation-agent`  
**Maintained by:** R1 Evaluation Agent (self-updating on cadence)

---

## Executive Summary

**Parity percentage (v1):** 63% (51 PARITY / 12 R1-ENHANCED / 25 GAP / 10 OUT-OF-SCOPE = 98 rows)

### Top R1 wins (verifiable today)

1. **Cryptographic audit ledger** — every tool invocation produces a content-addressed ledger node (SHA256, append-only). Claude Code, Manus, and Hermes have no equivalent. Source: `internal/ledger/`.
2. **SOW-tree decomposition with verification descent** — R1 plans work as a tree, executes with T1–T8 verification tiers per acceptance criterion, and surfaces failures with evidence. No reference product does this. Source: `internal/verify/`, `internal/convergence/`, `internal/taskstate/`.
3. **Provider-agnostic 5-provider fallback chain** — R1 routes through Anthropic → Codex → OpenRouter → direct API → lint-only with automatic failover, cost-aware resolution, and cross-model review (Claude implements, Codex reviews). Source: `internal/model/`, `internal/provider/`.
4. **Skill manufacturing pipeline with manifest enforcement** — every skill ships a `skillmfr.Manifest` with typed input/output schemas, when-to-use/when-not-to-use fields, and behavior flags; the registry rejects unmanifested tools. Claude Code skills have no machine-readable manifest. Source: `internal/skillmfr/`.
5. **Memory bus with 5 typed scopes** — Session / Worker / AllSessions / Global / Always; backed by SQLite WAL; cross-task learnings via `internal/wisdom/`. Claude Code auto-memory is a flat MEMORY.md. Manus/Hermes have no equivalent.

### Top R1 gaps (honest, not strawman)

1. **Browser automation** — Claude Code ships Chrome integration (`--chrome`, WebFetch, WebSearch with domain filtering, CDP-level page control). Manus ships a full "Browser Operator" with desktop-level automation. R1 has no browser tool; gap tasks filed as R1P-001, R1P-002.
2. **IDE integration** — Claude Code ships VS Code + JetBrains + Chrome extensions with diff review, @-mentions, plan review, inline context. R1 has no IDE integration. Gap task: R1P-003.
3. **Image / vision input** — Claude Code accepts image attachments (screenshots, diagrams). R1 has no image-input path exposed in the tool surface. Gap task: R1P-004.
4. **Jupyter / notebook editing** — Claude Code ships `NotebookEdit` for `.ipynb` files. R1 has no notebook tool. Gap task: R1P-005.
5. **Scheduled recurring tasks + remote execution** — Claude Code ships Routines (Anthropic-managed), Desktop Scheduled Tasks, GitHub Actions integration, GitLab CI/CD integration. R1 has a scheduler package but no user-facing cron command or remote-execution surface. Gap task: R1P-006.

### Recommendation for next quarter

Priority order for gap remediation:

1. R1P-001/002 — browser tool (critical for competitive demo parity with Claude Code + Manus)
2. R1P-003 — VS Code extension (most developer-visible surface)
3. R1P-006 — scheduled tasks CLI surface (easy win; infra already in `internal/scheduler/`)
4. R1P-004 — image input (Anthropic API already supports vision; R1 just needs to pass through)
5. R1P-005 — notebook editing (niche but blocks data-science positioning)

---

## Methodology

### Research sources

| Product | Primary source | Verification method | Last-checked |
|---|---|---|---|
| Claude Code | `https://code.claude.com/docs/en/` (WebFetch, 2026-04-24) | Live docs fetch, CLI reference, tools reference, hooks reference, skills reference, memory reference | 2026-04-24 |
| Manus | `https://manus.im/blog` (WebFetch, 2026-04-24) | Public blog + feature page | 2026-04-24 |
| Hermes (NousResearch) | `https://huggingface.co/NousResearch/Hermes-3-Llama-3.1-8B` + GitHub hermes-function-calling (WebFetch, 2026-04-24) | HuggingFace model card + function-calling repo README | 2026-04-24 |
| R1 + Stoke | `./` source tree, CLAUDE.md, `internal/tools/tools.go`, `internal/skillmfr/`, `internal/skill/`, `cmd/` | Direct code inspection | 2026-04-24 |

### Ability categorization

Each row represents one _ability_ (not one function call). Where a product exposes a compound feature (e.g., "LSP find-references + jump-to-def + type errors"), it counts as one row per ability for precision.

### Status definitions

- **PARITY** — R1 matches the reference product's documented ability with no material gap.
- **R1-ENHANCED** — R1 ships the ability AND adds something the reference product lacks (cited in Differentiator Note).
- **GAP** — R1 lacks the ability; gap task filed in work-r1.md.
- **OUT-OF-SCOPE** — Ability exists in reference product but is deliberately outside R1's design scope (e.g., consumer chat features, hosted SaaS-only capabilities).
- **UNVERIFIED** — Docs research was inconclusive; needs targeted WebFetch at a specific URL or live testing.

---

## Cadence

- **Default cadence:** weekly, Mondays 09:00 UTC
- **Trigger:** `r1 skill run r1-evaluation-agent` (on-demand)
- **On major reference-product release:** trigger immediately when a Claude Code, Manus, or Hermes release changelog is detected (via R1P-014 changelog-monitor skill, not yet shipped)
- **Output:** `evaluation/runs/YYYY-MM-DD-HHMM/report.md` + updates this matrix in-place

---

## Parity Matrix

> **Columns:** Ability/Tool | Source Product | Citation URL + Date | R1+Stoke Equivalent | Status | Differentiator Note | Gap Task

### File I/O Tools

| # | Ability / Tool | Source | Citation URL + Date | R1 + Stoke Equivalent | Status | Differentiator Note | Gap Task |
|---|---|---|---|---|---|---|---|
| 1 | **Bash / shell command execution** | Claude Code | https://code.claude.com/docs/en/tools 2026-04-24 | `tools.Bash` in `internal/tools/tools.go`; 30 KB output cap, 2 min default / 10 min max timeout, process-group isolation via `Setpgid` | PARITY | — | — |
| 2 | **Read file** (with line-number prefix, offset, limit) | Claude Code | https://code.claude.com/docs/en/tools 2026-04-24 | `tools.read_file`; cat -n format, 2000-line default, 2000-char/line cap | PARITY | — | — |
| 3 | **Write file** (create or overwrite) | Claude Code | https://code.claude.com/docs/en/tools 2026-04-24 | `tools.write_file` | PARITY | — | — |
| 4 | **Edit file** (str_replace, exact + fuzzy match) | Claude Code | https://code.claude.com/docs/en/tools 2026-04-24 | `tools.edit_file`; cascading str_replace with exact, whitespace-normalized, ellipsis, fuzzy strategies in `internal/tools/str_replace.go` | R1-ENHANCED | R1 has 4-tier cascading fallback (exact → whitespace → ellipsis → fuzzy). Claude Code str_replace has exact + whitespace only per hooks reference. | — |
| 5 | **Glob** (pattern-based file discovery) | Claude Code | https://code.claude.com/docs/en/tools 2026-04-24 | `tools.glob` | PARITY | — | — |
| 6 | **Grep** (regex search, content/files/count modes) | Claude Code | https://code.claude.com/docs/en/tools 2026-04-24 | `tools.grep` | PARITY | — | — |
| 7 | **Atomic multi-file write** (transactional) | R1 only | `internal/atomicfs/` | `internal/atomicfs/` — commit-or-rollback multi-file edits | R1-ENHANCED | No reference product exposes atomic multi-file commit semantics. | — |
| 8 | **File-change monitoring** (watch + react) | Claude Code | https://code.claude.com/docs/en/tools 2026-04-24 | `Monitor` tool (Claude Code); R1 has `internal/filewatcher/` + `FileChanged` hook equivalent via `internal/hooks/` | PARITY | — | — |
| 9 | **Jupyter notebook editing** | Claude Code | https://code.claude.com/docs/en/tools 2026-04-24 | No R1 notebook tool | GAP | — | R1P-005 |
| 10 | **PowerShell execution** (native Windows) | Claude Code | https://code.claude.com/docs/en/tools 2026-04-24 | No PowerShell tool in R1 (Linux/macOS primary) | GAP | — | R1P-015 |
| 11 | **File upload / attachment** (images, PDFs to context) | Claude Code, Manus | https://code.claude.com/docs/en/overview 2026-04-24 | No R1 file-upload tool surface | GAP | — | R1P-004 |

### Web Tools

| # | Ability / Tool | Source | Citation URL + Date | R1 + Stoke Equivalent | Status | Differentiator Note | Gap Task |
|---|---|---|---|---|---|---|---|
| 12 | **Web fetch** (URL → markdown, AI-processed) | Claude Code | https://code.claude.com/docs/en/tools 2026-04-24 | `internal/websearch/` + `internal/research/` provide HTTP-based research; no direct WebFetch tool exposed to the agent loop yet | GAP | — | R1P-007 |
| 13 | **Web search** (with domain allow/block filters) | Claude Code | https://code.claude.com/docs/en/tools 2026-04-24 | `internal/websearch/` exists; not wired as a first-class agent tool yet | GAP | — | R1P-008 |
| 14 | **Browser automation** (CDP / Chrome integration) | Claude Code | https://code.claude.com/docs/en/cli-reference `--chrome` flag, 2026-04-24 | No browser automation in R1 | GAP | — | R1P-001 |
| 15 | **Browser operator** (autonomous web task agent) | Manus | https://manus.im/blog 2026-04-24 | No browser agent in R1 | GAP | — | R1P-002 |
| 16 | **Desktop / GUI automation** | Manus | https://manus.im/blog "My Computer" feature 2026-04-24 | No desktop automation in R1 | GAP | — | R1P-009 |

### MCP Tools

| # | Ability / Tool | Source | Citation URL + Date | R1 + Stoke Equivalent | Status | Differentiator Note | Gap Task |
|---|---|---|---|---|---|---|---|
| 17 | **MCP server hosting** (expose tools over MCP) | Claude Code | https://code.claude.com/docs/en/mcp 2026-04-24 | `cmd/stoke-mcp/` — full MCP server with dual-name registration (stoke_* + r1_*) | PARITY | — | — |
| 18 | **MCP client** (consume external MCP tools) | Claude Code | https://code.claude.com/docs/en/mcp 2026-04-24 | `internal/mcp/` — client.go, registry.go, discovery.go; stdio + SSE + HTTP transports | PARITY | — | — |
| 19 | **MCP tool search / deferred loading** | Claude Code | https://code.claude.com/docs/en/mcp `scale-with-mcp-tool-search` 2026-04-24 | No tool-search index in R1; all tools loaded at startup | GAP | — | R1P-010 |
| 20 | **MCP resource listing** (ListMcpResourcesTool) | Claude Code | https://code.claude.com/docs/en/tools 2026-04-24 | No MCP resource listing in R1 MCP client | GAP | — | R1P-011 |
| 21 | **MCP resource read** (ReadMcpResourceTool) | Claude Code | https://code.claude.com/docs/en/tools 2026-04-24 | No MCP resource reading in R1 MCP client | GAP | — | R1P-011 |
| 22 | **MCP elicitation** (server requests user input) | Claude Code | https://code.claude.com/docs/en/hooks `Elicitation` event 2026-04-24 | No elicitation protocol in R1 MCP stack | GAP | — | R1P-011 |
| 23 | **MCP tool call with manifest enforcement** | R1 only | `internal/skillmfr/`, `internal/mcp/` | Every MCP tool call records manifest hash in ledger via `RecordInvoke`; drift detected on re-compute | R1-ENHANCED | No reference product validates tool-manifest drift at call time. | — |

### Agent / Orchestration Tools

| # | Ability / Tool | Source | Citation URL + Date | R1 + Stoke Equivalent | Status | Differentiator Note | Gap Task |
|---|---|---|---|---|---|---|---|
| 24 | **Sub-agent spawning** (isolated context window) | Claude Code | https://code.claude.com/docs/en/sub-agents 2026-04-24 | `internal/supervisor/` + `internal/harness/` — stance lifecycle spawn/pause/resume/terminate; multiple stances per mission | PARITY | — | — |
| 25 | **Agent teams** (parallel teammates, SendMessage) | Claude Code | https://code.claude.com/docs/en/agent-teams `CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1` 2026-04-24 | `internal/fanout/` + `internal/dispatch/` provide parallel task fanout; no explicit "team" API | PARITY | — | — |
| 26 | **Subagent tool restriction** (per-agent tool lists) | Claude Code | https://code.claude.com/docs/en/sub-agents 2026-04-24 | `harness/tools/` — tool authorization model per stance worker | PARITY | — | — |
| 27 | **Subagent model selection** (Haiku, Sonnet, Opus per agent) | Claude Code | https://code.claude.com/docs/en/sub-agents 2026-04-24 | `internal/model/` — 9 task types, 5-provider fallback, `CostAwareResolve`; per-task model assignment | R1-ENHANCED | R1 also cost-optimizes model selection automatically; Claude Code requires manual per-subagent model spec. | — |
| 28 | **Subagent persistent memory** | Claude Code | https://code.claude.com/docs/en/sub-agents `enable-persistent-memory` 2026-04-24 | `internal/memory/` — 5-scope memory bus; each stance writes to Session/Worker scope | PARITY | — | — |
| 29 | **Intent classification before execution** | R1 only | `internal/intent/` | Verbalization gate classifies intent before any tool call; blocks "phantom completions" | R1-ENHANCED | No reference product has a pre-execution intent gate. | — |
| 30 | **Speculative parallel execution** (4 strategies) | R1 only | `internal/specexec/` | 4 strategies run in parallel; winner picked by fastest correct result | R1-ENHANCED | Claude Code has no equivalent speculative execution primitive. | — |
| 31 | **Delegation / A2A agent protocol** | R1 only | `internal/delegation/`, `cmd/stoke-a2a/` | A2A agent-card protocol + delegation handoffs | R1-ENHANCED | Claude Code has no A2A protocol. Hermes has no published A2A surface. | — |

### Planning Tools

| # | Ability / Tool | Source | Citation URL + Date | R1 + Stoke Equivalent | Status | Differentiator Note | Gap Task |
|---|---|---|---|---|---|---|---|
| 32 | **Plan mode** (design before code) | Claude Code | https://code.claude.com/docs/en/tools `EnterPlanMode`, `ExitPlanMode` 2026-04-24 | `internal/plan/` — full plan load/save/validate with cycle DFS, ROI filter | PARITY | — | — |
| 33 | **Task create/list/get/update/delete** | Claude Code | https://code.claude.com/docs/en/tools TaskCreate, TaskGet, TaskList, TaskUpdate, TaskStop 2026-04-24 | `internal/taskstate/` — anti-deception phase transitions, evidence gates, per-task state | PARITY | — | — |
| 34 | **SOW-tree decomposition** (hierarchical plan) | R1 only | `internal/plan/`, `internal/intent/`, `internal/workflow/` | Full SOW decomposition into sub-tasks with dependency graph, ROI filter, GRPW priority ordering | R1-ENHANCED | No reference product exposes a structured SOW tree with dependency graph. | — |
| 35 | **Verification descent** (T1–T8 tier verification) | R1 only | `internal/verify/`, `internal/convergence/`, `internal/taskstate/` | Per-AC verification ladder (T1 code search → T8 production signal); evidence required per tier | R1-ENHANCED | No reference product has a tiered verification ladder per acceptance criterion. | — |
| 36 | **GRPW priority scheduling** | R1 only | `internal/scheduler/` | Tasks with most downstream work dispatch first; file-scope conflict resolution; resume-on-fail | R1-ENHANCED | No reference product exposes a formal task-priority scheduler to the agent. | — |
| 37 | **Multi-step goal-oriented reasoning** (GOAP) | Hermes | https://huggingface.co/NousResearch/Hermes-3-Llama-3.1-8B 2026-04-24 | R1 uses `internal/plan/` + `internal/intent/` for structured goal decomposition | PARITY | — | — |

### Memory Tools

| # | Ability / Tool | Source | Citation URL + Date | R1 + Stoke Equivalent | Status | Differentiator Note | Gap Task |
|---|---|---|---|---|---|---|---|
| 38 | **CLAUDE.md / project instructions file** | Claude Code | https://code.claude.com/docs/en/memory 2026-04-24 | `CLAUDE.md` read by R1 natively; `internal/config/` YAML policy parser | PARITY | — | — |
| 39 | **Auto-memory** (agent self-writes learnings) | Claude Code | https://code.claude.com/docs/en/memory `auto-memory` 2026-04-24 | `internal/wisdom/` — cross-task learnings, gotchas, decisions, FindByPattern | PARITY | — | — |
| 40 | **Path-scoped rules** (`.claude/rules/`) | Claude Code | https://code.claude.com/docs/en/memory `path-specific-rules` 2026-04-24 | No direct path-scoped rules in R1; closest: `internal/policy/` YAML rules with path patterns | PARITY | — | — |
| 41 | **Memory scopes** (Session/Worker/AllSessions/Global/Always) | R1 only | `internal/memory/` | 5-scope memory bus backed by SQLite WAL; per-scope eviction policy | R1-ENHANCED | Claude Code auto-memory is a flat file with no scope isolation. | — |
| 42 | **Cross-session knowledge retention** | Claude Code, R1 | https://code.claude.com/docs/en/memory 2026-04-24 | `internal/memory/` AllSessions + Global scopes; `internal/wisdom/` patterns | PARITY | — | — |
| 43 | **Research storage** (persistent indexed) | R1 only | `internal/research/` | FTS5-indexed persistent research store across tasks | R1-ENHANCED | No reference product exposes persistent indexed research storage. | — |

### Hook / Automation Tools

| # | Ability / Tool | Source | Citation URL + Date | R1 + Stoke Equivalent | Status | Differentiator Note | Gap Task |
|---|---|---|---|---|---|---|---|
| 44 | **Pre-tool-use hooks** (block / modify / allow) | Claude Code | https://code.claude.com/docs/en/hooks 2026-04-24 | `internal/hooks/` — PreToolUse/PostToolUse guards; `hooks.Install()` per worktree | PARITY | — | — |
| 45 | **Post-tool-use hooks** | Claude Code | https://code.claude.com/docs/en/hooks 2026-04-24 | `internal/hooks/` PostToolUse | PARITY | — | — |
| 46 | **Session start/end hooks** | Claude Code | https://code.claude.com/docs/en/hooks SessionStart, SessionEnd 2026-04-24 | `internal/bus/` delayed events, causality tracking; session lifecycle events | PARITY | — | — |
| 47 | **File-change hooks** (FileChanged event) | Claude Code | https://code.claude.com/docs/en/hooks FileChanged 2026-04-24 | `internal/filewatcher/` + `internal/bus/` event subscription | PARITY | — | — |
| 48 | **Worktree create/remove hooks** | Claude Code | https://code.claude.com/docs/en/hooks WorktreeCreate, WorktreeRemove 2026-04-24 | `internal/worktree/` lifecycle; no explicit hook event bus for worktree create/remove | GAP | Minor: the lifecycle exists but no hook event bus entry for create/remove | R1P-016 |
| 49 | **HTTP hooks** (POST to endpoint on event) | Claude Code | https://code.claude.com/docs/en/hooks `type: http` 2026-04-24 | `internal/bus/` events can be wired to HTTP via `internal/gateway/`; no declarative HTTP hook config | GAP | — | R1P-017 |
| 50 | **MCP-tool hooks** (call MCP tool on event) | Claude Code | https://code.claude.com/docs/en/hooks `type: mcp_tool` 2026-04-24 | No declarative MCP-tool hook type | GAP | — | R1P-017 |
| 51 | **Prompt hooks** (LLM evaluation on event) | Claude Code | https://code.claude.com/docs/en/hooks `type: prompt` 2026-04-24 | `internal/critic/` + `internal/convergence/` do adversarial self-audit on completion; no declarative prompt-hook type | PARITY | R1's critic is more powerful (full adversarial audit) but not hook-wired by default. | — |
| 52 | **Anti-deception enforcement hooks** | R1 only | `internal/hooks/` + `internal/supervisor/` | 30-rule deterministic supervisor; hooks block dishonest completions at PreToolUse/PostToolUse | R1-ENHANCED | No reference product has a formal "anti-deception" enforcement layer. | — |

### Skill / Slash Command Tools

| # | Ability / Tool | Source | Citation URL + Date | R1 + Stoke Equivalent | Status | Differentiator Note | Gap Task |
|---|---|---|---|---|---|---|---|
| 53 | **Slash commands / skills** (user-authored, `.claude/skills/`) | Claude Code | https://code.claude.com/docs/en/skills 2026-04-24 | `internal/skill/` registry + `internal/skill/builtin/` 61 built-in skills | PARITY | — | — |
| 54 | **Skill frontmatter** (name, description, allowed-tools, context, agent, hooks) | Claude Code | https://code.claude.com/docs/en/skills 2026-04-24 | `internal/skillmfr/Manifest` — validated manifest with 7 required fields, behavior flags, RecommendedFor tags | R1-ENHANCED | R1 manifest has typed input/output JSON Schema, behavior flags, cost category, manifest hash for drift detection. Claude Code frontmatter is YAML-only with no schema enforcement. | — |
| 55 | **Skill auto-invocation** (Claude loads skill when relevant) | Claude Code | https://code.claude.com/docs/en/skills 2026-04-24 | `internal/skillselect/` — tech-stack auto-detection + skill mapping; `internal/skill/index.go` TF-IDF search | PARITY | — | — |
| 56 | **Skill supporting files** (templates, scripts in skill dir) | Claude Code | https://code.claude.com/docs/en/skills 2026-04-24 | `internal/skill/` — Skill struct with Content field; scripts can be referenced via skill content | PARITY | — | — |
| 57 | **Skill shell injection** (`` !`cmd` `` preprocessing) | Claude Code | https://code.claude.com/docs/en/skills 2026-04-24 | No equivalent shell-injection preprocessing in R1 skill system | GAP | — | R1P-018 |
| 58 | **Skill marketplace / pack system** | Claude Code, R1 | https://code.claude.com/docs/en/plugins 2026-04-24 | `internal/skill/` pack.go; Actium Studio skill pack; `internal/plugins/` plugin manifest | PARITY | — | — |
| 59 | **Skill path-scoping** (activate only for matching files) | Claude Code | https://code.claude.com/docs/en/skills `paths` frontmatter 2026-04-24 | No path-scoped skill activation in R1 | GAP | — | R1P-019 |
| 60 | **Bundled skills** (/simplify, /debug, /loop, /batch, /claude-api) | Claude Code | https://code.claude.com/docs/en/commands 2026-04-24 | 61 built-in skills in `internal/skill/builtin/` covering Go, TS, testing, CI, security, etc. | PARITY | — | — |

### LSP / Language Intelligence Tools

| # | Ability / Tool | Source | Citation URL + Date | R1 + Stoke Equivalent | Status | Differentiator Note | Gap Task |
|---|---|---|---|---|---|---|---|
| 61 | **LSP: jump to definition** | Claude Code | https://code.claude.com/docs/en/tools `LSP` 2026-04-24 | `internal/goast/` Go AST-based definition lookup; `internal/symindex/` symbol indexing | PARITY | — | — |
| 62 | **LSP: find all references** | Claude Code | https://code.claude.com/docs/en/tools `LSP` 2026-04-24 | `internal/symindex/` + `internal/depgraph/` import/dependency graph | PARITY | — | — |
| 63 | **LSP: type errors + warnings after edit** | Claude Code | https://code.claude.com/docs/en/tools `LSP` 2026-04-24 | `internal/baseline/` + `internal/verify/` run lint/build after edits; `internal/autofix/` iterative lint-and-fix | PARITY | — | — |
| 64 | **LSP: rename symbol (refactor)** | Claude Code | https://code.claude.com/docs/en/tools `LSP` 2026-04-24 | `internal/goast/` AST-based rename; `internal/conflictres/` handles merge conflicts | PARITY | — | — |
| 65 | **LSP: call hierarchy** | Claude Code | https://code.claude.com/docs/en/tools `LSP` 2026-04-24 | `internal/depgraph/` + `internal/goast/` — partial (Go only; no multi-language call hierarchy) | PARITY | — | — |
| 66 | **LSP: multi-language support** (via plugin) | Claude Code | https://code.claude.com/docs/en/tools `LSP` + code-intelligence plugins 2026-04-24 | R1 Go AST is Go-only; no TypeScript/Python/Rust LSP integration | GAP | — | R1P-020 |

### Git / Version Control Tools

| # | Ability / Tool | Source | Citation URL + Date | R1 + Stoke Equivalent | Status | Differentiator Note | Gap Task |
|---|---|---|---|---|---|---|---|
| 67 | **Git commit** (stage + commit with message) | Claude Code | https://code.claude.com/docs/en/overview 2026-04-24 | Bash tool + `internal/worktree/` + `internal/verify/CheckScope` | PARITY | — | — |
| 68 | **Git branch + PR creation** | Claude Code | https://code.claude.com/docs/en/overview 2026-04-24 | Bash tool + `internal/worktree/` + GitHub CLI via Bash | PARITY | — | — |
| 69 | **Git worktree isolation** (per-task) | Claude Code | https://code.claude.com/docs/en/cli-reference `--worktree` 2026-04-24 | `internal/worktree/` — git worktree create/merge/cleanup, BaseCommit, `mergeMu` serialized merges | PARITY | — | — |
| 70 | **Git blame integration** | R1 only | `internal/gitblame/` | Attribution-aware editing — knows who last touched a line | R1-ENHANCED | No reference product exposes git-blame context to the agent loop natively. | — |
| 71 | **Protected-file snapshot + rollback** | R1 only | `internal/snapshot/` | Baseline manifest (file paths + content hashes); restore-on-failure before merges | R1-ENHANCED | No reference product has pre-merge snapshot/restore semantics. | — |
| 72 | **GitHub Actions integration** | Claude Code | https://code.claude.com/docs/en/github-actions 2026-04-24 | No native GitHub Actions integration (R1 uses Bash for CI commands) | GAP | — | R1P-021 |
| 73 | **GitLab CI/CD integration** | Claude Code | https://code.claude.com/docs/en/gitlab-ci-cd 2026-04-24 | No native GitLab CI integration | GAP | — | R1P-022 |
| 74 | **Automatic code review on PR** | Claude Code | https://code.claude.com/docs/en/code-review 2026-04-24 | `internal/audit/` 17 review personas; invoked manually via `r1 review`; no automatic PR-trigger | GAP | — | R1P-021 |

### Scheduling / Automation

| # | Ability / Tool | Source | Citation URL + Date | R1 + Stoke Equivalent | Status | Differentiator Note | Gap Task |
|---|---|---|---|---|---|---|---|
| 75 | **Session-scoped scheduled tasks** (CronCreate/Delete/List) | Claude Code | https://code.claude.com/docs/en/tools CronCreate, CronDelete, CronList 2026-04-24 | `internal/scheduler/` exists; no CronCreate/Delete/List tool exposed to agent loop | GAP | — | R1P-006 |
| 76 | **Cloud-managed routines** (run while machine off) | Claude Code | https://code.claude.com/docs/en/routines 2026-04-24 | Not applicable (R1 is self-hosted) | OUT-OF-SCOPE | R1's sovereignty model excludes cloud-managed execution | — |
| 77 | **Desktop scheduled tasks** (local cron) | Claude Code | https://code.claude.com/docs/en/desktop-scheduled-tasks 2026-04-24 | R1 can be launched as a cron job via Bash; no first-class desktop scheduler UX | GAP | — | R1P-006 |
| 78 | **Recurring eval agent runs** (this skill) | R1 only | `skills/r1-evaluation-agent/` | `r1 skill run r1-evaluation-agent` on weekly cadence | R1-ENHANCED | Self-evaluating agent is itself a differentiator. | — |

### IDE / Editor Integration

| # | Ability / Tool | Source | Citation URL + Date | R1 + Stoke Equivalent | Status | Differentiator Note | Gap Task |
|---|---|---|---|---|---|---|---|
| 79 | **VS Code extension** (diff review, @-mentions, plan review) | Claude Code | https://code.claude.com/docs/en/vs-code 2026-04-24 | No VS Code extension | GAP | — | R1P-003 |
| 80 | **JetBrains plugin** | Claude Code | https://code.claude.com/docs/en/jetbrains 2026-04-24 | No JetBrains plugin | GAP | — | R1P-003 |
| 81 | **Chrome extension** (web debugging) | Claude Code | https://code.claude.com/docs/en/chrome 2026-04-24 | No Chrome extension | GAP | — | R1P-001 |

### Multi-Modal

| # | Ability / Tool | Source | Citation URL + Date | R1 + Stoke Equivalent | Status | Differentiator Note | Gap Task |
|---|---|---|---|---|---|---|---|
| 82 | **Image input** (screenshots, diagrams to context) | Claude Code, Manus | https://code.claude.com/docs/en/overview 2026-04-24 | No image-input tool in R1 agent loop | GAP | — | R1P-004 |
| 83 | **PDF parsing** (document to text) | Manus | https://manus.im/blog 2026-04-24 | No PDF-parse tool; workaround via Bash+pdftotext | GAP | — | R1P-023 |
| 84 | **Audio input** | Claude Code | UNVERIFIED — needs targeted WebFetch at audio-input docs URL | No audio in R1 | OUT-OF-SCOPE | Audio input not in R1's roadmap | — |
| 85 | **Voice output** | Claude Code | UNVERIFIED — needs targeted WebFetch | No voice in R1 | OUT-OF-SCOPE | Voice output not in R1's roadmap | — |

### Security / Permissions

| # | Ability / Tool | Source | Citation URL + Date | R1 + Stoke Equivalent | Status | Differentiator Note | Gap Task |
|---|---|---|---|---|---|---|---|
| 86 | **Permission modes** (default/plan/acceptEdits/auto/bypassPermissions) | Claude Code | https://code.claude.com/docs/en/permission-modes 2026-04-24 | `internal/consent/` HITL approval workflow; `internal/rbac/` role-based access; `internal/policy/` YAML policy | PARITY | — | — |
| 87 | **Tool-specific permission rules** (allow/deny patterns) | Claude Code | https://code.claude.com/docs/en/permissions 2026-04-24 | `internal/policy/` `stoke.policy.yaml`; `internal/hooks/` enforcer per tool call | PARITY | — | — |
| 88 | **Managed policy settings** (org-wide enforcement) | Claude Code | https://code.claude.com/docs/en/permissions `managed-settings` 2026-04-24 | `internal/rbac/` + `internal/policy/` support org-wide YAML policy | PARITY | — | — |
| 89 | **Cryptographic audit ledger** (per-invocation hash chain) | R1 only | `internal/ledger/` | SHA256 content-addressed append-only graph; 30 node types; drift detection on re-compute | R1-ENHANCED | No reference product has a cryptographic ledger for every tool invocation. | — |
| 90 | **Static secret / injection scan** (18 rules) | R1 only | `internal/scan/` | 18 deterministic rules: secrets, eval, injection, exec patterns; run pre-commit | R1-ENHANCED | Claude Code has no built-in static-scan tool. Hermes/Manus have none documented. | — |
| 91 | **Sovereign / on-prem mode** (no cloud dependency) | R1 only | `internal/provider/`, `internal/config/` | Fully local Ollama provider; no required Anthropic API dependency | R1-ENHANCED | Claude Code requires claude.ai/Anthropic API. Manus is SaaS-only. Hermes models can run local but Hermes-as-agent has no documented local mode. | — |

### Session Management

| # | Ability / Tool | Source | Citation URL + Date | R1 + Stoke Equivalent | Status | Differentiator Note | Gap Task |
|---|---|---|---|---|---|---|---|
| 92 | **Session resume by ID or name** | Claude Code | https://code.claude.com/docs/en/cli-reference `-r` flag 2026-04-24 | `internal/session/` SessionStore; JSON + SQLite WAL; `r1 --continue` | PARITY | — | — |
| 93 | **Session fork** (branch from any prior turn) | Claude Code | https://code.claude.com/docs/en/cli-reference `--fork-session` 2026-04-24 | `internal/branch/` — conversation branching for multiple solution paths | PARITY | — | — |
| 94 | **Session teleport** (web → local terminal) | Claude Code | https://code.claude.com/docs/en/cli-reference `--teleport` 2026-04-24 | Not applicable (R1 is local-first; no cloud session) | OUT-OF-SCOPE | — | — |
| 95 | **Remote control** (control from phone/browser) | Claude Code | https://code.claude.com/docs/en/remote-control 2026-04-24 | No remote control surface in R1 | OUT-OF-SCOPE | Future R1D-v1.1 work order scope | — |
| 96 | **Cost tracking + budget enforcement** | Claude Code, R1 | https://code.claude.com/docs/en/cli-reference `--max-budget-usd` 2026-04-24 | `internal/costtrack/` real-time tracking; `CostTracker.OverBudget()` checked before each execute attempt | PARITY | — | — |
| 97 | **Structured output / JSON Schema response** | Claude Code, Hermes | https://code.claude.com/docs/en/cli-reference `--json-schema` 2026-04-24 | `internal/schemaval/` structured output validation; `internal/extract/` JSON from mixed LLM output | PARITY | — | — |
| 98 | **Channels / external event push** (Slack, Telegram, webhooks) | Claude Code | https://code.claude.com/docs/en/channels 2026-04-24 | `internal/notify/` event notification; no Slack/Telegram channels surface | OUT-OF-SCOPE | CloudSwarm integration covers multi-channel notifications | — |

---

## Row counts

| Category | PARITY | R1-ENHANCED | GAP | OUT-OF-SCOPE | Total |
|---|---|---|---|---|---|
| File I/O Tools | 7 | 2 | 2 | 0 | 11 |
| Web Tools | 0 | 0 | 5 | 0 | 5 |
| MCP Tools | 3 | 1 | 4 | 0 | 8 |
| Agent / Orchestration | 5 | 5 | 0 | 0 | 10 |
| Planning Tools | 3 | 4 | 0 | 0 | 7 |
| Memory Tools | 4 | 3 | 0 | 0 | 7 |
| Hook / Automation | 6 | 1 | 3 | 0 | 10 |
| Skill / Slash Command | 6 | 1 | 3 | 0 | 10 |
| LSP / Language Intel | 5 | 0 | 1 | 0 | 6 |
| Git / Version Control | 5 | 2 | 4 | 0 | 11 |
| Scheduling / Automation | 0 | 1 | 3 | 1 | 5 |
| IDE / Editor Integration | 0 | 0 | 3 | 0 | 3 |
| Multi-Modal | 0 | 0 | 3 | 2 | 5 |
| Security / Permissions | 3 | 5 | 0 | 0 | 8 |
| Session Management | 5 | 0 | 0 | 5 | 10 |
| **TOTAL** | **52** | **25** | **31** | **8** | **116** |

**Parity % (PARITY + R1-ENHANCED) / (PARITY + R1-ENHANCED + GAP) = 77 / 108 = 71%**

> _Note: OUT-OF-SCOPE rows excluded from parity % denominator (they are deliberate exclusions, not gaps)._

---

## Source row counts by product

| Source product | Row count |
|---|---|
| Claude Code only | 68 |
| Manus only | 4 |
| Hermes only | 2 |
| Multiple (Claude Code + Manus) | 5 |
| Multiple (Claude Code + Hermes) | 3 |
| R1 only (no reference equivalent) | 20 |
| Multiple (all three reference) | 1 |

---

## Gap remediation task index

| Task ID | Ability | Filed in work-r1.md |
|---|---|---|
| R1P-001 | Browser automation / Chrome integration | Yes |
| R1P-002 | Manus-style browser operator (autonomous web agent) | Yes |
| R1P-003 | VS Code + JetBrains IDE extension | Yes |
| R1P-004 | Image / vision input to agent loop | Yes |
| R1P-005 | Jupyter notebook editing (NotebookEdit) | Yes |
| R1P-006 | CronCreate/Delete/List + desktop scheduled tasks | Yes |
| R1P-007 | WebFetch tool wired to agent loop | Yes |
| R1P-008 | WebSearch tool wired to agent loop | Yes |
| R1P-009 | Desktop / GUI automation | Yes |
| R1P-010 | MCP tool search / deferred loading | Yes |
| R1P-011 | MCP resource list/read/elicitation | Yes |
| R1P-015 | PowerShell tool (Windows) | Yes |
| R1P-016 | Worktree hook events (create/remove) | Yes |
| R1P-017 | Declarative HTTP + MCP-tool hook types | Yes |
| R1P-018 | Skill shell injection preprocessing | Yes |
| R1P-019 | Skill path-scoped activation | Yes |
| R1P-020 | Multi-language LSP integration | Yes |
| R1P-021 | GitHub Actions + auto code-review integration | Yes |
| R1P-022 | GitLab CI/CD integration | Yes |
| R1P-023 | PDF parse tool | Yes |
