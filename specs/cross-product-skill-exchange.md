<!-- STATUS: done -->
<!-- CREATED: 2026-05-11 -->
<!-- BUILD_STARTED: 2026-05-12 -->
<!-- BUILD_COMPLETED: 2026-05-12 -->
<!-- DEPENDS_ON: -->
<!-- BUILD_ORDER: 46 -->

# Cross-Product Deterministic Skill Exchange — Implementation Spec

## Overview

SOW item C7: ship the R1-side substrate that lets R1's existing signed
deterministic skill-packs be federated into CloudSwarm, Heroa templates,
and Veritize verification flows, all sharing a common skill registry.
This spec is the R1 contribution only — the consumer-side adoption
work in CloudSwarm/Heroa/Veritize lives in those repos' own spec
queues.

The pack manufacturing pipeline, manifest schema v1, ed25519 signing
(`internal/skillmfr/pack_signature.go`), in-process HTTP registry
(`cmd/r1/skills_pack_server.go` serving `/v1/packs`) and the operator
CLI surface (`r1 skills pack init|info|install|list|publish|search|
sign|verify|update|serve`) all already ship per `specs/oss-hub.md`
(STATUS:done). This spec is purely about MAKING THE EXISTING SUBSTRATE
FEDERATED — adding a v2 manifest schema with cross-product compatibility
declarations, a federated trust root, runtime-adapter contracts, and a
`pack adopt` workflow. We do not rewrite, replace, or break v1.

The marketplace narrative this unlocks: pack authors publish once
against the R1 substrate; operators of any portfolio product pull
the same artifact and adopt it for their runtime via a typed adapter.
Federation comes from the trust root (multi-publisher key set), not
from a central authority.

## Stack & Versions

- Go 1.23 (matches `go.mod`).
- ed25519 via `crypto/ed25519` (already used in
  `internal/skillmfr/pack_signature.go` and
  `internal/ledger/redact_sign.go`).
- `net/http` standard library for `/v2/` registry endpoints; reuse
  patterns from `cmd/r1/skills_pack_server.go`.
- `gopkg.in/yaml.v3` (already in `go.mod`) for manifest parsing.
- `golang.org/x/time/rate` for HTTP rate limiting. Add to `go.mod`
  if not already vendored (`golang.org/x/time` is a small,
  well-vetted addition with no transitive deps).
- OCI artifact path uses media-type strings only — no ORAS client
  dependency is taken on in this spec (operators who want OCI host
  pull packs via their existing CI). Reference media-type:
  `application/vnd.r1.pack.v2+json` following ORAS conventions
  (see Salaboy 2026-04 + Thomas Vitale 2026 writeups on Agent
  Skills as OCI Artifacts — established pattern, no novel work
  here).
- Sigstore cosign integration is OUT OF SCOPE for THIS spec. The
  trust-root design is forward-compatible with replacing
  raw-ed25519 entries with Fulcio-issued cert chains later, but
  v2 ships with raw ed25519 keys to match the existing pack
  signing flow.

## Existing Patterns to Follow

- Pack on-disk layout + `pack.yaml` loader:
  `internal/skillmfr/pack.go` (`PackMeta`, `LoadPack`).
- ed25519 detached-signature flow:
  `internal/skillmfr/pack_signature.go` (`SignPack`,
  `VerifyPackSignature`, `pack.sig.json` envelope).
- HTTP registry server, bearer-token auth, archive streaming:
  `cmd/r1/skills_pack_server.go` (`skillPackRegistryServer`,
  `writePackArchive`).
- Manifest validation + drift hashing:
  `internal/skillmfr/manifest.go` (`Manifest`, `BehaviorFlags`,
  `ComputeHash`).
- CLI subcommand dispatch + flag parsing:
  `cmd/r1/skills_pack_cmd.go` (`skillsPackCmd`,
  `parseSkillPackArgs`).
- Builtin manifest backfill (idempotent registration):
  `internal/skill/manifest.go` (`BackfillManifests`).
- Audit-bus event emission + canonical-form signing patterns:
  `internal/ledger/redact_sign.go` (`SignRecord`,
  `SignedRedactionEvent`, canonical-form JSON).

## Library Preferences

