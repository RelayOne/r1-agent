# H-66: deterministic scrub for ESM/CJS package.json "type" mismatch

## Context

Meta-reasoner reports on 2026-04-19 flagged `cross_package_contract` as
a recurring failure in sow-serial/R05 runs: when a types package has
`.ts` source files using ES module import/export syntax but the same
package's `package.json` declares `"type": "commonjs"` (or omits
"type" entirely, which node defaults to CJS), the consumer package's
`tsc` / `vitest` / `node` run fails with `ERR_REQUIRE_ESM` or
`SyntaxError: Cannot use import statement outside a module`.

This is a deterministic bug caught by static analysis: if a package
has ES-module syntax files but doesn't declare `"type": "module"`,
add the declaration. No LLM needed — a regex scan is enough.

## Task scope

Add ONE new scrubber function `PreflightFixModuleType` in the file
`internal/plan/sow_devdep_preflight.go` alongside the existing
`PreflightWorkspaceDevDeps`.

Call it from the same call site in `cmd/r1/main.go` right after
the existing preflight. Output one diagnostic line per fix.

## What the function does

1. Walk the repo root for every `package.json` (skip the usual
   `node_modules`, `.git`, `dist`, `build`, `target`, `.turbo`,
   `.next` directories).
2. For each package.json:
   a. Load its `"type"` field (default empty).
   b. Skip if `"type": "module"` is already set.
   c. Walk the package's `src/` directory (if it exists) or the
      package root otherwise, for `.ts` / `.tsx` / `.mjs` / `.js` files.
   d. If any of those files contain ES module syntax — regex match
      on `^\s*(import\s+|export\s+|export\s*{)` at line start — then
      set `"type": "module"` in the package.json and write it back.
3. Return a slice of diagnostic strings like:
   `"packages/types/package.json: set "type":"module" (found ES module syntax in X files)"`.

## Acceptance criteria

1. File `internal/plan/sow_devdep_preflight.go` contains the new function
   `PreflightFixModuleType(repoRoot string) []string`.
2. File `cmd/r1/main.go` calls `plan.PreflightFixModuleType(absRepo)`
   right after the existing `plan.PreflightWorkspaceDevDeps` call, with
   a diagnostic print loop matching the same style.
3. New test file `internal/plan/sow_devdep_preflight_module_test.go`
   with at least two cases:
   - Package with ES module source + missing "type": gets "module" added.
   - Package with ES module source + existing "type": "commonjs": is
     upgraded to "module" with a diagnostic mentioning the change.
4. `go build ./cmd/r1` succeeds.
5. `go test ./internal/plan/... -run Module -count 1` passes.
6. `go vet ./...` passes.

## What NOT to do

- Do not touch the existing `PreflightWorkspaceDevDeps` function.
- Do not add a new package. Keep code in
  `internal/plan/sow_devdep_preflight.go`.
- Do not modify the SOW's package.json files recursively into
  node_modules. Skip those dirs explicitly.
- Do not add runtime dependencies beyond stdlib.
