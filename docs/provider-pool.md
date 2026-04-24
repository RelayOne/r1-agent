# Multi-provider pool (`STOKE_PROVIDERS`)

R1 normally picks one LLM provider via `detectSmartDefaults()` — a
probe chain of LiteLLM -> claude binary -> codex binary -> `ANTHROPIC_API_KEY`.
That single provider services every role: workers (task dispatch),
reasoning (planning, judging), and reviewers (second-opinion gates).

The `STOKE_PROVIDERS` env var overrides that chain with a **per-role
provider pool**. Each role (worker / reasoning / reviewer) resolves
independently from the pool, so operators can mix providers — for
example Anthropic Claude for reasoning, Ollama for workers, Gemini
for reviewer second-opinions.

## Env var shape

`STOKE_PROVIDERS` is a JSON array of provider entries:

```json
[
  {
    "name": "anthropic-main",
    "url": "https://api.anthropic.com",
    "key": "sk-ant-...",
    "models": ["claude-sonnet-4-6", "claude-opus-4-6"],
    "role": "reasoning"
  },
  {
    "name": "ollama-local",
    "url": "http://localhost:11434",
    "key": "",
    "models": ["llama3:70b", "mistral"],
    "role": "worker"
  }
]
```

### Fields

| Field    | Required | Notes                                                                                     |
| -------- | -------- | ----------------------------------------------------------------------------------------- |
| `name`   | yes      | Stable identifier surfaced in logs. Must be unique within the pool.                       |
| `url`    | yes      | Base URL for the provider's API. Protocol is auto-detected from the URL (see below).      |
| `key`    | no       | API key. Empty string is valid for local endpoints like Ollama.                           |
| `models` | yes      | List of model IDs this provider serves. Must contain at least one non-blank entry.        |
| `role`   | yes      | One of `worker`, `reasoning`, `reviewer`, `any`.                                          |

### Protocol detection

The pool picks the Provider implementation from the URL:

- **OpenAI-compatible** (`NewOpenAICompatProvider`) when the URL
  contains one of: `openrouter.ai`, `api.openai.com`, `api.together.xyz`,
  `api.fireworks.ai`, `api.deepseek.com`, `generativelanguage.googleapis.com`,
  the Ollama default port `11434`, or the substring `/ollama`.
- **Anthropic Messages API** (`NewAnthropicProvider`) for everything
  else, including LiteLLM and custom proxies that speak the Anthropic
  wire format.

## Roles

| Role         | When used                                                                         |
| ------------ | --------------------------------------------------------------------------------- |
| `worker`     | Task dispatch — the workhorse that writes code and runs tools.                    |
| `reasoning`  | Planning, decomposition, judge passes, convergence loops.                         |
| `reviewer`   | Second-opinion / adversarial review at merge gates.                               |
| `any`        | Catch-all. An entry with `role: any` can serve any role when no exact match wins. |

### Resolution order

For each (role, model) lookup:

1. Find an entry where `role` matches exactly **and** `model` is in `models`.
2. If none, find an entry where `role == "any"` **and** `model` is in `models`.
3. Otherwise, return an error naming the role + model.

When the caller only cares about "whatever provider is configured for
role X", it gets the first entry whose role matches (or the first
`any` entry), along with the entry's first listed model.

## Complete example: Anthropic reasoning + Ollama workers

```bash
export STOKE_PROVIDERS='[
  {"name":"anthropic","url":"https://api.anthropic.com","key":"sk-ant-...","models":["claude-sonnet-4-6","claude-opus-4-6"],"role":"reasoning"},
  {"name":"ollama","url":"http://localhost:11434","key":"","models":["llama3:70b","mistral"],"role":"worker"}
]'

stoke repl
```

Worker dispatches go to Ollama; planning / review go to Anthropic.

## Behavior when unset

If `STOKE_PROVIDERS` is empty or unset, R1 falls back to
`detectSmartDefaults()` verbatim. The pre-S-6 probe chain runs,
one provider is picked, and every role uses that one provider.
This is the default experience and requires no config.

## Behavior when set but a role is missing

When the pool is configured but no entry serves the requested role
(and no `role: any` fallback exists), R1 exits with a fatal error
naming the role. Example:

```
STOKE_PROVIDERS: worker role not resolvable: provider pool: no entry for role=worker (and no role=any fallback)
```

This is deliberate: silently falling back to SmartDefaults would hide
the misconfiguration and produce surprising mixed behavior where some
roles came from the pool and others from the probe chain.

## Validation errors

The pool rejects malformed config at parse time, before any provider
is built. Surfaces a clear error on:

- Empty `STOKE_PROVIDERS` (but array literal `[]`): "expected at least one entry"
- Empty `name`, duplicate `name`, empty `url`, empty `models` list
- Unknown `role` (typos like `revieer` or `workers`)
- Blank model IDs inside the `models` list
- JSON that does not parse as an array of entries

## Interaction with existing flags

The pool sits **in front of** `detectSmartDefaults()` — when set, the
single-provider SmartDefaults path is not consulted for the REPL /
shell TUI entry points. Explicit command-line flags on `stoke sow`
(`--native-api-key`, `--native-model`, `--reasoning-model`, etc.) are
unaffected by the pool; those flags remain the most specific source
of provider configuration.
