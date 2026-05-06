# R1 agent-serve (Task 24)

`r1 agent-serve` exposes an R1 instance as an HTTP endpoint
so other agents — or TrustPlane — can hire it and receive a
verified result back. Distinct from `r1 serve` which runs the
mission-orchestrator API consumed by stoke-server / dashboards.

## CLI

```
r1 agent-serve [--addr :8440] [--task-timeout 10m] [--caps CSV]
```

- `--addr` — listen address. Default `:8440`. Point a reverse
  proxy + TLS terminator here; do NOT expose the bare listener
  publicly.
- `--task-timeout` — per-task wall-clock deadline. Default `10m`.
- `--caps` — CSV list of advertised task types. Empty (default)
  advertises every registered executor.

Accepted tokens for `X-Stoke-Bearer` come from the
`STOKE_SERVE_TOKENS` env var (comma-separated). Empty → no auth,
which is appropriate for localhost development only.

## Endpoints

### `GET /api/capabilities`

Discovery. Public even when `STOKE_SERVE_TOKENS` is set — callers
need this to know whether to bother hiring in the first place.

```
$ curl -s http://localhost:8440/api/capabilities
{
  "version": "dev",
  "task_types": ["research", "browser", "deploy"],
  "budget_usd": 0,
  "requires_auth": false
}
```

### `POST /api/task`

Submit and run a task. Synchronous in the MVP — the response body
arrives after the executor returns or the task timeout fires.

```
$ curl -s -X POST http://localhost:8440/api/task \
    -H 'Content-Type: application/json' \
    -H 'X-Stoke-Bearer: sk-...' \
    -d '{
      "task_type": "research",
      "description": "FINTRAC MSB thresholds",
      "query": "FINTRAC MSB thresholds",
      "effort": "standard"
    }'
{
  "id": "t-8c36b0e2-...",
  "status": "completed",
  "task_type": "research",
  "created_at": "2026-04-21T05:12:44Z",
  "started_at": "2026-04-21T05:12:44Z",
  "completed_at": "2026-04-21T05:12:57Z",
  "summary": "research: 3 claims, 2 sources",
  "size": 1284
}
```

Status progresses `queued → running → completed | failed`. On
failure, `error` is populated with a descriptive message. On
success, `summary` + `size` are populated from the executor's
Deliverable.

### `GET /api/task/{id}`

Poll a previously submitted task. Handy for future async mode;
today it just re-reads the state you already got from POST.

## Auth

Set `STOKE_SERVE_TOKENS` to a comma-separated token list:

```
STOKE_SERVE_TOKENS="sk-partnerA,sk-partnerB" r1 agent-serve --addr :8440
```

Requests must include `X-Stoke-Bearer: <token>` where `<token>` is
one of the registered values. Missing or wrong token → 401.
Capabilities endpoint stays public for discovery.

## Synchronous vs async

The MVP runs the executor in the POST handler's goroutine. A slow
task holds the connection open. Advantages:

- Zero in-flight state persistence (process crash = one task lost,
  not every task lost)
- Simple client code (POST returns the result)
- Easy timeout semantics (client aborts = context cancel)

Future async mode will:

1. POST returns 202 with `status: "queued"` immediately.
2. Executor runs in a worker pool drained on shutdown.
3. Callback URL + webhook when the task finishes.
4. Persistence so restarts don't lose in-flight tasks.

## Security

- Run behind a TLS terminator + authenticated reverse proxy when
  exposed beyond localhost.
- The websearch allowlist (`internal/websearch/`) does NOT apply
  transitively through agent-serve — if a hired task uses the
  browser executor, the operator of THIS R1 is responsible
  for the URL surface those requests end up hitting.
- The honeypot pipeline (`internal/critic/honeypot.go`) runs
  regardless of invocation path — a hired agent that tries to
  exfiltrate the system prompt trips the canary just like a
  locally-dispatched task would.

## Related subcommands

- `r1 serve` — mission-orchestrator API + dashboard. Different
  binary surface entirely.
- `r1 mcp-serve` — MCP protocol server. A separate protocol
  with its own auth + semantics.
- `r1 task` — local CLI task dispatch. Same executor registry,
  same task types, no network surface.
