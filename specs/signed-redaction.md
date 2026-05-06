<!-- STATUS: ready -->
<!-- CREATED: 2026-05-05 -->
<!-- DEPENDS_ON: -->
<!-- BUILD_ORDER: 35 -->

# Signed redaction events (ed25519 attestation)

## 1. Overview

PR #165 (`internal/ledger.SignedRedactionEvent`) reserved `Signer` + `SignatureHex` fields but currently records every redaction as unsigned. Spec `ledger-redaction.md` §6 declared the signing path "the responsibility of the encryption-at-rest spec" — that boundary is what this spec closes.

After this lands, every redaction recorded via `Store.Redact` (and the `RedactAndLog` convenience wrapper) carries an ed25519 signature over the canonical record. Readers via `Store.RedactionsFor` can verify each event before display; tampered or absent signatures surface as a render-time anomaly distinct from the existing "no log captured" anomaly.

## 2. Stack & Versions

- Existing `internal/ledger/redact.go` (Store.Redact / IsRedacted)
- Existing `internal/ledger/redact_log.go` (PR #165: SignedRedactionEvent + RecordRedaction + RedactionsFor)
- New surface: `internal/ledger/redact_sign.go` — signing key management (load/generate, persist), `SignRecord`, `VerifyRecord`
- `crypto/ed25519` stdlib — no new deps

## 3. Architecture

```
RedactAndLog(ctx, nodeID, reason)
  ├─ Redact(ctx, nodeID, reason)              ← existing, shreds content
  ├─ canonicalize(rec)                        ← new: stable JSON for signing
  ├─ Sign(canonical, privKey)                 ← new: ed25519 over canonical
  └─ RecordRedaction(SignedRedactionEvent{
      ...rec,
      Signer:       fingerprint(pubKey),
      SignatureHex: hex(signature),
    })

RedactionsFor(nodeID) []SignedRedactionEvent
  ├─ load NDJSON                              ← existing
  └─ verify each entry against pubkey         ← new: marks invalid in-place
```

Verification result is reflected back to the dashboard via a new `Verified bool` field on `RedactionEvent` (the projection in `cmd/r1-server/redaction.go`). Unverified entries render with a different glyph + tooltip ("⚠ signature failed verification") so an operator can distinguish a tampered log from a legitimate one.

## 4. Key management

- **Private key path:** `<root>/redactions/sign-priv.pem` (separate from the encryption-at-rest master key per spec separation).
- **Public key path:** `<root>/redactions/sign-pub.pem`. Distributed alongside the public ledger if remote verification is needed.
- **First-run generation:** if no private key exists at the configured path, generate a fresh ed25519 keypair via `ed25519.GenerateKey(rand.Reader)` and persist both halves. Log the public key fingerprint at INFO so operators can record it for cross-reference.
- **Rotation:** out of scope for this spec. The `Signer` field carries the fingerprint of whichever pubkey was active at sign time, so multi-key rotation can be added later via a sidecar `keys.json` index.

## 5. Canonical form

The signature covers the canonical JSON encoding of the record-without-signature:

```go
type canonicalForm struct {
    NodeID     string `json:"node_id"`
    RedactedAt string `json:"redacted_at"`
    Reason     string `json:"reason"`
    Signer     string `json:"signer"`
}
```

Field order is fixed by the struct declaration; encoding via `encoding/json` with no extra options. Signer field is INCLUDED in the signed payload (so swapping the signer can't reattribute a record).

## 6. Boundaries

- **No HSM integration.** File-backed keys only. HSM is a future spec.
- **No multi-sig.** Single ed25519 signature per event.
- **No key rotation in-place.** New private key = new signer fingerprint = new audit trail epoch.
- **No verification at write time.** Sign-and-record path doesn't re-verify; verification happens at read.
- **Backward compatibility:** unsigned entries (Signer == "" || SignatureHex == "") from pre-this-spec deployments stay readable and render as "legacy unsigned" rather than failing the audit.

## 7. Implementation checklist (7 items — self-contained)

- [ ] T1 — Write `internal/ledger/redact_sign.go` with `LoadOrGenerateSigningKey(rootDir) (priv ed25519.PrivateKey, fingerprint string, err error)`: reads `<root>/redactions/sign-priv.pem`, generates if absent, persists both halves with mode 0600/0644 respectively. Fingerprint = first 12 chars of SHA256 hex of the public key. Tests in `redact_sign_test.go`: 5 cases — fresh-generate creates files, second call reads same key, missing pub-but-priv-present regenerates pub from priv, corrupted priv returns error, fingerprint stable across reads.
- [ ] T2 — Add `SignRecord(rec SignedRedactionEvent, priv ed25519.PrivateKey) (signed SignedRedactionEvent, err error)` in same file. Returns rec with Signer + SignatureHex populated. Canonical form per §5. Test: same input ≠ same output across two different keys; same input + same key = same output (deterministic for ed25519).
- [ ] T3 — Add `VerifyRecord(rec SignedRedactionEvent, pub ed25519.PublicKey) error`: re-canonicalizes, ed25519.Verify, returns `nil` on match else a typed `ErrSignatureMismatch`. Test cases: happy path verifies, swapped Reason field rejects, swapped Signer rejects, empty Signature returns `ErrUnsigned` (distinct from mismatch).
- [ ] T4 — Extend `Store.RedactAndLog` to: load-or-generate the signing key on first call (cached on the Store); after Redact returns, call SignRecord, persist via RecordRedaction. Keep the unsigned `RecordRedaction` entry-point for callers that bring their own signature. Test: end-to-end roundtrip — RedactAndLog → RedactionsFor returns one entry with non-empty Signer + SignatureHex.
- [ ] T5 — Extend `Store.RedactionsFor` to verify each entry against the loaded public key. Add a sibling method `RedactionsForVerified(nodeID) ([]VerifiedRedactionEvent, error)` returning each event tagged with `Verified bool` (true = signature OK, false = mismatch OR unsigned). Existing `RedactionsFor` semantics unchanged for backward-compat callers. Test: 3-entry log with one tampered → two Verified=true + one Verified=false.
- [ ] T6 — Update `cmd/r1-server/redaction.go::projectSignedEvents` to pass `Verified` through into the rendered side panel + add a CSS class `redaction-event--unverified` styled to surface the warning. Update `cmd/r1-server/ui/web/partials/redaction-events.html` partial to render `[signature ⚠ unverified]` next to entries where Verified=false. Test in `cmd/r1-server/redaction_test.go`: rendered HTML contains the warning text iff one entry's Verified is false.
- [ ] T7 — Document the signing path in `cmd/r1-server/README.md`: where keys live, fingerprint format, what an unverified event means in the side panel, how to rotate (= delete the keys, regenerate, mark prior epoch in audit log). Cross-link from `specs/ledger-redaction.md` §6 ("...signed via signed-redaction.md").

## 8. Acceptance

- `go build ./... && go test ./internal/ledger/... ./cmd/r1-server/...` clean.
- A redaction performed via `RedactAndLog` produces a log entry with non-empty Signer + SignatureHex.
- A hand-edit of any character in a log entry's Reason field causes that entry's Verified to flip false on next read.
- The side panel renders the audit trail with the unverified warning visible when (and only when) verification fails.