- Signing: ed25519 via `crypto/ed25519`. DO NOT introduce gpg/pgp
  or sigstore as a dependency in v2 (forward-compatible only).
- HTTP server: `net/http` + `http.ServeMux`. DO NOT introduce a
  third-party router; keep parity with v1 server.
- Rate limiting: `golang.org/x/time/rate` token-bucket. DO NOT
  pull in a heavier framework.
- YAML: `gopkg.in/yaml.v3`. JSON: `encoding/json` only.
- Symlink/install semantics: reuse `r1dir.Canonical` +
  `r1dir.Legacy` (already in `internal/r1dir/`).

## Data Models

### `manifest.v2.json` — federated pack manifest

| Field | Type | Constraints | Default |
|-------|------|-------------|---------|
| `manifest_schema_version` | string | semver; `"2.0.0"` for this spec | required |
| `name` | string | matches v1 pack name rules | required |
| `version` | string | pack semver | required |
| `description` | string | <=500 chars | "" |
| `min_r1_version` | string | opaque semver | "" |
| `compat` | []string | non-empty subset of `["r1","cloudswarm","heroa","veritize"]` | required |
| `runtime_assertions` | map[string][]string | key in `compat`; value list of invariant tokens | {} |
| `consumer_hooks` | map[string]HookSpec | key = hook name | {} |
| `dependencies` | []string | other pack names | [] |
| `signature_authority` | string | one of `r1|cloudswarm|heroa|veritize|tenant` | `"r1"` |

### HookSpec

| Field | Type | Constraints | Default |
|-------|------|-------------|---------|
| `kind` | string | one of `pre_invoke`, `post_invoke`, `transform_args`, `transform_return`, `error_map` | required |
| `payload_schema` | json.RawMessage | non-empty JSON Schema | required |
| `optional` | bool | false = consumer MUST honor; true = consumer MAY ignore | false |

### TrustRootEntry (`/v2/trust-root` response)

| Field | Type | Constraints | Default |
|-------|------|-------------|---------|
| `key_id` | string | matches `ed25519:<hex>` pattern from `derivePackKeyID` | required |
| `public_key` | string | base64 ed25519 pub key | required |
| `authority` | string | `r1|cloudswarm|heroa|veritize|tenant` | required |
| `tenant_id` | string | required when `authority == "tenant"` | "" |
| `not_before` | string | RFC3339 timestamp | "" |
| `not_after` | string | RFC3339 timestamp; empty = no expiry | "" |
| `scopes` | []string | optional pack-name prefix allowlist | [] |

## API Endpoints

All endpoints serve under `/v2/` to coexist with the existing `/v1/`
surface in `cmd/r1/skills_pack_server.go`. The v1 handlers are left
untouched.

### GET /v2/packs
**Auth:** optional bearer (same scheme as v1, env
`R1_SKILL_PACK_REGISTRY_TOKEN`). HTTPS-only in production (see T9a).
**Response (200):** `{ "source_root":"...", "pack_count":N,
"packs":[ PackSummaryV2 ] }`. PackSummaryV2 extends the v1 summary
with `manifest_schema_version`, `compat`, `signature_authority`.
**Filtering:** query `?compat=cloudswarm` filters to packs declaring
that runtime.

### GET /v2/packs/{id}
**Response (200):** full v2 manifest + envelope (signed-blob metadata).
**404:** unknown pack id.

### GET /v2/packs/{id}/blob.tar.gz
**Response (200):** identical archive shape to v1 `archive.tar.gz` — a
tarball of the pack tree. Sets `Content-Type: application/gzip`,
`Content-Disposition: attachment; filename="{id}.tar.gz"`.

### GET /v2/packs/{id}/sig
**Response (200):** the detached `pack.sig.json` envelope. Lets
clients verify the archive without re-downloading it.

### GET /v2/packs/search
**Query params:** `q` (free-text), `compat` (runtime filter),
`authority` (trust-root scope filter), `limit` (default 50, max 200).
**Response (200):** `{ "query":"...", "match_count":N,
"matches":[ PackSearchEntryV2 ] }`.

