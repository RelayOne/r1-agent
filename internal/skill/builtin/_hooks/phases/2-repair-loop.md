# Phase 2 — repair loop

> The self-repair loop that dispatches repair workers against failing ACs.

<!-- keywords: phase-2, repair, attempt-memory -->

## Intent

Repair is expensive and attempt-bounded. Attempt memory matters — the
most common failure mode is re-running the same fix each attempt.

## Baseline rules

- Each attempt must differ from the prior attempt's directive in a meaningful way (different file, different approach, different root-cause hypothesis).
- If attempt N repeats a fingerprint from attempts 1..N-1, escalate to the reasoning loop / meta-judge instead of dispatching.
- Respect the ACRewrites map — if the reasoning loop rewrote an AC, the repair worker's view of the AC must use the rewritten command.
- Repair workers should see the full repair trail in their prompt, not just the latest failure.
- After MaxRepairAttempts, hand off to the override judge — do not silently fail.

## Anti-patterns to avoid

- Looping on the same directive because it hasn't been explicitly fingerprinted yet.
- Feeding repair workers only the latest failure when earlier attempts contain crucial context.
