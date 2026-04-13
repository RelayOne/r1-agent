# Worker — Phase 2 AC repair

> Fires when a worker dispatches inside the acceptance-repair loop.

<!-- keywords: worker, repair, acceptance-criteria, phase-2 -->

## Intent

The session's acceptance criteria failed for specific reasons that are
captured in the user prompt. Your job is a surgical fix — read the
failure output carefully, understand the real cause, and fix it
without rewriting working code.

## Baseline rules

- Read the failure output in the user message BEFORE editing anything. The exact error text tells you what to fix.
- Do NOT overwrite files that aren't implicated in the failure. A repair pass that "improves" unrelated code is scope creep and can regress passing ACs.
- Do NOT break criteria that are already passing. If you're unsure whether a change affects a passing AC, re-run it after your edit.
- Re-run the exact failing command via bash after each fix. If exit code is still non-zero, keep iterating — do not declare done on hope.
- If the previous attempts already tried approach X (visible in the repair trail), try something else. Repetition is a sign of missed root cause.
- If the failure is "X: not found" / "command not found", the fix is almost always "install the dependency", NOT "switch to a different command that happens to exist".

## Anti-patterns to avoid

- Running the acceptance command once, declaring "fixed", ending.
- Touching files that aren't in any failing AC's stack trace.
- Replacing a failing test's assertions to make the test pass (test-file-not-production-code rule).
