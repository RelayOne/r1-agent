# Spec Review — 2026-04-21 (post-/scope gap-closure)

Seven specs drafted in this scoping cycle to close the gaps identified in the
2026-04-21 audit. Each spec covers one coherent chunk of follow-up work; none
overlap.

## Specs under review

| # | Spec | Lines | Items | Build order | Status |
|---|---|---|---|---|---|
| 1 | research-orchestrator.md | 206 | 35 | 16 | ready |
| 2 | browser-interactive.md | 388 | 47 | 17 | ready |
| 3 | event-log-proper.md | 220+ | 35 | 18 | ready |
| 4 | agent-serve-async.md | 153+ | 50 | 19 | ready |
| 5 | memory-full-stack.md | 231 | 64 | 20 | ready |
| 6 | operator-ux-commands.md | 906 | 68 | 21 | ready |
| 7 | finishing-touches.md | 587 | 62 | 22 | ready |

Totals: 2,691+ lines, 361 checklist items, build orders 16-22 (follows the
existing 1-15 of the prior scoping cycle).

## 10-check review

| Check | Pass all 7? | Notes |
|---|---|---|
| 1. STATUS/CREATED/DEPENDS_ON/BUILD_ORDER header | ✓ | All four fields present on every spec. |
| 2. Overview section | ✓ | Numbered and unnumbered heading styles both accepted. |
| 3. Acceptance criteria | ✓ | agent-serve-async.md initially missing; added §11 inline this review. |
| 4. Testing section | ✓ | event-log-proper.md initially scattered through checklist; consolidated into §10 this review. |
| 5. Rollout section | ✓ | Every spec has a rollout strategy (flag-gated where appropriate). |
| 6. Self-contained checklist items | ✓ | Spot-checked 5 items per spec; each names file path, function signature, test to add, and references existing file for pattern. |
| 7. No TBD / FIXME / RESEARCH-NEEDED markers | ✓ | Grep returns 0 such markers across all 7. |
| 8. Dependencies resolve | ✓ | Every DEPENDS_ON spec file exists in specs/. |
| 9. Backward-compat contract | ✓ | Each spec explicitly documents what does NOT change (MVP code paths preserved). |
| 10. Checklist sized reasonably | ✓ | Smallest is 35 items (research-orchestrator); largest is 68 (operator-ux-commands). Target range 30-80. |

## Cross-spec dependency graph

```
research-orchestrator (16)
  → browser-research-executors Part 1 (existing, 4) — for BrowserFetcher
  → provider-pool (existing, 13)
  → operator-ux-memory Part D (existing; superseded by memory-full-stack)

browser-interactive (17)
  → executor-foundation (existing, 3)
  → browser-research-executors (existing, 4)
  → research-orchestrator (16)         ⟵ ordering: 17 after 16

event-log-proper (18)
  → executor-foundation (existing, 3) §eventlog
  → provider-pool (existing, 13)

agent-serve-async (19)
  → executor-foundation (existing, 3)
  → event-log-proper (18)              ⟵ ordering: 19 after 18

memory-full-stack (20)
  → provider-pool (existing, 13)
  → operator-ux-memory Part D (existing, 7) — as legacy reference

operator-ux-commands (21)
  → operator-ux-memory Parts B + G (existing, 7)
  → memory-full-stack (20)             ⟵ ordering: 21 after 20
  → executor-foundation (existing, 3)
  → event-log-proper (18)

finishing-touches (22)
  → chat-descent-control (existing, 11) — sessionctl socket
  → event-log-proper (18)              ⟵ ordering: 22 after 18
```

All deps point backward in build order — no cycles. /build can execute
specs in BUILD_ORDER sequence (16 → 22) without blocking.

## Critical issues fixed inline

1. **agent-serve-async.md §11** — appended an explicit Acceptance Criteria
   section covering build/vet/test gates plus per-endpoint behavioral checks.

2. **event-log-proper.md §10** — consolidated the test coverage that was
   scattered through the checklist into a dedicated Testing section listing
   each test file + what it covers.

## Scope alignment

These 7 specs address exactly the gaps enumerated in the 2026-04-21 audit:

- **Task 20 research orchestrator upgrade** → `research-orchestrator.md`
- **Task 21 part 2 go-rod interactive browser** → `browser-interactive.md`
- **Task 18 full eventlog + SOW runner resume** → `event-log-proper.md`
- **Task 24 async agent-serve with webhooks + persistence** → `agent-serve-async.md`
- **S-9 memory full stack (Part D)** → `memory-full-stack.md`
- **Parts A/C/E/F of operator-ux-memory** → `operator-ux-commands.md`
- **stoke attach + stoke replay + SECURITY.md + redteam known-miss** → `finishing-touches.md`

## Not in scope (documented deferrals)

These specs already existed from the prior scoping cycle and go straight to
`/build` without needing new scoping:

- `mcp-client.md` — full build pending
- `policy-engine.md` — full build pending
- `fanout-generalization.md` — full build pending
- `deploy-phase2.md` — Vercel + Cloudflare adapters, full build pending
- `chat-descent-control.md` — full build pending (both gate + sessionctl halves)
- `delegation-a2a.md` — S-10 sliver built; full A2A / HMAC / x402 / cards / JWKS full build pending

## Recommendation

All 7 new specs are `ready` and safe to hand to `/build`. The existing 6
specs (above) are also `ready` and can be built in parallel with the new 7
since they have disjoint scope. Total outstanding build items: 13 specs
with ~720 checklist items across Build Orders 4-22.