### GET /v2/trust-root
**Response (200):** `{ "version":"...", "issued_at":"<RFC3339>",
"keys":[ TrustRootEntry ], "signature":"<base64 ed25519>" }`. The
trust-root document is itself signed by the registry's root operator
key (env `R1_REGISTRY_ROOT_KEY` or `<repo>/.r1/skills/trust-root.priv`).
Clients verify the document signature before trusting any contained
key.

### Response header: `X-R1-Registry-Sig`
Every v2 response (excluding archive bodies) carries a base64
ed25519 signature over
`SHA256(method + " " + path + "\n" + SHA256(body))`. Allows operators
to detect in-flight tampering even when TLS is terminated upstream.
Signing key: same root operator key as the trust-root signing key.

## Business Logic

### Pack load — v1 + v2 dual schema
1. Read `pack.yaml` (v1) AND optional `manifest.v2.json` from pack
   root.
2. If `manifest.v2.json` present, parse it. If missing, synthesize
   a v2 manifest in-memory with `compat: ["r1"]`,
   `manifest_schema_version: "2.0.0"`, copying `name/version/
   description/min_r1_version/dependencies` from v1 `PackMeta`,
   `signature_authority: "r1"`.
3. v2 manifest is the canonical in-memory representation downstream;
   v1 callers continue to receive `PackMeta` via the existing
   `LoadPack` return — no breakage.

### Pack adopt — emit consumer wrapper
1. Resolve pack via existing `resolveSkillPackSource`.
2. Load v2 manifest. If target product not in `compat`, error:
   `pack <id> not compatible with <product> (compat=[...])`.
3. Look up adapter via `internal/skill/compat/{product}.go`'s
   exported `Adapt(pack *ManifestV2) ([]byte, error)`.
4. Write the wrapper file alongside the pack under
   `<repo>/.r1/skills/packs/<id>/wrappers/<product>.wrapper`.
5. Emit `pack.adopted` event (T10).
6. Print operator instructions: where to copy the wrapper, the env
   variable the target product expects, and the verification
   command.

### Pack-runtime negotiation (T7)
1. Pack load reads the `compat` field.
2. R1 runtime: if `"r1"` not in compat -> refuse load with
   `pack <id> declares compat=[...] but R1 not present`.
3. Adapter for each sibling product performs the same check against
   its product key.
4. `runtime_assertions[<product>]` is evaluated by the adapter; each
   string is a free-form invariant token the adapter looks up in
   its closed allow-set. Unknown tokens are a load-time error so a
   malicious pack cannot bypass the adapter contract via novel keys.

### Trust verification
1. Pack signature loaded via existing
   `VerifyPackSignatureIfPresent`.
2. Extract `key_id` and `public_key` from `pack.sig.json`.
3. Fetch trust-root document (cached, refreshed every 5 min).
4. Verify trust-root document signature against root operator key.
5. Match `key_id` against trust-root entries; if absent refuse with
   `key_id <kid> not in trust root`.
6. Honor `not_before` / `not_after` / `scopes` constraints.
7. If `signature_authority` in v2 manifest does not match the
   trust-root entry's `authority`, refuse with
   `signature_authority mismatch: manifest says <x>, trust-root
   says <y>`.

### Negotiation sequence (textual diagram)

```
client                       /v2 registry                    pack
  |  GET /v2/trust-root          |                              |
  |----------------------------->|                              |
  |  signed TrustRootDocument    |                              |
  |<-----------------------------|                              |
  |  GET /v2/packs/{id}          |                              |
  |----------------------------->| LoadPack + LoadManifestV2    |
  |                              |--------------------------->  |
  |                              |   ManifestV2 + signature     |
  |                              |<---------------------------  |
  |  ManifestV2 + sig metadata   |                              |
  |<-----------------------------|                              |
  |  GET /v2/packs/{id}/blob...  |                              |
  |----------------------------->|                              |
  |  archive.tar.gz              |                              |
  |<-----------------------------|                              |
  |  VerifyPackSignature(local)  |                              |
  |  MatchKey(trust-root, kid)   |                              |
  |  CheckCompat(<runtime>)      |                              |
  |  Adapt(pack)  ->  wrapper    |                              |
```

## Error Handling

