# Known Gotchas (universal, injected into every agent)

Short-form failure patterns that recur across LLM-generated code. Each line: PATTERN → CORRECT. Override at `.stoke/known-gotchas.md` (project) or `~/.stoke/known-gotchas.md` (user).

## CLI flags LLMs emit that are wrong

- `vitest --grep` → `vitest -t` or `vitest --testNamePattern` (Vitest ≥2 dropped `--grep`, Jest syntax)
- `jest --config ./jest.config.ts --run` → `jest --run` alone works; `--config` needs explicit path if not at root
- `pnpm --filter web test --run` → `pnpm --filter web exec vitest run` (pnpm intercepts `--run`)
- `pnpm --filter web test --someflag` → `pnpm --filter web run test -- --someflag` (use `--` to forward)
- `tsc /some/dir` → `tsc -p tsconfig.json` or `tsc file1.ts file2.ts` (tsc doesn't take bare dir paths)
- `npm run build --parallel` → npm has no `--parallel`; use turbo/nx/pnpm-r for that
- `docker build` inside AC (CI usually doesn't have docker) → skip or gate on env
- `curl | bash` in any AC → never; bypasses all safety

## TypeScript / Next.js

- `app/**/route.ts` only exports `GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS` + config consts. Any other named export = build error "X is not a valid Route export field"
- `NextApiRequest` / `NextApiResponse` is pages-router API. App-router uses `NextRequest` / `NextResponse` from `next/server`
- `export *` across multiple barrels where names overlap → TS2308 / TS2484. Use named re-exports: `export { Foo } from './a'`
- Zod schemas + TS interfaces from the same domain — export one, derive the other with `z.infer`; don't duplicate
- `as any` inside tests is still a lie. If the real type is annoying, fix the test's fixture shape
- `strictNullChecks` + `exactOptionalPropertyTypes` = `{ foo?: string }` is NOT the same as `{ foo: string | undefined }`. Match signatures exactly
- tsc error TS6059 ("File is not under rootDir") = tsconfig `rootDir` doesn't cover the include paths. Fix rootDir or include, don't disable strict

## pnpm + monorepo

- `workspace:*` for sibling package deps, not semver ranges
- Every script's first token (`tsc`, `next`, `vitest`, `eslint`, …) must be in that package.json's own `devDependencies` — pnpm doesn't hoist bins by default
- `package.json` without `name` → pnpm can't resolve `workspace:*` references. Always set `name`
- Scripts that reference `./scripts/foo.js` must have `foo.js` exist on disk. Missing helper files = build break

## React / Next.js / UI

- Server components can't use hooks. `'use client'` at top of file to opt in
- Server actions need `'use server'` at top of file or inside async function
- `<Card>` / shadcn components need their import: `import { Card } from '@/components/ui/card'`. Build error `Unexpected token Card. Expected jsx identifier` = missing import
- `useEffect` with an async body = anti-pattern. Wrap an async inner function, call it, don't `return` the promise
- `key` is required on list items. Array index is not a safe key when the list reorders

## Testing

- Task "create Vitest test for X" — DELIVERABLE is `*.test.ts(x)` with `describe`/`it`/`expect`, NOT the production code for X
- Test file paths must match what the AC command checks. If AC runs `vitest run __tests__/foo.test.tsx`, the test file goes at that exact path
- `vi.mock` + `vi.mocked` — mock the module at top level; reference typed mocks inside tests
- Async assertions need `await expect(...).resolves.toEqual(...)` OR `await promise; expect(...)` — not plain `expect(promise)`
- Coverage thresholds in `vitest.config.ts`'s `test.coverage.thresholds`, not CLI

## Test quality (what tests actually catch bugs)

- **Coverage ≠ quality.** Measured evidence: some test suites hit 100% line coverage but 4% mutation score. 100% coverage with trivial assertions (`expect(result).toBeDefined()`, `expect(() => fn()).not.toThrow()`) is worthless
- **Write intent, not behavior.** LLM oracle accuracy on "is this assertion correct given the spec" is <50% (coin flip). If the code is wrong, a test that just mirrors what the code does will also be wrong. Read the AC / spec / docstring and assert what SHOULD happen, not what the function currently returns
- **Over-mocking is the agent anti-pattern.** Industry data: 36% of agent commits add mocks vs 26% for humans. A test that mocks the thing under test validates nothing. Reach for real dependencies; mock only at system boundaries (network, fs, clock, randomness)
- **Unordered-collection flakiness = 63% of flaky tests.** `Map`, `Set`, `dict`, `object.keys()`, `os.listdir()` — these return entries in unspecified order. Never assert on exact order unless you sort first, or assert on set equality
- **Assertion patterns that catch real bugs**: equality of concrete expected values, error-type + error-message matching, invariants that must hold after any input (roundtrip, idempotence, commutativity), boundary inputs (0, -1, MAX, empty, single-element, duplicates). Avoid `.toBeTruthy()`/`.toBeDefined()` as a primary assertion — they pass on almost any non-null output
- **Property-based tests find bugs example tests miss.** Combined PBT+EBT catches 81% of bugs vs 69% for either alone. When the framework supports it (Hypothesis/fast-check/proptest/jqwik) and the function has a clear invariant, prefer a property test over 10 example tests
- **Common-sense invariants worth asserting as properties**: `parse(serialize(x)) == x`, `sort(sort(x)) == sort(x)`, `merge(a,b) == merge(b,a)` when commutative, `len(output) <= len(input)` for filters, non-negative for durations/counts, monotonic for sequence IDs
- **Canary-style self-check for important suites**: one test that MUST fail if the test runner is actually executing (`expect(false).toBe(true)` guarded by an env var). Detects "test suite silently not running" — a recurring failure mode

## Test-file hygiene

- Never test-file-overwrite production code with a test file (same path). The AC will still fail and the diff will look insane
- Never commit `.only` or `.skip` — they silently disable surrounding tests
- `describe.only` / `it.only` locally is fine for iteration; remove before declaring the task done
- `toMatchSnapshot()` without first reviewing the snapshot is lazy — always read the generated snapshot file once

## pnpm + shadcn/ui specifics

- shadcn components frequently duplicate names across files (Cancel in alert-dialog and dialog, Close in both, etc). When building an index barrel, use named re-exports per file
- `@radix-ui/*` packages are peer deps of shadcn components. Declare in the package that uses the component, not just the package defining the component

## Build + CI

- `turbo` needs `turbo.json` and per-package `package.json` scripts
- CI jobs have no local node_modules — `pnpm install` is required first
- `EAS Build` needs `expo` + `eas-cli` and valid `eas.json` with real project IDs (not placeholders)
- `vercel.json` `buildCommand` must reference real scripts in the root `package.json`

## What LLM agents hallucinate most

- Non-existent CLI flags (always verify before using)
- File paths that are plausible but not declared in the task's Files list
- Dependencies not installed (importing `@radix-ui/react-slot` without adding to deps)
- Helper function names that sound right but don't exist in the imported module
- Config keys that used to work in an older version (`webpack` keys in Next.js 14, `moduleFileExtensions` in Vitest)

## Escalation

- When stuck: read the exact error message. Don't guess. Re-run with verbose flag.
- When still stuck: the problem is usually 1 layer above the file you're editing (parent config, build tool version, peer dep).
- Never declare "this isn't fixable" without first checking: (1) is the tool at the expected version? (2) are all peer deps declared? (3) is the command itself well-formed?
