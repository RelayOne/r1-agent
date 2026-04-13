# pnpm-workspace-script-args

> How to pass flags to package scripts through pnpm's filter + run syntax — the `--` separator that trips up every LLM-generated command

<!-- keywords: pnpm, pnpm filter, pnpm run, pnpm test, pnpm exec, workspace, argument forwarding -->

## The `--` separator rule

pnpm 8+ consumes flags that come after `--filter <pkg> <script>` as pnpm's own flags, not the script's. To forward flags to the underlying script, either insert `--` as a separator, or use `exec` (which does not intercept).

```bash
# BAD — --run is eaten by pnpm, script never sees it
pnpm --filter web test --run
# Error: Unknown option: 'run'

# GOOD — explicit separator
pnpm --filter web run test -- --run

# ALSO GOOD — exec passes everything through
pnpm --filter web exec vitest run
```

## `run` vs `exec`

- `pnpm --filter <pkg> run <script> -- <args>` runs the `scripts.<script>` entry from that package's `package.json`, forwarding `<args>` after `--`.
- `pnpm --filter <pkg> exec <binary> <args>` runs `<binary>` directly from that package's resolved `node_modules/.bin/`. No separator needed because `exec` treats everything after the binary name as the binary's argv.

`exec` is usually cleaner for tooling (tsc, vitest, eslint, prisma) because you skip the script-indirection layer and the `--` dance.

## Filtering syntax

```bash
pnpm --filter @sentinel/web test                 # one named package
pnpm --filter "./packages/*" typecheck           # path glob (quote it)
pnpm --filter '!@sentinel/web' test              # exclude one package
pnpm -r typecheck                                # all packages, sequentially
pnpm -r --parallel typecheck                     # all packages, parallel
```

Path globs must be quoted or bash will expand them before pnpm sees them.

## Correct command examples

Run one package's tests, one shot (no watch):

```bash
pnpm --filter @sentinel/web exec vitest run
```

Typecheck every package in the workspace:

```bash
pnpm -r exec tsc --noEmit
# or, if each package has a "typecheck" script:
pnpm -r typecheck
```

Filter to a specific test file:

```bash
pnpm --filter @sentinel/web exec vitest run src/lib/parse.test.ts
```

Filter by test name:

```bash
pnpm --filter @sentinel/web exec vitest run -t "parses ISO dates"
```

Run tests with coverage:

```bash
pnpm --filter @sentinel/web exec vitest run --coverage
```

Run in CI mode via the script (note the `--`):

```bash
pnpm --filter @sentinel/web run test -- --run --reporter=verbose
```

## Common failure modes

```bash
# Fails: "Unknown option: coverage"
pnpm --filter web test --coverage

# Works: separator makes pnpm pass --coverage through
pnpm --filter web run test -- --coverage

# Works: exec bypasses pnpm's flag parser entirely
pnpm --filter web exec vitest run --coverage
```

```bash
# Fails: pnpm thinks -t is its own flag
pnpm --filter web test -t "pattern"

# Works:
pnpm --filter web exec vitest run -t "pattern"
```

## Gotchas

- **`pnpm --filter X script --flag` does NOT forward `--flag`** in pnpm >= 8. Either use `-- --flag` after `run script`, or switch to `exec`.
- **`exec` is the cleanest path** for running `vitest`, `tsc`, `eslint`, `prisma`, etc., because it skips argument interception.
- **Quote path-glob filters**: `"./packages/*"`, not `./packages/*`. Bash expands the latter before pnpm runs.
- **`pnpm -r` runs sequentially by default**. Add `--parallel` for parallel execution; note that parallel breaks dependency order — use turbo if order matters.
- **`pnpm --filter !<pkg>` exclusion**: the `!` must be quoted in most shells (`'!@sentinel/web'`) or escaped.
- **`run` is the explicit verb**: `pnpm --filter web test` works because pnpm infers `run`, but `pnpm --filter web run test -- --flag` is the safest spelling when forwarding arguments.
- **`ERR_PNPM_NO_SCRIPT`**: the script you named does not exist in that package's `package.json`. Fix by adding the script, not by running the tool from a different cwd.