| Failure | Strategy | User Sees |
|---------|----------|-----------|
| v1 pack missing v2 manifest | synthesize defaults | (silent — backwards compat) |
| v2 manifest unparseable | fail load | `manifest.v2.json invalid: <reason>` |
| `compat` missing/empty | fail load | `manifest_v2: compat must list >=1 runtime` |
| Unknown runtime in `compat` | fail load | `manifest_v2: unknown runtime <name>` |
| `signature_authority` not in known set | fail load | `manifest_v2: signature_authority <x> not allowed` |
| Trust-root key_id mismatch | refuse load | `key_id <kid> not in trust root` |
| Trust-root document signature invalid | refuse load + log alert | `trust-root signature invalid — registry compromised?` |
| Registry rate limit exceeded | 429 + `Retry-After` | `Too Many Requests; retry after Ns` |
| `X-R1-Registry-Sig` verification fails (client side) | refuse to use payload | `registry response signature invalid` |
| Adapter for unknown product requested | fail | `unsupported adoption target: <product>` |

## Boundaries — What NOT To Do

- DO NOT modify v1 `PackMeta` struct in `internal/skillmfr/pack.go`.
  v2 lives in a NEW `internal/skill/manifest_v2.go`.
- DO NOT change the `pack.sig.json` envelope shape — v2 reuses v1
  signing verbatim; federation lives in the trust root, not in the
  signature envelope.
- DO NOT modify the existing `/v1/packs` HTTP handlers.
- DO NOT add CloudSwarm/Heroa/Veritize source files under their
  package trees in THIS repo; consumer-side adoption is a separate
  spec in each of those repos.
- DO NOT introduce sigstore/fulcio/rekor as a runtime dependency.
  Keep the trust-root model forward-compatible but raw-ed25519 in
  v2.
- DO NOT support arbitrary user-supplied runtime adapter names —
  closed set: `r1`, `cloudswarm`, `heroa`, `veritize`. New runtimes
  require their own spec PR.
- DO NOT centralize trust on a single key — federated by design.
- DO NOT break the existing CLI surface; `pack adopt` is additive.

## Testing

### `internal/skill/manifest_v2.go`
- [ ] Happy: parse minimal v2 manifest with `compat: ["r1"]`.
- [ ] Happy: parse v2 manifest with all 4 runtimes + all 5 hook
  kinds.
- [ ] Error: empty `compat` -> `compat must list >=1 runtime`.
- [ ] Error: unknown runtime in `compat` -> `unknown runtime <name>`.
- [ ] Edge: v1 `pack.yaml` only (no v2 file) -> synthesized in-memory
  v2 manifest with `compat:["r1"]`.

### `internal/skill/compat/{r1,cloudswarm,heroa,veritize}.go`
- [ ] Adapter `Adapt(pack)` round-trip for a v2 pack declaring that
  runtime -> produces a non-empty wrapper payload.
- [ ] Adapter refuses pack without its runtime in `compat`.
- [ ] Adapter rejects unknown `runtime_assertions[<product>]` tokens.
- [ ] Argument-shape transform: a sample pack manifest's R1 input
  schema is reshaped to the target's expected param shape.

### `cmd/r1/pack_adopt_cmd.go`
- [ ] Happy: `r1 skills pack adopt --pack <id> --for cloudswarm`
  writes the wrapper file under `<repo>/.r1/skills/packs/<id>/
  wrappers/cloudswarm.wrapper` and emits `pack.adopted`.
- [ ] Error: target not in `compat` -> non-zero exit + error
  message.
- [ ] Error: pack not found -> non-zero exit.

### `cmd/r1/skills_pack_server.go` (additions)
- [ ] `/v2/packs` returns v2 summaries for packs with v2 manifests
  AND synthesizes summaries for v1-only packs (compat=["r1"]).
- [ ] `/v2/packs?compat=cloudswarm` filters correctly.
- [ ] `/v2/trust-root` returns a signed document parseable by the
  client.
- [ ] `/v2/packs/{id}/sig` returns the existing `pack.sig.json`.
- [ ] `X-R1-Registry-Sig` present on every JSON response; clients
  verify against the root operator pub key.
- [ ] Rate limit kicks in at 60 req/min per IP (configurable via
  `R1_PACK_REGISTRY_RATE_LIMIT`); 429 returned with
  `Retry-After`.

