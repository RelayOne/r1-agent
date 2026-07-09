# Archived: Substrate standalone-product positioning

Archived 2026-07-09 on branch `converge/r1-2026-07-08` (honest-stack convergence, manifest 04-R1 §1.1 + §5).

## Decision

Substrate (deterministic coding accelerator, `RelayOne/substrate`) is **deprecated as
a standalone product** and folded into R1 as an **internal cost accelerator**. R1
routes routine, covered codegen to the deterministic engine first and falls through
to frontier inference on any miss or uncertainty (fail-safe). The routing mechanism
now lives at `internal/substrate/` (provenance in that package's doc comment;
internalized from `RelayOne/substrate` commit `5dc3287`).

## Why the positioning is NOT copied into this repo

R1 is a **public** (MIT) repo. Substrate's standalone-product positioning — its
thesis writeup, competitive framing, first-customer narrative, and the "1-2 orders of
magnitude" efficiency numbers — is **confidential (Good Ventures internal)** and its
efficiency numbers have **unresolved provenance** versus the April 2026 "Compiled AI"
work. Copying that material into a public repo would both leak confidential IP and
restate unverified efficiency claims.

So this archive intentionally records the **decision and provenance**, not the
confidential text:

- The confidential standalone positioning stays in the private `RelayOne/substrate`
  repository; it is not imported here.
- R1 makes **no efficiency claims** about Substrate. The only Substrate-related
  numbers R1 emits are measured at runtime (tokens actually used on a given task).
- The deterministic engine itself (the confidential Rust crystallization pipeline)
  stays external and is invoked as a release binary / MCP server; R1 does not embed
  its corpus or templates.

## Pointers

- Internalized routing mechanism: `internal/substrate/offload.go` (+ `offload_test.go`).
- Wiring: `cmd/r1/sow_native.go` (`substrate.PrePass` before model dispatch),
  gated behind `R1_SOW_OFFLOAD=1`; MCP server declared in `r1.policy.yaml`.
- Source of the mechanism: `RelayOne/substrate` @ `5dc3287`.
