<!-- STATUS: done -->
<!-- CREATED: 2026-04-20 -->
<!-- BUILD_STARTED: 2026-04-21 -->
<!-- BUILD_COMPLETED: 2026-04-21 -->
<!-- DEPENDS_ON: spec-1 (truthfulness contract), spec-2 (event emission), spec-3 (executor tool layer) -->
<!-- BUILD_ORDER: 8 -->

# MCP Client — Implementation Spec

## Overview

Stoke ships a fully-wired MCP **server** (`internal/mcp/codebase_server.go`, `stoke_server.go`) but its **client** side (`internal/mcp/client.go`) is an abstract stdio stub with no SSE, no Streamable-HTTP, no auth, no discovery, no circuit breaker, and no wiring into the worker tool set (RT-STOKE-SURFACE §10). This spec finishes the client: real stdio + Streamable-HTTP / SSE transports, tool discovery, dynamic tool-name prefixing, env-var auth with redaction, per-server circuit breaker, bus + streamjson event emission per spec-2, and injection into `internal/engine/native_runner.go` so each worker can invoke `mcp_github_*`, `mcp_linear_*`, `mcp_slack_*` (and any custom server) from the agentloop tool list. Every MCP call is covered by the truthfulness contract from spec-1 and verified by the content-faithfulness judge (`internal/plan/content_judge.go`).

## Stack & Versions

- Go 1.22+ (existing `go.mod`)
- MCP protocol `2025-11-25` with backward-compat negotiation for `2025-06-18`, `2025-03-26`, `2024-11-05` (library handles this)
- Library: `github.com/mark3labs/mcp-go v0.42+` (selection rationale below)
- JSON-RPC 2.0 wire format (already used by existing `internal/mcp/client.go`)
- Transports: **stdio** (subprocess) and **Streamable-HTTP** (includes SSE back-compat); pure-SSE dial-only when a server advertises only the legacy endpoint
- Bus: `internal/bus/bus.go`; streamjson: `internal/streamjson/emitter.go`

## Library selection

### Candidates evaluated (accessed 2026-04-20)

