# anthropic

> Anthropic Claude Messages API: content blocks, tool use, extended thinking, prompt caching. Use @anthropic-ai/sdk.

<!-- keywords: anthropic, claude, claude api, messages api, tool use, extended thinking, prompt caching, sonnet, opus, haiku -->

**Official docs:** https://platform.claude.com/docs  |  **Verified:** 2026-04-14 via web search.

## Base URL + auth

- REST base: `https://api.anthropic.com/v1/`
- Auth: `x-api-key: sk-ant-...` + `anthropic-version: 2023-06-01`
- SDKs: `@anthropic-ai/sdk` (Node/TS), `anthropic` (Python), `anthropic-go`.

## Messages API — core shape

Content is always an array of typed blocks. Do NOT send a bare string — the API accepts it but SDKs normalize to blocks regardless.

```ts
import Anthropic from "@anthropic-ai/sdk";
const client = new Anthropic();
const msg = await client.messages.create({
  model: "claude-sonnet-4-6",
  max_tokens: 1024,
  system: [{ type: "text", text: "You are concise.", cache_control: { type: "ephemeral" } }],
  messages: [
    { role: "user", content: "Summarize in 3 bullets" },
  ],
});
// msg.content is [{type:"text", text:"..."}] or tool_use blocks.
```

`max_tokens` is required. System prompt can be a string or array-of-blocks for cache control.

## Tool use

```ts
const msg = await client.messages.create({
  model: "claude-sonnet-4-6",
  max_tokens: 1024,
  tools: [{
    name: "lookup_city",
    description: "Get city details",
    input_schema: { type: "object", properties: { name: { type: "string" } }, required: ["name"] },
  }],
  messages: [{ role: "user", content: "Look up NYC" }],
});
// msg.stop_reason === "tool_use" means model wants to call a tool.
// msg.content contains tool_use blocks with { type, id, name, input }.
```

When the model emits tool_use, respond with a `role: "user"` message carrying `tool_result` blocks:

```ts
{
  role: "user",
  content: [{ type: "tool_result", tool_use_id: "toolu_abc", content: "result..." }],
}
```

## Extended thinking

Enable step-by-step reasoning before the final answer. Separate budget from `max_tokens`.

```ts
{
  thinking: { type: "enabled", budget_tokens: 8000 },
  ...
}
```

Response includes `thinking` (or `redacted_thinking`) content blocks before `text`. Stoke's collectModelText falls back to thinking when the model returns no `text` block — always use it rather than reading `content[0].text` directly.

## Prompt caching (big cost lever)

Mark stable prefixes with `cache_control: { type: "ephemeral" }`. Up to 4 breakpoints per request: commonly system + last tool definition + last 2 messages. Cache hits rebate 90% of input-token cost on 5-min TTL.

```ts
tools: [
  tool1, tool2,
  { ...toolN, cache_control: { type: "ephemeral" } }, // mark LAST tool
]
```

**Byte-prefix sensitivity:** any change before a breakpoint invalidates. Render prompts deterministically (stable key order, no timestamps in prefix).

## Streaming

```ts
const stream = await client.messages.create({ ..., stream: true });
for await (const event of stream) {
  if (event.type === "content_block_delta" && event.delta.type === "text_delta") {
    process.stdout.write(event.delta.text);
  }
}
```

## Model IDs (April 2026)

- `claude-opus-4-6` — flagship reasoning
- `claude-sonnet-4-6` — balanced workhorse
- `claude-haiku-4-5-20251001` — fast/cheap
- Legacy ids supported but may have deprecation notices.

## Key reference URLs

- Messages API: https://platform.claude.com/docs/en/api/messages
- Tool use: https://platform.claude.com/docs/en/agents-and-tools/tool-use/overview
- Extended thinking: https://platform.claude.com/docs/en/build-with-claude/extended-thinking
- Prompt caching: https://platform.claude.com/docs/en/build-with-claude/prompt-caching
- Models: https://platform.claude.com/docs/en/about-claude/models/overview
