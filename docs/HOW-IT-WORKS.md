# How It Works

This is the current operator and developer walkthrough for R1 on
`main`.

## Audience

- operators running R1 day to day
- engineers onboarding to the runtime
- reviewers validating the shipped deterministic-skill story

## Journey 1: Run A Mission

The core loop is still:

1. define or generate a plan
2. execute against the repo
3. verify outputs against build/test/scope gates
4. review before merge or completion

That foundation did not change in the latest cycle.

## Journey 2: Build Or Install A Deterministic Skill

The newer trunk story begins before execution:

1. scaffold or author a pack with `r1 skills pack init`
2. inspect or publish it with `info` and `publish`
3. sign and verify it with `sign` and `verify`
4. activate it with `install`
5. refresh it with `update`
6. search and distribute it through the pack libraries or HTTP registry

This is the operational shift on `main`: deterministic assets are now
meant to move between repos and user libraries with explicit integrity
checks.

## Journey 3: Expose A Pack Registry

`r1 skills pack serve` now turns the published pack library into a
small read-only HTTP registry:

- `/healthz`
- `/v1/packs`
- `/v1/packs/<pack>`
- `/v1/packs/<pack>/archive.tar.gz`

That gives downstream consumers a stable distribution mechanism without
manual copying or symlink inspection.

## Journey 4: Invoke Runtime Helpers

When deterministic runtimes execute through the MCP/backend bridge, the
runtime can now:

- collect metrics snapshots
- emit execution audit output
- query ledger audit data
- honor timeout and cancellation paths
- expose cost metadata for oneshot runtime work

Those are not just convenience helpers. They make deterministic work
more inspectable and safer to operate.

## Journey 5: Validate The Runtime Story

The operator can validate the overall story through:

- parity evidence in `evaluation/`
- signed pack verification
- audit/metrics outputs from the runtime helpers
- the usual mission verification and review gates

## Status

### Done

- core mission loop
- deterministic pack lifecycle
- HTTP pack registry
- runtime audit/metrics/timeout/cancel/cost helper surfaces

### In Progress

- broader adoption of the deterministic skill lane across more runtime
  surfaces

### Scoped

- stronger release and packaging controls around pack distribution

### Scoping

- more explicit superiority and runtime-proof loops

### Potential-On Horizon

- broader network effects from reusable deterministic skills
