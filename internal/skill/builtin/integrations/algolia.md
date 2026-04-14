# algolia

> Algolia hosted search: push records from your DB, serve sub-50ms results to users. Admin keys server-side (index management); search-only keys client-side (search queries).

<!-- keywords: algolia, search, full text search, instantsearch, algolia client, indexing -->

**Official docs:** https://www.algolia.com/doc  |  **Verified:** 2026-04-14 via web search.

## Application + index identifiers

- **Application ID** (`APP_ID`): safe to expose client-side.
- **Admin API key**: FULL access. Server-only, never in the browser bundle.
- **Search-only API key**: restricted to search queries. Safe for client-side.
- **Secured API key**: time-limited + filter-restricted derivative of search key — generate per-user for multi-tenant apps.

## Install + init (Node admin)

```bash
pnpm add algoliasearch
```

```ts
import { algoliasearch } from "algoliasearch";
const client = algoliasearch(APP_ID, ADMIN_API_KEY);
```

## Indexing (server-side)

```ts
// Push records
await client.saveObjects({
  indexName: "products",
  objects: [
    { objectID: "prod_1", name: "Blue widget", price: 19.99, category: "widgets" },
    { objectID: "prod_2", name: "Red gadget", price: 29.99, category: "gadgets" },
  ],
});

// Partial update (only changed attributes)
await client.partialUpdateObjects({
  indexName: "products",
  objects: [{ objectID: "prod_1", price: 17.99 }],
});

// Delete
await client.deleteObjects({ indexName: "products", objectIDs: ["prod_1"] });

// Clear whole index
await client.clearObjects({ indexName: "products" });
```

Object size limit: 100KB. Batch size recommendation: 1000-10000 objects per call. Use `saveObjects` for bulk creates/overwrites, `partialUpdateObjects` for deltas.

## Configuring relevance

```ts
await client.setSettings({
  indexName: "products",
  indexSettings: {
    searchableAttributes: ["name", "description", "category"],   // order matters — earlier = more weight
    attributesForFaceting: ["category", "filterOnly(price)"],
    customRanking: ["desc(popularity)", "desc(rating)"],
    // Typo tolerance, synonyms, stop words all configurable.
  },
});
```

Configure via dashboard for iteration, then export the config JSON into code for reproducibility.

## Search (client-side search-only key)

```ts
const client = algoliasearch(APP_ID, SEARCH_ONLY_KEY);
const { results } = await client.search({
  requests: [{
    indexName: "products",
    query: "blue wid",
    hitsPerPage: 20,
    facets: ["category"],
    filters: "price < 50",
  }],
});
// results[0].hits is the array of matching records
```

## InstantSearch (UI library)

For React: `react-instantsearch-hooks-web` (new) / `react-instantsearch-dom` (legacy). Wraps `searchClient` and gives `<SearchBox>`, `<Hits>`, `<RefinementList>`, etc.

## Secured API keys (multi-tenant)

Restrict a search key per-user so one user can't search another's data:

```ts
import { algoliasearch } from "algoliasearch";
const securedKey = algoliasearch(APP_ID, ADMIN_KEY).generateSecuredApiKey({
  parentApiKey: SEARCH_ONLY_KEY,
  restrictions: {
    filters: `tenant_id:${userTenantId}`,
    validUntil: Math.floor(Date.now() / 1000) + 3600,     // 1h
  },
});
// Send securedKey to the client for that user's session.
```

Filter must be encoded into every record (`tenant_id` attribute). Dashboard → Index → Configuration → Searchable attributes + attributesForFaceting.

## Replica indices (sort orders)

Create replicas of the primary index with different `customRanking` for alternate sorts (e.g. "sort by price asc"). Updates auto-propagate from primary.

## Common gotchas

- **Admin key in a client bundle = full data compromise**. Use search-only or secured keys only.
- **`objectID` must be unique**; batch saves overwrite existing records with the same ID.
- **Geographical region**: new apps pick a region; latency matters for search. EU + US clusters available.
- **Stop words off by default** — turn on in settings for better recall on natural-language queries.
- **Facet counts cost indexing time**; don't facet on every attribute.

## Key reference URLs

- REST API: https://www.algolia.com/doc/rest-api/search
- JavaScript client: https://www.algolia.com/doc/libraries/javascript/
- Indexing guide: https://www.algolia.com/doc/guides/sending-and-managing-data/send-and-update-your-data/
- Secured API keys: https://www.algolia.com/doc/guides/security/api-keys/how-to/generate-user-token/
- InstantSearch React: https://www.algolia.com/doc/guides/building-search-ui/getting-started/react/
