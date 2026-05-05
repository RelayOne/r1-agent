# Stoke Implementation Guide — Read This First

This is a self-contained implementation guide derived from 100 deep-research files (24,824 lines) covering AI coding harness architecture, the Anthropic Messages API, deterministic enforcement, agent failure modes, skill systems, configuration wizards, event buses, and benchmarking. **You should not need to read the underlying research to implement this guide.** Every claim, number, and pattern below is sourced from research that has already been done.

## Audience

You are Claude Code (or another coding agent) working on the Stoke repository at `github.com/ericmacdougall/stoke`. The repo is an 87,000-line Go-based AI coding orchestrator with 103 internal packages. Your job is to bolt in the goals described below.

## The four goals (from Eric)

1. **Use incoming research intelligently.** Auto-detect what tech a repo uses, select relevant skills from a library, inject them into prompts within a token budget. Skill set should expand as the repo changes.
2. **Auto-discover skill writing/updating opportunities** as work and research progress. New gotchas observed in failures should become persistent skill content.
3. **Become its own full harness.** Stop depending on Claude Code CLI and Codex CLI as execution backends. Build a native agentic loop using the Anthropic Messages API directly, with Stoke's own tool execution layer.
4. **Configuration wizard per project** with "research and self-configure" as the default mode.
5. **Hook system more powerful than Claude Code's** — deterministic enforcement, third-party tool integration, pub/sub throughout all loops.

The benchmarking framework is a separate concern (see `09-validation-gates.md`).

## The single most important strategic insight

From research file P67 ("The architecture of every major open-source AI coding harness in 2026"):

> mini-SWE-agent (July 2025): just 100 lines of Python using only bash as a tool achieved 65%+ on SWE-bench Verified. SWE-agent is now in maintenance-only mode, superseded by mini-swe-agent. As models improve, elaborate scaffolding yields diminishing returns.

**Stoke's 87K lines and 103 packages were designed when Claude 3.5 needed extensive scaffolding.** With Claude 4.6 / Sonnet 4.6 / Opus 4.6, much of that scaffolding may now work against the model rather than helping it.

This has a direct implication for how you implement everything in this guide: **architect for graceful deprecation.** Every new package must justify its existence against "could the model do this without help?". Some packages will earn their place clearly (anti-deception, hub, multi-model orchestration, persistent storage). Others may become obsolete as models improve. Build with the assumption that today's essential infrastructure may be tomorrow's unnecessary complexity.

**You should not delete existing packages as part of this work.** Create a tagging document (`PACKAGE-AUDIT.md` at the repo root) tagging each of the 103 packages as CORE / HELPFUL / DEPRECATED, but do not actually remove anything. Eric will make the deletion calls based on your audit + actual benchmark results.

## Stoke's strategic positioning (validated by research)

From research file P85 ("Preventing AI coding agents from faking completeness"):

> Anti-deception phase transitions, adversarial self-audit, and evidence gates exist in zero publicly available tools beyond Stoke's internal system.

Stoke's existing combination of `taskstate` + `convergence` + `critic` + `scan` + `hooks` is **genuinely novel** in the open-source ecosystem. Quantitative validation:

- AutoCodeRover quantified a **34.6% overfitting rate** — patches that pass tests but don't match developer intent
- Claude Code GitHub Issue #32650 documents 16 distinct "phantom execution" failure modes
- Users spend **30–40% of interaction time re-verifying agent claims**
- AI test suites achieve **91% coverage but only 34% mutation score** — meaning 57% of injected bugs go undetected
- AI-generated code is **2.74× more likely** to introduce XSS, **1.91×** for IDOR, **1.88×** for password handling
- AI-assisted commits leak secrets at **2× the baseline rate**

This positions Stoke as **the harness for production engineering teams who can't tolerate AI deception**. Not the fastest, not the cheapest, not the most autonomous — the most honest and the most verifiable. Every architectural decision in this guide reinforces that positioning.

## How to use this guide

The guide is a sequence of phases. Each phase has its own file with:

- Architecture decisions (with research backing)
- Files to create or modify
- Concrete code patterns and exact API specifications
- Validation gates that must pass before moving to the next phase
- Test plans

**Phase order matters because phases have dependencies.** Do them in order:

| File | Phase | What it covers | Depends on |
|---|---|---|---|
| `01-architecture-decisions.md` | — | Locked-in design choices, must read first | — |
| `02-implementation-order.md` | — | Dependency graph and parallelization | 01 |
| `03-phase1-skills.md` | 1 | Wire skills into the prompt pipeline (skill registry rewrite + skillselect) | 02 |
| `04-phase2-wizard.md` | 2 | Configuration wizard with auto-detect default | 03 |
| `05-phase3-hub.md` | 3 | Event bus replacing the 3 disconnected hook systems | 03 |
| `06-phase4-harness-independence.md` | 4 | Tools + agentloop + native runner (no more Claude Code CLI) | 03, 05 |
| `07-phase5-wisdom-and-fixes.md` | 5 | Wisdom SQLite migration, prompt cache alignment, package audit | 04, 05, 06 |
| `08-skill-library-extraction.md` | parallel | Convert the 61 engineering research files into skill content | independent |
| `09-validation-gates.md` | — | Per-phase validation summary, "done" definition | all |
| `10-bench-framework.md` | 6 | Full benchmark framework spec — multi-harness comparison, anti-deception tasks, PoLL ensemble judging | 03, 04 |
| `11-honesty-judge.md` | 7 | Extends Phase 3's hub with the 7-layer Honesty Judge (test integrity, hallucinated imports, claim decomposition, CoT monitoring, confession elicitation) | 03, 05 |
| `12-additional-skills.md` | parallel | 5 additional engineering skills from research bundle 02 (observability, kubernetes, go-file-storage, terraform-multicloud) | independent |

