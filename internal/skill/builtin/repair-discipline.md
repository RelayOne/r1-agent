# repair-discipline

> When fixing a failing acceptance criterion, fix ONE thing, verify it, then move to the next. Never batch-fix multiple unrelated failures in one pass.

<!-- keywords: repair, fix, acceptance, criterion, failing, stuck, retry, debug -->

## The single-fix rule

When you're in repair mode fixing a failing acceptance criterion:

1. **Read the failure output completely.** Don't skim — the specific error string tells you exactly what's wrong.

2. **Identify the SINGLE most fundamental root cause.** If you see "tsc: not found" AND "Cannot find module zod", fix "tsc: not found" first because it's more fundamental (missing typescript dep means nothing can typecheck).

3. **Make ONE change** that addresses that root cause. Don't touch other files "while you're at it".

4. **Re-run the exact failing command** via bash and confirm it exits 0.

5. **Only then** look at whether other criteria still fail.

## Why single-fix matters

When a repair agent tries to fix 4 things at once:
- It fixes 2 correctly
- It partially fixes 1 (leaving a subtle bug)
- It breaks something that was passing while fixing the 4th

Net result: still 2-3 failing criteria, but now different ones, and the repair loop can't converge because the failure set keeps shifting.

When a repair agent fixes 1 thing per pass:
- Pass 1: fixes the most fundamental issue
- Pass 2: fixes the next issue (which may have been a symptom of the first)
- Pass 3: fixes a real remaining issue
- Pass 4: done

Same number of total changes, but each one is verified before moving on.

## Diagnosing root causes

Common failure patterns and their actual root causes:

| What you see | What's actually wrong | The real fix |
|---|---|---|
| 4 ACs fail with "Cannot find module X" | `pnpm install` wasn't run after adding a dep | Run `pnpm install`, then re-check all 4 |
| 3 ACs fail with different TypeScript errors | One shared package has a type error that cascades | Fix the shared package, re-typecheck |
| 2 ACs fail with "file not found" | The task created the file in the wrong directory | Move the file, don't create a second copy |
| AC fails with "missing script: build" | package.json has no build script | Add the script, don't change the AC command |
| AC fails with "0 tests found" | Test runner config doesn't match test file glob | Fix the config's `include` pattern |

## Gotchas

- Fixing 4 things at once usually fixes 2 and breaks 1 — fix ONE, verify, then next
- "tsc: not found" and "Cannot find module zod" are often the SAME root cause (missing dep)
- Don't rewrite the AC command unless the reasoning loop told you to (verdict: ac_bug)
- Always re-run the exact failing command BEFORE ending — "should be fixed" is not verification
- pnpm install after any package.json change — the most common missed step in repair

## What NOT to do in repair mode

- Don't rewrite the AC command unless the reasoning loop explicitly told you to (verdict: `ac_bug`)
- Don't add new features — repair mode is for fixing, not extending
- Don't delete test files because they fail — fix why they fail
- Don't switch to a different testing framework because the configured one has errors
- Don't "simplify" code to make an error go away — fix the error
- Don't end your turn saying "this should fix it" without actually running the command to verify
