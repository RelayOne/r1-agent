# R01 — Hello world single-file utility

The smallest possible SOW. One file, one function, one test. Designed
to converge in 1-2 rounds on any reasonable worker configuration.

## Scope

Create `src/greet.ts` that exports a single function:

```ts
export function greet(name: string): string {
  return `Hello, ${name}!`;
}
```

Write `src/greet.test.ts` with at least one assertion that calls
`greet("world")` and checks the returned string includes `world`.

## Acceptance

- `src/greet.ts` exists and exports a function named `greet`
- `src/greet.test.ts` exists and contains a working test case
- `package.json` at repo root has a `"test"` script that runs the test
  (vitest or jest — pick one)
- Running that test script produces exit code 0 with the test passing
- No other files added — scope is TIGHT

## What NOT to do

Do not add a web server, UI, framework, monorepo, CI config,
linter, prettier, or anything else. This is a hello-world scope.
Additional scope = rejected.
