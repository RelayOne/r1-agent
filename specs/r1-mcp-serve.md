<!-- STATUS: done -->
<!-- CREATED: 2026-05-09 -->
<!-- BUILD_STARTED: 2026-05-09 -->
<!-- BUILD_COMPLETED: 2026-05-09 -->
<!-- DEPENDS_ON: cortex-core, agentic-test-harness -->
<!-- BUILD_ORDER: 100 -->

> **Built.** `r1 mcp serve` (no flags) now runs the stdio MCP JSON-RPC
> server. Cortex backend is workspace-only by default; `--no-cortex`
> opt-out and `--session-id` are wired; `R1_MCP_KEY` auth gate is
> active when set. See `cmd/r1/mcp_serve_runtime.go`,
> `cmd/r1/mcp_serve_runtime_test.go`, and the `WithAuthKey` /
> `ToolDefinitions` updates in `internal/mcp/r1_server.go`. The 4 open
> questions below were answered in implementation: (1) default
> workspace-only, (2) `--session-id` flag with PID-based default
> emitted to stderr, (3) `cmd/r1-mcp` kept distinct, (4) multi-client
> deferred. The 5-task plan below is preserved as historical record;
> in practice TASK-2 (wire helpers) was unnecessary because
> `StokeServer.ServeStdio` already existed.

# r1-mcp-serve — Production runtime for `r1 mcp serve`

## Overview

`internal/mcp/` already ships the full r1.* tool catalog (38 tools across 10
categories per agentic-test-harness §12), including all 5 `r1.cortex.*`
handlers (`cortex_server.go`), the `WithCortex(CortexBackend)` injection
point on `StokeServer` (`r1_server.go:153`), and end-to-end tests
(`cortex_handlers_test.go`). What is missing is the production runtime:
`cmd/r1/mcp.go:104-128` (`runMCPServe`) returns
`"server back-end not yet wired in this checkpoint"` when invoked without
`--print-tools`. External MCP clients can read the static catalog but
cannot call any tool.

This spec wires the runtime: a minimal stdio MCP JSON-RPC 2.0 server,
spawned in-process by `r1 mcp serve`, that constructs a `StokeServer`,
attaches a `*cortex.Cortex`, and routes incoming `tools/call` requests
through `StokeServer.HandleToolCall`. Out of scope: multi-client
transport, persistent attach to a long-running session, the legacy
`cmd/r1-mcp` binary's "4 Stoke primitive" surface (those keep their
existing transport).

## State today (verified 2026-05-09 on `origin/dev`)

- `internal/mcp/cortex_server.go` — 5 cortex handlers fully implemented
  with happy-path + error-path tests (`cortex_handlers_test.go`).
- `internal/mcp/r1_server.go:142-156` — `WithCortex(c CortexBackend) *StokeServer`
  attaches the cortex backend (currently exercised only by tests).
- `internal/mcp/r1_server_catalog.go:149-176` — all 5 r1.cortex.* tools
  advertised; total catalog is the 38 tools that `--print-tools`
  emits.
- `cmd/r1/mcp.go:113` — `runMCPServe` short-circuits when `--print-tools`
  is absent. **This file is the wiring site.**
- `cmd/r1-mcp/main.go` — separate binary, separate surface (4 Stoke
  primitive tools: invoke / verify / audit / delegate). Stays as-is.
  Not consolidated with `r1 mcp serve` because the surfaces are
  intentionally distinct (STOKE-023 framework wrappers vs r1.* tool
  catalog).
- `cmd/r1-acp/main.go` — separate binary, separate protocol (Agent
  Client Protocol). Stays as-is.

## Stack & Versions

- **Go 1.25.5** (`go.mod` line 3). No new direct deps.
- **Pure stdlib** for the runtime: `bufio`, `context`, `encoding/json`,
  `os`, `os/signal`, `sync`. Match `cmd/r1-mcp/main.go`'s framing
  conventions verbatim where applicable.
