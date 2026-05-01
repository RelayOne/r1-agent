# Deployment

This is the current deployment summary for R1 on `main`.

## Build And Verification Gate

The repo-local build gate remains the same:

- `go build ./cmd/r1`
- `go test ./...`
- `go vet ./...`

Those are the core CI-quality checks documented in `CLAUDE.md`.

## Deployment Surfaces

| Surface | Best fit | Notes |
|---|---|---|
| CLI install | individual operators and developers | canonical `r1` binary |
| container/release artifacts | packaged distribution | release automation and signed artifacts live in repo tooling |
| IDE and desktop adjuncts | editor and local GUI usage | layered on top of the core runtime |
| pack registry HTTP service | deterministic skill distribution | `r1 skills pack serve` |

## What Operators Should Verify Now

The latest main-branch pack/runtime work changes post-deploy checks:

- the pack libraries are seeded or reachable where expected
- signed packs verify correctly before runtime registration
- runtime helper surfaces for metrics, audit, timeout, and cancellation
  behave correctly
- evaluation artifacts and parity evidence still line up with the build
  being promoted

## Runtime Inputs

Deployment still depends on the existing R1 runtime basics:

- Git
- at least one execution engine/provider path
- writable runtime state directories
- whatever model/provider credentials the chosen execution path needs

The new pack-registry surface adds one more optional operational input:
where deterministic packs live and how they are served or consumed.

## Status

### Done

- stable build/test/vet gate
- deployable CLI/runtime baseline
- pack registry HTTP surface
- runtime verification hooks for signed packs and helper functions

### In Progress

- broader integration verification across more deterministic-skill use
  cases

### Scoped

- stronger release checks around pack packaging and dependencies

### Scoping

- broader outward-facing publish and distribution workflows

### Potential-On Horizon

- richer cross-product deterministic distribution pipelines