You can run **Phase 1 + Phase 8 + Phase 12** in parallel (one agent on code, two on skill content). Phase 2 (wizard) and Phase 3 (hub) can run in parallel after Phase 1 is done. Phase 4 (harness independence) is the largest chunk and depends on Phase 3. Phase 5 is cleanup that runs after the core code phases. Phase 6 (bench) and Phase 7 (Honesty Judge extension) come after Phases 3 and 4 — Phase 7 is required to make Phase 6's headline numbers compelling.

## Hard rules you must follow

1. **Never delete existing packages or break existing tests.** This is a long-running production codebase. Add new code, refactor surgically, but preserve backward compatibility unless explicitly told otherwise.
2. **Every new package must have unit tests with >70% coverage.** Run `go test ./...` after every meaningful change.
3. **Run `go vet ./...` and fix anything it catches.**
4. **Run `go build ./cmd/r1` after every meaningful change.** Build breakage is a hard stop.
5. **Don't invent imports.** Every import must reference a package that actually exists. Check `go.mod` and verify with `go doc <package>`.
6. **Don't write placeholder code.** No `TODO`, `FIXME`, `panic("not implemented")`, or empty function bodies. If something can't be done now, write a note to Eric in `STOKE-IMPL-NOTES.md` at the repo root explaining what's blocked and why.
7. **Don't rewrite tests to make them pass.** If a test fails after your change, the implementation is wrong, not the test.
8. **Don't use `@ts-ignore`, `as any`, `eslint-disable`, `# nolint`, or equivalent in any code you write.**
9. **When you're unsure, ask Eric** by writing the question to `STOKE-IMPL-NOTES.md` and proceeding with the safest interpretation.
10. **Read the existing code before changing it.** The Stoke codebase has 103 packages and a lot of subtle interactions. Don't assume — read.

## Files you'll create at the repo root

Beyond the per-package files described in each phase:

- `PACKAGE-AUDIT.md` — your tagging of all 103 packages as CORE/HELPFUL/DEPRECATED (Phase 7)
- `STOKE-IMPL-NOTES.md` — running log of decisions, blockers, and questions for Eric
- `.stoke/skills/` — skill library directory (populated by Phase 8)
- `.stoke/config.yaml` — example config produced by the wizard (Phase 2)
- `.stoke/wizard-rationale.md` — example rationale document (Phase 2)
- `docs/architecture/skill-pipeline.md` — architecture doc for the skill pipeline
- `docs/architecture/hub.md` — architecture doc for the event bus
- `docs/architecture/agentloop.md` — architecture doc for the native runner
- `docs/architecture/wizard.md` — architecture doc for the wizard

## Research file references

Throughout this guide, claims are tagged with research file IDs like `[P61]`, `[P67]`, `[TOB]`. These reference the underlying research the user has but you do not need to read. They exist so Eric can verify any claim against the source. The IDs map to:

- `P61` — Building an agentic tool-use loop with the Anthropic Messages API
- `P62` — Tool schemas and architecture of AI coding agents
- `P63` — Maximizing AI coding agent instruction following within token budgets
- `P64` — Auto-detecting repository tech stacks from file structure
- `P65` — MCP server implementation patterns
- `P66` — Multi-model orchestration for AI coding harnesses
- `P67` — The architecture of every major open-source AI coding harness in 2026
- `P68` — Every way AI coding agents fail
- `P69` — Prompt caching economics for multi-turn agentic coding
- `P70` — Workspace isolation patterns for parallel AI coding agents
- `P71` — Measuring and improving AI agent skills
- `P72` — How developer tools build intelligent configuration wizards
- `P73` — How tools reverse-engineer a repository's complete technology profile
- `P74` — Self-configuring AI: ten design dimensions
- `P75` — Auto-detecting project maturity to calibrate engineering rigor
- `P76` — Cloud infrastructure decision frameworks for venture studios
- `P77` — Automating compliance detection from code signals
- `P78` — How every AI coding tool handles project configuration
- `P79` — Designing a universal event bus
- `P80` — Enforcing deterministic rules in AI agent systems
- `P85` — Preventing AI coding agents from faking completeness
- `TOB` — Trail of Bits' Claude Code skills: a complete anatomy
- `CMD` — What battle-tested teams actually put in their CLAUDE.md files
- `P81` — Building the central nervous system for AI coding agents (LSP/DAP/SSE/OTel integration patterns)
- `P82` — High-performance IPC in Go for event bus systems (gRPC over UDS, JSON framing, benchmarks)
- `P83` — The AI coding benchmark landscape is broken (SWE-bench contamination, METR's 19% slowdown finding)
- `P84` — LLMs as judges: reliability, biases, mitigation (PoLL ensemble, position bias, code blind spots)
- `P86` — Hidden test suites for grading AI coding agents (EvalPlus methodology, mutation validation)
- `P87` — Building reproducible, isolated benchmark execution at scale (SWE-bench Docker tiers, Firecracker)
- `P88` — Adversarial testing methodologies for AI coding agents (ImpossibleBench, Code-A1, four-loop evolution)
- `DECEPT` — Detecting deception in AI coding agents (Anthropic 7-property framework, 7-layer Honesty Judge)

## Now go read `01-architecture-decisions.md`.
