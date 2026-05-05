# RT-JSDOM-VITEST-NODE22

## Topic

r1-agent's CI is currently pinned to Node 20.x (cloudbuild.yaml uses
`node:20`; .github/workflows/desktop-augmentation.yml uses
`node-version: '20'`). PR #143 had to pin `jsdom` to `^26.1.0` because
`jsdom@27+` brings in `html-encoding-sniffer@6` -> `@exodus/bytes` which
is ESM-only with top-level await, and Node 20.x prior to 20.19 rejects
that under `require()`. Closed dependabot PRs #131 (jsdom 29), #134
(vitest 4 web-components), #136 (vitest 4 desktop), #146 (jsdom 29
desktop), #147 (vitest-coverage-v8 4 desktop) all blocked on the same
class of ESM-via-require failure (jsdom side) or rolldown native binding
side (vitest side).

This research determines whether (a) Node 20.x has caught up enough that
the pins can be lifted in place, or (b) we need to bump CI to Node 22 LTS,
and produces a migration plan.

## Current state (May 2026)

### Node.js 20.x require(esm)

- `--experimental-require-module` was backported to Node 20 via PR #53500
  (Joyee Cheung), shipped flagged in 20.17.0 (Aug 2024).
- Node **20.19.0** (released **2025-03-13**) **unflagged require(esm)**
  on the v20 line via PR #56927. From 20.19.0 onward, synchronous ESM
  graphs load via `require()` with no flag.
- `require(esm)` is now marked **stable** across all current LTS lines
  (20.19+, 22.12+) per Joyee Cheung's "from experiment to stability"
  post (Dec 2025). No experimental warning unless `--trace-require-module`.
- **Top-level await is still rejected.** If the imported ES module (or any
  module in its sync graph) contains top-level await, Node still throws
  `ERR_REQUIRE_ASYNC_MODULE` on every release line, including Node 24.
  This is by design -- TLA is async, `require()` is sync.