### Conformance test suite (`internal/skill/compat/conformance_test.go`)
- [ ] Pack signed by R1 publisher -> loaded into mock CloudSwarm
  runtime via `cloudswarm.go` adapter -> returns expected value.
- [ ] Same pack -> loaded into mock Heroa template runtime ->
  returns.
- [ ] Same pack -> loaded into mock Veritize verification runtime
  -> returns.
- [ ] Pack with `compat:["r1","cloudswarm"]` rejected by Heroa
  adapter.
- [ ] Pack signed by tenant-specific key valid when trust-root
  contains the key.
- [ ] Pack signed by revoked key (`not_after` past) fails
  verification.

### v1 backwards compatibility regression
- [ ] All existing `internal/skillmfr/*_test.go` tests still pass.
- [ ] `cmd/r1/skills_pack_cmd_test.go` tests still pass.
- [ ] A v1 pack (no `manifest.v2.json`) installs, lists, serves
  via `/v1/packs/...` exactly as before.

## Acceptance Criteria

- WHEN a v1 pack is loaded THE substrate SHALL continue to operate
  unchanged (no regression in existing tests).
- WHEN a pack declares `compat: ["r1","cloudswarm"]` THE
  conformance suite SHALL round-trip the pack through both R1 and
  the mock CloudSwarm runtime and return the expected value.
- WHEN a pack is signed by a tenant-specific key AND that key is
  present in `/v2/trust-root` THE verification SHALL succeed.
- WHEN a pack is signed by a key NOT in the trust root THE
  verification SHALL fail with `key_id <kid> not in trust root`.
- WHEN the registry receives more than the configured per-IP
  request rate THE server SHALL return HTTP 429 with a
  `Retry-After` header.
- WHEN a v2 response is tampered with in flight THE client-side
  `X-R1-Registry-Sig` verification SHALL fail and the client SHALL
  refuse the payload.
- WHEN `r1 skills pack adopt --pack <id> --for <product>` runs
  against a pack with the product in `compat` THE command SHALL
  write the wrapper file AND emit a `pack.adopted` event to
  bus + ledger.

## Implementation Checklist

1. [x] **T1: v2 manifest schema** — create
   `internal/skill/manifest_v2.go`. Define `ManifestV2` struct,
   `HookSpec`, `LoadManifestV2(packRoot) (*ManifestV2, error)`.
   Honor backwards-compat path: if `manifest.v2.json` missing,
   synthesize from v1 `PackMeta` with `compat:["r1"]`. Validate:
   non-empty `compat`, runtime names in closed set
   `{r1,cloudswarm,heroa,veritize}`, `signature_authority` in
   `{r1,cloudswarm,heroa,veritize,tenant}`, each hook's `kind`
   in closed set, `payload_schema` non-empty. Add `*_test.go`
   covering each error path + the v1-only synthesis path.
   Files touched: `internal/skill/manifest_v2.go`,
   `internal/skill/manifest_v2_test.go`.

2. [x] **T2: Federated registry HTTP — `/v2` handlers** — extend
   `cmd/r1/skills_pack_server.go` with a parallel `/v2/` handler
   tree. Reuse `registryPackPaths`, add v2 detail builder
   `buildRegistryPackDetailV2`. Routes: `/v2/packs`,
   `/v2/packs/{id}`, `/v2/packs/{id}/blob.tar.gz`,
   `/v2/packs/{id}/sig`, `/v2/packs/search`, `/v2/trust-root`.
   Implement `?compat=<r>` filtering on the list + search routes.
   Do NOT touch v1 handlers. Files touched:
   `cmd/r1/skills_pack_server.go`,
   `cmd/r1/skills_pack_server_test.go`.

3. [x] **T3: OCI artifact distribution path (documentation
   only)** — add `docs/skills/oci-distribution.md` documenting the
   media-type `application/vnd.r1.pack.v2+json` and the
   pack-as-OCI-artifact layout (config blob = manifest v2 JSON;
   layer blob = the pack tarball; signature blob = pack.sig.json).
   Reference ORAS conventions. NO Go code changes — this is a
   documented interoperability surface for operators who want to
   host packs on GHCR/Docker Hub/Harbor/AR. File touched:
   `docs/skills/oci-distribution.md`.

