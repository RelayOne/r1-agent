# mistral

> Mistral AI: chat completions (Mistral Large/Medium/Small), embeddings, function calling. OpenAI-compatible shape. Strong European hosting story for EU-data residency.

<!-- keywords: mistral, mistral ai, mistral large, mistral medium, codestral, european llm -->

**Official docs:** https://docs.mistral.ai  |  **Verified:** 2026-04-14.

## Base URL + auth

- REST base: `https://api.mistral.ai/v1/`
- Auth: `Authorization: Bearer <API_KEY>`
- SDKs: `@mistralai/mistralai` (Node), `mistralai` (Python).

## Current models (2026)

- `mistral-large-latest` — flagship reasoning
- `mistral-medium-latest` — balanced
- `mistral-small-latest` — fast/cheap
- `codestral-latest` — code-specialized (autocomplete, fill-in-middle)
- `mistral-embed` — embedding

Aliases ending `-latest` track the newest stable version. Pin to a date-versioned ID in prod.

## Chat

```ts
import { Mistral } from "@mistralai/mistralai";
const client = new Mistral({ apiKey: process.env.MISTRAL_API_KEY });

const resp = await client.chat.complete({
  model: "mistral-large-latest",
  messages: [{ role: "user", content: "Hello" }],
  temperature: 0.3,
  maxTokens: 512,
});
console.log(resp.choices[0].message.content);
```

Streaming:

```ts
const stream = await client.chat.stream({ model, messages });
for await (const event of stream) process.stdout.write(event.data.choices[0].delta.content ?? "");
```

## Function calling (OpenAI-compat)

```ts
const resp = await client.chat.complete({
  model: "mistral-large-latest",
  messages,
  tools: [{ type: "function", function: { name: "lookup", parameters: {...} } }],
  toolChoice: "auto",
});
// resp.choices[0].message.toolCalls is populated when model calls
```

## Embeddings

```ts
const emb = await client.embeddings.create({
  model: "mistral-embed",
  inputs: ["chunk 1", "chunk 2"],
});
// emb.data[0].embedding is a 1024-float vector
```

## Codestral (code-specific)

```ts
// Fill-in-the-middle:
const resp = await client.fim.complete({
  model: "codestral-latest",
  prompt: "def fibonacci(n):",
  suffix: "    return fibonacci(n-1) + fibonacci(n-2)",
});
```

## Common gotchas

- **`mistral-large` vs alias**: pinning matters for prod; the alias moves when a new release ships.
- **EU residency**: Mistral hosts in Europe by default (good for GDPR). Azure-Mistral or Vertex-Mistral exist for customer-controlled regions.
- **Context: 128K tokens** on Large. Plenty for most RAG.

## Key reference URLs

- API reference: https://docs.mistral.ai/api/
- Chat: https://docs.mistral.ai/capabilities/completion/
- Function calling: https://docs.mistral.ai/capabilities/function_calling/
- Codestral: https://docs.mistral.ai/capabilities/code_generation/
