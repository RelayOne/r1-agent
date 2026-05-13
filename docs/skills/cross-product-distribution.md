# Cross-product skill distribution (C7)

Audience: pack authors who want their R1 skill pack to be adopted by
CloudSwarm, Heroa, and Veritize without duplicating manifests or
re-signing per product.

## When to declare `compat`

Every v2 pack carries a `compat` list under `manifest.v2.json`. The
list is a non-empty subset of the closed runtime set:

```
{ "r1", "cloudswarm", "heroa", "veritize" }
```

Rule of thumb: declare every product whose adapter the pack has been
tested against. Adding `cloudswarm` to `compat` is a promise that
the pack's manifest passes the CloudSwarm adapter's
`runtime_assertions` check and that the consumer can invoke the
pack via the documented arg/return shape transforms below.

Omitting a product from `compat` is the right move when the pack's
input or output schema cannot be cleanly mapped onto that product's
contract — the adapter will refuse `Adapt(...)` with
`pack <id> declares compat=[...] but <product> not present`.

## `runtime_assertions`

The `runtime_assertions` field is a map keyed by runtime id; each
value is a list of free-form invariant tokens the adapter for that
runtime looks up in its closed allow-set. Unknown tokens are a
LOAD-time error, not a runtime warning — a malicious pack cannot
bypass the adapter contract by inventing novel keys.

Per-runtime allow-sets:

| Runtime    | Tokens |
|------------|--------|
| r1         | `native`, `vetted`, `trust_root`, `strict_mcp`, `sandbox_on`, `sandbox_off` |
| cloudswarm | `trust_low`, `trust_medium`, `trust_high`, `async_only`, `sync_only` |
| heroa      | `public`, `private`, `region_us`, `region_eu`, `region_apac`, `region:<id>` |
| veritize   | `enforcement_advisory`, `enforcement_block`, `enforcement_warn`, `sources_required`, `reasoning_required` |

Example:

```json
{
  "manifest_schema_version": "2.0.0",
  "name": "deploy-acme",
  "version": "1.0.0",
  "compat": ["r1", "heroa"],
  "runtime_assertions": {
    "heroa": ["region:us-east", "public"]
  },
  "signature_authority": "r1"
}
```

## Hook kinds with examples

`consumer_hooks` map names to `HookSpec` records. The `kind` is one
of:

| Kind                | Purpose |
|---------------------|---------|
| `pre_invoke`        | Run before the consumer invokes the skill. |
| `post_invoke`       | Run after the skill returns. |
| `transform_args`    | Reshape arguments before invoke. Heroa templates surface these as parameters. |
| `transform_return`  | Reshape the return value before the consumer sees it. |
| `error_map`         | Translate R1 error classes into consumer-native errors. |

Each hook carries a `payload_schema` JSON Schema — non-empty by
construction. Hooks with `optional: true` may be ignored by the
consumer; default `false` means the consumer MUST honor the hook.

## Argument/return-shape mapping

| Source (R1 v2 pack)                  | Target (consumer) |
|--------------------------------------|--------------------|
| `inputSchema.properties.context`     | CloudSwarm `params.context` |
| `outputSchema.guidance`              | CloudSwarm `result.guidance` |
| `inputSchema.properties.context`     | Veritize `VerifyRequest.Context` |
| `outputSchema.guidance`              | Veritize `VerifyResponse.Findings` |
| `ConsumerHooks[*]` w/ transform_args | Heroa template parameters |
| `MinR1Version`                       | Heroa `substrate_min_version` |
| `RuntimeAssertions["heroa"]` region: | Heroa `region_allowlist`     |

The adapters under `internal/skill/compat/` are pure functions
covered by the conformance suite. Pack authors should not hand-edit
the wrapper output — re-run `r1 skills pack adopt --pack <id> --for
<product>` after any manifest change.

## Testing a pack against the 4 adapters locally

```bash
# 1. Author the pack
r1 skills pack init --pack alpha --description "test pack"
# Add manifest.v2.json with compat: ["r1","cloudswarm","heroa","veritize"]

# 2. Sign the pack
r1 skills pack sign --pack alpha --key ~/.ssh/r1_packs_ed25519

# 3. Run the adopt workflow for each target
for target in r1 cloudswarm heroa veritize; do
  r1 skills pack adopt --pack alpha --for "$target"
done

# 4. Inspect the wrappers
ls .r1/skills/packs/alpha/wrappers/
# cloudswarm.wrapper  heroa.wrapper  r1.wrapper  veritize.wrapper
```

## Negotiation sequence

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
