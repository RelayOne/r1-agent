# vitest-cli-discipline

> How to invoke Vitest correctly in 2026 — flag renames, pnpm forwarding, test filtering, coverage, and common "doesn't work" causes

<!-- keywords: vitest, test, testing, unit test, testNamePattern, grep, pnpm test, coverage -->

## The flag rename that breaks most LLM-generated commands

Vitest 2.x renamed `--grep` to `--testNamePattern` (short form `-t`). Jest-era tutorials still document `--grep`, and LLMs emit it reflexively. If you see `--grep` in a Vitest command, it is wrong and will fail with "Unknown option".

```bash
# BAD — Jest syntax, Vitest will reject
vitest run --grep "handles empty input"

# GOOD
vitest run -t "handles empty input"
vitest run --testNamePattern "handles empty input"
```

The pattern is a substring match by default and supports regex.

## `vitest` vs `vitest run`

`vitest` with no subcommand starts watch mode and hangs forever. This is a common CI/agent failure: the task "runs tests" and never returns. `vitest run` executes once and exits.

```bash
# BAD — hangs in CI, hangs under stoke
vitest

# GOOD — one shot, exits with code 0 or non-zero
vitest run
```

## Filtering to a specific file

Pass the file path positionally. No glob required; Vitest resolves relative paths from cwd.

```bash
vitest run src/lib/parse.test.ts
vitest run -t "parses ISO dates" src/lib/parse.test.ts
```

## Coverage requires more than a flag

`vitest run --coverage` alone will fail if the coverage provider is not installed. Vitest's default provider is `v8`, which requires the separate `@vitest/coverage-v8` devDep AND a `coverage` block in `vitest.config.ts`.

```json
// package.json
{ "devDependencies": { "@vitest/coverage-v8": "^2.0.0" } }
```

```ts
// vitest.config.ts
export default defineConfig({
  test: {
    coverage: {
      provider: 'v8',
      reporter: ['text', 'html'],
    },
  },
})
```

Only then does `vitest run --coverage` work.

## Setup files live in config, not on the CLI

There is no `--setup` flag. Global setup lives in `vitest.config.ts`:

```ts
export default defineConfig({
  test: {
    setupFiles: ['./test/setup.ts'],
    globals: true,
  },
})
```

## Mock hoisting and dynamic import

`vi.mock('./foo', ...)` is hoisted to the top of the file by Vitest's import analyzer at parse time. Dynamic `import('./foo')` inside an `it()` block is NOT hoisted, so the mock is never wired up and the real module loads.

```ts
// BAD — mock doesn't apply, real module is imported
it('works', async () => {
  vi.mock('./api', () => ({ fetchX: () => 'mocked' }))
  const { useX } = await import('./useX')
})

// GOOD — top-level mock, hoisted before any import
vi.mock('./api', () => ({ fetchX: () => 'mocked' }))
import { useX } from './useX'
```

## CLI cheatsheet

Five commands that cover 95% of needs:

```bash
vitest run                                    # all tests, one shot
vitest run src/foo.test.ts                    # one file
vitest run -t "pattern"                       # filter by test name
vitest run --coverage                         # with coverage (requires setup)
vitest run --reporter=verbose                 # show each it() name
```

## Gotchas

- **`--grep` does not exist in Vitest 2.x**: use `-t` or `--testNamePattern`. Jest guides lie here.
- **Bare `vitest` hangs**: always use `vitest run` in CI or agent contexts.
- **Coverage needs a provider package**: `@vitest/coverage-v8` (or `-istanbul`) must be in devDeps, and `test.coverage.provider` must be set in `vitest.config.ts`.
- **No `--setup` flag**: setup files go in config under `test.setupFiles`.
- **Dynamic imports break mocks**: put `vi.mock(...)` at the top level of the test file, never inside `it()`.
- **Paths are relative to cwd**: run from the package root, not the monorepo root, or pass an absolute path.
- **Exit code matters**: `vitest run` exits non-zero on failure. Don't pipe through anything that swallows the exit code.