4. [x] **T4a: R1 native adapter** — create
   `internal/skill/compat/r1.go`.
   `Adapt(pack *ManifestV2) ([]byte, error)` returns a passthrough
   JSON descriptor; arg/return transform is identity. Used by the
   conformance suite as the baseline. Files touched:
   `internal/skill/compat/r1.go`,
   `internal/skill/compat/r1_test.go`.

5. [x] **T4b: CloudSwarm runtime adapter** — create
   `internal/skill/compat/cloudswarm.go`. Read CloudSwarm's
   `SkillDefinition` contract from
   `/home/eric/repos/CloudSwarm/platform/skills/core/base.py`
   (fields `name`, `display_name`, `description`,
   `params_schema`, `trust_level_required`, `tags`, `triggers`,
   `version`). Adapter maps R1 v2 manifest -> CloudSwarm
   `SkillDefinition` JSON. Argument shape: R1
   `inputSchema.properties.context` -> CloudSwarm
   `params.context`. Return shape: R1 `outputSchema.guidance` ->
   CloudSwarm `result.guidance`. Error map: R1
   `ErrPackSignatureInvalid` -> CloudSwarm trust-level violation.
   Files touched: `internal/skill/compat/cloudswarm.go`,
   `internal/skill/compat/cloudswarm_test.go`.

6. [x] **T4c: Heroa template adapter** — create
   `internal/skill/compat/heroa.go`. Reference Heroa's template
   shape from `/home/eric/repos/heroa/cmd/heroa/deploy.go` and
   `TemplatePolicy`. Adapter wraps a v2 pack as a Heroa template
   slug: derives `slug` from `pack.name`, surfaces
   `inputSchema.properties` as Heroa template parameters, maps
   the v2 `min_r1_version` to Heroa's substrate min-version
   field. Error propagation: R1 load errors -> structured
   error analogous to Heroa's `TemplateRegionExcludedError`.
   Files touched: `internal/skill/compat/heroa.go`,
   `internal/skill/compat/heroa_test.go`.

7. [x] **T4d: Veritize verification-flow adapter** — create
   `internal/skill/compat/veritize.go`. Reference verification
   flow from
   `/home/eric/repos/veritize/relayone/veritize/internal/api/verify_handler.go`
   (`VerifyRequest`/`VerifyResponse`). Adapter wraps a v2 pack as
   a verification-flow step: `inputSchema.properties.context` ->
   Veritize `VerifyRequest.Subject`, `outputSchema.guidance` ->
   Veritize `VerifyResponse.Findings`. Error map: pack-signature
   failures -> Veritize "subject untrusted" error. Files
   touched: `internal/skill/compat/veritize.go`,
   `internal/skill/compat/veritize_test.go`.

8. [x] **T5: `r1 skills pack adopt` command** — create
   `cmd/r1/pack_adopt_cmd.go`. Wire into `skillsPackCmd`
   dispatcher in `skills_pack_cmd.go` as new case `"adopt"`.
   Flags: `--pack <id>` (required), `--for <product>` (required,
   one of `r1|cloudswarm|heroa|veritize`), `--repo <path>`
   (default `"."`). Resolves pack via existing
   `resolveSkillPackSource`, loads v2, validates compat,
   dispatches to the matching adapter, writes
   `<pack>/wrappers/<product>.wrapper`, emits `pack.adopted`,
   prints next-step instructions. Files touched:
   `cmd/r1/pack_adopt_cmd.go`, `cmd/r1/pack_adopt_cmd_test.go`,
   `cmd/r1/skills_pack_cmd.go` (one-line dispatcher addition +
   updated usage banner).

9. [x] **T6: Federated trust root — keys + scopes** — create
   `internal/skill/trustroot.go`. Types: `TrustRootDocument`,
   `TrustRootEntry`. Functions: `LoadTrustRoot(path) (*Doc,
   error)`, `SignTrustRoot(doc, priv) error`,
   `VerifyTrustRoot(doc, rootPub) error`,
   `MatchKey(doc, kid, packName) (*TrustRootEntry, error)`.
   Honor `not_before` / `not_after` / `scopes`. Persist as
   `<repo>/.r1/skills/trust-root.json`. Default-empty trust root
   is valid (single-publisher mode = only the local signing key);
   when no trust-root document is present, R1 falls back to v1
   behavior (signature-only check). Files touched:
   `internal/skill/trustroot.go`,
   `internal/skill/trustroot_test.go`.

