# r1-agent IDE Protocol

This document is the source of truth for the HTTP contract that the
**VS Code** (`ide/vscode/`) and **JetBrains** (`ide/jetbrains/`) plugins
speak to a locally-running r1-agent daemon. Both ports stay aligned
through this document, not shared code (the two ecosystems use
different runtimes; sharing types would force a build-time bridge).

The wire format is the **agentserve** API exposed by
`stoke agent-serve` (source: `internal/agentserve/server.go`,
CLI: `cmd/stoke/agent_serve_cmd.go`).

## Daemon endpoint

| Setting | IDE default | Notes |
| --- | --- | --- |
| `r1.daemonUrl` | `http://127.0.0.1:7777` | Override via IDE settings or the `R1_DAEMON_URL` env var. |
| `r1.apiKey` | empty | Falls back to the `R1_API_KEY` env var. Sent as `X-Stoke-Bearer` (during the rename window R1 keeps the legacy header). |

> **Discrepancy with stoke default:** `stoke agent-serve --addr` defaults
> to `:8440`. The IDE protocol publishes `127.0.0.1:7777` so users
> running multiple stoke instances can pin a stable IDE-only port.
> Operators must therefore start the daemon with
> `stoke agent-serve --addr :7777` (or override `r1.daemonUrl`).
> When the rename to r1-agent finalises (work-r1-rename.md) we expect
> the new default to be `7777`; the IDE protocol is forward-aligned.

## Endpoints (subset used by IDEs)

### `GET /api/capabilities`

Returns the capability advertisement. Public (no auth required).

Response body:

```json
{
  "version": "0.1.0",
  "task_types": ["research", "code_change", "explain"],
  "budget_usd": 5.0,
  "requires_auth": true
}
```

The `requires_auth` flag tells the IDE whether the user must configure
`r1.apiKey`. If the daemon advertises `requires_auth: true` and the
IDE has no key configured, surface a notification.

### `POST /api/task`

Submit a task. Synchronous in MVP — the response carries the terminal
state. Body:

```json
{
  "task_type": "explain",
  "description": "Explain what this function does",
  "query": "<file contents or selection>",
  "spec": "",
  "budget": 0,
  "extra": { "source": "vscode", "filename": "main.go" }
}
```

Required: `task_type`, plus at least one of `description` / `query`.
Allowed `task_type` values come from `/api/capabilities.task_types`.

Response (`TaskState`):

```json
{
  "id": "t-<uuid>",
  "status": "completed",
  "task_type": "explain",
  "created_at": "2026-04-26T12:00:00Z",
  "started_at": "2026-04-26T12:00:00Z",
  "completed_at": "2026-04-26T12:00:03Z",
  "summary": "<final answer>",
  "size": 142
}
```

Terminal `status` values: `completed`, `failed`, `cancelled`. The
`error` field is populated when `status == "failed"`.

### `GET /api/task/{id}`

Poll for state (mirror of the POST response). Useful when the IDE
disconnects mid-flight; the daemon keeps tasks in memory until
shutdown.

### `POST /api/task/{id}/cancel`

Abort an in-flight task. The IDE should surface a Cancel button on
the chat panel that fires this.

### `GET /api/task/{id}/events`

Server-Sent Events stream of `taskEvent` frames:

```
event: queued
data: {"kind":"queued","state":{...},"terminal":false}

event: started
data: {"kind":"started","state":{...},"terminal":false}

event: completed
data: {"kind":"completed","state":{...},"terminal":true}
```

Both IDE plugins fall back to polling `/api/task/{id}` when SSE is
unavailable (corporate proxies sometimes break SSE).

## Headers

| Header | Direction | Purpose |
| --- | --- | --- |
| `X-Stoke-Bearer` | IDE → daemon | API key. Required when `requires_auth=true`. |
| `Content-Type: application/json` | IDE → daemon | All POST bodies. |
| `Accept: text/event-stream` | IDE → daemon | SSE event stream only. |

## Error envelope

Non-2xx responses are JSON:

```json
{ "error": "task_type required" }
```

The IDE displays this verbatim in the chat panel / output channel.

## Versioning

This protocol document is versioned **v1** and is wired into both
plugins' settings UI. When the daemon adds a non-backwards-compatible
field, the version bumps and both IDE clients pin a minimum version
via `/api/capabilities.version`.

## Out of scope

* Authentication beyond a static bearer token. OAuth + per-task keys
  ship with TASK-T20 / TrustPlane and the IDE plugins will adopt them
  when the daemon advertises `auth_methods: ["oauth2"]`.
* Streaming partial output mid-task. The current daemon returns the
  full `summary` only on terminal transition; once the daemon adds
  `delta` frames to the SSE stream both IDEs should incrementally
  render them.
