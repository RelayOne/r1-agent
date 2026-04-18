# R07 — Sentinel session slice

A 1-session chunk of the real Sentinel SOW: the monorepo foundation +
shared-infra section. ~15KB of prose, ~8-12 files, no cross-session
dependencies. This is the same slice E9 is running against as of
2026-04-18.

## Scope

See `/home/eric/repos/sentinel-simple-opus/SOW_MONOREPO_SLICE.md` for
the full prose. It's the preamble + project background + overall scope
summary + the "Shared infrastructure" section from the full Sentinel
SOW, explicitly scoped to the monorepo scaffold only.

## Acceptance

Per the sliced SOW: pnpm workspace + Turborepo + tooling/ + empty
workspace stubs (apps/web, apps/caregiver, apps/installer,
packages/types, packages/api-client, packages/design-tokens,
packages/ui-web, packages/ui-mobile, packages/i18n, packages/utils).
`pnpm install` runs clean. No business logic.

This rung exists specifically to test: can stoke converge on a
realistic but tight slice of a real project SOW?
