# gemini

> Google Gemini API (Generative Language). Native REST at `generativelanguage.googleapis.com` + OpenAI-compatibility shim at `/v1beta/openai/chat/completions`. Key-based auth.

<!-- keywords: gemini, google ai, google generative ai, gemini api, genai, gemini 3 -->

**Official docs:** https://ai.google.dev/gemini-api/docs  |  **Verified:** 2026-04-14.

## Auth + endpoints

- Native base: `https://generativelanguage.googleapis.com/v1beta/`
- OpenAI-compat base: `https://generativelanguage.googleapis.com/v1beta/openai/`
- Auth: `x-goog-api-key: AIza...` header (native) or `Authorization: Bearer AIza...` (OpenAI-compat).
- SDKs: `@google/genai` (Node), `google-genai` (Python).

## Current model IDs (April 2026)

- `gemini-3.1-pro-preview` — flagship (supersedes retired `gemini-3-pro-preview`, shut down 2026-03-09)
- `gemini-2.5-pro` — stable GA (slated for June 2026 deprecation)
- `gemini-2.5-flash` — stable fast tier
- `gemini-2.5-flash-lite` — budget option
- `gemini-3.1-flash-lite-preview` — newer preview budget tier

Check https://ai.google.dev/gemini-api/docs/changelog before launching; Google rotates preview IDs every few months.

## Native API: generate content

```ts
import { GoogleGenAI } from "@google/genai";
const genai = new GoogleGenAI({ apiKey: process.env.GEMINI_API_KEY! });

const resp = await genai.models.generateContent({
  model: "gemini-3.1-pro-preview",
  contents: [{ role: "user", parts: [{ text: "Summarize War and Peace in 3 bullets" }] }],
  config: { maxOutputTokens: 512, temperature: 0.3 },
});
console.log(resp.text);
```

## OpenAI-compatible path (drop-in for OpenAI code)

```ts
import OpenAI from "openai";
const client = new OpenAI({
  apiKey: process.env.GEMINI_API_KEY!,
  baseURL: "https://generativelanguage.googleapis.com/v1beta/openai/",
});
const resp = await client.chat.completions.create({
  model: "gemini-3.1-pro-preview",
  messages: [{ role: "user", content: "Hello" }],
});
```

## Tool / function calling

```ts
const resp = await genai.models.generateContent({
  model: "gemini-3.1-pro-preview",
  contents,
  config: {
    tools: [{ functionDeclarations: [{
      name: "lookup_weather",
      description: "Get weather for a city",
      parameters: { type: "object", properties: { city: { type: "string" } }, required: ["city"] },
    }]}],
  },
});
// resp.functionCalls is populated when model wants to call
```

## Streaming

```ts
const stream = await genai.models.generateContentStream({ model, contents });
for await (const chunk of stream) process.stdout.write(chunk.text ?? "");
```

## Common gotchas

- **Preview model IDs change**: pin to a specific version in prod. The `-preview` suffix signals Google can retire it.
- **Response shape differs from OpenAI** (parts array vs message.content string) unless you use the OpenAI-compat endpoint.
- **Long context (1M+ tokens)**: available on `-pro` models; billing is per-token as usual.
- **Safety filters**: responses may be filtered (`promptFeedback.blockReason`); handle the empty-response case explicitly.

## Key reference URLs

- Models list: https://ai.google.dev/gemini-api/docs/models
- Generate content: https://ai.google.dev/gemini-api/docs/text-generation
- Function calling: https://ai.google.dev/gemini-api/docs/function-calling
- OpenAI compat: https://ai.google.dev/gemini-api/docs/openai
- Changelog: https://ai.google.dev/gemini-api/docs/changelog
