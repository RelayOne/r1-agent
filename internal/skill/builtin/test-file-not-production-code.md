# test-file-not-production-code

> When a task says "create a test for X", the deliverable is a .test.ts file with describe/it blocks — NOT the code being tested

<!-- keywords: test, vitest, jest, testing, describe, it, expect, test file, spec, __tests__ -->

## Read the deliverable, not the subject

A task like "Create Vitest test for `parseAlarm`" has exactly one deliverable: a test file (e.g. `__tests__/parseAlarm.test.ts`) that imports `parseAlarm` and asserts its behavior. It does NOT mean "write or rewrite `parseAlarm`". The production code is the SUBJECT of the test and must already exist.

Confusion between deliverable and subject is the single most common failure mode for this kind of task. The worker reads the subject, decides to "improve" it, and produces `src/parseAlarm.ts` content instead of `__tests__/parseAlarm.test.ts`. The test file ends up empty or missing.

## What a test file must contain

Three non-negotiable properties:

1. At least one `describe(...)` or top-level `it(...)` block from `vitest` (or `@jest/globals`).
2. An import of the thing being tested: `import { parseAlarm } from '../src/parseAlarm'`.
3. At least one `expect(...)` assertion inside each `it()` block.

```ts
// __tests__/parseAlarm.test.ts
import { describe, it, expect } from 'vitest'
import { parseAlarm } from '../src/parseAlarm'

describe('parseAlarm', () => {
  it('parses a well-formed row', () => {
    const result = parseAlarm({ id: '1', level: 'high' })
    expect(result).toEqual({ id: '1', level: 'high' })
  })

  it('rejects missing id', () => {
    expect(() => parseAlarm({ level: 'high' })).toThrow(/id/i)
  })
})
```

## Task descriptions that list two files

If the task lists both `__tests__/alarm.test.ts` AND `src/alarm.ts`:

- `__tests__/alarm.test.ts` is the DELIVERABLE. Create this file.
- `src/alarm.ts` is the SUBJECT. It must already exist. Do not overwrite it.

If the subject does not exist, the task is mis-specified — stop and flag it rather than inventing the subject to make the test pass.

## Sanity check before marking done

Run this before claiming the task is complete:

```bash
grep -cE "describe|it\(|expect\(" __tests__/alarm.test.ts
```

A correctly-written test file returns a number greater than zero. A zero means the file has no test structure and the task is not done.

Also confirm the file actually runs:

```bash
pnpm --filter <pkg> exec vitest run __tests__/alarm.test.ts
```

A passing run is the proof of deliverable. A "no tests found" error means the file is not a test file.

## Template: React component test

```tsx
// __tests__/AlarmCard.test.tsx
import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { AlarmCard } from '../src/components/AlarmCard'

describe('<AlarmCard />', () => {
  it('renders the alarm title', () => {
    render(<AlarmCard title="Door open" level="high" />)
    expect(screen.getByText('Door open')).toBeInTheDocument()
  })

  it('applies the high-severity class', () => {
    render(<AlarmCard title="X" level="high" />)
    expect(screen.getByRole('article')).toHaveClass('severity-high')
  })
})
```

Requires `@testing-library/react` and `@testing-library/jest-dom` in devDeps, plus a setup file that imports `'@testing-library/jest-dom/vitest'`.

## Template: plain TypeScript function test

```ts
// __tests__/buildCSVRow.test.ts
import { describe, it, expect } from 'vitest'
import { buildCSVRow } from '../src/buildCSVRow'

describe('buildCSVRow', () => {
  it('quotes fields containing commas', () => {
    expect(buildCSVRow(['a', 'b,c'])).toBe('a,"b,c"')
  })

  it('escapes embedded quotes', () => {
    expect(buildCSVRow(['he said "hi"'])).toBe('"he said ""hi"""')
  })

  it('returns empty string for empty input', () => {
    expect(buildCSVRow([])).toBe('')
  })
})
```

## Gotchas

- **The deliverable is the test file, not the subject**. "Create test for X" never means "implement X".
- **Do not overwrite the subject**: if `src/X.ts` exists, leave it alone. If it does not exist, stop and flag — do not invent it.
- **No `expect()` = not a test**: a file with `describe` and `it` but no assertions is not a deliverable, it's a stub. Every `it()` needs at least one `expect`.
- **Import the real subject**: `import { X } from '../src/X'`. Do not redefine X inside the test file.
- **`.test.ts` / `.test.tsx` extension**: Vitest's default `include` pattern is `**/*.{test,spec}.{ts,tsx,js,jsx}`. Files without this suffix are ignored.
- **Sanity-grep before marking done**: `grep -cE "describe|it\(|expect\(" <file>` must be > 0.
- **Run the test, don't just write it**: `vitest run <path>` should exit 0. "No tests found" means the file is not structured as a test.
- **React tests need the DOM**: set `test.environment = 'jsdom'` in `vitest.config.ts` and import `@testing-library/jest-dom/vitest` in a setup file, or `toBeInTheDocument` is undefined.
