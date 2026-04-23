<!-- STATUS: done -->
<!-- BUILD_STARTED: 2026-04-23 -->
<!-- BUILD_COMPLETED: 2026-04-23 -->

# CI infra cleanup — Implementation Spec

## Overview

Lint + security CI jobs have been red on every PR since the go.mod
`go 1.25.5` bump landed. Cause: the runner's `actions/setup-go`
version is pinned to `1.23`, and the pre-built golangci-lint + gosec
binaries downloaded by the actions were compiled with Go 1.24 (or
older). Go's toolchain-version-check rejects running a linter built
with an older Go than the module targets.

Additionally, `gosec ./...` scans every `corpus/*/initial/`,
`corpus/*/visible_tests/`, and `corpus/*/hidden_tests/` directory.
Each of those is an independent Go module with its own
`go.mod` declaring `go 1.22` — different from the repo root. gosec
loads those packages with the host's Go toolchain and fails on the
version mismatch (`go: go.mod requires go >= 1.25.5`).

## Fixes

### 1. Bump `actions/setup-go` version 1.23 → 1.25.5

All four jobs (`build`, `race`, `lint`, `security`) currently pin
`go-version: "1.23"`. Change to `"1.25.5"` so the runner's host
toolchain matches `go.mod` exactly. This eliminates the
`GOTOOLCHAIN=auto` download-on-every-run cost in the build job and
fixes the version mismatch that breaks linters.

### 2. Switch golangci-lint install mode to `goinstall`

Current: `golangci/golangci-lint-action@v6` with
`version: v1.64.8`. The action downloads a pre-built binary that
was compiled with Go 1.24, which rejects our Go 1.25.5 target.

Fix: set `install-mode: goinstall` so the action runs
`go install github.com/golangci/golangci-lint/cmd/golangci-lint@vX`
using the **already-installed** Go 1.25.5 toolchain. Requires
golangci-lint-action@v7 (v6 did not support goinstall mode cleanly).

Version pin: `v1.64.8` → `v1.64.8` (unchanged). The version tag
refers to the golangci-lint release, not the binary. Building from
source with Go 1.25.5 is the fix.

### 3. Exclude `corpus/` from gosec

gosec errors on every corpus subpackage because each declares
`go 1.22` while the host runs 1.25.5 (gosec expects `go >= go.mod`
in every discovered package). The corpus modules are benchmark
fixtures — they're not shipped as part of the Stoke binary and
don't need security scanning.

Fix: change `args: ./...` to use `-exclude-dir=corpus`, which gosec
understands natively, OR change args to `./cmd/... ./internal/...`
which is more explicit.

Recommend the explicit path form — it's self-documenting, and new
top-level directories (e.g. `bench/`) don't silently slip under the
scan.

### 4. Race job apt-get flake — NOT fixed here

The `race` job occasionally fails on
`Install build deps → apt-get update → Hash Sum mismatch` for the
Google Chrome repo. This is a transient runner-network issue. A
proper fix would disable the Chrome repo on the runner (via
`sudo rm /etc/apt/sources.list.d/google-chrome*`) before
`apt-get update`. Deferred to a separate PR so this spec stays
focused on the Go-version-mismatch fixes.

## Changes

### `.github/workflows/ci.yml`

Replace every `go-version: "1.23"` with `go-version: "1.25.5"`
(four instances).

Replace the lint step:
```yaml
- name: golangci-lint
  uses: golangci/golangci-lint-action@v6
  with:
    version: v1.64.8
```
with:
```yaml
- name: golangci-lint
  uses: golangci/golangci-lint-action@v7
  with:
    version: v1.64.8
    install-mode: goinstall
```

Replace the gosec step:
```yaml
- name: gosec
  uses: securego/gosec@v2.21.4
  with:
    args: ./...
```
with:
```yaml
- name: gosec
  uses: securego/gosec@v2.21.4
  with:
    args: ./cmd/... ./internal/...
```

## Boundaries — What NOT To Do

- Do NOT bump `go 1.25.5` in root `go.mod` to match an older
  toolchain. The bump was intentional (per spec decision when
  TASK 11 memory nodes landed).
- Do NOT change `corpus/*/go.mod` from `go 1.22` to `go 1.25.5`.
  Those are frozen benchmark fixtures — upgrading them breaks the
  benchmark reproducibility contract.
- Do NOT add runner-network retry logic for the apt-get flake here.
  Separate scope.

## Testing

- [ ] `go build ./cmd/stoke` still passes locally (unchanged).
- [ ] `go vet ./...` still passes locally (unchanged).
- [ ] After PR opens, all four CI jobs (`build`, `race`, `lint`,
  `security`) complete with `SUCCESS` conclusion. The only
  acceptable failure is the apt-get Chrome flake (deferred).

## Acceptance

- WHEN a PR opens THE `lint` job SHALL complete with golangci-lint
  running successfully (no "Go language version (go1.24) used to
  build golangci-lint is lower than the targeted Go version
  (1.25.5)" error).
- WHEN a PR opens THE `security` job SHALL complete with gosec
  skipping `corpus/` (no "go: go.mod requires go >= 1.25.5" error
  on corpus subpackages).
- WHEN a PR opens THE `build` job SHALL skip the toolchain
  auto-download step (runner's setup-go already matches).

## Implementation Checklist

1. [x] Bump setup-go `go-version: "1.23"` → `"1.25.5"` in all four jobs (commit d8ec28b).
2. [x] Upgrade golangci-lint-action v6 → v7 + `install-mode: goinstall` (commit d8ec28b).
3. [x] Narrow gosec scan to `./cmd/... ./internal/...` (commit d8ec28b).
4. [x] Push PR, observe CI.
