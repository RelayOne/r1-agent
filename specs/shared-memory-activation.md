<!-- STATUS: done -->
<!-- BUILD_COMPLETED: 2026-07-02 -->
<!-- CREATED: 2026-07-02 -->
<!-- DEPENDS_ON: specs/harness-activation.md (A041 stance runner — supplies the concurrent stance workers this substrate serves) -->
<!-- BUILD_ORDER: 2 -->
<!-- AUDIT: A070 (internal/sharedmem STOKE-017 built-not-integrated) in audit/complete-systems-2026-07-01.md -->
<!-- HOLISTIC: production-readiness/collision/playwright N/A — internal Go substrate wiring. -->

# Shared-Memory Activation — Wire STOKE-017 Blocks Into Concurrent Stance Workers

## Status

STOKE-017 (`internal/sharedmem`) — shared memory blocks with PROV-AGENT
provenance, three write semantics (Insert/Replace/Rethink), reducers, and
namespace isolation (STOKE-021) — was a **complete, tested deliverable
(1,207 LOC + 784 test LOC) with zero importers module-wide** (audit A070). It
is now **built AND integrated**: the harness owns one namespace-scoped store
per mission and binds each stance to its collaboration namespace, so concurrent
stance workers share reducer-mediated blocks.

## What was dormant

- `internal/sharedmem` had no consumer: no agent/harness/mission/CLI path ever
  constructed a store or a block. The package rode the CI gate as dead weight.
- The package doc (`block.go`) promised access control via a `PolicyEnforcer`
  interface injecting `trustplane.RealClient.EvaluatePolicy` — **no such type
  ever existed** (and `trustplane` itself was removed in A072). A false
  integration-surface claim.

## What is now live

1. **Harness owns the substrate.** `harness.New` constructs a
   `sharedmem.MemoryStore` wrapped in a `sharedmem.NamespacedStore`
   (`internal/harness/harness.go`). `RegisterSharedReducer` installs reducers
   (e.g. `AddReducer` for append-only findings logs).
2. **Per-stance seam.** `Harness.StanceMemory(stanceID)`
   (`internal/harness/sharedmem.go`) returns a `StanceMemory` bound to the
   stance's identity and collaboration namespace. Every read is
   namespace-checked; every write (`CreateBlock`/`Insert`/`Replace`/`Rethink`)
   carries PROV-AGENT provenance stamped with the stance's ID.
3. **Namespace = collaboration scope.** Stances in the same consensus loop
   (else task, else mission) resolve to one namespace and share blocks; stances
   outside it are denied with `sharedmem.ErrNamespaceDenied`.
4. **Doc corrected.** The `PolicyEnforcer`/`trustplane` claim in `block.go` is
   replaced with the real access model: `NamespacedStore`'s per-caller
   `NamespaceAllowList` + optional `InferenceMonitor`, bound by the harness via
   `StanceMemory`. The Store deliberately holds no `PolicyEnforcer`.

## Test evidence

`internal/harness/sharedmem_test.go` (`go test ./internal/harness/... -race`):

- `TestStanceMemory_TwoStancesShareBlock` — two concurrently-running stances on
  one loop share a block; `AddReducer` merges both writes; provenance is
  attributed one insert each to the two stance IDs.
- `TestStanceMemory_NamespaceIsolation` — a stance in a different loop is denied
  read and write with `ErrNamespaceDenied`; the owner's value is untouched.
- `TestStanceRunner_SharedMemoryCollaboration` — end-to-end through the real
  `StanceRunner`: two concurrent runners route their authorized `ledger_write`
  tool call through a shared-memory executor into one reducer-mediated block.

## Residual / follow-up (out of scope here)

- No production `ToolExecutor` yet exposes explicit `sharedmem_*` tools to the
  model; stances collaborate through shared memory only when their executor
  routes a call into `StanceMemory` (as the runner test does). Adding
  first-class shared-memory tools + role authorization is a separate spec.
- `services/r1-docs/docs/README.md:447` still lists `sharedmem` in the
  operational roster with no annotation — accurate now that it is wired, but a
  docs pass could cross-link this spec.