10. [x] **T6b: Wire trust-root into pack load** — extend
    `loadSkillPackWithSignature` in `cmd/r1/skills_pack_cmd.go`
    to, when a trust-root document is present, call
    `trustroot.MatchKey` and refuse load on mismatch. When no
    trust-root document is present, continue with v1 behavior
    (signature-only check). Files touched:
    `cmd/r1/skills_pack_cmd.go` (extend existing helper; keep
    behavior gated on trust-root presence).

11. [x] **T7: Pack-runtime negotiation** — implement runtime
    check in `internal/skill/manifest_v2.go`:
    `(m *ManifestV2) CheckCompat(runtime string) error`. Called
    by each adapter's `Adapt` entry. Also called by R1's existing
    load path when v2 manifest is present (no-op for
    v1-synthesized manifests because they default to
    `compat:["r1"]`). Negotiation textual diagram is in the
    Business Logic section above. Files touched: same as T1.

12. [x] **T8: v1 backwards compatibility regression** — add
    regression tests that run the full v1 pack lifecycle (init,
    sign, verify, install, publish, list, info, search) against
    a pack that ships ZERO v2 artifacts, and assert no behavior
    change in `cmd/r1/skills_pack_cmd_test.go` outcomes. Also
    assert `/v1/packs` and `/v1/packs/{id}` continue to return
    identical JSON shapes (golden file). Files touched:
    `cmd/r1/skills_pack_cmd_test.go` (regression group),
    `cmd/r1/skills_pack_server_test.go`,
    `cmd/r1/testdata/v1_compat/` (golden files).

13. [x] **T9a: HTTPS + cert config** — add `--cert` and `--key`
    flags to `r1 skills pack serve` for TLS. Behavior: if both
    present, server uses `ListenAndServeTLS`; if neither, HTTP
    (dev only) with a one-line warning to stderr; if one but not
    the other, fail with usage error. Files touched:
    `cmd/r1/skills_pack_server.go`,
    `cmd/r1/skills_pack_server_test.go`.

14. [x] **T9b: Per-IP rate limit** — add a
    `golang.org/x/time/rate` limiter per remote IP (kept in a
    `sync.Map` of `string -> *rate.Limiter`). Configurable via
    `R1_PACK_REGISTRY_RATE_LIMIT` env (req/min; default 60). On
    exceedance, return HTTP 429 with `Retry-After: 60`. Cleanup
    goroutine evicts limiters idle >10 min. Apply to ALL `/v2/`
    routes; do NOT apply to `/v1/` (backwards compat). Files
    touched: `cmd/r1/skills_pack_server.go`,
    `go.mod` / `go.sum` if `golang.org/x/time/rate` not already
    vendored.

15. [x] **T9c: Registry response signing —
    `X-R1-Registry-Sig`** — wrap the v2 response writer with a
    signature middleware that computes `sig =
    ed25519.Sign(rootPriv, SHA256(method + " " + path + "\n" +
    SHA256(body)))` and sets the header before flushing. Clients
    verify by recomputing. Use the same root operator key as the
    trust-root signing key. Files touched:
    `cmd/r1/skills_pack_server.go`.

16. [x] **T10: Audit trail — `pack.adopted` event** — emit a
    typed event via the existing bus/ledger surface every time
    `pack adopt` succeeds. Payload: `{ pack_id, pack_version,
    target_product, tenant_id, signer_key_id, adopted_at }`.
    Reuse the `internal/ledger/redact_sign.go` canonical-form
    pattern so the event itself is signed by the operator's key.
    Files touched: `cmd/r1/pack_adopt_cmd.go` (emit call),
    `internal/ledger/pack_adopted_event.go` (event type +
    canonical form + persistence helper),
    `internal/ledger/pack_adopted_event_test.go`.

