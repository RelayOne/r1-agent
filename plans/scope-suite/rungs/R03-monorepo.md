# R03 — pnpm + Turborepo monorepo scaffold

A working monorepo foundation: workspaces + turbo + tooling configs +
empty workspace stubs. No business logic. Designed to test whether
stoke can cleanly set up cross-package infrastructure.

## Scope

Create the following structure:

```
├── package.json                    # pnpm workspaces + turbo
├── pnpm-workspace.yaml
├── turbo.json                      # pipeline for build/dev/test/typecheck
├── tsconfig.base.json              # shared TS config
├── .eslintrc.json                  # shared eslint
├── apps/
│   ├── web/package.json           # Next.js app stub
│   └── api/package.json           # Express/Node API stub
└── packages/
    ├── types/
    │   ├── package.json
    │   └── src/index.ts           # exports one TS type
    └── utils/
        ├── package.json
        └── src/index.ts           # exports one utility function
```

## Acceptance

- Running `pnpm install` at repo root completes without errors
- Running `pnpm turbo run typecheck` succeeds on all packages
- Each workspace package has its own `package.json` with a `name`
  starting with `@scope/` or similar
- `packages/types/src/index.ts` exports at least one TypeScript type
  alias or interface
- `packages/utils/src/index.ts` exports at least one working function
- `apps/web/package.json` declares Next.js as a dependency (no need
  to run a Next page)
- `apps/api/package.json` declares Express or equivalent (no need to
  run a server)

## What NOT to do

No running apps, no API routes, no React components, no deployment
configs, no CI, no tests of business logic. Scaffold only.
