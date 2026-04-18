# R02 — Single TypeScript package scaffold

A complete but minimal TypeScript library package: package.json +
tsconfig.json + src/ + tests/ + README + working build + passing
tests. Scope = one directory tree, no monorepo, no framework.

## Scope

Create a package `@scope/slugify` that exports:

- `slugify(input: string): string` — converts `"Hello World!"` to
  `"hello-world"`. Strip non-alphanumeric, collapse whitespace to
  single hyphens, lowercase.

Tests at `tests/slugify.test.ts` covering:
- Simple ASCII: `"Foo Bar"` → `"foo-bar"`
- Mixed case: `"Hello World"` → `"hello-world"`
- Special chars: `"Hello, World!"` → `"hello-world"`
- Multiple whitespace: `"a   b"` → `"a-b"`
- Empty input: `""` → `""`

## Acceptance

- `package.json` with `name`, `version`, `main` (points to dist/ or src/),
  `scripts.test` (runs the test runner), `scripts.build` (tsc or similar)
- `tsconfig.json` configured for Node ESM output
- `src/slugify.ts` exports the function with correct semantics
- `tests/slugify.test.ts` has >= 5 assertions covering the cases above
- `README.md` explains what the package does and how to install/use it
- Running `npm test` (or equivalent) exits 0
- Running `npm run build` produces compiled output or reports no errors

## What NOT to do

No CI config, no linter, no web server, no framework. This is a
standalone utility library.