| Library | Spec parity | Transports | Auth | Maintenance | License | Notes |
|---|---|---|---|---|---|---|
| `github.com/mark3labs/mcp-go` | 2025-11-25 + back-compat 2025-06-18 / 2025-03-26 / 2024-11-05 | stdio, Streamable-HTTP, SSE, in-process | `NewOAuthSSEClient` + `NewOAuthStreamableHttpClient` with PKCE; custom bearer via `WithHTTPHeaders` | Top-ranked MCP Go framework (score 96.09, rank #4), 2.85× dependents of the official SDK | **MIT** | Mature, opinionated, less boilerplate |
| `github.com/modelcontextprotocol/go-sdk` | Official (Google-maintained) | stdio, Streamable-HTTP, SSE | Bearer + custom round-trippers | Published early 2026, 96 contributors, 87% close rate, younger | Dual: **MIT** (existing) + **Apache-2.0** (new contributions) | Spec-parity guarantees; less opinionated |
| `github.com/riza-io/mcp-go`, `dwrtz/mcp-go` | Older draft | stdio only | none | Low activity | MIT | Not viable |

### Decision: `github.com/mark3labs/mcp-go`

- **Apache-2.0 compatibility**: Stoke is Apache-2.0; MIT dependencies are permissively compatible for inclusion.
- **Transports we need on day one**: stdio + Streamable-HTTP + SSE all ship out-of-box.
- **OAuth / bearer**: `NewOAuthStreamableHttpClient` gives PKCE for free; env-var bearer uses `WithHTTPHeaders(map[string]string{"Authorization": "Bearer "+tok})`.
- **Reconnect**: SSE transport exposes `SetConnectionLostHandler()` which we wrap in our circuit-breaker.
- **Upgrade path**: if official SDK overtakes on spec parity, the `Client` interface below is transport-library agnostic — swap is localized to one file (`internal/mcp/transport_*.go`).

Sources:
- mcp-go repo: https://github.com/mark3labs/mcp-go (accessed 2026-04-20)
- Official Go SDK: https://github.com/modelcontextprotocol/go-sdk (accessed 2026-04-20)
- Framework comparison: https://agentrank-ai.com/blog/mcp-server-framework-comparison/ (accessed 2026-04-20)
- Streamable-HTTP replaces SSE (spec `2025-03-26`): https://modelcontextprotocol.io/specification/2025-03-26/basic/transports (accessed 2026-04-20)
- SSE deprecation rationale: https://blog.fka.dev/blog/2025-06-06-why-mcp-deprecated-sse-and-go-with-streamable-http/ (accessed 2026-04-20)
- `.well-known/mcp.json` discovery: https://www.ekamoira.com/blog/mcp-server-discovery-implement-well-known-mcp-json-2026-guide (accessed 2026-04-20)
- 2026 MCP roadmap: https://blog.modelcontextprotocol.io/posts/2026-mcp-roadmap/ (accessed 2026-04-20)

## Existing Patterns to Follow

- Current client stub: `internal/mcp/client.go:1-80+` (`StdioClient`, `ServerConfig`, JSON-RPC structs)
- MCP server reference (for symmetry of types): `internal/mcp/codebase_server.go`, `internal/mcp/stoke_server.go`
- Worker tool list injection point: `internal/engine/native_runner.go` (agentloop tool registration)
- Policy YAML parser: `internal/config/` (existing `stoke.policy.yaml` loader)
- Bus event shapes: `internal/bus/bus.go:31-69`; new `mcp.*` types follow the same `Type + Data map[string]any` convention
- StreamJSON subtypes: `_stoke.dev/mcp_*` extensions on `EmitSystem` / `EmitUser` per spec-2
- Truthfulness-contract injection: `cmd/r1/sow_native.go:3906-3918` (spec-1 established this slot)
- Content judge: `internal/plan/content_judge.go:54-154`
- Trust-level plumbing (when spec-10 lands): `internal/rbac/`

## Library Preferences

- `github.com/mark3labs/mcp-go` for transport + JSON-RPC; do NOT reimplement on top of the existing `StdioClient` stub — migrate its callers (if any) to the new `Client` interface and delete the legacy struct.
- stdlib `net/http` for `.well-known/mcp.json` probes (do not add a separate HTTP library).
- stdlib `context` for all timeouts; no `time.AfterFunc` goroutines.
- Secret redaction: extend existing `internal/logging` redactor (no new lib).
- Circuit breaker: inline struct with `sync.Mutex`; do NOT pull `sony/gobreaker` (6 lines of logic).

## Data Models

### `internal/mcp.ServerConfig` (rewritten)

| Field | Type | Constraints | Default |
|-------|------|-------------|---------|
| `Name` | string | `[a-z][a-z0-9_-]{0,31}`; unique | required |
| `Transport` | string | one of `stdio`, `streamable_http`, `sse` | `stdio` |
| `URL` | string | required if `Transport != stdio`; HTTPS in production (`http://localhost*` allowed for dev) | `""` |
| `Command` | []string | required if `Transport == stdio`; first element is argv[0] | `nil` |
| `AuthEnv` | string | env-var name holding bearer token / API key; never logged | `""` |
| `AuthScheme` | string | `bearer` (default), `apikey_header`, `none` | `bearer` |
| `AuthHeader` | string | header name when `AuthScheme=apikey_header` (e.g. `X-Api-Key`) | `""` |
| `ConnectTimeout` | duration | cap 30s | `10s` |
| `CallTimeout` | duration | per-tool-call; cap 120s | `30s` |
| `MaxConcurrent` | int | per-server in-flight cap | `4` |
| `CircuitThreshold` | int | consecutive failures before open | `5` |
| `CircuitCooldown` | duration | time circuit stays open | `60s` |
| `TrustLevel` | int | minimum worker trust level required (policy-gated); 0 = public | `2` |
| `Enabled` | bool | disable without deleting | `true` |
| `AllowTools` | []string | if set, whitelist of tool names the server may expose (post-prefix) | `nil` (all) |
| `DenyTools` | []string | always filtered out | `nil` |

### `internal/mcp.Client` (new interface)

```go
type Client interface {
    Connect(ctx context.Context) error
    ListTools(ctx context.Context) ([]Tool, error)
    CallTool(ctx context.Context, name string, args map[string]any) (*ToolResult, error)
    Close() error
    Name() string
    Healthy() bool
}

type Tool struct {
    Name        string          // server-local name, e.g. "create_issue"
    Description string
    InputSchema json.RawMessage // JSON Schema from ListTools response
}

type ToolResult struct {
    Content   []Content // MCP content parts (text | image | resource_link)
    IsError   bool
    Truncated bool
    Meta      map[string]any // server-sent metadata (excluded from prompts)
}
```

Concrete implementations: `stdioClient`, `httpClient` (Streamable-HTTP; SSE back-compat is an auto-detected codepath inside it), each backed by `mark3labs/mcp-go`.

### `internal/mcp.Registry` (new)

Loads `mcp_servers:` from `stoke.policy.yaml`, instantiates one `Client` per server, lazily connects on first use, and exposes:

```go
func (r *Registry) AllTools(ctx) ([]NamedTool, error) // flattened, prefixed
func (r *Registry) Call(ctx, prefixedName, args) (*ToolResult, error)
func (r *Registry) Health() map[string]Status
func (r *Registry) Close() error
```

### `policy.yaml` schema (full example)

```yaml
mcp_servers:
  - name: github
    transport: streamable_http
    url: https://api.githubcopilot.com/mcp/
    auth_env: GITHUB_MCP_TOKEN
    auth_scheme: bearer
    connect_timeout: 10s
    call_timeout: 45s
    max_concurrent: 4
    circuit_threshold: 5
    circuit_cooldown: 60s
    trust_level: 3        # create_pr, create_issue require reviewer stance
    allow_tools: [create_issue, list_issues, get_pull_request, search_code]
  - name: linear
    transport: stdio
    command: [linear-mcp-server, --team, ENG]
    auth_env: LINEAR_API_KEY
    auth_scheme: apikey_header
    auth_header: X-Api-Key
    call_timeout: 20s
    trust_level: 2
  - name: slack
    transport: sse              # legacy server that still advertises SSE-only
    url: https://slack-mcp.example.com/sse
    auth_env: SLACK_MCP_TOKEN
    trust_level: 3
    deny_tools: [post_message_as_user]
  - name: custom
    transport: stdio
    command: [./bin/my-mcp-server]
    auth_env: ""                # no auth, local process
    trust_level: 1
```

### Tool-name mapping

```
prefixed_name = "mcp_" + server.Name + "_" + tool.Name
```

Rules (enforced in `Registry.AllTools`):
1. Both segments lower-snake_case; tool names from server are normalized (`-` → `_`, `.` → `_`, reject anything that still doesn't match `[a-z][a-z0-9_]*`).
2. Collisions across servers are impossible (server name is part of the prefix).
3. Collisions **within** a server are detected on `ListTools` and produce a `mcp.call.error` subtype=`duplicate_tool` + the later entry is dropped.
4. Final name length ≤ 64 chars (agentloop tool-name cap); longer names are truncated to `mcp_<server>_<hash8>` and the original recorded in event metadata.
5. Tool descriptions are passed through verbatim but truncated to 1024 chars for the agentloop list and prefixed with `[mcp:<server>]`.

## Transport Details

### stdio

- `Client.Connect` forks the configured `Command`; stdin/stdout is JSON-RPC framed per MCP spec.
- stderr is captured line-buffered into the bus as `mcp.server.log` (level=warn) and redacted for secrets.
- Process lifecycle: `os/exec` with `Setpgid: true` (match decision #7 in CLAUDE.md) so `Close()` reaps the tree via SIGTERM → SIGKILL after 5s.
- Reconnect: stdio is connection-local; death → circuit breaker trips, no auto-respawn until cooldown elapses.

### Streamable-HTTP (preferred for remote servers)

- Single `POST` endpoint per server URL; server may respond with `Content-Type: text/event-stream` for streamed tool output or `application/json` for a one-shot response (per spec `2025-03-26`).
- On `Connect`, send `initialize` → `initialized` notifications; record `Mcp-Session-Id` header and resend on every subsequent request.
- Idle connections are released back to the pool; `max_concurrent` gates in-flight calls via a buffered semaphore.
- Retries: one automatic retry on HTTP 502 / 503 / 504 / network error with 250ms jitter; subsequent failures increment the circuit-breaker counter (no retry storms).
- Resumption: if a streamed response drops mid-flight, the client emits `mcp.call.error` subtype=`stream_truncated` and **does not** auto-resume (resumability flag is off-by-default; revisit in spec-10).

### SSE (legacy)

- Only used when the server explicitly advertises `sse` (matches `.well-known/mcp.json` → `transport:"sse"` or user config).
- Uses `mcp-go`'s `NewSSEClient`; reconnection handler wired to the circuit breaker.
- Deprecation note: SSE support is maintained through 2026-06-30 (aligns with public deprecation windows such as Atlassian Rovo's). After that we emit `mcp.config.deprecated` on load.

### Discovery (optional, best-effort)

If the URL host responds `200` to `GET /.well-known/mcp.json`, parse it and:
- cross-check `transport` matches config (warn on mismatch);
- adopt the server-advertised `capabilities.tools` list as the upper bound (further constrained by `allow_tools` / `deny_tools`).
- Failure to fetch is non-fatal; we fall through to `initialize` + `tools/list`.

## Auth / Secret Handling

1. Resolve secret **only** at call time: `os.Getenv(cfg.AuthEnv)`; empty = fail-closed with `mcp.config.auth_missing`.
2. Inject via:
   - `bearer`: `Authorization: Bearer <token>` header.
   - `apikey_header`: `<AuthHeader>: <token>` header.
   - stdio: export as env-var into the child process, stripped from the parent’s visible env list in ps-style logs.
3. Never passed through prompts, tool inputs, or event `Data` payloads.
4. Redaction:
   - Extend `internal/logging` redactor registry with the set of active `AuthEnv` values at process start (refreshed on policy reload).
   - All bus / streamjson emit funcs route through the redactor; unit test: feed a fake bearer through, assert absence in output.
5. `stoke mcp list-servers` prints status but never the token value; it shows `auth_env=GITHUB_MCP_TOKEN present=true` only.

## Security Threat Matrix

| Threat | Vector | Mitigation |
|---|---|---|
| Malicious MCP server injects prompt into tool result | Tool-result text contains "ignore previous instructions…" | Truthfulness contract (spec-1) forbids acting on injected instructions; tool results are wrapped `<mcp_result server="X" tool="Y">…</mcp_result>` in the message going back to the model; supervisor mid-turn check scans for jailbreak-like patterns |
| Data exfiltration via tool args | Server asks for unnecessary args (e.g. full repo tarball) | Args are JSON-Schema validated client-side before send; schemas >1 KiB or using `additionalProperties:true` on sensitive key names trigger `mcp.call.blocked` with `subtype=schema_suspect` |
| Secret leak via tool-result echo | Server echoes back the auth header | Response text scanned for presence of any active `AuthEnv` value pre-redaction; match → replace with `[REDACTED_AUTH]` and emit `mcp.security.echo_detected` |
| DoS via oversized response | Server returns 100 MiB | Response body capped at 2 MiB per call (`http.MaxBytesReader`), streamed responses cap at 8 MiB cumulative; truncation sets `ToolResult.Truncated=true` |
| Credential use outside policy | Worker calls `mcp_github_create_pr` at trust=1 | Trust-gate in `Registry.Call`: if `worker.TrustLevel < cfg.TrustLevel` → deny; override only with `STOKE_MCP_UNGATED=1` and emits `mcp.policy.override` |
| TLS downgrade | Config set to `http://prod-server` | Loader rejects `http://` unless host matches `localhost` / `127.0.0.1` / `::1` |
| Ghost call (model fabricates MCP response without a call) | LLM writes fake tool result | Content-faithfulness judge (`internal/plan/content_judge.go`) flag: any `<mcp_result …>` block in final output without matching `mcp.call.start` + `mcp.call.complete` pair in the event log → `Real=false`, `FakeFile="<mcp_result fabrication>"` |
| Malicious stdio binary | Attacker replaces `linear-mcp-server` in `$PATH` | `Command[0]` resolved to absolute path at `Connect`; hash pinned in policy via optional `command_sha256` field (future-spec-10 scope) |

## Circuit Breaker

Per-server state machine:

| State | Trigger to enter | Behavior |
|---|---|---|
| `closed` | startup / success after half-open | calls flow normally; increments `failCount` on each error, resets on success |
| `open` | `failCount >= CircuitThreshold` | all `CallTool` return `ErrCircuitOpen` immediately; `ListTools` also short-circuits; emits `mcp.call.error` subtype=`circuit_open`; scheduled to transition to `half_open` at `now+CircuitCooldown` |
| `half_open` | cooldown elapsed | one probe call permitted; success → `closed`, failure → `open` with doubled cooldown (cap `CircuitCooldown*4`) |

Failure classification (what increments `failCount`):
- Transport error (dial fail, TLS, EOF, non-200 after retry)
- Timeout (context deadline exceeded)
- Server returned JSON-RPC error object
- **Not counted**: client-side validation (schema-mismatch) — those are caller bugs, not server failures

State is per-`Registry` (per-process) with a `sync.Mutex`; each state change publishes `mcp.circuit.state` on the bus.

## Anti-Deception Integration (spec-1 extension)

### Contract addition

Add this line to `truthfulnessContract` constant in `cmd/r1/sow_native.go` (established by spec-1 at lines 3906-3918):

> `- Never fabricate MCP tool responses. If an MCP call fails, say so explicitly (e.g. "mcp_github_create_issue returned circuit_open"). Do not invent issue numbers, URLs, ticket IDs, message IDs, or other outputs you did not receive from a real MCP tool_use/tool_result pair.`

### Judge hook

Extend `content_judge.go:JudgeDeclaredContent` with a pre-check:
1. Scan final assistant text for `mcp_<server>_<tool>` substrings AND for regex patterns that look like MCP outputs (`issue #\d+`, `PR #\d+`, `ticket [A-Z]+-\d+`, Slack `ts=\d+\.\d+`).
2. For each hit, require at least one corresponding `mcp.call.complete` event in the session's event log for the matching server+tool where the result contained the claimed identifier.
3. Missing evidence → verdict `Real=false, Reason="MCP response fabricated for <server>.<tool>"`, `FakeFile="<mcp_ghost_call>"`.
4. Non-gating by default (matches existing philosophy) but promoted to blocking under `STOKE_MCP_STRICT=1`.

## Event Emission

All MCP activity emits on bus + streamjson per spec-2 contract.

| Event type | When | Data (no secrets, no args) |
|---|---|---|
| `mcp.server.connected` | `Connect` success | `{server, transport, protocol_version, tool_count, duration_ms}` |
| `mcp.server.disconnected` | `Close` or unexpected drop | `{server, reason, uptime_ms}` |
| `mcp.call.start` | before send | `{server, tool, prefixed_name, call_id, timeout_ms, trust_level}` |
| `mcp.call.complete` | on success | `{server, tool, call_id, duration_ms, bytes_in, bytes_out, truncated}` |
| `mcp.call.error` | on failure | `{server, tool, call_id, duration_ms, subtype, message_redacted}` |
| `mcp.circuit.state` | state transition | `{server, from, to, fail_count, cooldown_ms}` |
| `mcp.policy.override` | `STOKE_MCP_UNGATED=1` used | `{server, tool, worker_trust, required_trust}` |
| `mcp.security.echo_detected` | auth value found in response | `{server, tool, call_id}` |
| `mcp.config.deprecated` | SSE-only server configured | `{server, reason, sunset}` |

`subtype` values on `mcp.call.error`: `timeout`, `transport`, `jsonrpc`, `circuit_open`, `auth_missing`, `schema_invalid`, `stream_truncated`, `duplicate_tool`, `size_cap_exceeded`, `policy_denied`.

StreamJSON mirror: `subtype:"stoke.mcp.<event>"`, payload in `_stoke.dev/mcp`.

**Never logged / emitted**: tool args, raw response bodies, auth headers, env-var values. `bytes_in`/`bytes_out` are counts, not contents.

## Permission Gating

- `trust_level` on the server config is the **minimum** worker trust level required for any call to any tool on that server.
- Per-tool override is not in this spec (spec-10 delivers policy-engine fine-grain). For now, if a sub-tool needs higher trust than the server default, split into two server entries with different `allow_tools` subsets.
- If `internal/rbac` exposes `worker.TrustLevel` (already present) the registry consults it. If not available, default trust=2.
- Escape hatch: `STOKE_MCP_UNGATED=1` bypasses all trust checks, always emits `mcp.policy.override`, and is banned in CI via `scan/` rule (`rule: env_mcp_ungated`).

## CLI Surface (`cmd/r1/mcp.go` new)

| Command | Behavior |
|---|---|
| `stoke mcp list-servers` | Table: name, transport, url|command, auth_env, present, enabled, trust_level, health (closed/open/half_open), last_error (redacted) |
| `stoke mcp list-tools <server>` | Connects if not connected, runs `tools/list`, prints prefixed name + description + input-schema title; `--json` for machine output |
| `stoke mcp test <server> [--timeout N]` | Connect + `initialize` + `tools/list` only (no tool calls); exits 0 on success, 1 on auth fail, 2 on transport fail, 3 on timeout |
| `stoke mcp call <server> <tool> --args @file.json` | Diagnostic: single tool call; refuses without `--yes-i-know` because it bypasses the worker trust gate; always emits events |

All commands read `stoke.policy.yaml` via the existing config loader; no new flags on top-level `stoke`.

## Wiring Into `native_runner.go`

1. On worker spawn, `NativeRunner` receives the process-wide `*mcp.Registry` (constructed once in `cmd/r1/main.go` after config load).
2. Before calling `agentloop.Run`, `NativeRunner` calls `registry.AllToolsForTrust(workerTrust)` to get `[]agentloop.Tool` filtered by trust.
3. The agentloop tool handler for any name matching `mcp_*` dispatches to `registry.Call(ctx, name, args)`; every other name routes as today.
4. Tool-result content is wrapped `<mcp_result server="X" tool="Y" call_id="Z">…</mcp_result>` before being appended to the conversation (reinforces that it came from outside the model).
5. Concurrency: MCP calls hold the server's in-flight semaphore; the worker's overall concurrency is unchanged.

## Error Handling

| Failure | Strategy | User / Model sees |
|---|---|---|
| Env var empty at connect | Fail closed, emit `mcp.call.error:auth_missing`, circuit stays closed | Tool result: `error: auth_missing; set GITHUB_MCP_TOKEN` |
| Network timeout | One retry (HTTP), then count as failure, emit `mcp.call.error:timeout` | `error: timeout after 30s` |
| Server returns JSON-RPC error | Surface `message` verbatim (redacted), emit `mcp.call.error:jsonrpc` | `error: <server message>` |
| Circuit open | Short-circuit, zero latency, emit `mcp.call.error:circuit_open` | `error: circuit_open; server=<name>; retry_after=45s` |
| Response > 2 MiB | Truncate, set `Truncated=true`, emit `mcp.call.error:size_cap_exceeded` (non-fatal) | Result with `[truncated: response exceeded 2 MiB cap]` appended |
| Stdio process dies | Circuit trips, do not auto-respawn; next probe after cooldown | Result: `error: transport; restart pending` |
| Schema validation fail (client side) | Do NOT send; return immediately, do NOT count toward circuit | `error: schema_invalid; <field>: <reason>` |

## Boundaries — What NOT To Do

- Do **NOT** modify `internal/mcp/codebase_server.go` or `internal/mcp/stoke_server.go` (server side is complete).
- Do **NOT** build the policy engine — spec-10 handles fine-grained per-tool permissions.
- Do **NOT** introduce agent-to-agent delegation — spec-5 territory.
- Do **NOT** add a browser-as-MCP-server transport — possible future spec.
- Do **NOT** proxy calls through `internal/mcp/memory.go` (that's server-side context memory, not client-side connection pooling).
- Do **NOT** expose tool args in any event payload. Period.
- Do **NOT** implement response resumption; emit `stream_truncated` and fail.

## Testing

### `internal/mcp/client_stdio_test.go`
- [ ] Happy: fake stdio MCP server (table-driven) responds to `initialize`, `tools/list`, `tools/call` → expect `Client.Connect`, `ListTools`, `CallTool` all succeed; tool name normalization (`-` → `_`).
- [ ] Error: process exits mid-call → `mcp.call.error:transport`; fail count == 1.
- [ ] Edge: 2 MiB response → truncated, `ToolResult.Truncated=true`.

### `internal/mcp/client_http_test.go`
- [ ] Happy: `httptest.Server` speaks Streamable-HTTP POST/GET; SSE-framed response correctly assembled.
- [ ] Auth: missing env var → `auth_missing` without dialing the server.
- [ ] Auth redaction: token value never present in any event payload or log.
- [ ] Legacy: server advertises SSE-only → falls back to SSE transport; emits `mcp.config.deprecated`.

### `internal/mcp/circuit_test.go`
- [ ] Happy: 4 failures (below threshold=5) → closed; success resets.
- [ ] Trip: 5 consecutive failures → open; next call returns `ErrCircuitOpen` without hitting transport.
- [ ] Recovery: wait cooldown → half_open; probe success → closed; probe fail → open with 2× cooldown.
- [ ] Concurrent: 20 goroutines racing in half_open → exactly one probe issued.

### `internal/mcp/registry_test.go`
- [ ] Policy load with 3 servers (github/linear/slack) → 3 clients constructed, not yet connected.
- [ ] `AllToolsForTrust(1)` filters out `trust_level=3` server tools; `AllToolsForTrust(3)` returns all.
- [ ] Duplicate server name in YAML → loader error.
- [ ] `deny_tools` takes precedence over `allow_tools`.

### `internal/mcp/security_test.go`
- [ ] Response containing auth value → `[REDACTED_AUTH]`, emits `mcp.security.echo_detected`.
- [ ] `http://prod.example.com` URL → loader error; `http://localhost:3000` accepted.
- [ ] Tool-result prompt-injection string doesn't change the prompt: judge wrapping `<mcp_result>` preserved in transcript.

### `internal/plan/content_judge_mcp_test.go`
- [ ] Assistant text mentions `issue #4242` without any `mcp.call.complete` event → `Real=false`, `FakeFile="<mcp_ghost_call>"`.
- [ ] Assistant text mentions `issue #4242` with matching event whose result body contains `4242` → `Real=true`.

### `cmd/r1/mcp_cmd_test.go`
- [ ] `stoke mcp list-servers` prints table, exit 0 even when circuit is open.
- [ ] `stoke mcp test bogus` → exit 2, stderr contains `server not configured`.

## Acceptance Criteria

- WHEN `stoke.policy.yaml` configures github/linear/slack THEN `Registry.Close()` terminates all subprocesses within 5s and no zombie remains (verified via `ps` in test).
- WHEN a worker at trust=2 requests `mcp_github_create_pr` (trust=3) THEN the call is denied without hitting network and a `mcp.policy.override`-**absent** `mcp.call.error:policy_denied` event is emitted.
- WHEN the same server fails 5 times consecutively THEN subsequent `CallTool` returns in <10ms without network I/O.
- WHEN an MCP response contains the value of the configured auth env var THEN the redacted form is what appears in every emitted event and log line.
- WHEN `STOKE_MCP_UNGATED` is unset THEN no server with `trust_level > worker.TrustLevel` is dialed.
- WHEN a tool call succeeds THEN exactly one `mcp.call.start` and one `mcp.call.complete` are published; on error, one `mcp.call.start` + one `mcp.call.error`.
- WHEN the content-faithfulness judge sees a `mcp_*` mention with no matching `mcp.call.complete` THEN it returns `Real=false`.

### Bash-check acceptance (CI gate)

```bash
go build ./cmd/r1
go test ./internal/mcp/... -run TestMCPClientStdio
go test ./internal/mcp/... -run TestMCPClientSSE
go test ./internal/mcp/... -run TestMCPClientStreamableHTTP
go test ./internal/mcp/... -run TestCircuitBreaker
go test ./internal/mcp/... -run TestRegistryTrustFilter
go test ./internal/mcp/... -run TestAuthRedaction
go test ./internal/plan/... -run TestContentJudgeGhostMCP
go vet ./internal/mcp/... ./cmd/r1/...
./stoke mcp list-servers | head -3
./stoke mcp test github --timeout 5s
./stoke mcp list-tools linear --json | jq -e '.[0].name | startswith("mcp_linear_")'
sqlite3 .stoke/events.db 'SELECT COUNT(*) FROM events WHERE type = "mcp.call.complete"'
sqlite3 .stoke/events.db 'SELECT COUNT(*) FROM events WHERE type = "mcp.call.start" AND data NOT LIKE "%token%" AND data NOT LIKE "%Bearer%"'
```

## Implementation Checklist

1. [ ] Add `github.com/mark3labs/mcp-go` to `go.mod`; vendor and pin a specific version ≥ v0.42. Do NOT upgrade other deps in the same commit.
2. [ ] Define `internal/mcp/types.go`: `Client` interface, `Tool`, `ToolResult`, `Content`, `ServerConfig`, `ErrCircuitOpen`, `ErrAuthMissing`, `ErrPolicyDenied`, `ErrSchemaInvalid`, `ErrSizeCap`. Delete the old `ServerConfig` and `ToolDefinition` from `client.go` (migrate callers — none remain in the worker path).
3. [ ] Implement `internal/mcp/transport_stdio.go` using `mcp-go`'s stdio client: connect, initialize, list, call, close. Process-group isolation (Setpgid) + SIGTERM → SIGKILL on Close.
4. [ ] Implement `internal/mcp/transport_http.go` using `mcp-go`'s Streamable-HTTP client; on `initialize` reject, fall through to SSE client (`transport_sse.go`) if server advertises SSE. Wire per-server semaphore for `max_concurrent`.
5. [ ] Implement `internal/mcp/transport_sse.go` (thin wrapper around `mcp-go` SSE) with reconnect-handler → circuit-breaker hook. Emit `mcp.config.deprecated` on first connect.
6. [ ] Implement `internal/mcp/discovery.go`: `GET /.well-known/mcp.json`, parse, cross-check transport + tool list. Non-fatal on 404 / timeout (500ms cap). Unit-test with `httptest`.
7. [ ] Implement `internal/mcp/circuit.go`: state machine, per-server `sync.Mutex`, `Allow()`, `OnSuccess()`, `OnFailure()`, transitions published to bus. Unit test: closed → open → half_open → closed; exponential cooldown cap; concurrent probe serialization.
8. [ ] Implement `internal/mcp/registry.go`: YAML parse, per-server client construction, `AllTools`, `AllToolsForTrust`, `Call` (handles prefix split, trust gate, circuit gate, event emission, redaction wrapper), `Health`, `Close`.
9. [ ] Implement `internal/mcp/redact.go`: register active `AuthEnv` values into `internal/logging` redactor at Registry construction; unregister on Close. Test round-trip.
10. [ ] Implement `internal/mcp/events.go`: `publishStart`, `publishComplete`, `publishError` helpers that pack the defined payload shape onto bus and streamjson. Never include args / bodies / tokens.
11. [ ] Wire `stoke.policy.yaml` loader: extend `internal/config` to parse `mcp_servers:`, validate (regex on name, http→https rule, required-fields-per-transport). Add golden-file test.
12. [ ] Wire `internal/engine/native_runner.go`: accept `*mcp.Registry` in the runner constructor, call `AllToolsForTrust` before `agentloop.Run`, register a dispatch func for `mcp_*` names that routes to `registry.Call`. Wrap results in `<mcp_result server=… tool=… call_id=…>…</mcp_result>`.
13. [ ] Add `cmd/r1/mcp.go`: `list-servers`, `list-tools`, `test`, `call` subcommands; register under `stoke mcp` parent. Include `--json` on `list-tools`. Respect `--timeout`.
14. [ ] Extend spec-1's truthfulness contract constant with the MCP line (see §Anti-Deception Integration); no new flag.
15. [ ] Extend `internal/plan/content_judge.go` with the MCP ghost-call check (§Anti-Deception Integration). Gate via `STOKE_MCP_STRICT=1` env for blocking behavior; default non-gating advisory.
16. [ ] Add `scan/` rule `env_mcp_ungated` that rejects any commit containing `STOKE_MCP_UNGATED=1` in files under `.github/` or `scripts/ci/`.
17. [ ] Tests: `client_stdio_test.go`, `client_http_test.go`, `client_sse_test.go`, `circuit_test.go`, `registry_test.go`, `security_test.go`, `redact_test.go`, `content_judge_mcp_test.go`, `mcp_cmd_test.go`. All listed in §Testing, all self-contained (fixtures inline).
18. [ ] Docs: update `README.md` section "MCP Servers" with the policy example and three CLI commands. Do not create a new MD file.
19. [ ] Smoke: run `./stoke mcp test <server>` against a local `linear-mcp-server` fake; capture sqlite event counts as in §Acceptance-Criteria Bash block.
20. [ ] Final CI gate: `go build ./cmd/r1 && go test ./... && go vet ./...` — all green.
