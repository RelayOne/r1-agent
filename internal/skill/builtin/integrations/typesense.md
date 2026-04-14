# typesense

> Typesense: open-source search engine, Algolia alternative. Typo-tolerant, faceted, geo-search. Self-host or Typesense Cloud. Similar feature set to Meilisearch with different tradeoffs (Typesense has built-in HA clustering).

<!-- keywords: typesense, search, algolia alternative, full-text search, self-hosted search -->

**Official docs:** https://typesense.org/docs  |  **Verified:** 2026-04-14 (v28+).

## Setup + auth

Self-host: `docker run -p 8108:8108 -v/tmp/data:/data typesense/typesense:28.0 --api-key=xyz --data-dir=/data`
Cloud: sign up → cluster endpoint + API keys.

Header: `X-TYPESENSE-API-KEY: <key>`

## Collections (schema-first)

Unlike Meili, Typesense requires a schema:

```ts
import Typesense from "typesense";
const client = new Typesense.Client({
  nodes: [{ host: "localhost", port: 8108, protocol: "http" }],
  apiKey: "xyz",
});

await client.collections().create({
  name: "products",
  fields: [
    { name: "title", type: "string" },
    { name: "price", type: "float", facet: true },
    { name: "tags", type: "string[]", facet: true },
    { name: "created", type: "int64", sort: true },
  ],
  default_sorting_field: "created",
});
```

## Documents

```ts
await client.collections("products").documents().import([
  { id: "1", title: "Keyboard", price: 99.00, tags: ["mech"], created: 1700000000 },
], { action: "upsert" });
```

JSONL format for bulk. `action`: `create` / `upsert` / `update` / `emplace`.

## Search

```ts
const res = await client.collections("products").documents().search({
  q: "keybord",                    // typo tolerated
  query_by: "title",
  filter_by: "price:<100 && tags:=mech",
  sort_by: "created:desc",
  facet_by: "tags",
  per_page: 20,
});
```

## Scoped API keys (multi-tenant)

Generate a child key that embeds a permanent filter:

```ts
const key = client.keys().generateScopedSearchKey(
  SEARCH_ONLY_KEY,
  { filter_by: "tenantId:=42", expires_at: Math.floor(Date.now()/1000) + 3600 },
);
// give `key` to frontend; user can only see tenant 42 docs
```

Safe to expose scoped keys to browsers.

## Vector / semantic search

```ts
fields: [
  { name: "embedding", type: "float[]", num_dim: 1536 },
]
// then
search({ q: "*", vector_query: "embedding:([0.1,0.2,...], k:10)" })
```

Or use built-in embedders (OpenAI/TF/PaLM) so Typesense embeds on write + query:

```ts
{ name: "embedding", type: "float[]", embed: { from: ["title","desc"], model_config: { model_name: "openai/text-embedding-3-small", api_key: "..." } } }
```

## Common gotchas

- **Schema is strict**: unknown fields in documents are rejected unless you mark field as `optional` or use `enable_nested_fields`.
- **ID field is always a string** — even if you store numeric IDs, coerce to string on write.
- **API key permission tiers**: master key (full), "search-only" key for clients, scoped keys derived from either.
- **Sorting requires `sort: true` or numeric type with `default_sorting_field`** — missing this returns "not a valid sorting field" errors.
- **Cluster writes**: single leader, all writes go to one node, replicated. Reads fan out. Use the nearest-node option in clients for lower latency.

## Key reference URLs

- API reference: https://typesense.org/docs/latest/api/
- Search parameters: https://typesense.org/docs/latest/api/search.html
- Scoped API keys: https://typesense.org/docs/latest/api/api-keys.html#generate-scoped-search-key
- Vector search: https://typesense.org/docs/latest/api/vector-search.html
