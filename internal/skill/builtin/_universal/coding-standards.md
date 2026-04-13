# Coding Standards (universal, injected into every agent)

Dense rules for LLM consumption. Override at `.stoke/coding-standards.md` (project) or `~/.stoke/coding-standards.md` (user).

## Output discipline

- Complete every task. Do not declare done if any declared file is missing or empty.
- No stubs. No `TODO`, `FIXME`, `unimplemented!`, `panic("TODO")`, `raise NotImplementedError`, `throw new Error("not implemented")`, `// placeholder`, empty function bodies that return mock data.
- No type-safety bypasses unless a comment cites a real reason: no `any` without justification, no `@ts-ignore`, no `as unknown as X`, no `// eslint-disable` without a rule name + reason.
- Real error handling on boundaries: external I/O (HTTP, DB, FS, subprocess) must handle error paths. Internal pure functions trust their inputs.

## File semantics

- One concern per file. Route handlers don't re-export helpers. Barrel files use named re-exports, not `export *` across overlapping namespaces.
- Test files live at `__tests__/<name>.test.ts(x)` or colocated as `<name>.test.ts(x)`. Production code goes elsewhere.
- Imports are explicit. No default exports except where the framework requires them (Next.js `page.tsx`, `layout.tsx`, `middleware.ts`).
- Config in config files. No secrets in code. Env var access goes through one typed loader.

## Test discipline

- When a task says "create test for X", the deliverable is the test file. Tests must contain `describe`/`it`/`expect` (or equivalent) and actual assertions, not just `expect(true).toBe(true)`.
- Mocks are explicit + typed. No untyped `vi.fn()` that swallow real signatures.
- Coverage goals are in config files (`vitest.config.ts`, `jest.config.ts`), not CLI flags.

## Verification before declaring done

- Run the verification command for the task before reporting success. If the task's AC is `pnpm build`, run `pnpm build` and inspect its exit code.
- Read your own output. A worker that emits `✓ done` without running the verification command is lying by omission.
- Any non-zero exit from the AC command = task not done. No "mostly working" outcomes.

## Monorepo boundaries

- Each package owns its `package.json` dependencies. Importing `@scope/pkg` from `apps/web` requires `@scope/pkg` in `apps/web/package.json`, not just the root.
- `workspace:*` for sibling package references.
- Every `package.json` has `name`, `version`, `private: true` (internal packages), and correct `exports`/`main`.

## Commit-level hygiene

- Don't commit `node_modules`, `.env`, build artifacts, generated lockfiles you don't own.
- Don't create documentation files (`README.md`, etc.) unless a task explicitly asks for them.
- Don't add code comments that describe what the next line does — comments explain WHY, not WHAT.

## Anti-deception

- Silent skips are forbidden. If a check is skipped, the skip must be explicit with a reason.
- Don't mark failures as "pre-existing" or "out of scope" to avoid fixing them — either fix it or surface it as BLOCKED with the specific reason.
- When the verification command is broken (malformed flag, missing binary, etc), report that as a finding. Don't rewrite the AC to pass.