- **Existing internal deps reused**:
  - `internal/mcp` (StokeServer, R1ToolCatalog, HandleToolCall,
    WithCortex)
  - `internal/cortex` (NewCortex, default Lobe registration)
  - `internal/logging` (stderr-only — stdout is the JSON-RPC frame
    channel)

## Existing patterns to follow

- **Stdio JSON-RPC framing** — verbatim from `cmd/r1-mcp/main.go`
  (`bufio.Scanner` on stdin, one JSON object per line on stdout).
  Stdout reserved for RPC; logs go to stderr.
- **Signal handling** — `os/signal.Notify` on SIGINT/SIGTERM, drain
  in-flight handler, exit 0. Match `cmd/r1-acp/main.go`'s pattern.
- **API key auth** — `R1_MCP_KEY` env var. When set, every request
  must include `meta.r1_mcp_key` with a constant-time comparison.
  When unset, open local-dev mode. Match `cmd/r1-mcp/main.go`'s
  `STOKE_MCP_KEY` pattern.
- **MCP protocol surface** — the three required JSON-RPC methods only:
  - `initialize` — capability negotiation (return `serverInfo`,
    `protocolVersion`, empty `capabilities` for now)
  - `tools/list` — return `mcp.R1ToolCatalog()` projected into the
    MCP `tools` schema
  - `tools/call` — route `params.name` through
    `StokeServer.HandleToolCall(name, params.arguments)`; wrap result
    in MCP `content[]` with a single `text` item

## Design decisions

### D-1. Transport: stdio JSON-RPC 2.0 only (Phase 1)

One client per process, one process per `r1 mcp serve` invocation. WebSocket
and Unix-socket transports are deferred to a separate spec. Rationale:
parity with `cmd/r1-mcp` and `cmd/r1-acp`; MCP clients (Claude Desktop,
Zed, etc.) all speak stdio; multi-client is YAGNI for the current use
cases (per-session lint, dev-loop catalog inspection).

### D-2. Cortex source: ephemeral in-process by default, `--cortex-attach <session-id>` deferred

`r1 mcp serve` constructs a fresh `*cortex.Cortex` per invocation via
`cortex.NewCortex(cortex.Config{...})` with default Lobes registered.
Without an active session, the cortex is empty (no Notes, no Lobe
activity) — but the handlers still work (return empty Note lists,
publish writes to the empty Workspace, lobes_list returns the
registered defaults).

A future `--cortex-attach <session-id>` flag connects to a running
session's cortex via a Unix-socket peer protocol. Out of scope here;
flag value `--cortex-attach` reserves the namespace.

### D-3. Authentication: `R1_MCP_KEY` env var (single-token, optional)

When set, every `tools/call` request validates `params.meta.r1_mcp_key`
via `subtle.ConstantTimeCompare`. Mismatch returns JSON-RPC error
`-32000` ("unauthorized"). When unset, requests are unauthenticated
(local-dev mode). `tools/list` and `initialize` are always
unauthenticated so an MCP client can negotiate before checking
auth. Match `cmd/r1-mcp` exactly; rename only.

### D-4. Logging: stderr-only, no stdout pollution

Stdout is the JSON-RPC frame channel. Any `log.*` call to stdout
breaks framing. Wire `internal/logging` to stderr; document the
constraint at the top of the new wiring file.

### D-5. Lifecycle: handler runs synchronously per request, no concurrency

Per spec MCP semantics for stdio: requests are serial. No goroutine
fan-out per request. SIGINT/SIGTERM drains the current handler then
exits 0. Match `cmd/r1-mcp/main.go`.

### D-6. Catalog projection: reuse `R1ToolCatalog`, project into MCP tools schema

`mcp.R1ToolCatalog()` returns `[]mcp.ToolDefinition` (r1's internal
shape). The wiring layer projects each to the MCP wire shape:

```go
type mcpTool struct {
    Name        string          `json:"name"`
    Description string          `json:"description"`
    InputSchema json.RawMessage `json:"inputSchema"`
}
```

Existing `ToolDefinition.InputSchema` already carries the JSON
Schema; pass through unchanged.

