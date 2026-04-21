# Research Index — Stoke Full Agent Scoping (2026-04-20)

## Raw research files

| File | Topic | Key recommendation | Primary spec consumer |
|------|-------|-------------------|----------------------|
| RT-CLOUDSWARM-MAP.md | CloudSwarm integration surface | `stoke run --output stream-json` is the primary gap; `hitl_required` is the only protocol-critical event; Stoke is opaque to Cedar | spec-2 |
| RT-STOKE-SURFACE.md | Current Stoke state | Descent + AgentLoop + StreamJSON all wired; memory/delegation/trustplane exist but unwired; bus has zero publishers | all |
| RT-01-playwright-go.md | Headless browser | `github.com/go-rod/rod` (MIT, pool-ready, pure Go) | spec-4, spec-6 |
| RT-02-cedar-policy.md | Policy engine | cedar-agent HTTP sidecar + `github.com/cedar-policy/cedar-go` v1.6.0; local YAML fallback for standalone | spec-2 (deferred) |
| RT-03-a2a-protocol.md | Agent-to-agent | A2A v1.0 (LF, 2026-03-12); `github.com/a2aproject/a2a-go` v2.2.0; TrustPlane via a2a-x402 extension URI | spec-5 |
| RT-04-ndjson-patterns.md | NDJSON streaming | Two-lane emitter (critical blocks, observability drop-oldest); `os.Stdout` unbuffered in Go; mutex-serialized `json.Encoder`; SIGSTOP-survivable | spec-2 |
| RT-05-stateless-harness.md | Event log | SQLite+WAL single `events` table, ULID + hash-chained; call/result two-phase; `BranchID` for speculative execution | spec-3 |
| RT-06-anti-deception-prompts.md | Worker honesty | TRUTHFULNESS_CONTRACT (260 words, ready-to-paste); PRE_COMPLETION_GATE with `<pre_completion>` XML (280 words, ready-to-paste); BLOCKED: escape hatch | spec-1 |
| RT-07-multi-agent-research.md | Lead+subagent research | Opus 4.7 lead + Sonnet 4.5 subagents; filesystem as comm channel; 4-stage claim verify; effort scaling (1/2-4/10+) | spec-4 |
| RT-08-memory-consolidation.md | Persistent memory | Stoke's memory/ already has CoALA tiers + contradiction; add scope hierarchy + auto-retrieval + SQLite+FTS5+sqlite-vec + live meta-reasoner | spec-7 |
| RT-09-delegation-trust.md | Trust-clamped delegation | Extend `internal/delegation/scoping.go`; HMAC real-verifier from day 1 (not CloudSwarm's V-114 stub); macaroons as inspiration; MAX_DEPTH=3 | spec-5 |
| RT-10-deploy-providers.md | Deploy targets | Fly.io first (widest languages, explicit rollback, NDJSON matches our engine/stream model); Vercel second; Cloudflare third | spec-6 |
| RT-11-planning-modes.md | Planning + ask/notify | Devin 3-mode + `<think>` gate; Manus notify/ask; Factory DROID Intent Gate (verbatim); Cursor per-file cap (3) | spec-1, spec-7 |

## Synthesized summaries

See `specs/research/synthesized/` for per-topic consolidations with cross-references.

## Open questions

See `specs/research/open-questions/index.md` for decisions needed from operator before final spec write.
