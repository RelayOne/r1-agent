# Archived: pre-honest-crypto ledger anchor

Archived 2026-07-09 on branch `converge/r1-2026-07-08` (honest-stack convergence, manifest 04-R1 §1.2 + §5).

## What this is

`anchor.go` (package `legacyanchor`, formerly `internal/ledger/anchor.go`, package
`ledger`) is R1's original bespoke Merkle anchor-log: interval commitments over the
reasoning ledger (S-U-009), schema-versioned anchor chain, and empty-interval
"nothing happened between t1 and t2" proofs.

## Why it moved

The canonical Merkle commitment + hash-chained record shape are now owned by
`internal/honestcrypto` — the Go port of RelayOne's `@honest/crypto` per
`RelayOne/packages/honest-crypto/PORTS.md`. New anchoring goes through the canonical
seam: `internal/ledger.AnchorRecords` -> `honestcrypto.GetAnchorer()`
(default backend `trusted-timestamp`), so a stronger anchor backend swaps in by
config with zero caller changes (see the swap-proof test).

## Why it was preserved, not deleted (PRIME DIRECTIVE 1)

This interval-anchor chain is a **distinct tamper-evidence feature** — it commits to
windows of ledger nodes and can prove an interval was empty. The canonical Anchorer
commits a *set of record hashes*; it does not replace the empty-interval capability.
`r1 receipt verify` still consumes this chain (`cmd/r1/receipt_cmd.go`), so the code
stays live here rather than being removed. The convergence path, if the
empty-interval capability is later re-expressed on the canonical
`honestcrypto.EmptyIntervalProof` primitive, is to fold this into the canonical seam
and retire this package.

## Provenance

- Original location: `internal/ledger/anchor.go` (package `ledger`).
- Original design references (unchanged in the archived source): Google Trillian /
  Sigstore Rekor / Key Transparency empty-interval commitments; the internal
  audit-pipeline anchoring pattern.
- Canonical golden vector every port asserts:
  `52665c72b3e26cbfaaddbe35a71e92ffec1dfc8e3d1af69851a568fe1398c2cf`.