## Public surface

### CLI flags (additions to `cmd/r1/mcp.go`)

| Flag | Default | Effect |
|---|---|---|
| `--print-tools` | false | (existing) emit catalog and exit; serve runtime not started |
| `--markdown` | false | (existing) only with `--print-tools` |
| **`--no-cortex`** | false | (NEW) skip `WithCortex` wiring; r1.cortex.* tools return "cortex backend not wired" — useful when caller does not want to spin up a Lobe runner |
| **`--session-id`** | `""` | (NEW) value passed to `cortex.Config.SessionID`; defaults to a generated UUID; required only when an external client must match a specific session id in `r1.cortex.*` calls |

### Environment variables

| Var | Required? | Effect |
|---|---|---|
| `R1_MCP_KEY` | optional | when set, validates `params.meta.r1_mcp_key` on every `tools/call`; `tools/list` and `initialize` unauthenticated |
| `R1_MCP_LOG_LEVEL` | optional | one of `debug|info|warn|error`; default `info`; stderr-only |

### Exit codes

| Code | Meaning |
|---|---|
| 0 | Normal exit (SIGINT/SIGTERM drain, EOF on stdin) |
| 1 | Bad CLI flag, malformed initial bootstrap, or fatal cortex construction error |
| 2 | Unauthorized when `R1_MCP_KEY` mismatch (only for the very first request — subsequent mismatches stay in-process and return JSON-RPC errors) |

## Tasks

### TASK-1 — Wire `runMCPServe` to start the stdio MCP server

File: `cmd/r1/mcp.go:104-128` (rewrite the `if !*printTools { return 1 }`
short-circuit).

MUST:
- Parse new flags `--no-cortex` and `--session-id`.
- Construct `mcp.NewStokeServer()` (or whatever the existing
  constructor is — verify by reading `internal/mcp/r1_server.go`
  before writing).
- If `!*noCortex`, construct `cortex.NewCortex(...)` and call
  `s.WithCortex(c)`. Default Lobes registered per
  `cortex.DefaultLobes()` if such a helper exists; otherwise
  enumerate the 6 spec-2 v1 Lobes inline.
- Run the stdio JSON-RPC loop. Read one line at a time, decode,
  dispatch, encode response, flush.
- Handle SIGINT/SIGTERM: cancel in-flight handler context, exit 0.

VERIFY:
- `go build ./cmd/r1` succeeds.
- New table test in `cmd/r1/mcp_serve_runtime_test.go` that pipes a
  3-message MCP exchange (`initialize` → `tools/list` →
  `tools/call r1.cortex.lobes_list`) through the server's stdin
  and asserts the response shapes.
- `go test ./cmd/r1/... -count=1 -run TestMCPServeRuntime` passes.

Effort: M.

### TASK-2 — MCP wire-format projection helpers

File: `internal/mcp/wire.go` (NEW, ~80 lines).

MUST:
- Define `type WireMessage struct{...}` for the JSON-RPC envelope
  (jsonrpc, id, method, params, result, error).
- Define `type WireError struct{Code int; Message string; Data any}`.
- Define `ProjectToolDefinition(td mcp.ToolDefinition) mcpTool` that
  emits the MCP-wire-format tool shape.
- Define `WrapResult(text string) map[string]any` that builds the
  `{"content":[{"type":"text","text":text}]}` MCP result envelope.
- Define standard error codes: `ErrParse=-32700`, `ErrMethod=-32601`,
  `ErrParams=-32602`, `ErrInternal=-32603`, `ErrUnauthorized=-32000`.

VERIFY:
- `go test ./internal/mcp/... -count=1 -run TestWire` passes with at
  least 4 subtests (parse error, method-not-found, success
  envelope, error envelope).

Effort: S.

### TASK-3 — Auth gate

File: `internal/mcp/auth.go` (NEW, ~40 lines).

MUST:
- `func CheckMCPKey(req WireMessage, expected string) error` — when
  `expected != ""`, requires `req.params.meta.r1_mcp_key` to match
  via `subtle.ConstantTimeCompare`.