- Node **20 EOL was 2026-04-30** (5 days ago as of today). No further
  security patches on the 20.x line. ubuntu-latest GitHub Actions image
  still ships 20.20.2 by default (issue #13833) -- this is now an
  unsupported runtime. GitHub plans to jump runner to Node 24 in fall 2026.

### jsdom upstream

- **jsdom 27.0.0** (Mar 2025): minimum Node bumped to v20. Adopted
  `@exodus/bytes` for byte decoding (replaces iconv-lite).
- **jsdom 27.4.0**: improved byte decoding via `@exodus/bytes`.
- **jsdom 28.0.0** (Mar 2025): `html-encoding-sniffer@6` -> `@exodus/bytes@1.14`
  in dep graph, ESM-only with TLA. cssstyle still synchronously
  `require()`s html-encoding-sniffer (CJS->ESM-with-TLA crash).
- **jsdom 29.0.0** (Mar 2025): minimum Node bumped to **v22.13.0+ on the
  v22 line** (engines: `^20.19.0 || ^22.13.0 || >=24.0.0`). CSSOM
  overhauled with css-tree.
- **jsdom 29.1.1**: latest as of May 2026.
- **html-encoding-sniffer@6.0.0** engines: `^20.19.0 || ^22.12.0 || >=24.0.0`.
- **@exodus/bytes@1.15.0** (Apr 2026, latest): same engine constraint;
  still ESM-first. The package has **not shipped a CJS-compat build**.
- **No official CJS-compat line for jsdom 27+.** Upstream's stance is: if
  you can run Node 20.19+ or 22.12+, require(esm) handles it. They will
  not maintain a parallel CJS build.
- Open issue jsdom#4138 (Apr 2026, closed): tracked a separate downstream
  TLA crash from `lru-cache@11.3.0` via `@asamuzakjp/css-color`. Resolved
  by lru-cache reverting TLA. Indicates jsdom maintainers are willing to
  push downstreams off TLA where feasible, but **@exodus/bytes is owned
  by Exodus, not jsdom**, and remains TLA.

### vitest 4 + rolldown

- **vitest 4.0.0** released **2025-10-22**. Engines: Node `>=20.0.0`,
  Vite `>=6.0.0`. Browser Mode stable; visual regression built in.
- **vitest 4.1** (May 2026): Vite 8 day-one support, test tags, AI agent
  reporter, "native Node.js execution" mode that bypasses Vite's module
  runner.
- **rolldown / rolldown-vite**: vitest 3.2.2+ already supports rolldown-vite
  as opt-in. vitest 4 keeps it opt-in. **rolldown bindings are still
  shipped as platform optionalDependencies**, with the well-known npm
  optionalDependencies bug (issue rolldown#9068) -- if `package-lock.json`
  was generated on a different platform, `@rolldown/binding-linux-x64-gnu`
  may be omitted and the install will fail at runtime.
- Workaround on CI: `npm install --include=optional` or regenerate the
  lockfile on Linux. The same bug affects `@rollup/rollup-linux-x64-gnu`
  (vite discussion #15532) and is well-understood. **It is not a Node 20
  vs 22 question** -- bumping the runner does not fix it.
- vitest 4 itself does not require rolldown. The default bundler stays
  Rollup-via-Vite. Rolldown is opt-in via `rolldownVersion` in vite config.

### CI base images

- Cloud Build uses `node:20` (cloudbuild.yaml line 10) for the web
  workspace step and `golang:1.25` for the Go steps. These are independent
  Docker images per step -- the Go image is unaffected by Node policy.
- `node:22` and `node:22-bookworm-slim` are GA on Docker Hub; Node 22 LTS
  runs through 2027-04-30.
- Cloud Build best practice for r1-agent's setup is: pin to a specific
  Docker tag (e.g. `node:22.13.1-bookworm-slim`) per step. The
  multi-stage / distroless guidance applies to the **published artifact**,
  not the build step images, so it's orthogonal here.

## Recommendation

**Bump CI to Node 22 LTS now.** Three reasons, in priority order:

1. **Node 20 went EOL 5 days ago (2026-04-30).** Continuing to gate CI on
   an unsupported runtime is a security finding in its own right (no more
   CVE patches), and the GitHub Actions ubuntu-latest default version
   (20.20.2) is now flagged unsupported.

2. **jsdom 27+/28+/29+ depend on `@exodus/bytes` which is ESM-only with
   TLA.** Even on Node 20.19+ where require(esm) is unflagged, the TLA
   path still throws `ERR_REQUIRE_ASYNC_MODULE` because cssstyle uses sync
   `require()`. Node 22.12+ has the same restriction -- but Node 22 is
   where the entire ecosystem is converging, and Exodus + jsdom upstream
   have been clear they will not ship CJS-compat. **The `jsdom@^26.1.0`
   pin is a dead end on Node 20 too.** Lifting the pin requires bumping
   *and* getting cssstyle to switch to dynamic import, which is upstream
   work we don't control. (jsdom 29 is what we'd want to land for current
   security fixes.)

3. **vitest 4 is supported on Node 20**, so the vitest deferral is
   solvable independently of the Node bump -- it's the rolldown optional
   dep bug, fixable with `npm install --include=optional` and/or
   regenerating the lockfile on Linux. **But** doing the Node bump first
   simplifies the matrix: one Node version, one set of vitest pins.

**Do not jump to Node 24** even though it's Active LTS until May 2028.
Node 22 is Maintenance LTS until Apr 2027 and is what jsdom 29 explicitly
tests against. Node 24 ecosystem coverage is still catching up (per
jsdom#4138 history). Re-evaluate the 22->24 jump in Q4 2026 when GitHub
Actions makes it the default.

**Note on jsdom:** even bumping to Node 22, the
`@exodus/bytes` TLA + cssstyle sync-require crash chain may still bite.
The migration plan below includes a verification step that actually runs
`npm test` against jsdom 29 on Node 22 in a worktree before the merge.
If it fails, the fix is `overrides` in package.json to pin
cssstyle/html-encoding-sniffer to last-known-good versions that don't
trigger the TLA path; we should NOT downgrade jsdom past 27 again because
27.x is itself unmaintained at this point.

## Migration plan if bumping

### Step 1: cloudbuild.yaml

Change the `node:20` step image to `node:22.13`. The Go steps stay on
`golang:1.25`. Diff:

```diff
--- a/cloudbuild.yaml
+++ b/cloudbuild.yaml
@@ -7,7 +7,7 @@ steps:
   # web/ build + test runs first so the dist/ output is in place
   # before the Go build embeds it (per spec web-chat-ui item 50).
   # Failure here fails CI before any Go gate runs.
-  - name: 'node:20'
+  - name: 'node:22.13-bookworm-slim'
     id: web-build
     entrypoint: bash
     args:
@@ -17,7 +17,7 @@ steps:
         # npm ci in web/ would walk up to the workspace root and fail
         # because the root has no lockfile (workspaces install via the
         # root's hoisted node_modules). Use the workspace-aware flow
         # that desktop-augmentation.yml uses successfully.
-        npm install --workspaces --include-workspace-root --no-audit --no-fund
+        npm install --workspaces --include-workspace-root --include=optional --no-audit --no-fund
         npm run build --workspace=web
         npm run test --workspace=web
     waitFor: ['-']
```

The `--include=optional` flag is the rolldown/rollup native binding
workaround (issue rolldown#9068). It's harmless on installs that don't
have rolldown in the tree.

### Step 2: .github/workflows/desktop-augmentation.yml

Three jobs use setup-node@v4 with `node-version: '20'` (lines 89-91,
102-104, 120-122). Bump all three to `'22'`. Also bump the matrix
`ubuntu-22.04` runners to `ubuntu-24.04` since Node 22 is what
ubuntu-latest will default to and `ubuntu-22.04` will be deprecated by
GitHub during the Node 24 migration in fall 2026.

```diff
--- a/.github/workflows/desktop-augmentation.yml
+++ b/.github/workflows/desktop-augmentation.yml
@@ -51,7 +51,7 @@ jobs:
     strategy:
       fail-fast: false
       matrix:
-        os: [ubuntu-22.04, macos-latest, windows-latest]
+        os: [ubuntu-24.04, macos-latest, windows-latest]
     steps:
       - uses: actions/checkout@v4
       ...
@@ -83,11 +83,11 @@ jobs:
   components:
     name: components (vitest)
-    runs-on: ubuntu-22.04
+    runs-on: ubuntu-24.04
     steps:
       - uses: actions/checkout@v4
       - uses: actions/setup-node@v4
         with:
-          node-version: '20'
+          node-version: '22'
       - name: npm install (workspace root)
-        run: npm install --workspaces --include-workspace-root
+        run: npm install --workspaces --include-workspace-root --include=optional
       - name: npm test (web-components)
         run: npm test --workspace=@r1/web-components

   desktop-vitest:
     name: desktop (vitest)
-    runs-on: ubuntu-22.04
+    runs-on: ubuntu-24.04
     steps:
       - uses: actions/checkout@v4
       - uses: actions/setup-node@v4
         with:
-          node-version: '20'
+          node-version: '22'
       - name: npm install (workspace root)
-        run: npm install --workspaces --include-workspace-root
+        run: npm install --workspaces --include-workspace-root --include=optional
       - name: npm test (desktop)
         run: npm test --workspace=desktop

   e2e:
     ...
       matrix:
-        os: [ubuntu-22.04, macos-latest, windows-latest]
+        os: [ubuntu-24.04, macos-latest, windows-latest]
     steps:
       - uses: actions/checkout@v4
       - uses: actions/setup-node@v4
         with:
-          node-version: '20'
+          node-version: '22'
```

### Step 3: package.json engines (root + workspaces)

Add or update `"engines": { "node": ">=22.13.0" }` to root `package.json`,
`web/package.json`, `desktop/package.json`, and any `packages/*/package.json`
that currently declare engines. This catches contributors on Node 20
locally before they push.

### Step 4: Lift the pins

After Steps 1-3 are merged and CI is green:

- web/package.json: bump `jsdom` from `^26.1.0` to `^29.1.0`.
- web/package.json (and desktop/package.json if applicable): bump
  `vitest` to `^4.1.0`, `@vitest/coverage-v8` to matching `^4.1.0`.
- Re-open or recreate dependabot PRs #131, #134, #136, #146, #147 against
  the bumped baseline.

### Step 5: Verify the @exodus/bytes TLA path doesn't crash on Node 22

In a worktree, run `npm test --workspace=web` after Step 4. If it fails
with `ERR_REQUIRE_ASYNC_MODULE`, the fallback is `npm overrides` in
root `package.json`:

```json
"overrides": {
  "html-encoding-sniffer": "5.0.0",
  "cssstyle": "4.6.0"
}
```

These are the last versions before the TLA path was introduced. This is
a documented mitigation pattern -- only use it if the unmodified jsdom 29
on Node 22 actually fails. (Per upstream signals it should now work
because Node 22.12+ require(esm) is unflagged AND html-encoding-sniffer 6
declares the matching engines, so the runtime should accept the sync ESM
graph as long as no top-level await is hit. The package has not been
verified to be TLA-free; verification is required.)

### Step 6: docs

Update docs/DEPLOYMENT.md "Done" section noting Node 22 LTS migration.
Per CLAUDE.md repo policy, this is its own commit.

## Sources

- [require(esm) Backported to Node.js 20 (Socket)](https://socket.dev/blog/require-esm-backported-to-node-js-20) -- accessed 2026-05-05
- [PR #53500 [v20.x] backport require(esm)](https://github.com/nodejs/node/pull/53500) -- accessed 2026-05-05
- [PR #56927 [v20.x] backport unflagging of require(esm)](https://github.com/nodejs/node/pull/56927) -- accessed 2026-05-05
- [Node.js 20.19.0 release notes (LTS, 2025-03-13)](https://nodejs.org/en/blog/release/v20.19.0) -- accessed 2026-05-05
- [Joyee Cheung: require(esm) from experiment to stability (Dec 2025)](https://joyeecheung.github.io/blog/2025/12/30/require-esm-in-node-js-from-experiment-to-stability/) -- accessed 2026-05-05
- [Node.js 20 EOL Migration Playbook (Apr 30, 2026)](https://dev.to/matheus_releaserun/nodejs-20-end-of-life-migration-playbook-for-april-30-2026-2onh) -- accessed 2026-05-05
- [endoflife.date / Node.js](https://endoflife.date/nodejs) -- accessed 2026-05-05
- [GitHub Changelog: Deprecation of Node 20 on GHA runners (2025-09-19)](https://github.blog/changelog/2025-09-19-deprecation-of-node-20-on-github-actions-runners/) -- accessed 2026-05-05
- [actions/runner-images #13833 ubuntu-latest Node 20.20.2 unsupported](https://github.com/actions/runner-images/issues/13833) -- accessed 2026-05-05
- [jsdom Releases (27.x - 29.1.1)](https://github.com/jsdom/jsdom/releases) -- accessed 2026-05-05
- [jsdom 27.0.0 release notes](https://github.com/jsdom/jsdom/releases/tag/27.0.0) -- accessed 2026-05-05
- [jsdom 28.0.0 release notes](https://github.com/jsdom/jsdom/releases/tag/28.0.0) -- accessed 2026-05-05
- [jsdom #4138 broken on Node 24 due to downstream TLA (closed)](https://github.com/jsdom/jsdom/issues/4138) -- accessed 2026-05-05
- [jsdom #4000 ERR_REQUIRE_ESM via parse5](https://github.com/jsdom/jsdom/issues/4000) -- accessed 2026-05-05
- [jsdom/html-encoding-sniffer releases (6.0.0)](https://github.com/jsdom/html-encoding-sniffer/releases) -- accessed 2026-05-05
- [@exodus/bytes on npm (1.15.0)](https://www.npmjs.com/package/@exodus/bytes) -- accessed 2026-05-05
- [ExodusOSS/bytes GitHub](https://github.com/ExodusOSS/bytes) -- accessed 2026-05-05
- [isomorphic-dompurify #394 ERR_REQUIRE_ESM @exodus/bytes](https://github.com/kkomelin/isomorphic-dompurify/issues/394) -- accessed 2026-05-05
- [node-lru-cache #397 v11.3.0 TLA breaks jsdom/vitest](https://github.com/isaacs/node-lru-cache/issues/397) -- accessed 2026-05-05
- [Vitest 4.0 release announcement (vitest.dev/blog)](https://vitest.dev/blog/vitest-4) -- accessed 2026-05-05
- [Vitest 4.0.0 release (GitHub, 2025-10-22)](https://github.com/vitest-dev/vitest/releases/tag/v4.0.0) -- accessed 2026-05-05
- [Vitest 4.1 release notes](https://vitest.dev/blog/vitest-4-1.html) -- accessed 2026-05-05
- [Vitest 4 Browser Mode (InfoQ Dec 2025)](https://www.infoq.com/news/2025/12/vitest-4-browser-mode/) -- accessed 2026-05-05
- [VoidZero: Announcing Vitest 4.0](https://voidzero.dev/posts/announcing-vitest-4) -- accessed 2026-05-05
- [Vitest #8086 Document impact/plan for rolldown-vite integration](https://github.com/vitest-dev/vitest/issues/8086) -- accessed 2026-05-05
- [rolldown #9068 binding-linux-x64-gnu missing on pnpm install](https://github.com/rolldown/rolldown/issues/9068) -- accessed 2026-05-05
- [vite #15532 Cannot find module @rollup/rollup-linux-x64-gnu in Docker](https://github.com/vitejs/vite/discussions/15532) -- accessed 2026-05-05
- [PkgPulse Node 20 to 22 Upgrade guide](https://www.pkgpulse.com/guides/nodejs-22-vs-nodejs-20-upgrade-guide) -- accessed 2026-05-05
- [Cloud Build: Build and test Go applications](https://cloud.google.com/build/docs/building/build-go) -- accessed 2026-05-05
- [Docker Hub golang official image](https://hub.docker.com/_/golang/) -- accessed 2026-05-05
