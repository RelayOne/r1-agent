# Operator marketplace playbook (C7 T13)

Audience: portfolio operators evaluating the cross-product story
end-to-end — "how does a pack flow from publisher to all four
products?"

## Quick summary

R1 ships the substrate. Each sibling product (CloudSwarm, Heroa,
Veritize) ships its own consumer-side adoption work in its own
repo. This spec (specs/cross-product-skill-exchange.md) is ONLY the
R1 contribution — sibling-product adoption is each team's own spec
work.

## End-to-end workflow

1. **Publisher signs the pack with their per-publisher ed25519 key.**

   ```bash
   r1 skills pack sign --pack alpha --key ~/.ssh/r1_pack_signing
   ```

2. **Operator publishes the pack to their `/v2/`-enabled R1 registry.**

   ```bash
   r1 skills pack publish --pack alpha --dest-root /srv/r1
   ```

3. **Consumer operator discovers the pack.**

   ```bash
   curl https://r1.example.com/v2/packs/search?compat=cloudswarm
   ```

   Returns a `PackSearchEntryV2` list filtered to packs that declare
   CloudSwarm in their `compat`. The same search supports `authority`
   filtering (trust-root scope) and `q` free-text on name +
   description.

4. **Consumer operator pulls the trust root + pack manifest.**

   ```bash
   curl https://r1.example.com/v2/trust-root
   curl https://r1.example.com/v2/packs/alpha
   curl https://r1.example.com/v2/packs/alpha/sig
   curl -O https://r1.example.com/v2/packs/alpha/blob.tar.gz
   ```

   Every JSON response carries an `X-R1-Registry-Sig` header signed
   by the registry's root operator key. Clients MUST verify this
   signature before trusting the payload — defends against in-flight
   tampering even when TLS is terminated upstream.

5. **Consumer operator adopts the pack for their product.**

   ```bash
   r1 skills pack adopt --pack alpha --for cloudswarm
   ```

   - Loads the pack via `resolveSkillPackSource`.
   - Loads `manifest.v2.json` (or synthesizes a v2 manifest from a
     v1-only pack with `compat:["r1"]`).
   - Verifies `compat` includes the target.
   - Verifies the signing kid against the local trust-root document
     (when present).
   - Dispatches to `internal/skill/compat/<product>.go::Adapt`.
   - Writes the wrapper to `<pack>/wrappers/<product>.wrapper`.
   - Emits a signed `pack.adopted` ledger event for the audit log.

6. **Consumer product loads the wrapper.**

   This step is OUT OF SCOPE for the R1-side spec. Each sibling
   product (CloudSwarm, Heroa, Veritize) ships its own adoption
   spec describing how the wrapper plugs into its skill registry.

## Ongoing operations

### Key rotation

Walk the steps in `federated-trust.md`. The overlapping
`not_before`/`not_after` window keeps in-flight adoptions valid
during the transition.

### Pack upgrades

Bump `version` in both `pack.yaml` and `manifest.v2.json`, re-sign,
re-publish. Consumers re-pull via the standard install path; v2
manifests carry semver so consumers can pin against `>=1.0.0,<2.0.0`
windows.

### Audit-log queries

Every `r1 skills pack adopt` writes a `pack.adopted` ledger node.
Query the audit trail via:

```bash
ls .r1/ledger/chain/pack-adopted-*.json | xargs jq .
```

The signed payload carries `pack_id`, `target_product`, `tenant_id`,
`signer_key_id`, `adopted_at`. Tamper-evident because the canonical
form excludes the signature field by construction.

## What's NOT in this spec

The consumer-side adoption work in CloudSwarm, Heroa, and Veritize
is each team's own work. This spec ships the R1 substrate (manifest
v2 schema, federated trust root, registry API, adapter contracts,
`pack adopt` workflow) — the consumer products each ship their own
spec describing how the wrappers integrate into their skill
registries.

The Go-side conformance suite under
`internal/skill/compat/conformance_test.go` pins the R1 side of the
contract; the sibling repos pin the consumer side in their own
conformance suites. The R1-side adapters were built against the
upstream consumer contracts read directly from the local sibling
repos (`/home/eric/repos/CloudSwarm/platform/skills/core/base.py`,
`/home/eric/repos/heroa/cmd/heroa/deploy.go`,
`/home/eric/repos/veritize/relayone/veritize/internal/api/dto.go`).