- Apply only on `tools/call` (not `initialize`, not `tools/list`).
- Return `WireError{Code: ErrUnauthorized, Message: "unauthorized"}`
  on mismatch.

VERIFY:
- `go test ./internal/mcp/... -count=1 -run TestCheckMCPKey` covers
  empty-expected (always pass), match (pass), mismatch (fail).

Effort: S.

### TASK-4 — Lobe defaults helper

File: `internal/cortex/lobes/defaults.go` (NEW or extend existing).

MUST:
- `func DefaultLobes() []cortex.Lobe` returns the 6 v1 Lobes
  (memoryrecall, walkeeper, rulecheck, planupdate, clarifyq,
  memorycurator) plus AntiTruncLobe — matching the convention used
  by `cortex.NewFake(t)` if such a helper exists.
- Idempotent and stateless; constructable without external deps.

VERIFY:
- `go test ./internal/cortex/lobes/... -count=1 -run TestDefaultLobes`
  asserts len==7 and all kinds are populated.

Effort: S.

### TASK-5 — Documentation update

Files: `docs/AGENTIC-API.md` (regenerate via `make docs-agentic` after
TASK-1 lands), `docs/HOW-IT-WORKS.md` (add a section on
`r1 mcp serve`).

MUST:
- Update `cmd/r1/mcp.go`'s package doc to remove the stale
  "back-end not yet wired" sentence.
- Add `r1 mcp serve` to the `r1 --help` summary.

VERIFY:
- `make docs-agentic` runs clean.
- `r1 mcp serve --help` output includes new flags.

Effort: S.

## Acceptance criteria

- [ ] `r1 mcp serve` (no flags) starts a stdio MCP JSON-RPC server
- [ ] An MCP client can `initialize`, `tools/list`, and `tools/call`
      `r1.cortex.lobes_list` and receive a populated lobes array
- [ ] `r1.cortex.publish` followed by `r1.cortex.notes` round-trips
      a Note via the in-process Workspace
- [ ] `R1_MCP_KEY=foo r1 mcp serve` rejects calls without
      `params.meta.r1_mcp_key=foo`
- [ ] `--no-cortex` flag returns "cortex backend not wired" for all
      r1.cortex.* tools
- [ ] SIGINT exits 0 cleanly; in-flight handler completes
- [ ] `go build ./cmd/r1` ok; `go test ./cmd/r1/... ./internal/mcp/...`
      ok; `go vet ./...` ok

## Open questions for review

1. **Cortex Lobe defaults** — should `r1 mcp serve` start every Lobe
   (the full 7) or just the workspace-only subset (no LLM Lobes,
   so calls don't hit Anthropic API and don't need credentials)?
   Default could be `--lobes=workspace-only` to ship without
   credentials; full set behind a flag.

2. **Session-id semantics** — the cortex handlers' `session_id`
   arg gates against `c.SessionID()`. For an ephemeral
   `r1 mcp serve`, the session id is generated at startup. How
   does the external client discover it? Options:
   (a) `initialize` response includes `serverInfo.sessionId`
   (b) a new `r1.session.current` MCP tool returns it
   (c) require the client pass `--session-id` so it knows the value
   it set. Recommend (c) for explicitness; (a) as a fallback.

3. **Should `r1 mcp serve` exec into `cmd/r1-mcp` for the legacy
   surface?** Likely no — the surfaces are intentionally distinct.
   But we should document the relationship in `docs/HOW-IT-WORKS.md`
   so operators don't think the binaries duplicate work.

4. **Multi-client / persistent attach** — out of scope for this
   spec. A follow-up `r1-mcp-serve-attach` spec would add a
   Unix-socket transport and `--cortex-attach <session-id>` to
   connect to a running session's cortex. File a placeholder spec?

## Verification gate

`go build ./cmd/r1 && go test ./... && go vet ./...` — the standard
CI gate per `CLAUDE.md`. Anything red is a finding, not "pre-existing".
