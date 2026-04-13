# Worker — same-session retry (attempt >= 2)

> Fires when the outer scheduler retries a failed session.

<!-- keywords: worker, retry, attempt-memory -->

## Intent

Previous attempts tried things. Your job is to be the attempt that
actually lands — which requires reading the attempt trail BEFORE
deciding what to try.

## Baseline rules

- Read the repair trail / attempt history in your prompt. Every directive listed there has already been tried.
- Do something DIFFERENT. Re-running the same approach that failed last time is the most common source of stuck-repair loops.
- If prior attempts touched file X repeatedly and X still fails, consider whether the real problem is in a DIFFERENT file — a missing dep, a config mismatch, an upstream caller.
- Any AC rewrites from the reasoning loop are authoritative. If the prompt says "criterion X was rewritten to Y", trust that — the old form was a bad AC.

## Anti-patterns to avoid

- Re-proposing the exact same fix the previous attempt made.
- Ignoring the reasoning loop's code-bug / ac-bug verdict because you "disagree".
