# meilisearch

> Meilisearch: open-source typo-tolerant search engine. Simple REST API. Self-host or Meilisearch Cloud. Sub-50ms search on millions of documents.

<!-- keywords: meilisearch, search, full-text search, typo tolerant, self-hosted search -->

**Official docs:** https://www.meilisearch.com/docs  |  **Verified:** 2026-04-14 (v1.13+).

## Setup

Self-host: `docker run -p 7700:7700 -e MEILI_MASTER_KEY=your-key getmeili/meilisearch:latest`
Cloud: sign up → get host URL + master key.

## Auth

- **Master key**: full access. Never expose to clients.
- **API keys**: scoped (actions + indexes). Create via `POST /keys`.
- **Tenant tokens**: short-lived JWTs signed with an API key — embed user-specific filters. Use for multi-tenant clients.

```
Authorization: Bearer <API_KEY>
```

## Index + documents

```ts
import { MeiliSearch } from "meilisearch";
const client = new MeiliSearch({ host: "http://127.0.0.1:7700", apiKey: KEY });

// Create index (implicit on first add)
await client.index("movies").addDocuments([
  { id: 1, title: "Inception", year: 2010, genres: ["sci-fi"] },
  { id: 2, title: "The Matrix", year: 1999, genres: ["sci-fi", "action"] },
]);
```

`id` must be a primary key — string or int. Meilisearch infers it from a field named `id`/`uid` or you set it explicitly.

## Search

```ts
const res = await client.index("movies").search("matirx", {     // typo tolerated
  limit: 20,
  filter: ["year > 2000", "genres = sci-fi"],
  sort: ["year:desc"],
  attributesToHighlight: ["title"],
  facets: ["genres"],
});
// res.hits, res.facetDistribution, res.processingTimeMs
```

## Settings (searchable + filterable + sortable attributes)

Configure per-index — filter/sort attributes must be declared or queries fail:

```ts
await client.index("movies").updateSettings({
  searchableAttributes: ["title", "description"],
  filterableAttributes: ["year", "genres"],
  sortableAttributes: ["year", "rating"],
  rankingRules: ["words", "typo", "proximity", "attribute", "sort", "exactness"],
});
```

## Tenant tokens (multi-tenant)

Sign JWT with an API key, encoding `searchRules` that restrict what each tenant can see:

```ts
const token = client.generateTenantToken(apiKeyUid, {
  movies: { filter: "tenantId = 42" },
}, { apiKey: apiKey });
// give `token` to client; client uses it for searches — Meili enforces the filter
```

Never give raw API keys to browsers; always use tenant tokens for client-side search.

## Sync from your DB

Meili has no CDC. Push on writes: wrap your DB writes with `index.addDocuments([...])`, or run a periodic reindex job. Task queue is async — every call returns `{ taskUid }` you can poll via `client.getTask(taskUid)`.

## Common gotchas

- **`filter`/`sort` require the attribute to be declared** as filterable/sortable. Add to settings FIRST or search errors.
- **Tasks are async** — `addDocuments` returns immediately; docs aren't searchable until task reaches `succeeded`. Poll for sync tests.
- **Master key in client-side code = game over** — generate tenant tokens or use search-only API keys for browsers.
- **Default typo tolerance**: 1 typo on 5+ char words, 2 on 9+ char words. Tunable per-index via `typoTolerance` setting.
- **Embeddings (hybrid search)**: v1.6+ supports vector search via `embedders` setting, combining semantic + keyword relevance.

## Key reference URLs

- REST API: https://www.meilisearch.com/docs/reference/api/overview
- Search params: https://www.meilisearch.com/docs/reference/api/search
- Tenant tokens: https://www.meilisearch.com/docs/learn/security/tenant_tokens
- Hybrid search (vector): https://www.meilisearch.com/docs/learn/ai_powered_search/overview
