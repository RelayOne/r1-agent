# package-json-hygiene

> Every `package.json` you create must be self-consistent: every import has a declared dep, every script exists, every referenced file is on disk.

<!-- keywords: package.json, scripts, dependencies, npm run, pnpm run, missing script, module not found -->

## Before you close out a task that edited any package.json

Walk this four-point check yourself (use bash to verify, don't just trust that you did it):

1. **Every `scripts` entry you declare resolves to a real command.** `"build": "tsc"` is only valid if `tsc` is in node_modules/.bin (i.e. `typescript` is in devDependencies). `"test": "vitest run"` needs `vitest` in devDependencies. Test with `pnpm --filter <pkg> <script> --help` or by actually running the script once.

2. **Every `dependency` / `devDependency` you add shows up in at least one import somewhere in the package.** Dead deps are noise but inversely, every import in source files must correspond to a declared dep. Grep the package's `src/` for imports and cross-check against `package.json`.

3. **Every script referenced by downstream acceptance criteria actually exists.** If the SOW's AC says `pnpm --filter @sentinel/types build`, then `packages/types/package.json` MUST have a `"build"` script. Read the SOW's acceptance commands for the session and verify each script name is declared.

4. **`pnpm install` has been re-run** if you modified any `package.json`. The node_modules graph is stale until you do. Run it yourself via bash before ending the task.

## The script-vs-binary distinction

These are different:

```json
{ "scripts": { "build": "tsc --noEmit" } }
```
Running `pnpm build` runs `tsc --noEmit`. `tsc` must be in node_modules/.bin.

```json
{ "devDependencies": { "typescript": "^5.4.0" } }
```
This puts `tsc` in node_modules/.bin. Without it, the `build` script will fail with "tsc: not found".

Both are required. Only declaring one is a silent gotcha.

## Dependency placement rules

| Where you use it | Where it goes |
|---|---|
| Imported at runtime in source files (`import X from 'Y'`) | `dependencies` |
| Only used by tooling (tsc, eslint, vitest, prettier, turbo) | `devDependencies` |
| Referenced by JSX from React | `dependencies` (react is always a dep, not a devDep) |
| Types packages (`@types/node`, `@types/react`) | `devDependencies` |
| Peer deps of a library you're building | `peerDependencies` |

For a monorepo package that gets consumed by other workspace packages, prefer `dependencies` over `devDependencies` for runtime libs — consumers need them at runtime.

## The "I added an import, didn't touch package.json" failure

When you write:
```ts
import { z } from 'zod'
```
You MUST also have:
```json
{ "dependencies": { "zod": "^3.22.4" } }
```
in the SAME package's `package.json` (or a parent workspace package's with `workspace:*`).

If you skip this, the typechecker passes (types resolve because of hoisting sometimes), but:
- `pnpm build` fails because the bundler can't find it
- `pnpm test` fails with "Cannot find module 'zod'"
- Even if it works in dev, a fresh clone + `pnpm install` breaks

The fix is always: add the dep, run `pnpm install`, verify.

## The "I added a dep, forgot to run pnpm install" failure

`package.json` changes don't take effect until `pnpm install` runs. Symptom: you added `"zod": "^3.22.4"`, the import in your code is correct, but `tsc` or `pnpm build` still says "Cannot find module 'zod'".

Fix: `pnpm install` from the workspace root (the root pnpm-workspace.yaml dir). pnpm installs only the changed package's deps.

## Common missing-script errors

| Error | Cause | Fix |
|---|---|---|
| `ERR_PNPM_NO_SCRIPT: Missing script: build` | The package's package.json has no `"build"` script | Add the script. Running `tsc` from the root is NOT a fix. |
| `command not found: tsc` | typescript isn't in the package's devDependencies | Add `"typescript": "^5.4.0"` to devDeps and `pnpm install` |
| `Cannot find module '@pkg/X'` for a workspace package | The dep is missing `workspace:*` or the sibling package doesn't exist | Add `"@pkg/X": "workspace:*"` to deps and verify sibling exists |
| `ELIFECYCLE` command failed | The script ran and exited non-zero | Run the script manually to see the real error |

## Five commands every new package.json should support

If the SOW stack is TypeScript and the package contains source code, every package.json should declare at minimum:

```json
{
  "scripts": {
    "build": "tsc -b",
    "typecheck": "tsc --noEmit",
    "clean": "rm -rf dist",
    "lint": "eslint .",
    "test": "vitest run"
  }
}
```

Missing any of these is fine IF there are no downstream consumers, but if the SOW's acceptance criteria reference `pnpm <script>`, that script must exist.

## Gotchas

- Adding a dep to package.json without `pnpm install` = "Cannot find module" on next command
- `"build": "tsc"` without `"typescript"` in devDeps = "tsc: not found"
- workspace:* deps that point at a package without a matching `"name"` field = silent resolution failure
- `pnpm --filter X build` when X has no "build" script = ERR_PNPM_NO_SCRIPT, not a helpful error
- Root package.json devDeps do NOT auto-hoist to child packages in pnpm strict mode

## Self-verify before ending

The model's most common mistake is ending a task while the workspace is still in a "half-baked" state. Before you end, run:

```bash
# from the repo root
pnpm install --silent &&
pnpm --filter "./packages/*" typecheck &&
pnpm --filter "./packages/*" build
```

If any of this fails, fix it before ending. Do not hand off a known-broken workspace to the next task or the acceptance gate.
