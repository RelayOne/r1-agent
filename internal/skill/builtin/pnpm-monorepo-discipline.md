# pnpm-monorepo-discipline

> How pnpm workspaces, turborepo, and monorepo dependencies actually work — what you must declare in every package.json so nothing breaks silently

<!-- keywords: pnpm, monorepo, turborepo, workspace, turbo, pnpm-workspace, workspace:* -->

## The one rule that breaks 80% of first attempts

Every package in a pnpm workspace owns its own `package.json`. Dependencies DO NOT automatically hoist across packages — if `apps/web` imports `@sentinel/types`, then `apps/web/package.json` must declare `@sentinel/types` in its `dependencies`. Adding it only to the root `package.json` does nothing for `apps/web`.

This is the single most common failure in generated monorepo code. The model writes `import { Alarm } from '@sentinel/types'` inside `apps/web/app/page.tsx`, leaves `apps/web/package.json` with no mention of `@sentinel/types`, and then every build/typecheck fails with "Cannot find module".

## Workspace-local dependencies

Use the `workspace:*` protocol so pnpm resolves to the sibling package instead of a registry lookup:

```json
{
  "dependencies": {
    "@sentinel/types": "workspace:*",
    "@sentinel/api-client": "workspace:*"
  }
}
```

`workspace:*` means "whatever version the workspace has right now". pnpm replaces it with a real version during publish. During dev, it's a symlink into the sibling package.

## Root package.json vs package package.json

The root `package.json` should only contain:
- `"private": true`
- `"packageManager": "pnpm@9.x.x"`
- `devDependencies` that are genuinely shared tooling (turbo, prettier, typescript)
- `scripts` that run workspace-wide (`turbo run build`, `turbo run test`)

Each package inside `apps/*` and `packages/*` has its own `package.json` with its own `dependencies`, `devDependencies`, and `scripts`. Framework runtime deps (react, next, expo, vitest) belong in the package that actually uses them, NOT the root.

## Running scripts with --filter

`pnpm --filter <pkg> <script>` runs a package's script from the monorepo root. Examples:

```bash
pnpm --filter @sentinel/types build      # build one package
pnpm --filter "./packages/*" typecheck   # typecheck all packages under ./packages/
pnpm --filter '!@sentinel/web' test      # test everything except web
```

Two things to know:
1. `pnpm --filter X Y` fails if `Y` isn't declared as a script in X's `package.json`. The error is `ERR_PNPM_NO_SCRIPT`. Fix by adding the script to the package, NOT by running `Y` directly from a different directory.
2. Path globs (`./packages/*`) must be quoted or bash will expand them.

## tsconfig extends

A package's `tsconfig.json` typically extends a shared base:

```json
{
  "extends": "../../tooling/tsconfig/base.json",
  "compilerOptions": {
    "outDir": "./dist",
    "rootDir": "./src"
  },
  "include": ["src/**/*"]
}
```

DO use a **relative path** (`"../../tooling/tsconfig/base.json"`) — that always works.

DO NOT use a package name (`"@sentinel/tsconfig/base.json"`) unless that package is actually published to the workspace with a `main` field pointing at `base.json`. The model often guesses at scoped package syntax here and tsc fails with "File 'xyz' not found".

## Every package needs typescript in its own devDeps

This is counterintuitive: even though typescript is in the root `package.json`, each package that runs `tsc` directly needs typescript in its OWN `devDependencies`. pnpm does not hoist by default. Symptom: `tsc: not found` when running `pnpm --filter X typecheck`.

```json
{
  "scripts": { "typecheck": "tsc --noEmit" },
  "devDependencies": { "typescript": "^5.4.0" }
}
```

## The turborepo layer

`turbo.json` declares task pipelines:

```json
{
  "$schema": "https://turbo.build/schema.json",
  "tasks": {
    "build": { "dependsOn": ["^build"], "outputs": ["dist/**"] },
    "typecheck": { "dependsOn": ["^build"] },
    "test": { "dependsOn": ["^build"] }
  }
}
```

`^build` means "run build in every upstream dependency first". If `apps/web` depends on `@sentinel/types` (via `workspace:*`), then `turbo run build --filter=apps/web` will build `@sentinel/types` first.

## Gotchas (CRITICAL — read before writing any monorepo code)

- **Every package needs its own deps**: pnpm does NOT hoist. If `apps/web` imports `@sentinel/types`, `apps/web/package.json` must declare it. Root devDeps don't help child packages.
- **Every package that runs tsc needs typescript in its own devDeps**: "tsc: not found" means add typescript to THAT package's devDeps, not the root.
- **After editing any package.json, run `pnpm install`**: the dep graph is stale until you do.
- **workspace:* for sibling deps**: use `"@sentinel/types": "workspace:*"` not a version number.
- **tsconfig extends uses relative paths**: `"../../tooling/tsconfig/base.json"` not `"@sentinel/tsconfig/base.json"`.
- **`node_modules` looks weird**: pnpm uses a symlinked `node_modules/.pnpm/` content-addressable store. Each package's `node_modules/` is just symlinks into the store. This is fine; don't try to "fix" it.
- **`pnpm install` is idempotent but NOT free**. After editing a `package.json`, you MUST re-run `pnpm install` before expecting new deps to resolve.
- **`pnpm exec X` and direct `X` both work** when stoke runs acceptance commands (stoke prepends `node_modules/.bin` to PATH). Prefer direct.
- **New package not recognized**: if you add `packages/foo/` with a `package.json`, run `pnpm install` from the root to register it in the workspace.
- **`workspace:*` in a package published to npm**: pnpm rewrites these at publish time to the actual version. Don't worry about the registry implications during dev.
