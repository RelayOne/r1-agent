# api-versioning-evolution

> API versioning strategies, backward compatibility enforcement, contract testing, and schema evolution across REST, GraphQL, gRPC, and event streams

<!-- keywords: api version, backward compatible, breaking change, deprecation, contract test, pact, semver, openapi, protobuf, avro, schema evolution, content negotiation -->

## Critical Rules

1. **Add fields as optional. Never rename or remove fields without a deprecation period.** Every addition must have a default value. Removal requires a multi-phase deprecation: announce, mark deprecated, stop sending, then remove after all consumers have migrated.

2. **Deploy consumers before producers when evolving schemas.** Consumer deploys with the new field treated as optional with sensible defaults. Only then does the producer start sending the field. No synchronized deploys required.

3. **Configure JSON deserializers to ignore unknown properties.** The Tolerant Reader pattern (Postel's Law): services extract only needed fields and ignore everything else. This enables independent evolution where producers add fields freely.

4. **Protobuf golden rule: never change or reuse field numbers.** The wire format uses numbers, not names. Renaming fields is safe. Use `reserved` declarations for removed fields. Avro golden rule: always add fields with defaults.

5. **Set `auto.register.schemas=false` in production.** Register schemas exclusively through CI/CD pipelines after passing compatibility checks against the Schema Registry `/compatibility` endpoint.

## Versioning Strategies

### URL Path Versioning (`/v1/users`)
Most explicit and cacheable. Each version is a distinct resource. Downside: multiple codepaths to maintain. GitHub, Stripe, and most public APIs use this. Best for public APIs where clarity matters.

### Header Versioning (`Accept: application/vnd.api+json; version=2`)
Cleaner URLs. Content negotiation is semantically correct for format changes. Harder to test (can't paste URL in browser). Best for internal APIs with sophisticated clients.

### Query Parameter (`/users?version=2`)
Simple to implement and test. Not RESTful (version is not a resource attribute). Best for pragmatic internal APIs during transitions.

### No Explicit Versioning (Additive Only)
Never remove or rename fields. Only add optional fields with defaults. Stripe's approach for most changes: versioned API dates (`Stripe-Version: 2024-01-01`) with automatic per-account pinning. Minimizes client migration burden. Requires disciplined schema design.

## Breaking vs Non-Breaking Changes

| Change | REST | gRPC | Kafka/Events |
|--------|------|------|-------------|
| Add optional field | Safe | Safe | Safe (with default) |
| Remove field | Breaking | Breaking (use reserved) | Breaking (Avro without default) |
| Rename field | Breaking | Safe (Protobuf uses numbers) | Breaking (Avro), Safe (Protobuf) |
| Change field type | Breaking | Limited safe promotions | Format-dependent |
| Add enum value | Typically safe | Safe (stored as int) | Breaking (Avro old readers fail) |
| Add required field | Breaking | N/A (proto3 all optional) | Breaking |
| Reorder fields | Safe (JSON) | Safe (Protobuf) | Breaking (Avro) |

## Contract Testing with Pact

Consumer-driven contract testing (CDCT): consumers define the minimum API contract they need, generate a contract file, publish to Pact Broker. Provider CI pulls contracts and verifies. "Can I Deploy?" gates deployment -- a provider cannot ship changes breaking any consumer's contract.

Key discipline: contracts should be as loose as possible. Only test fields the consumer actually uses. This prevents unnecessary coupling.

## Schema Registry Compatibility Modes (Kafka)

| Mode | Deployment Order | Safe Changes |
|------|-----------------|-------------|
| BACKWARD (default) | Consumers first | Delete fields; add optional with defaults |
| FORWARD | Producers first | Add fields; delete optional with defaults |
| FULL | Either order | Add/delete optional fields with defaults only |
| FULL_TRANSITIVE | Either order | Safest; checked against all historical versions |

Use RecordNameStrategy for event-sourcing with multiple event types per topic. TopicNameStrategy (default) for one event type per topic.

## gRPC Service Evolution

- Add new methods freely (non-breaking for existing clients).
- Add new fields with new field numbers (non-breaking).
- Deprecate methods with `option deprecated = true` before removal.
- Use `google.protobuf.FieldMask` for partial updates to avoid null-vs-absent ambiguity.
- Streaming RPCs can evolve message types independently of the stream contract.

## GraphQL Schema Evolution

- Adding fields, types, and enum values is always safe.
- Use `@deprecated(reason: "Use newField instead")` for deprecation.
- Monitor field usage via Apollo Studio or custom middleware before removing deprecated fields.
- Never change a field's return type. Add a new field with the desired type instead.
- Depth limiting (5-10 levels), breadth limiting, and computed complexity scoring prevent abuse.

## Deprecation Lifecycle

1. **Announce**: Document deprecation in changelog, API docs, and response headers (`Deprecation: true`, `Sunset: Sat, 01 Jan 2028 00:00:00 GMT`).
2. **Instrument**: Log usage of deprecated fields/endpoints. Identify remaining consumers.
3. **Warn**: Return `Warning` header (RFC 9218) or custom deprecation notices in response bodies.
4. **Migrate**: Provide migration guides. Offer automated tooling where possible.
5. **Remove**: Only after usage drops to zero or the sunset date passes. Keep the endpoint returning 410 Gone for a grace period.

## CI/CD Schema Enforcement

Layer four tools: **Semgrep** for static analysis of API contracts, **openapi-diff** or **oasdiff** to detect breaking changes in OpenAPI specs at PR time, **buf breaking** for Protobuf schema compatibility, and **Pact** for consumer-driven contract verification. Gate deployments on all four passing. Stripe runs automated backwards-compatibility checks on every PR.

## Common Gotchas

- **Pagination cursor changes**: Changing cursor encoding breaks all existing cursors clients hold. Always support old cursor formats during transitions.
- **Error format changes**: Changing error response structure breaks client error handling. Version error formats alongside the API.
- **Implicit contracts**: Response ordering, null vs absent fields, and numeric precision are implicit contracts clients depend on. Changing any of these is a breaking change in practice.
- **Feature flags as versioning**: Using flags for temporary changes is fine. Using them as permanent version selectors creates unmaintainable branching.
