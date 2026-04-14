# cohere

> Cohere: Chat, Rerank, Embed. Strong on enterprise-grade rerank (search relevance boost) and embedding for RAG. Bearer token auth.

<!-- keywords: cohere, rerank, embeddings, chat, rag, retrieval -->

**Official docs:** https://docs.cohere.com  |  **Verified:** 2026-04-14.

## Base URL + auth

- REST base: `https://api.cohere.com/v2/`
- Auth: `Authorization: Bearer <API_KEY>`
- SDKs: `cohere-ai` (Node), `cohere` (Python).

## Chat

```ts
import { CohereClient } from "cohere-ai";
const co = new CohereClient({ token: process.env.COHERE_API_KEY });

const resp = await co.chat({
  model: "command-a-03-2025",    // or command-r7b, command-r-plus-08-2024
  messages: [{ role: "user", content: "Explain RAG in 2 sentences" }],
});
console.log(resp.message?.content?.[0]?.text);
```

## Rerank (signature product — re-order search results by relevance)

```ts
const rerank = await co.v2.rerank({
  model: "rerank-english-v3.0",
  query: "best laptop for programming",
  documents: [
    "MacBook Pro M3 Max: ...",
    "ThinkPad X1 Carbon: ...",
    "Dell XPS 13: ...",
    // ...up to 1000 docs per call
  ],
  topN: 3,
});
// rerank.results: [{ index, relevanceScore }] in descending order
```

Typical flow: Algolia/BM25 returns 50 candidates → Cohere rerank → top 5 → LLM as RAG context. Rerank is the best-in-class "put the right docs at the top" lever.

## Embed (for vector search)

```ts
const { embeddings } = await co.embed({
  model: "embed-english-v3.0",
  texts: ["first doc", "second doc"],
  inputType: "search_document",    // or "search_query" for query-side embed
});
// embeddings[0] is a 1024-dim vector
```

Use `search_document` when indexing, `search_query` when searching — Cohere has asymmetric embedders so query-time vectors differ from document-time.

## Common gotchas

- **API v2 vs v1**: v2 is the current recommended path. v1 still works but doesn't get new features.
- **Rerank latency**: ~100ms for 20 docs. Put it AFTER your fast first-pass (BM25/vector) not replacing it.
- **Model IDs versioned**: `rerank-english-v3.0`, `rerank-multilingual-v3.0` — pick the one that matches corpus language.

## Key reference URLs

- Chat: https://docs.cohere.com/reference/chat
- Rerank: https://docs.cohere.com/reference/rerank
- Embed: https://docs.cohere.com/reference/embed
- Model list: https://docs.cohere.com/docs/models
