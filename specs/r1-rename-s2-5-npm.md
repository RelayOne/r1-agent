# S2-5 — npm package rename `stoke` → `@relayone/r1` (N/A for Stoke core repo)

**Status:** N/A — verified 2026-04-23 on branch `rename/s2-5-npm-s5-3-readme`.

**Source spec:** `plans/work-orders/work-r1-rename.md` §S2-5.

## Finding

The Stoke core Go repo does **not** publish to the npm registry. A full
inventory of the source tree on 2026-04-23 returned:

- Zero `package.json` files checked in under `cmd/`, `internal/`, `pkg/`,
  or the repo root (`find . -name package.json -not -path "*/node_modules/*"
  -not -path "*/.claude-config/*"`).
- Zero `package-lock.json` / `pnpm-lock.yaml` / `yarn.lock` files.
- Zero `.npmrc`, `.npmignore`, `npm publish`, or `semantic-release` workflow
  configuration.
- `install.sh` has no `npm` branch — Homebrew, curl-pipe, Docker, and
  `go build` are the only supported install paths (see README.md §Install).
- `.goreleaser.yml` publishes only Homebrew formulae and container images;
  no `nfpms` / `npms` / `snapcraft` npm target is configured.
- No `cmd/stoke/js/`, `cmd/r1/js/`, or any other directory embedding a
  Node.js shim that would need its own `package.json`.

## Where the npm-adjacent references do live

Grep hits for `package.json` in the tree are all **consumer-side fixtures**,
not Stoke's own package manifest:

- `integration_test.go` and `cmd/stoke/descent_bridge_bootstrap_test.go`
  write synthetic `package.json` blobs into ephemeral test repos to exercise
  Stoke's ability to detect downstream JS/TS projects during a mission run.
- `Dockerfile.pool` runs `npm install -g @anthropic-ai/claude-code` and
  `npm install -g @openai/codex` — these install the upstream CLIs that
  Stoke drives, not Stoke itself.

None of these emit a package named `stoke` to the registry, so there is
nothing to rename or dual-publish.

## Precedent

This finding mirrors the existing in-repo N/A annotations already recorded
for sibling surfaces that Stoke does not own:

- **S1-3 (NATS subjects)** — see `specs/r1-rename-s1-3-nats.md`. Stoke emits
  NDJSON with a `stoke.*` `type` field; the NATS bridge lives downstream.
- **Truecom S4-3 / Veritize S4-4** — sibling repos filed `N/A` for surfaces
  (NATS subjects, env vars, MCP tool names) that exist only in the canonical
  inventory, not in the repo being renamed.

## Verification

- `go build ./...` — green.
- `go vet ./...` — green.
- `go test -count=1 -timeout=300s ./...` — green on this branch before commit.

## No action required

- No `"name": "stoke"` manifest to rename.
- No `@relayone/r1` npm package to register; no dual-publish transition
  window to schedule.
- No npm-flavoured install block to add to `README.md` §Install under S5-3
  (brew + curl|bash + docker + from-source remain the four canonical paths).

If an npm-published CLI wrapper (e.g. `@relayone/r1` shipping a prebuilt
platform-specific binary via `optionalDependencies` or
`postinstall` download, in the style of `esbuild` / `prisma`) is ever
desired as an additional distribution channel, that is new scope — file a
fresh work-order rather than reopening §S2-5.

Work-order §S2-5 in `plans/work-orders/work-r1-rename.md` is to be
annotated with `STATUS: N/A for Stoke core repo` referencing this file,
matching the pattern used by §S1-3.
