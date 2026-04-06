# graphql-patterns

> GraphQL API design, resolver patterns, performance, and federation for microservices

<!-- keywords: graphql, apollo, relay, schema, resolver, subscription, federation -->

## Critical Rules

1. **Design the schema for the client, not the database.** GraphQL types are not ORM models. Shape the API around what the UI needs to render.

2. **Every list field must be paginated.** Unbounded lists kill performance. Use cursor-based pagination (Relay connection spec) for any field that can grow.

3. **Never expose internal IDs without wrapping.** Use opaque global IDs (`base64("TypeName:123")`). This enables caching, federation, and prevents clients from guessing IDs.

4. **Batch all data loading.** Every resolver that fetches data must use DataLoader or equivalent. Without batching, nested queries cause exponential database calls.

5. **Limit query complexity.** Malicious or careless clients can craft deeply nested queries that crash the server. Enforce depth limits and complexity scoring.

## Schema Design

### Schema-First vs Code-First
- **Schema-first (SDL):** Write `.graphql` files, generate resolvers. Better for team collaboration and API review.
- **Code-first:** Define schema in code (Nexus, TypeGraphQL, gqlgen). Better for type safety and refactoring.
- Pick one and be consistent. Schema-first is recommended for larger teams.

### Naming Conventions
- Types: `PascalCase`. Fields: `camelCase`. Enums: `SCREAMING_SNAKE`. Mutations: verb-first (`createUser`). Input types: suffix with `Input`.

### Nullable by Default
- Fields are nullable unless you are certain they will always have a value.
- Non-null (`!`) is a contract: if the resolver throws, the null propagates up to the nearest nullable parent, potentially nullifying large portions of the response.

## Resolver Patterns

### DataLoader for N+1 Prevention
- DataLoader collects all IDs requested in a single tick, then issues one batched query.
- Create a new DataLoader instance per request (never share across requests to avoid cache leaks).
- Use the `maxBatchSize` option to avoid oversized queries.

### Authentication and Authorization
- Authenticate in middleware: parse JWT/session, attach user to context.
- Authorize in resolvers or directives: `@auth(role: ADMIN)` or check `context.user` in each resolver.
- Field-level authorization: some fields visible only to certain roles. Use schema directives or resolver middleware.
- Never trust client-supplied variables for access control decisions.

### Error Handling
- Use the `errors` array for client-actionable errors, not for logging.
- Include an error `code` (enum) and human-readable `message`. Extensions for machine-readable details.
- Partial data is valid in GraphQL: a query can return data for some fields and errors for others. Design clients to handle this.
- Avoid throwing generic errors. Map domain exceptions to specific GraphQL error codes.

## Pagination (Relay Connection Spec)

- Use the connection pattern: `UserConnection { edges { node, cursor }, pageInfo { hasNextPage, endCursor } }`.
- Cursors are opaque strings. Encode the sort key, not the offset.
- Support both forward (`first`/`after`) and backward (`last`/`before`) pagination.
- `totalCount` is optional and can be expensive. Only include if clients need it.

## Subscriptions

- Use for real-time features: chat messages, notifications, live dashboards.
- Transport: WebSocket (graphql-ws protocol, not the deprecated subscriptions-transport-ws).
- Keep subscription payloads small. Clients can refetch full data via queries.
- Server-side filtering: only push events the subscriber cares about. Don't filter on the client.
- Handle reconnection gracefully: clients should re-subscribe and fetch missed events.

## Federation

- Split a single graph across multiple services. Each service owns its types and resolvers.
- Use `@key` directive to define entity lookup fields across services.
- The gateway composes schemas and routes queries to the right service.
- Avoid circular dependencies between federated services.
- Each subgraph must be independently deployable and testable.

## Performance

### Query Complexity Analysis
- Assign a cost to each field (1 for scalars, 10 for relations, multiplied by pagination limit).
- Reject queries exceeding a maximum complexity score (e.g., 1000).
- Enforce a maximum depth limit (e.g., 10 levels) to prevent deeply nested attacks.

### Persisted Queries
- Clients send a hash instead of the full query string. Server looks up the query by hash.
- Reduces bandwidth, prevents arbitrary queries in production, and enables CDN caching.
- Use automatic persisted queries (APQ): client sends hash, server requests full query on cache miss.

### Response Caching
- Cache at the resolver level with TTL per type. Use `@cacheControl` directives to annotate max-age per field.
- CDN caching works with persisted queries and GET requests for read-only operations.

## Common Gotchas

- **Over-fetching with fragments:** Spreading large fragments when you need two fields wastes bandwidth. Be intentional.
- **Mutation input validation:** Validate inputs in resolvers, not just with GraphQL type constraints. `String!` accepts empty strings.
- **Enums are breaking changes:** Adding an enum value is safe. Removing one breaks clients with exhaustive switches.
- **File uploads:** GraphQL is not great for file uploads. Use a separate REST endpoint or the multipart request spec.
- **Schema stitching is deprecated.** Use federation (Apollo Federation or similar) instead of schema stitching for combining graphs.
