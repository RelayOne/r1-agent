# Spec Review — 2026-04-21 (scope cycle 2)

Five specs drafted in this scoping cycle to close the r1-server RS-7…RS-11
gaps and the 7 research-validated corrections from the user-provided paste.

## Specs under review

| # | Spec | Lines | Items | Build order | Status |
|---|---|---|---|---|---|
| 1 | memory-bus.md | 580 | 58 | 23 | ready |
| 2 | ledger-redaction.md | 405 | 48 | 24 | ready |
| 3 | encryption-at-rest.md | 269 | 61 | 25 | ready |
| 4 | retention-policies.md | 263 | 48 | 26 | ready |
| 5 | r1-server-ui-v2.md | 471 | 91 | 27 | ready |

Totals: 1,988 lines, 306 checklist items, build orders 23-27 (follows the
prior cycle's 16-22).

## 10-check review

| Check | All 5 pass? | Notes |
|---|---|---|
| 1. STATUS / CREATED / DEPENDS_ON / BUILD_ORDER header | ✓ | Standard header on each. |
| 2. Overview section | ✓ | All use numbered headings (## 1. Overview). |
| 3. Acceptance criteria | ✓ | Explicit section on each. |
| 4. Testing section | ✓ | Explicit section on each. |
| 5. Rollout section | ✓ | Flag-gated rollout documented on each. |
| 6. Self-contained checklist items | ✓ | Spot-checked 5 items per spec — each has file path, function signature, test to add, reference to existing pattern. |
| 7. No TBD / FIXME markers | ✓ | Grep returns 0. |
| 8. Dependencies resolve | ✓ | All referenced specs (executor-foundation, provider-pool, memory-full-stack, operator-ux-memory, memory-bus, ledger-redaction, encryption-at-rest, retention-policies) exist in specs/. |
| 9. Backward-compat contract | ✓ | Each spec flag-gates the new behavior (STOKE_MEMORY_BUS, STOKE_LEDGER_V2, STOKE_ENCRYPTION, STOKE_RETENTION, R1_SERVER_UI_V2). |
| 10. Checklist size reasonable | ✓ | Range 48-91 items. Fits the 30-80 target band for 4 of 5; r1-server-ui-v2 at 91 is justified by covering 4 corrections + 2 RS features in one spec. |

## Cross-spec dependency graph

```
memory-bus (23)
  └─ executor-foundation (built, 3)
  └─ operator-ux-memory Part D (built, 7)
  └─ memory-full-stack (scoped, 20) — sibling for stoke_memories vs stoke_memory_bus

ledger-redaction (24)
  └─ (no deps — retrofits existing internal/ledger/)

encryption-at-rest (25)
  └─ memory-bus (23) — encrypts content_encrypted column
  └─ ledger-redaction (24) — provides redaction-signer key

retention-policies (26)
  └─ memory-bus (23) — sweeps expires_at
  └─ ledger-redaction (24) — calls Store.Redact()
  └─ encryption-at-rest (25) — sources redaction signer

r1-server-ui-v2 (27)
  └─ memory-bus (23) — renders memory_stored/recalled nodes
  └─ ledger-redaction (24) — renders redacted-node placeholders
  └─ retention-policies (26) — settings-page HTTP handlers
```

Strict linear build order: 23 → 24 → 25 → 26 → 27. Every dependency points
backward. No cycles.

## What these specs address from the user-provided paste

| Source doc | Section | Covering spec |
|---|---|---|
| r1-server-work.md | §RS-7 Scoped memory bus | memory-bus.md |
| r1-server-work.md | §RS-8 Skill load/unload viz | r1-server-ui-v2.md (integrated) |
| r1-server-work.md | §RS-9 Encryption at rest | encryption-at-rest.md |
| r1-server-work.md | §RS-10 Retention policies | retention-policies.md |
| r1-server-work.md | §RS-11 Memory explorer UI | r1-server-ui-v2.md (integrated) |
| r1-server-research-validated.md | Correction 1 (writer goroutine) | memory-bus.md |
| r1-server-research-validated.md | Correction 2 (SQLCipher not AES) | encryption-at-rest.md |
| r1-server-research-validated.md | Correction 3 (two-level commitment) | ledger-redaction.md |
| r1-server-research-validated.md | Correction 4 (3D perf: InstancedMesh + Worker) | r1-server-ui-v2.md |
| r1-server-research-validated.md | Correction 5 (htmx+SSE, no CDN) | r1-server-ui-v2.md |
| r1-server-research-validated.md | Correction 6 (waterfall default, 3D secondary) | r1-server-ui-v2.md |
| r1-server-research-validated.md | Correction 7 (memory grouped-list default) | r1-server-ui-v2.md |
| stoke-work.md (S-0…S-11) | All already shipped | n/a |

Every item in the paste that was actionable maps to one of these 5 specs or
was already shipped.

## Inline ambiguity resolutions captured by agents

- **memory-bus.md** — reused the wisdom SQLite handle; split fresh writer
  goroutine per instance; NATS migration path documented but not designed.
- **ledger-redaction.md** — kept `nodes/` dir as read-only shadow during
  migration with a `legacy_id` fallback in Verify(); Ed25519 signer pluggable
  via opts (encryption-at-rest supersedes once built).
- **encryption-at-rest.md** — sqlite3mc SHA research step (not hardcoded);
  added `stoke encryption export-backup` as age-wrapped backup escape valve;
  std base64 over URL-safe for JSONL (tail -f friendly); Windows LocalSystem
  DPAPI caveat documented.
- **retention-policies.md** — both `ledger_content` and legacy-named
  `prompts_and_responses` trigger redaction (forward-compat); fail-soft on
  config parse error; "WIPE ALL DATA" literal for forced wipe.
- **r1-server-ui-v2.md** — MVP UI stays default for one release cycle; v2 behind
  `R1_SERVER_UI_V2=1`; `/session/:id/graph` preserves MVP path.

## Critical issues

None. Auto-review passed on first dispatch for all 5 specs.

## Recommendation

All 5 specs are `ready` and safe to hand to `/build`. Combined with the 7
specs from scope cycle 1 (build orders 16-22) and the 14 specs from the
prior cycle, total outstanding build items: **21 specs, ~1,030 checklist items**
covering the full Stoke agent-platform vision. /build executes them in
BUILD_ORDER sequence.
