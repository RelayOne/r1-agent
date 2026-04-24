# MCP Security: Prompt-Injection Responsibility Boundary

R1 sits on both sides of the Model Context Protocol wire:

- **Inbound** — R1 is an MCP **client** that calls third-party MCP tool servers via `internal/mcp.StdioClient.CallTool`. Whatever those servers return (filesystem contents, DB rows, HTTP responses, scraped HTML) is attacker-influenced text.
- **Outbound** — R1 is an MCP **server** via `internal/mcp/stoke_server.go` (the `stoke mcp-serve-stoke` path) and `cmd/stoke-mcp/` (the standalone primitives binary). The responses those servers emit can contain repo content, user SOW text, build logs, and agent output.

Prompt-injection defenses live at different layers on each side. This document fixes the contract.

---

## Inbound (R1 as MCP client)

**Rule.** Every `CallTool` result that flows into an LLM prompt MUST be routed through `agentloop.SanitizeToolOutput` before it reaches the model. Results that are only parsed by code, returned to a non-LLM HTTP client, or written to a log do NOT need sanitization.

**Why the caller, not the client.** `CallTool` returns `json.RawMessage`. Sanitizing in the client would corrupt structured payloads that non-LLM consumers depend on. The caller is the only layer that knows whether the bytes are destined for a model.

**Task 2 status.** `agentloop.SanitizeToolOutput` will be introduced by Track A Task 2. Until it lands, call sites that feed an LLM must leave a `TODO(mcp-sanitization-audit)` comment referencing this doc and the missing helper.

**Per-call-site audit.** Every `CallTool` invocation (including the method definition itself) is tagged with a one-line comment of the form:

```go
// mcp-sanitization-audit: result consumed by LLM — sanitized via agentloop.SanitizeToolOutput.
// mcp-sanitization-audit: result consumed by code — no sanitization needed.
```

**Maintenance check.** The audit is greppable. Run:

```bash
grep -rn "mcp-sanitization-audit:" internal/ cmd/ --include='*.go'
```

The number of audit lines MUST be greater than or equal to the number of `CallTool` sites:

```bash
grep -rn "CallTool" internal/ cmd/ --include='*.go' | grep -v _test.go
```

If a reviewer sees a `CallTool` call with no matching `mcp-sanitization-audit:` marker within a few lines, the patch should be blocked.

**Current snapshot (2026-04-20).** There is exactly one `CallTool` site: the method definition on `*StdioClient` in `internal/mcp/client.go`. Zero internal packages call it today. External callers (other tracks, downstream embedders) MUST add the marker when they add a call.

---

## Outbound (R1 as MCP server)

**Rule.** R1's MCP servers (`internal/mcp/stoke_server.go` and `cmd/stoke-mcp/`) return tool results **verbatim**. They do NOT strip, escape, or rewrite attacker-influenced substrings before sending them to the MCP client.

**Why not pre-sanitize outbound.**

1. **Non-LLM consumers need raw data.** Dashboards, CI runners, structured-output parsers, and agent framework adapters (LangGraph / Vercel AI SDK / CrewAI) all consume these payloads programmatically. Inserting sanitization markers would break JSON shape assumptions and corrupt byte-exact diffs and log contents.
2. **LLM consumers disagree on defenses.** Anthropic's Claude pipeline, OpenAI's tool-use pipeline, and self-hosted frameworks each prefer different prompt-injection strategies (tag wrapping, entity encoding, delimiter replacement, none). The MCP server cannot know which strategy its consumer wants, and guessing wrong is worse than doing nothing.
3. **MCP is not exclusively an LLM protocol.** The spec is neutral on payload semantics; sanitizing at the server would overreach.

**What downstream MCP consumers MUST do.** If you connect to an R1 MCP server and intend to pipe its results into an LLM prompt, apply your own prompt-injection defenses at the consumption site. Treat every field as untrusted text. In particular: build logs (`stoke_get_mission_status`), verify/audit output (`stoke-mcp` primitives), and the TrustPlane pass-through layer can all carry attacker text from the repo, SOW, or upstream tool.

---

## Related docs

- `docs/security/prompt-injection.md` — the cross-product prompt-injection playbook. **Not yet present.** Track A Task 25 will create it.
- `internal/mcp/client.go` — `CallTool` docstring with the inbound sanitization note.
- `internal/mcp/stoke_server.go` — package doc with the outbound policy.
- `cmd/stoke-mcp/main.go` — package doc with the outbound policy.

---

## Maintenance checklist

On every PR that touches `internal/mcp/` or adds an MCP caller anywhere in the tree:

1. `grep -rn "CallTool" internal/ cmd/ --include='*.go' | grep -v _test.go`
2. `grep -rn "mcp-sanitization-audit:" internal/ cmd/ --include='*.go'`
3. Confirm every hit from step 1 has a matching marker from step 2 within the surrounding context.
4. If a new caller routes results into an LLM, confirm the marker says "consumed by LLM — sanitized via agentloop.SanitizeToolOutput" and that the code actually calls that helper (or carries a `TODO(mcp-sanitization-audit)` until Task 2 lands).
