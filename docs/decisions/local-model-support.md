# Local-model support (seam note)

Status: decided (2026-07-09, honest-stack convergence, manifest 04-R1 §3.7)

## Decision

R1 adds **no special local-model portability layer**. Local-model support,
where wanted, is delivered as **ordinary multi-provider support only** — a
local model is just another OpenAI-compatible backend.

## Why nothing needs building

R1 already routes through `provider.OpenAICompatProvider`
(`internal/provider/anthropic.go`), which speaks the OpenAI
`/v1/chat/completions` shape. Ollama and vLLM both expose that exact API, so a
local model is reachable today by pointing an OpenAI-compatible provider at the
local endpoint (e.g. `http://localhost:11434/v1` for Ollama, or a vLLM server),
with no new mechanism. The 5-provider fallback chain
(`internal/model/router.go`) is the seam; a local backend slots into it like any
other provider.

## Boundary (do not cross)

- If local models are wanted: add an ordinary provider entry (OpenAI-compatible
  base URL), nothing more. One seam, no special mechanism.
- Do not introduce a bespoke decoding or model-portability subsystem. It is not
  needed and is out of scope for R1's role.
