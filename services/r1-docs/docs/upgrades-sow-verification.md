# R1 Upgrades SOW — Verification Report

Generated: 2026-04-15 after commit `7aa61b2`
Branch: `feat/smart-chat-mode`
Auditor: parallel agents + codex cross-check

## Summary

```
STOKE IMPLEMENTATION STATUS
===========================

Tier 1 (S-U-001 through S-U-007):      PASS 19  PARTIAL 3  MISSING 16
Tier 2 (S-U-008 through S-U-013):      PASS 13  PARTIAL 2  MISSING  8
Tier 3 (S-U-014 through S-U-021):      PASS 13  PARTIAL 1  MISSING  9
Tier 4 — Atlas (S-U-022 through S-U-032): PASS 11  PARTIAL 3  MISSING 33
Tier 5 — AC layer:                     PASS  1  PARTIAL 0  MISSING 15

TOTAL: PASS 57 (42%) · PARTIAL 9 · MISSING 81
```

## Confirmed shipped

- **S-U-004 redact**: patterns + ≥18 char preservation + tests ✅; wired only to `internal/logging/` (not yet `replay/stream/bus`).
- **S-U-005 promptguard**: 8 patterns + 3 actions + default-Warn + builtin skip + tests ✅.
- **S-U-006 dual-threshold compression**: `buildNativeCompactor` + `CompactThreshold` (default 100k, configurable via `--compact-threshold`) + workflow.go 80% trigger implement the 50%/85% architecture ✅.
- **S-U-012 stancesign + matrix**: `docs/anti-deception-matrix.md` ✅, `internal/stancesign/` ed25519 keys out-of-band ✅, wired into main.go:3227 but NOT worktree/sow_native.go commit sites (the gap the matrix itself flags). `critic.Verdict.EvidenceRefs` exists ✅.
- **S-U-013 failure fingerprint**: 10-class taxonomy + fingerprint + TS/Go/Rust/Python parsers ✅.
- **S-U-014 OAuth usage**: PollClaudeUsage + circuit breaker (StatusCircuitOpen) + beta header ✅; header-based rate-limit fallback PARTIAL.
- **S-U-015 scheduling**: GRPW + Continuum + continuum-lite ✅; benchmark harness MISSING.
- **S-U-017 reviewereval + modelsource**: Case struct + LoadCorpus + FP/FN computation + Builder/Reviewer routing + --builder-model / --reviewer-model flags ✅; result artifacts MISSING (experiment not yet run).
- **S-U-020 stream-json**: `--output-format stream-json` + emitter + Claude-Code-compatible system/assistant/user/result/stream_event + `_stoke.dev/` namespace + terminal result on all exit paths (incl. fatal, feasibility, strict-validation, panic, normal completion) ✅.

## Shipped this session

- **S-U-001 discovery paths + scripts/assets passthrough** (commit 0c7914f): added .claude/, .codex/, .cursor/ discovery paths and scripts/+assets/ loaders. Still missing: name regex, description length check, exporter.
- **S-U-011 InjectCatalogBudgeted + ReadSkill + ListSkillNames** (commit e8d6cfd, revised d1dd09b): metadata-only catalog emits at level 0. Not yet wired as default in prompt construction; full tool integration (read_skill tool) pending.
- **S-U-020 stream-json** (commits e8d6cfd → 7aa61b2): 4 codex review rounds; terminal result emitted on every sowCmd exit path including fatal() and os.Exit.

## Top 5 gaps by blast radius

1. **S-U-012e stancesign not wired into commit sites** — the whole anti-deception story has a documented hole: signing keys exist but commits are NOT signed per-stance. `internal/worktree/` + `cmd/r1/sow_native.go` grep for `stancesign` shows zero imports.
2. **S-U-004 redact wiring incomplete** — shipped but only plugged into `internal/logging/`, not `internal/replay/`, `internal/stream/`, or hub events. Secrets can still pin to disk through replay recordings and bus payloads.
3. **S-U-011 progressive disclosure not the default** — new InjectCatalogBudgeted exists but active prompt builders still use InjectPromptBudgeted. Skill library growth will pressure token budgets.
4. **S-U-017 experiment never run** — measurement harness ready for months; no result artifacts in `docs/` or `bench/` so no defensible "Claude implements, Codex reviews" numbers.
5. **S-U-002 ACP adapter missing entirely** — `cmd/r1-acp/` doesn't exist. Every editor integration is still custom per-editor.

## Tier 4/5 status (honest)

Tier 4 (S-U-022 through S-U-032):
- **S-U-022 SOWPlan 9-state machine**: existing `internal/plan/` has flat task list + session states, not the 9-state node machine with WBS hierarchy.
- **S-U-023 capability manifest**: `internal/skillmfr/` exists but doesn't enforce whenToUse/whenNotToUse nor reject missing manifests.
- **S-U-024 Merkle DAG**: SHA256 content-addressing + 13 node types + 7 edge types ✅ (per SOW); parent-hash Merkle chain + TrustPlane anchoring + HITL node types + subgraph dedup + migration tool all MISSING.
- **S-U-025 cross-model verification at every gate**: modelsource is shipped; extension to non-code gates not yet implemented.
- **S-U-026 TrustPlane external hire**: MISSING — depends on TrustPlane T-U-030/T-U-036.
- **S-U-027 interactive track**: `internal/chat/` + `internal/conversation/` exist (smart-chat-mode branch work in flight); priority arbiter and intent classifier PARTIAL.
- **S-U-028 anti-deception 10 patterns**: smoketest + websearch SHIPPED per SOW; 7 of the other 8 patterns MISSING.
- **S-U-029 anti-laziness 5 patterns**: depcheck SHIPPED per SOW; circuit breakers + todo-list + drift detection + forced tool use MISSING.
- **S-U-030 4-tier memory**: vecindex + memory + skill exist as partial primitives; not architecturally tiered as working/episodic/semantic/procedural.
- **S-U-031 consolidation + misevolution**: MISSING.
- **S-U-032 platform surface**: `internal/mcp/` exists; framework wrappers (LangGraph, Vercel AI SDK, CrewAI) MISSING.

Tier 5 AC-U-001 through AC-U-015: entirely MISSING (as expected — not yet started).

## What this session added to the ledger

Commits d1dd09b, 5500312, 7aa61b2 + 0c7914f + e8d6cfd landed:
- Cross-tool skill discovery (`.claude/`, `.codex/`, `.cursor/`) + scripts/assets passthrough
- InjectCatalogBudgeted + ReadSkill + ListSkillNames (level-0 disclosure primitives)
- streamjson package + `--output-format stream-json` + terminal result emission on every exit path
- All validated via 4 codex review rounds (final: clean)

## Recommended next moves

Immediate (<1 day each):
- Wire S-U-004 redact into `internal/replay/`, `internal/stream/`, hub event payloads
- Wire S-U-012e stancesign into commit sites (close the matrix-flagged gap)
- Add S-U-001 name regex + description length validation

Next sprint (1 week each):
- S-U-002 ACP adapter (biggest distribution unlock)
- S-U-011 Phase 2: register read_skill tool + swap default prompt builder
- S-U-017 actually run the cross-model experiment on the shipped reviewereval harness
- S-U-008 extend cache_control to rolling message window (audit prefix stability)
