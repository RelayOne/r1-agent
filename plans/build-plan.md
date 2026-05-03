# Build Plan — cortex-concerns (BUILD_ORDER 2)

Spec: `specs/cortex-concerns.md`. Branch: `build/cortex-concerns`. Started 2026-05-02.

## Items

**Shared LLM infra (TASKs 1-5):**
- [x] TASK-1: LobePromptBuilder (cache-aligned) (commit: 6692660b)
- [ ] TASK-2: Escalator (Haiku → Sonnet flag)
- [ ] TASK-3: Lobe enable-flags config schema
- [ ] TASK-4: Note.Meta key conventions
- [ ] TASK-5: AcquireLLMSlot wrapper

**MemoryRecall Lobe (TASKs 6-9):**
- [ ] TASK-6: MemoryRecallLobe scaffold + tfidf
- [ ] TASK-7: OnRound publishes top-3
- [ ] TASK-8: Subscribe + reindex on memory_added
- [ ] TASK-9: Privacy redaction

**WALKeeper Lobe (TASKs 10-12):**
- [ ] TASK-10: WALKeeperLobe scaffold + framing
- [ ] TASK-11: Backpressure
- [ ] TASK-12: Restart-replay verification

**RuleCheck Lobe (TASKs 13-15):**
- [ ] TASK-13: Subscribe to supervisor.rule.fired
- [ ] TASK-14: severityFor mapping
- [ ] TASK-15: Convert to sticky Notes

**PlanUpdate Lobe (TASKs 16-20):**
- [ ] TASK-16: Scaffold
- [ ] TASK-17: Trigger cadence (every 3rd turn or verb-scan)
- [ ] TASK-18: Haiku call + system prompt verbatim
- [ ] TASK-19: JSON parse + auto-apply edits, queue adds/removes
- [ ] TASK-20: Apply on user confirmation

**ClarifyingQ Lobe (TASKs 21-25):**
- [ ] TASK-21: Scaffold
- [ ] TASK-22: tool schema verbatim
- [ ] TASK-23: system prompt verbatim
- [ ] TASK-24: Turn-after-user trigger + cap-3
- [ ] TASK-25: Resolve on user-answer

**MemoryCurator Lobe (TASKs 26-31):**
- [ ] TASK-26: Scaffold + PrivacyConfig
- [ ] TASK-27: tool schema verbatim
- [ ] TASK-28: system prompt verbatim
- [ ] TASK-29: Trigger every 5 turns
- [ ] TASK-30: Privacy filter + audit log
- [ ] TASK-31: r1 cortex memory audit CLI

**Cross-cutting integration (TASKs 32-36):**
- [ ] TASK-32: All-lobes integration test (no goroutine leaks)
- [ ] TASK-33: Cost budget honored
- [ ] TASK-34: Enable flags honored
- [ ] TASK-35: Daemon-restart Note recovery
- [ ] TASK-36: Cache-hit verification under fan-out

## Approach

Dispatch in dependency batches:
- B1 (parallel): TASKs 1, 2, 3, 4, 5 — different files
- B2 (parallel): TASK-6 (memrecall scaffold), TASK-10 (walkeeper scaffold), TASK-13 (rulecheck scaffold), TASK-16 (planupdate scaffold), TASK-21 (clarifyq scaffold), TASK-26 (curator scaffold) — all in different package paths
- B3 (parallel): TASK-7, 8, 9 (memrecall), TASK-11, 12 (walkeeper), TASK-14, 15 (rulecheck), TASK-17–20 (planupdate), TASK-22–25 (clarifyq), TASK-27–31 (curator)
- B4: TASK-32–36 integration

## Status

In progress.
