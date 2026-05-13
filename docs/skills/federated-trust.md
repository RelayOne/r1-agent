# Federated trust-root runbook (C7)

Audience: registry operators running `r1 skills pack serve` for a
fleet of pack publishers.

## What the trust root is

A `TrustRootDocument` is a signed list of ed25519 public keys keyed
by `kid` (matching `derivePackKeyID`: `ed25519:<hex16>`). Each entry
declares an `authority` (`r1`, `cloudswarm`, `heroa`, `veritize`, or
`tenant`), optional `tenant_id`, optional `not_before` / `not_after`
RFC3339 timestamps, and optional pack-name `scopes`.

The document itself is signed by the registry's **root operator
key**. Clients verify the document signature before trusting any
contained publisher key. Federation is by design: no central
authority needs to bless a publisher; an operator's trust root
encodes the trust set for their fleet.

## Generating the root operator key

```bash
# Either point to your own path...
export R1_REGISTRY_ROOT_KEY=/etc/r1/registry/root.key
r1 skills pack serve --source-root /srv/r1

# ...or let the server load-or-generate it under the source root.
r1 skills pack serve --source-root /srv/r1
# A fresh key lands at /srv/r1/.r1/skills/trust-root.priv (0600).
```

The same key signs:

- the `/v2/trust-root` document
- every `/v2/` JSON response via the `X-R1-Registry-Sig` header

Distribute the matching **public key** out-of-band to every client
that consumes your registry; clients use it to verify both the
trust-root document and the response signatures.

## Adding a publisher key

Edit `<source-root>/.r1/skills/trust-root.json`:

```json
{
  "version": "1",
  "issued_at": "2026-05-12T00:00:00Z",
  "keys": [
    {
      "key_id": "ed25519:abc1234567890def",
      "public_key": "base64-of-ed25519-pubkey",
      "authority": "r1",
      "not_before": "2026-05-01T00:00:00Z",
      "not_after":  "2027-05-01T00:00:00Z"
    }
  ],
  "signature": ""
}
```

Re-sign the document:

```bash
r1 skills trustroot sign --root-key $R1_REGISTRY_ROOT_KEY \
                         --doc /srv/r1/.r1/skills/trust-root.json
```

(The signing CLI is intentionally a thin wrapper around
`skill.SignTrustRoot` — operators who script the workflow can call
the Go function directly.)

The `/v2/trust-root` cache TTL is 5 minutes; the next pull after that
window picks up the new document automatically.

## Rotating a key

1. Generate the new publisher keypair.
2. Add the new entry with a `not_before` that overlaps the existing
   entry's `not_after`. Example: 30-day grace window.
3. Re-sign + redistribute the document.
4. Once the grace window passes, REMOVE the old entry — do not
   simply rely on the expired `not_after` to make it a no-op,
   because expired entries still count toward the search/match
   surface area and increase the attack vector for stolen-key
   replay.

## Revoking a key

Set the entry's `not_after` to a timestamp in the past, re-sign,
redistribute. Verifying clients will see
`trustroot: key expired` and refuse load.

If the key is suspected to be compromised, **also rotate the root
operator key**. The trust-root document signed by the compromised
root is no longer trustworthy until the new root pub key is
out-of-band-distributed to every client.

## Scoping a tenant key

A tenant-scoped key sets `authority: "tenant"`, `tenant_id:
"<tenant>"`, and `scopes: ["<prefix>"]`. The match logic refuses to
verify a pack whose name does not start with one of the configured
prefixes:

```json
{
  "key_id": "ed25519:acme1234",
  "public_key": "...",
  "authority": "tenant",
  "tenant_id": "acme",
  "scopes": ["acme.", "acme-"]
}
```

Tenant keys are the recommended substrate for per-customer
publishers in a multi-tenant marketplace: the operator can issue +
revoke per-tenant keys without rotating any shared root.

## Audit-log interpretation

Every successful `r1 skills pack adopt` writes a `pack.adopted`
node to `<repo>/.r1/ledger/chain/`. The node carries the signed
payload (`pack_id`, `pack_version`, `target_product`, `tenant_id`,
`signer_key_id`, `adopted_at`, `signer`, `signature_hex`). The
signature is verifiable against the operator's adopt-signing key
(see `<repo>/.r1/skills/adopt-signing.key`). A node whose signature
fails verification has been tampered with downstream — investigate
before trusting the corresponding wrapper deployments.

## Incident response: root key compromise

1. Rotate the root operator key (generate fresh, persist at the
   configured path).
2. Re-sign the trust-root document with the new key.
3. Out-of-band-distribute the new root pub key to every consuming
   client.
4. Scan the audit log under `<repo>/.r1/ledger/chain/` for
   `pack.adopted` nodes signed during the suspected compromise
   window. Each such node MUST be re-validated against the pack's
   pack.sig.json — a pack whose signature kid is absent from the
   NEW trust root indicates an adoption that should be reverted.
5. The conformance suite under
   `internal/skill/compat/conformance_test.go` documents the
   trust-root expectations the new root must continue to satisfy.

## Forward-compatibility with Sigstore

The trust-root document carries raw ed25519 public keys today. A
future spec ADDS a `cert_chain` field next to `public_key` and
teaches `MatchKey` to verify Sigstore-issued cert chains binding an
OIDC identity. Older clients ignore the new field — the schema is
forward-compatible.
