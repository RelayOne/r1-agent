# WORK-stoke.md

**Status:** final.
**Repo:** `github.com/ericmacdougall/stoke` (branch `feat-smart-chat-mode`).
**Scope:** two tracks.

- **Track A (Tasks 1-7):** prompt-injection + tool-output hardening informed by CL4R1T4S corpus review.
- **Track B (Tasks 8-24):** agent-platform build — expanding Stoke from coding orchestrator into verified general agent platform with research, browser, deployment, and agent-delegation executors.

**Source of truth:** see PORTFOLIO-EXECUTION-INDEX.md for full item-by-item ordering. This file delegates to per-spec files in specs/ for full implementation checklists:

- Spec files for Stoke Track B (agent platform) are in /home/eric/repos/stoke/specs/:
  - specs/descent-hardening.md (S-4/5/7/8 + env + ghost-write)
  - specs/cloudswarm-protocol.md (S-0 emitter + stoke run + HITL)
  - specs/executor-foundation.md (S-3 VerifyFunc + event log + router)
  - specs/browser-research-executors.md (browser + research executor)
  - specs/delegation-a2a.md (S-10 hire-verify-settle + A2A)
  - specs/deploy-executor.md (Fly.io)
  - specs/operator-ux-memory.md (S-2 HITL + memory + intent gate)
  - specs/mcp-client.md
  - specs/deploy-phase2.md (Vercel + Cloudflare)
  - specs/policy-engine.md (standalone)
  - specs/chat-descent-control.md
  - specs/tui-renderer.md (S-1)
  - specs/provider-pool.md (S-6)
  - specs/fanout-generalization.md

**S-series → item mapping (per PORTFOLIO-EXECUTION-INDEX.md item 23, 24, 27):**
- S-0 emitter threaded: specs/cloudswarm-protocol.md
- S-1 TUI renderer: specs/tui-renderer.md
- S-2 HITL gate: specs/cloudswarm-protocol.md + specs/operator-ux-memory.md
- S-3 VerifyFunc: specs/executor-foundation.md
- S-4 anti-deception contract: specs/descent-hardening.md (item 1)
- S-5 forced self-check: specs/descent-hardening.md (item 2)
- S-6 provider pool: specs/provider-pool.md
- S-7 bootstrap fix: specs/descent-hardening.md (item 4)
- S-8 per-file repair cap: specs/descent-hardening.md (item 3)
- S-9 memory store: specs/operator-ux-memory.md
- S-10 hire verify-settle: specs/delegation-a2a.md
- S-11 local cost tracking: specs/operator-ux-memory.md (Part G cost dashboard)

**Execution model:** See PORTFOLIO-EXECUTION-INDEX.md items 23 (P0 — start immediately), 24, 27.

## Track A — Prompt-injection hardening

Ports Stoke's existing promptguard into three more ingest paths, adds tool-output sanitizer, wires dead honeypot.go, websearch domain allowlist, MCP sanitization audit, red-team corpus.

**Defect locations:**
- `internal/workflow/workflow.go:1702` — file contents into failure-analysis prompt
- `internal/plan/feasibility.go:93` — web-search bodies into feasibility prompts
- `internal/convergence/judge.go:178, 216` — file contents to LLM judge
- `internal/agentloop/loop.go:427` — tool output round-trips to model unsanitized
- `internal/critic/honeypot.go` — 438 lines, zero production call sites

Full details: see portfolio WORK-stoke.md Track A as pasted in the portfolio planning turn. This file is a pointer; implementation proceeds per the 14 existing specs.
