# Build Plan — cortex-concerns (BUILD_ORDER 2)

Spec: `specs/cortex-concerns.md`. Branch: `build/cortex-concerns`. Started 2026-05-02.

## Items

**Shared LLM infra (TASKs 1-5):**
- [x] TASK-1: LobePromptBuilder (cache-aligned) (commit: 6692660b)
- [x] TASK-2: Escalator (Haiku → Sonnet flag) (commit: 7f4ef58)
- [x] TASK-3: Lobe enable-flags config schema (commit: 125fb969 + 02f78795)
- [x] TASK-4: Note.Meta key conventions (commit: e79cbc41)
- [x] TASK-5: AcquireLLMSlot wrapper (commit: f9cfee63)

**MemoryRecall Lobe (TASKs 6-9):**
- [x] TASK-6: MemoryRecallLobe scaffold + tfidf (commit: 83340679)
- [x] TASK-7: OnRound publishes top-3 (commit: ef71372f)
- [x] TASK-8: Subscribe + reindex on memory_added (commit: 3a492b3f)
- [x] TASK-9: Privacy redaction (commit: 2c45c935)

**WALKeeper Lobe (TASKs 10-12):**
- [x] TASK-10: WALKeeperLobe scaffold + framing (commit: d67f2c0b)
- [x] TASK-11: Backpressure (commit: e29640bb)
- [x] TASK-12: Restart-replay verification (commit: 67d68bf1)

**RuleCheck Lobe (TASKs 13-15):**
- [x] TASK-13: Subscribe to supervisor.rule.fired (commit: bfba13c6)
- [x] TASK-14: severityFor mapping (commit: 8b4169a2)
- [x] TASK-15: Convert to sticky Notes (commit: b263e58e)

**PlanUpdate Lobe (TASKs 16-20):**
- [x] TASK-16: Scaffold (commit: fc91f638)
- [x] TASK-17: Trigger cadence (every 3rd turn or verb-scan) (commit: 542c485d)
- [x] TASK-18: Haiku call + system prompt verbatim (commit: a7a55542)
- [x] TASK-19: JSON parse + auto-apply edits, queue adds/removes (commit: b6594d20)
- [x] TASK-20: Apply on user confirmation (commit: 564b267d)

**ClarifyingQ Lobe (TASKs 21-25):**
- [x] TASK-21: Scaffold (commit: 137b277d)
- [x] TASK-22: tool schema verbatim (commit: 6c6c19c4)
- [x] TASK-23: system prompt verbatim (commit: a1180b34)
- [x] TASK-24: Turn-after-user trigger + cap-3 (commit: 62cdebf5 + 0deef8e5)
- [x] TASK-25: Resolve on user-answer (commit: f8c64307)

**MemoryCurator Lobe (TASKs 26-31):**
- [x] TASK-26: Scaffold + PrivacyConfig (commit: a0222ba5)
- [x] TASK-27: tool schema verbatim (commit: 16560fc1)
- [x] TASK-28: system prompt verbatim (commit: 923c1739)
- [x] TASK-29: Trigger every 5 turns (commit: c2741f21)
- [x] TASK-30: Privacy filter + audit log (commit: 0e08a42f)
- [x] TASK-31: r1 cortex memory audit CLI (commit: 3a5774cd)

**Cross-cutting integration (TASKs 32-36):**
- [x] TASK-32: All-lobes integration test (no goroutine leaks)
- [x] TASK-33: Cost budget honored
- [x] TASK-34: Enable flags honored
- [x] TASK-35: Daemon-restart Note recovery
- [x] TASK-36: Cache-hit verification under fan-out

## Approach

Dispatch in dependency batches:
- B1 (parallel): TASKs 1, 2, 3, 4, 5 — different files
- B2 (parallel): TASK-6 (memrecall scaffold), TASK-10 (walkeeper scaffold), TASK-13 (rulecheck scaffold), TASK-16 (planupdate scaffold), TASK-21 (clarifyq scaffold), TASK-26 (curator scaffold) — all in different package paths
- B3 (parallel): TASK-7, 8, 9 (memrecall), TASK-11, 12 (walkeeper), TASK-14, 15 (rulecheck), TASK-17–20 (planupdate), TASK-22–25 (clarifyq), TASK-27–31 (curator)
- B4: TASK-32–36 integration

## Status

In progress.
