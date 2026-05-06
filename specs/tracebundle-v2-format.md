<!-- STATUS: ready -->
<!-- CREATED: 2026-05-05 -->
<!-- DEPENDS_ON: -->
<!-- BUILD_ORDER: 37 -->

# Tracebundle format v2 (per-session filtering + signed manifest)

## 1. Overview

The current tracebundle format (v1, shipped via Spec 4 §7 + #167's `ledgerTracebundleSource`) returns the **full** ledger every time, regardless of which session the URL identified. Two reasons:
- `ledger.Store.ListNodes()` doesn't support per-session filtering today.
- The manifest carries no `chain_root_hash` because `Store` doesn't expose a per-session Merkle root.

This spec adds:
1. **Per-session filtering** at the Store layer (`ListNodesForSession(sessionID)`).
2. **Signed manifest** — the manifest.json carries an ed25519 signature so a downstream verifier can attest the bundle's chain-root hash is what the producer claimed (uses the same signing key surface added by `signed-redaction.md`).
3. **Format version bump** to `2` in manifest. v1 readers continue to work for legacy bundles; v2 readers can verify the new signature.

## 2. Stack & Versions

- Existing `internal/ledger.Store` (adds `ListNodesForSession`)
- Existing `cmd/r1-server/tracebundle.go` (format version constant bumps)
- Existing `cmd/r1-server/tracebundle_source.go` (adapter consumes the new Store method)
- `internal/ledger/redact_sign.go` (signing key — depends on signed-redaction.md OR ships its own key)

## 3. Architecture

```
GET /api/session/{id}/export.tracebundle
  ├─ ledgerTracebundleSource
  │   ├─ ListNodesForSession(sessionID)   ← NEW filter
  │   ├─ ListEdgesForSession(sessionID)   ← NEW filter
  │   └─ ChainRootHash(sessionID)         ← NEW Merkle root accessor
  ├─ writeTracebundle
  │   ├─ manifest.json (version=2 + chain_root_hash + signature_hex)
  │   ├─ chain.ndjson  (filtered to session)
  │   ├─ edges.ndjson  (filtered to session)
  │   └─ content/<id>.json + content/redacted.json
  └─ verify-side: external tool (out of scope here) reads manifest,
     re-derives chain_root_hash from the bundled chain.ndjson,
     validates the signature, accepts/rejects the bundle.
```

## 4. Per-session filtering

The Node struct already carries `MissionID string`. Today `Store.ListNodes` returns every chain row regardless of mission. Adding:

```go
func (s *Store) ListNodesForSession(sessionID string) ([]Node, error) {
    all, err := s.ListNodes()
    if err != nil { return nil, err }
    out := make([]Node, 0, len(all))
    for _, n := range all {
        if n.MissionID == sessionID || sessionID == "" {
            out = append(out, n)
        }
    }
    return out, nil
}
```

(Future: an actual disk-level partition per session — out of scope.)

Edges follow the same pattern via a new `ListEdgesForSession`.

## 5. Chain root hash

`ChainRootHash(sessionID)` walks the filtered chain in chronological order, hashes each node's metadata + commitment, links them via SHA256 chain. Returns the final hash. The manifest carries this so a downstream verifier can:
1. Untar the bundle.
2. Re-walk chain.ndjson to derive the hash locally.
3. Compare against `manifest.chain_root_hash`.
4. Verify the signature.

## 6. Manifest v2

```json
{
  "format": "tracebundle",
  "version": 2,
  "session_id": "sess-xyz",
  "chain_root_hash": "abcdef...",
  "generated_at": "2026-05-05T...",
  "signer": "fingerprint-12char",
  "signature_hex": "ed25519-signature-hex"
}
```

`signature_hex` covers the canonical encoding of the manifest WITHOUT the signature_hex field (so the signature doesn't sign itself).

## 7. Boundaries

- **No HSM.** File-backed signing keys, same as `signed-redaction.md`.
- **No chunked upload.** Bundles still stream as a single tar.gz response.
- **No incremental verification.** Reader downloads the full bundle then verifies — streaming verification is a future spec.
- **v1 reader compat.** Older bundles (no signature, no chain_root_hash) still parse — readers should check `version` first and skip the verify step on v1.

## 8. Implementation checklist (8 items — self-contained)

- [ ] T1 — Add `func (s *Store) ListNodesForSession(sessionID string) ([]Node, error)` to `internal/ledger/store.go`. Filter by `n.MissionID == sessionID`. Empty `sessionID` returns all (matches `ListNodes` semantics for backward compat). Add 4 unit tests in `store_test.go`: empty sessionID, matching session, non-matching session (returns empty slice), nodes with no MissionID set.
- [ ] T2 — Add `func (s *Store) ListEdgesForSession(sessionID string) ([]Edge, error)` (mirrors T1 but Edges don't currently have a sessionID field — accepts an optional filter via Edge.Metadata["session_id"] OR returns all edges if no field-level filter is possible; document the limitation in the godoc).
- [ ] T3 — Add `func (s *Store) ChainRootHashForSession(sessionID string) (string, error)`: walks `ListNodesForSession`, sorts by CreatedAt, computes the SHA256 chain. Empty session → "" (no error). Single-node session → SHA256 of that node. Test with 3-node fixtures asserting the same nodes in the same order produce the same root hash.
- [ ] T4 — In `cmd/r1-server/tracebundle.go` bump `tracebundleFormatVersion` from `1` to `2`. Add `Signer` + `SignatureHex` fields to `tracebundleManifest`. Update `writeTracebundle` to: (a) call `src.ChainRootHash()` if non-empty, populate manifest; (b) call `signManifest(manifest, privKey)` if a signing key is configured (Source can return a `Signer` interface) and stamp `Signer` + `SignatureHex` before writing.
- [ ] T5 — Update `cmd/r1-server/tracebundle_source.go` `ledgerTracebundleSource`:
    * Replace `Store.ListNodes()` calls with `Store.ListNodesForSession(s.sessionID)`.
    * Implement `ChainRootHash()` by calling `Store.ChainRootHashForSession(s.sessionID)`.
    * Add an optional `Signer ed25519.PrivateKey` field; when set, the writer stamps the signature.
- [ ] T6 — Add `func (s *Store) VerifyTracebundle(bundlePath string, pub ed25519.PublicKey) (bool, error)` for downstream use: opens the tar.gz, extracts manifest.json, re-derives chain_root_hash from chain.ndjson, returns true if both the chain match and the signature verify. Tests with a happy bundle + a tampered chain.ndjson (root mismatch) + a tampered manifest signature (sig mismatch).
- [ ] T7 — Update `cmd/r1-server/tracebundle_test.go` + `tracebundle_source_test.go`:
    * Existing `TestTracebundle_RoundTrip` — assert manifest.version == 2, signer field present.
    * New `TestTracebundle_SessionFilter`: 5-node ledger, 3 with sessionID="A", 2 with sessionID="B"; export sessionID=A; assert chain.ndjson has 3 lines.
    * New `TestVerifyTracebundle_HappyAndTampered`: roundtrip then verify; mutate chain.ndjson; verify returns false.
- [ ] T8 — Update the `cmd/r1-server/e2e/e2e-fullflow.mjs` Playwright runner: when fetching `/api/session/{id}/export.tracebundle`, also fetch the manifest and assert `version == 2` + `chain_root_hash` is non-empty (when the session has at least one node).

## 9. Acceptance

- `go build ./... && go test ./internal/ledger/... ./cmd/r1-server/...` clean.
- `curl /api/session/<X>/export.tracebundle | tar tzf -` lists ONLY session X's nodes.
- `manifest.json.version == 2`, `chain_root_hash` populated, `signer + signature_hex` populated when a signing key is configured.
- A tampered bundle's `VerifyTracebundle` returns false.
