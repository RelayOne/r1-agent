# Finding: web CI broken by stale package-lock (pre-existing, needs human dep review)

**Status:** BLOCKED (needs a scoped dependency review — not an autonomous fix)

## What's broken

The `web` GitHub Actions workflow (`lint (web)` + `build + test (web)`)
fails at `npm ci` with:

```
npm error Missing: @rolldown/binding-openharmony-arm64@1.1.4 from lock file
npm error Missing: @rolldown/binding-wasm32-wasi@1.1.4 from lock file
npm error Missing: @rolldown/binding-win32-{arm64-msvc,x64-msvc}@1.1.4 ...
npm error Missing: @emnapi/{core,runtime,wasi-threads}, @napi-rs/wasm-runtime,
                   @tybys/wasm-util, @rolldown/pluginutils ... from lock file
```

`npm ci` requires `web/package-lock.json` to be internally complete and
consistent with `web/package.json`; the committed lock is missing the
rolldown/napi optional platform bindings that `vitest@^4` + its
transitive `vite`/rolldown deps now pull in.

## When it started

`web` was green at `9ce34f25` (session start, 2026-07-02 18:56) and first
failed at `dee6e3ff` (2026-07-02 19:11). None of that window's merges
(#327 vite bump in `/desktop`, #316 docs, #315 substrate) touched `web/`,
so this is **not** caused by the sota-wave2 work (which touches 0 web
files) — it surfaced from `web/`'s own floating deps (`vitest ^4.0.0`,
`vite ^7.0.0`, canary `@ai-sdk/*`) re-resolving against a newer registry
state than the last committed lock.

## Why it wasn't auto-fixed

`npm install --package-lock-only --prefix web` regenerates a working lock
(verified: `npm ci --dry-run` then resolves cleanly), BUT the diff is
**17,189 lines** — a wholesale tree re-resolution bumping dozens of
transitive versions (e.g. babel 7.27→7.29, and many others) and adding
**6 new advisories (4 moderate, 2 high)**. That is dependency churn a
human should review and test deliberately, not an unattended commit.

## Recommended fix (human)

Pin the churn down rather than blanket-regenerate:
1. In `web/`, prefer minimal lock repair: `npm install --package-lock-only`
   then review the diff; ideally pin `vitest`/`vite`/`@ai-sdk/*` to exact
   versions (drop the `^`/canary ranges) so the lock stops drifting.
2. Run `npm audit` and address the 4 moderate + 2 high before merging.
3. Confirm `npm ci && npm run build && npm test` in `web/` locally, then
   PR so the `web` workflow verifies it.

This does not affect the Go build/test/vet gate or any r1 binary — it is
isolated to the `web/` frontend workspace's CI.
