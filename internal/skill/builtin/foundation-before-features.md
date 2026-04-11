# foundation-before-features

> Before writing feature code, verify the workspace compiles. Before running feature ACs, verify the build is green. Foundation failures cascade into every downstream check.

<!-- keywords: foundation, build, compile, typecheck, tsc, pnpm install, node_modules, session, acceptance -->

## The cascade problem

When a workspace's foundation is broken (deps not installed, TypeScript config wrong, shared package doesn't compile), EVERY acceptance criterion that touches that code fails. The failures look like:

- "tsc: not found" — deps not installed
- "Cannot find module '@sentinel/types'" — workspace:* link broken
- "error TS2688: Cannot find type definition file for 'node'" — @types/node missing
- "error TS1005: '>' expected" — real syntax error in shared code

The repair loop then tries to fix each AC individually, but the root cause is ONE foundation issue that affects ALL of them. Three repair attempts × four failing ACs × five reasoning-loop calls = 60+ wasted LLM calls for something `pnpm install` would fix.

## The rule for task agents

Before you start writing feature code for your task:

1. **Run `pnpm install`** at the workspace root. If it fails, fix the package.json issue before writing any feature code.

2. **Run `tsc --noEmit`** (or `pnpm typecheck` if the script exists). If it fails, the shared packages have errors that your feature code will inherit. Fix them first.

3. **Only then** start implementing the feature your task describes.

If step 1 or 2 fails and the error is in code YOU didn't write (from an earlier task in this session), fix it anyway — your task will fail acceptance if the foundation is broken regardless of whose fault the breakage is.

## The rule for acceptance criteria

Session ACs should include ONE foundational criterion that runs before feature-specific checks:

```
AC1: pnpm install --silent && pnpm --filter './packages/*' typecheck
```

This catches foundation issues before the feature ACs fire. If AC1 fails, the repair loop knows to fix the shared packages, not the feature code.

## Common foundation fixes

| Error | Root cause | Fix |
|---|---|---|
| `tsc: not found` | typescript not in package's devDeps | Add `"typescript": "^5.4.0"` to devDeps, run `pnpm install` |
| `Cannot find module '@sentinel/X'` | Missing workspace:* dep | Add `"@sentinel/X": "workspace:*"` to deps |
| `Cannot find type definition file for 'node'` | Missing @types/node | Add `"@types/node": "^20.0.0"` to devDeps |
| `error TS6053: File not found` in tsconfig extends | Wrong extends path | Use relative path `../../tooling/tsconfig/base.json` |
| `ENOENT: no such file or directory` from pnpm | Package dir doesn't exist | Create the package dir with package.json, re-run `pnpm install` |

## Gotchas

- "tsc: not found" is NOT a toolchain problem — it means typescript isn't in the package's devDeps
- A cascading type error in one shared package will fail EVERY downstream AC that typechecks
- `pnpm install` after adding deps is non-optional — the dep graph is stale without it
- Foundation issues must be fixed FIRST, before feature code. Don't skip to the feature.

## Why this matters for autonomous agents

Human developers instinctively run `npm install` and check that things compile before writing feature code. LLM agents don't have this instinct — they start writing feature files immediately because the task description says "implement X", not "make sure the workspace compiles first, then implement X". This skill fills that gap.