17. [x] **T11: Conformance test suite** — create
    `internal/skill/compat/conformance_test.go`. Build a fixture
    v2 pack with `compat:["r1","cloudswarm","heroa","veritize"]`,
    sign it with a fixture key, install it into a temp trust
    root, and assert each of the 4 adapters round-trips:
    input -> adapter transform -> mock runtime call -> output ->
    adapter return transform -> expected R1 shape. Also test the
    negative cases (one runtime missing from compat, key not in
    trust root, revoked key by `not_after`). Files touched:
    `internal/skill/compat/conformance_test.go`,
    `internal/skill/compat/testdata/` (fixture pack + key).

18. [x] **T12a: Pack-author docs** — create
    `docs/skills/cross-product-distribution.md`. Audience: people
    authoring packs. Cover: when to declare `compat`, how
    `runtime_assertions` work, hook kinds with examples, how to
    test a pack against the 4 adapters locally, and include the
    negotiation sequence diagram from this spec. File touched:
    above.

19. [x] **T12b: Operator trust-root runbook** — create
    `docs/skills/federated-trust.md`. Audience: registry
    operators. Cover: how to generate the root operator key, how
    to add a publisher key to the trust root, how to rotate
    (issue new key with overlapping `not_before`/`not_after`,
    deprecate old after grace period), how to revoke (set
    `not_after` to now), how to scope a tenant key to a
    pack-name prefix, audit-log interpretation, incident
    response if root key is suspected compromised. File touched:
    above.

20. [x] **T13: Operator marketplace playbook** — create
    `docs/skills/marketplace-playbook.md`. Audience: portfolio
    operators evaluating the cross-product story end-to-end.
    Cover: pack discovery via
    `/v2/packs/search?compat=<product>`, the adopt workflow,
    ongoing operations (key rotation, pack upgrades), and the
    fact that this spec is ONLY the R1-side substrate (sibling-
    product adoption is each team's own spec work). File
    touched: above.

21. [x] **T14: CLI help + usage text** — extend the help string
    in `skillsPackCmd` to include `adopt` in the subcommand list,
    and add detailed usage text in `parseSkillPackAdoptArgs`
    (mirror `parseSkillPackPublishArgs` style). Files touched:
    `cmd/r1/skills_pack_cmd.go`, `cmd/r1/pack_adopt_cmd.go`.

22. [x] **T15: README install-section callout** — add a
    one-paragraph "Cross-product skill distribution (preview)"
    note to `README.md` linking to
    `docs/skills/cross-product-distribution.md`. No behavioral
    change. File touched: `README.md`.

23. [x] **T16: CI-gate run** — `go build ./cmd/r1` +
    `go vet ./...` + `go test ./...` green. Address any new vet
    findings. Pre-existing failures (e.g. selfscan `//nolint`
    cite at `cmd/r1-server/import.go:254` and
    `cmd/r1/export_cmd.go:455` per oss-hub.md item 6) remain out
    of scope. No files touched if green.

24. [x] **T17: Mark spec STATUS done** — flip frontmatter
    `STATUS: ready` -> `STATUS: done`, add `BUILD_STARTED` /
    `BUILD_COMPLETED` lines, and per-checklist-item commit
    hashes. File touched:
    `specs/cross-product-skill-exchange.md`.

## Notes on Forward-Compatibility (Sigstore)

The trust-root model encodes raw ed25519 public keys in v2 because
that is what `internal/skillmfr/pack_signature.go` produces today.
A future Sigstore-keyless variant (a separate follow-up spec)
replaces a `TrustRootEntry.public_key` value with a Fulcio-issued
cert chain binding an OIDC identity. The on-disk schema is
forward-compatible: add a `cert_chain` field next to `public_key`
and teach `MatchKey` to verify against it. No v2 client breakage
required — older clients ignore the new field. Sigstore migration
is OUT OF SCOPE for this spec.

## Notes on OCI distribution

T3 documents the OCI media-type so operators with existing OCI
registry infrastructure (GHCR, Docker Hub, Harbor, AR) can host
packs without running `r1 skills pack serve` themselves. R1 itself
does not push or pull from OCI registries in this spec — the HTTP
`/v2/` surface is canonical. An optional OCI client wrapper
(`r1 skills pack push-oci` / `pull-oci` using the ORAS Go SDK) is
a follow-up that depends on this spec's manifest schema but is not
required to ship the federated runtime story.
