# openai

> OpenAI Chat Completions + Responses API, function/tool calling, streaming, embeddings. Use official SDKs (openai-node, openai-python).

<!-- keywords: openai, gpt, chatgpt, chat completions, responses api, function calling, tool calling, embeddings, gpt-5, gpt-4 -->

**Official docs:** https://platform.openai.com/docs  |  **Verified:** 2026-04-14 via web search.

## Base URL + auth

- REST base: `https://api.openai.com/v1/`
- Auth: `Authorization: Bearer sk-...`
- Organization header (optional, for multi-org accounts): `OpenAI-Organization: org-...`
- SDKs: `openai` npm package (TypeScript first-class), `openai` PyPI.

## Responses API (recommended for new builds, 2026+)

Unifies Chat Completions + Assistants + tool use into one endpoint. Use for all new agent-style builds.

```ts
import OpenAI from "openai";
const client = new OpenAI();
const resp = await client.responses.create({
  model: "gpt-5",
  input: "Summarize the attached transcript in 3 bullets",
  tools: [{ type: "function", function: fnSchema, strict: true }],
});
```

Responses API carries unified state across turns — pass `previous_response_id` rather than re-sending the history each call.

## Chat Completions API (still supported, widest ecosystem)

```ts
const c = await client.chat.completions.create({
  model: "gpt-5",
  messages: [
    { role: "system", content: "You are concise." },
    { role: "user", content: "2+2?" },
  ],
  tools: [{ type: "function", function: { name: "lookup", parameters: {...} } }],
  tool_choice: "auto",
});
```

Migration path: the Chat Completions API stays, but OpenAI recommends Responses for new projects.

## Function / tool calling

Pass `tools: [{ type: "function", function: { name, description, parameters: jsonSchema, strict: true } }]`. `strict: true` enforces exact schema compliance — the model's `arguments` will parse as the declared shape or the request fails. Always prefer strict mode.

Response shape when model calls a tool:

```json
{
  "choices": [{
    "message": {
      "role": "assistant",
      "content": null,
      "tool_calls": [{
        "id": "call_abc",
        "type": "function",
        "function": { "name": "lookup", "arguments": "{\"city\":\"NYC\"}" }
      }]
    }
  }]
}
```

Echo the tool result back as a `role: "tool"` message with matching `tool_call_id`.

## Streaming

```ts
const stream = await client.chat.completions.create({ ..., stream: true });
for await (const chunk of stream) {
  process.stdout.write(chunk.choices[0]?.delta?.content ?? "");
}
```

SSE-style `data: {json}\ndata: [DONE]\n\n` under the hood; the SDK handles parsing.

## Embeddings

```ts
const emb = await client.embeddings.create({
  model: "text-embedding-3-large",
  input: ["chunk one", "chunk two"],
});
// emb.data[0].embedding is a 3072-float vector (3-large); 1536 for 3-small.
```

## Rate limits + errors

- 429 with `Retry-After` header on quota exhaustion — respect it, exponential backoff.
- 500/503: retry 3x with jitter, then surface to caller.
- 400 `invalid_request_error` → log body, don't retry (bad prompt shape).

## Key reference URLs

- Responses API: https://platform.openai.com/docs/guides/responses
- Chat Completions: https://platform.openai.com/docs/api-reference/chat
- Function calling: https://platform.openai.com/docs/guides/function-calling
- Migrate to Responses: https://platform.openai.com/docs/guides/migrate-to-responses
- Streaming: https://platform.openai.com/docs/api-reference/streaming
