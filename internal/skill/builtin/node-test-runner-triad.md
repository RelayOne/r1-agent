# node-test-runner-triad

> Tests in JS/TS projects only run when three things exist together — the runner dep, the runner config file, and a script that invokes the runner. Any one missing and the tests are dead code.

<!-- keywords: vitest, jest, test runner, test script, vitest.config, jest.config, pnpm test -->

## The triad rule

A test file that imports `describe`, `it`, `expect` does NOT run unless all three of these exist:

1. **Runner dependency** declared in the `package.json` that owns the tests. For vitest: `"vitest": "^1.x"` in `devDependencies`. For jest: `"jest": "^29.x"` + `"@types/jest": "^29.x"` + a transform like `ts-jest` or `@swc/jest`.

2. **Runner config file** at the package root (or wherever the runner looks by default):
   - Vitest: `vitest.config.ts` or `vitest.config.js`
   - Jest: `jest.config.ts` or `jest.config.js` (CommonJS required unless using ESM flags)

3. **Test script** in the same `package.json` that invokes the runner:
   ```json
   { "scripts": { "test": "vitest run", "test:watch": "vitest" } }
   ```

Missing any one of these = tests don't run, test coverage is 0, and any AC that says "tests pass" silently becomes "no tests found".

## Vitest minimum setup

For a Next.js / Vite / standalone TS package:

`package.json`:
```json
{
  "scripts": { "test": "vitest run", "test:watch": "vitest" },
  "devDependencies": {
    "vitest": "^1.6.0",
    "@vitest/coverage-v8": "^1.6.0",
    "jsdom": "^24.0.0",
    "@testing-library/react": "^15.0.0"
  }
}
```

`vitest.config.ts`:
```ts
import { defineConfig } from 'vitest/config'

export default defineConfig({
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./vitest.setup.ts'],
    include: ['**/*.test.{ts,tsx}', '**/*.spec.{ts,tsx}'],
  },
})
```

`vitest.setup.ts` (empty is fine — it just has to exist):
```ts
import '@testing-library/jest-dom/vitest'
```

Run via `pnpm test` or `pnpm --filter <pkg> test`.

## Jest minimum setup

For older codebases or React Native:

`jest.config.ts`:
```ts
import type { Config } from 'jest'

const config: Config = {
  preset: 'ts-jest',
  testEnvironment: 'jsdom',
  setupFilesAfterEach: ['<rootDir>/jest.setup.ts'],
  moduleNameMapper: {
    '^@/(.*)$': '<rootDir>/src/$1',
  },
}

export default config
```

`package.json`:
```json
{
  "scripts": { "test": "jest" },
  "devDependencies": {
    "jest": "^29.7.0",
    "@types/jest": "^29.5.0",
    "ts-jest": "^29.1.0"
  }
}
```

## Where tests live matters

Tests should live in the SAME package as the code they test. Don't create a single `packages/tests/` package and point it at everyone else's code — each package runs its own test script via `pnpm --filter <pkg> test`.

File locations that runners pick up by default:
- `src/**/*.test.{ts,tsx}` (inline with source — recommended)
- `__tests__/**/*.{ts,tsx}`
- `tests/**/*.{ts,tsx}` (if configured in `include`)

## React Testing Library checklist

If you import anything from `@testing-library/react`:
1. `@testing-library/react` in devDeps
2. `@testing-library/jest-dom` in devDeps (for matchers like `toBeInTheDocument`)
3. `jsdom` in devDeps (the DOM environment vitest/jest uses)
4. `environment: 'jsdom'` in the runner config
5. A setup file that imports `@testing-library/jest-dom/vitest` (or `.../jest` for jest)

Skipping any of these produces confusing errors like "document is not defined" or "toBeInTheDocument is not a function".

## Gotchas

- Tests without a runner dep + config file + test script = dead code that silently passes "0 tests found"
- vitest needs `environment: 'jsdom'` for React components, otherwise "document is not defined"
- `@testing-library/jest-dom` must be imported in a setup file, not inline in each test
- Jest and vitest have different setup file keys: `setupFilesAfterSetup` vs `setupFiles`
- Test files must match the runner's `include` glob: `**/*.test.{ts,tsx}` is the vitest default

## Common failure signatures

| Error | Cause | Fix |
|---|---|---|
| `No test files found` | Runner config `include` glob doesn't match the test files | Fix the glob OR move tests to a default location |
| `0 tests passed, 0 tests failed` | Tests exist but runner skipped them all | Check `include`/`exclude` patterns and file name suffixes |
| `vitest: not found` | Runner not in package's own devDependencies | Add it to the package's devDeps, not the root |
| `Cannot find module 'vitest'` inside test file | Same as above | Same fix |
| `document is not defined` | Runner environment is `node`, should be `jsdom` | Set `environment: 'jsdom'` |
| `toBeInTheDocument is not a function` | `@testing-library/jest-dom` not imported in setup file | Add `import '@testing-library/jest-dom/vitest'` to setup |

## Don't ship test files without the runner

If the task says "add login page tests", you must:
1. Add the test file
2. Add vitest (or whatever runner) to the package's devDependencies
3. Create vitest.config.ts in the package
4. Add `"test": "vitest run"` script in the package's package.json
5. Run `pnpm install` to fetch vitest into node_modules
6. Run `pnpm --filter <pkg> test` yourself via bash and verify tests actually execute and pass

If any of these steps is missed, the test file is delivered as dead code and any downstream AC that checks `pnpm test` will fail.
