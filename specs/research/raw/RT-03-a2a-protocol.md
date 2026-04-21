# RT-03: A2A (Agent2Agent) Protocol -- State of the Art 2026

Date: 2026-04-20
Scope: How Stoke (Go CLI) should speak A2A as both client (hiring agents) and server (being hirable), with TrustPlane as the payment layer.

## 1. Protocol state (April 2026)

- **Name:** Agent2Agent Protocol (A2A).
- **Maintainer:** Linux Foundation, Agent2Agent Protocol Project. Originated at Google Cloud (announced 2025-04-09), donated to LF 2025-06-23 under neutral governance.
- **Current version:** **v1.0.0**, released 2026-03-12. Headline change vs. 0.3.x: signed Agent Cards, formalized REST binding, `A2A-Version` / `A2A-Extensions` headers.
- **Spec URL (authoritative):** https://a2a-protocol.org/latest/specification/
- **Repo:** https://github.com/a2aproject/A2A (Apache-2.0, 23k+ stars).
- **SDKs (official, under `a2aproject`):** Python (`a2a-sdk`), **Go (`github.com/a2aproject/a2a-go`, v2.2.0, requires Go 1.24.4+)**, JS (`@a2a-js/sdk`), Java, .NET.
- **Adoption (1-year mark, April 2026):** 150+ organizations; native integration in Google Cloud (Vertex Agent Engine), Microsoft (Copilot Studio, Foundry), AWS (Bedrock AgentCore, Agent Registry preview). Launch partners include Atlassian, Box, Cohere, LangChain, MongoDB, PayPal, Salesforce, SAP, ServiceNow, UiPath.
- **Transports:** JSON-RPC 2.0 over HTTP, HTTP/REST, gRPC. Streaming via Server-Sent Events (SSE) for push-pull and webhook push notifications for fully async.

## 2. Core primitives (v1.0 method names)

Protocol operations (transport-independent):

| Method | Purpose |
|---|---|
| `SendMessage` | Submit a message/task (synchronous or long-running). |
| `SendStreamingMessage` | Submit + subscribe to SSE event stream. |
| `GetTask` | Fetch task state + artifacts. |
| `ListTasks` | Paginated task listing with filters. |
| `CancelTask` | Request cancellation. |
| `SubscribeToTask` | Open SSE stream for an existing task. |
| `CreateTaskPushNotificationConfig` | Register webhook for async updates. |
| `GetTaskPushNotificationConfig` / `ListTaskPushNotificationConfigs` / `DeleteTaskPushNotificationConfig` | Webhook CRUD. |
| `GetExtendedAgentCard` | Authenticated card (richer than public `.well-known` card). |

Auth schemes advertised in cards and negotiated per-request:
`APIKeySecurityScheme`, `HTTPAuthSecurityScheme` (Basic/Bearer), `OAuth2SecurityScheme` (authorization_code, client_credentials, device_code), `OpenIdConnectSecurityScheme`, `MutualTlsSecurityScheme`. Required headers on every call: `A2A-Version: 1.0`, optional `A2A-Extensions: <uri>,<uri>`.

Errors: JSON-RPC error envelope, with a catalog of A2A-specific codes (task not found, auth required, rejected, unsupported modality, etc.). Retries are client responsibility; idempotency is recommended via client-supplied message IDs.

## 3. Agent Card schema

Served at `https://{host}/.well-known/agent-card.json` (RFC 8615 well-known URI). Key fields:

```jsonc
{
  "id": "stoke.relay.one/agent",
  "displayName": "Stoke",
  "description": "Autonomous Go/TS code agent",
  "provider": { "name": "relay", "url": "https://relay.one" },
  "service": {
    "url": "https://stoke.relay.one/a2a",
    "protocol": "JSONRPC",          // or "REST" | "GRPC"
    "version": "1.0"
  },
  "capabilities": {
    "streaming": true,
    "pushNotifications": true,
    "extendedAgentCard": true
  },
  "skills": [                         // structured, searchable
    { "id": "go-backend", "name": "Go backend", "tags": ["go","http","postgres"],
      "inputModes": ["text/plain","application/json"],
      "outputModes": ["text/x-diff","application/json"] }
  ],
  "extensions": [
    "https://a2a-protocol.org/extensions/x402/v0.1"
  ],
  "securitySchemes": { "oauth": { "type": "oauth2", ... },
                       "mtls":  { "type": "mutualTLS" } },
  "security": [ { "oauth": ["tasks:write"] } ],
  "signature": { "alg": "EdDSA", "kid": "...", "value": "..." }   // v1.0 signed card
}
```

Capabilities are a free-form skills array (name + tags + I/O modes); there is no mandated global taxonomy, but tags like `go`, `typescript`, `full-stack`, `api` are the de-facto convention. **The spec does not define pricing/quoting** -- it is delegated to extensions (x402, AP2) or out-of-band marketplaces.

## 4. Task lifecycle

`Task` object fields: `id`, `contextId`, `status` (`TaskStatus`), `artifacts[]`, `history[]` (messages), `metadata{}`.

States (`TaskState`): `SUBMITTED` -> `WORKING` -> terminal `COMPLETED`/`FAILED`/`CANCELED`/`REJECTED`, plus interruptible `INPUT_REQUIRED` and `AUTH_REQUIRED`.

Streaming events on SSE channel:
- `TaskStatusUpdateEvent { taskId, contextId, status, metadata }`
- `TaskArtifactUpdateEvent { taskId, artifact, append, lastChunk }`

Long-running pattern: client calls `SendMessage`, gets `taskId` + `SUBMITTED`. Client either (a) polls `GetTask`, (b) opens SSE via `SubscribeToTask`, or (c) registers a webhook via `CreateTaskPushNotificationConfig` and disconnects. Webhook payloads are the same update events, signed with the agent's key.

REST endpoint shape:
```
POST   /messages
POST   /messages:stream
GET    /tasks/{id}
GET    /tasks
POST   /tasks/{id}:cancel
GET    /tasks/{id}:subscribe           (SSE)
POST   /tasks/{id}/pushNotificationConfigs
GET    /.well-known/agent-card.json
GET    /.well-known/a2a/agent-card     (extended, authenticated)
```

## 5. Stoke as client (Go)

Use `github.com/a2aproject/a2a-go/v2` (a2aclient package). Client flow:

```go
import (
    "github.com/a2aproject/a2a-go/v2/a2aclient"
    "github.com/a2aproject/a2a-go/v2/a2apb"
)

card, err := a2aclient.FetchCard(ctx, "https://other.example.com/.well-known/agent-card.json")
cli,  err := a2aclient.FromCard(ctx, card, a2aclient.WithOAuth(tok))

task, err := cli.SendMessage(ctx, &a2apb.Message{
    Role: "user",
    Parts: []*a2apb.Part{{Text: "Refactor foo.go"}},
    Metadata: map[string]any{"budgetUsd": 2.50},
})

events, err := cli.SubscribeToTask(ctx, task.Id)          // <-chan Event
for ev := range events { handle(ev) }

final, _ := cli.GetTask(ctx, task.Id)
```

Stoke's internal adapter (`internal/a2a/client.go`) should wrap this with:
- cache for agent cards (signature + TTL verify),
- TrustPlane quote + escrow call before `SendMessage`,
- verifier hook on `COMPLETED` -> Stoke `verify.Pipeline` over returned artifacts,
- settle/refund on TrustPlane based on verify result.

The interface sketch in the brief is essentially right; rename `url` -> `cardURL` and fold streaming into a single `SendStreaming` returning `(taskID, <-chan Event, error)` to match the SDK.

## 6. Stoke as server (Go)

Use `a2asrv`. Stoke exposes `stoke serve --port 8080 --caps go,full-stack,typescript` and registers:

```go
exec := &stokeExecutor{ app: a }              // AgentExecutor impl
h    := a2asrv.NewRequestHandler(exec)
mux  := http.NewServeMux()
mux.Handle("/.well-known/agent-card.json", a2asrv.CardHandler(card))
mux.Handle("/a2a/",                        a2asrv.RESTHandler(h))
mux.Handle("/a2a/jsonrpc",                 a2asrv.JSONRPCHandler(h))
mux.HandleFunc("/healthz", healthz)
http.ListenAndServe(":8080", mux)
```

Required endpoints: the REST/JSON-RPC set above plus `/.well-known/agent-card.json`. Health/readiness are not in the spec; add `/healthz`, `/readyz` for ops. Sign the agent card at boot with a key from `internal/trustplane/dpop` (reuse existing key material).

`stokeExecutor` translates incoming A2A messages into Stoke's existing `mission.Runner`: each A2A task becomes a mission, `TaskArtifactUpdateEvent`s stream diffs/logs, terminal state set from `verify.Pipeline` outcome. Map Stoke failure classes to `FAILED` vs `REJECTED` (rejected = out-of-scope/unsupported skill; failed = tried and failed).

## 7. Relationship to MCP

Complementary, not overlapping.

- **MCP** (Anthropic) = model-to-tool. An agent's LLM pulls context and invokes tools. Stoke already has `mcp/` for this.
- **A2A** = agent-to-agent. An agent delegates a whole task to a peer agent (opaque -- you do not get its tools or memory, only its deliverables).

Dev-side mental model: MCP is the inside of the process, A2A is the wire between processes. A Stoke instance serving A2A may internally call MCP tools; the A2A caller never sees that. Discovery surfaces are also distinct: MCP servers register via client config; A2A agents via `.well-known` or a registry (AWS Bedrock Agent Registry, under preview since March 2026, indexes both).

## 8. TrustPlane hook points

The A2A v1.0 core spec deliberately omits payment. Settlement is an extension layer. Current landscape:

- **Google AP2** (Agent Payments Protocol, Sept 2025) -- intent + authorization layer, payment-instrument agnostic.
- **Coinbase x402** (HTTP 402 revival) -- on-chain USDC micro-settlement, ~$0.0001 fee, sub-2s finality.
- **A2A x402 Extension** -- joint Google/Coinbase, repo `google-agentic-commerce/a2a-x402`. Advertised via `extensions: ["https://.../x402/v0.1"]` on the agent card. Flow: server replies `payment-required` message, client returns `payment-submitted` with signed payment details, server verifies + settles + replies `payment-completed`, then normal task work proceeds.
- **Visa TAP, PayPal Agent Ready** -- fiat rails, launched Q1 2026.

For Stoke the right seam is:

1. **Advertise** TrustPlane on the card as an extension URI (`https://relay.one/trustplane/v1`) plus any of AP2/x402 Stoke bridges to.
2. **Quote** before task start: server emits a `payment-required` equivalent message containing a TrustPlane quote ID; `INPUT_REQUIRED` state until the client confirms.
3. **Escrow** via TrustPlane (`internal/trustplane/real.go`): client locks funds, passes receipt in message metadata (`trustplane.escrowId`), server verifies with TrustPlane before transitioning to `WORKING`.
4. **Settle** on terminal state: `COMPLETED` -> TrustPlane `Release`; `FAILED`/`REJECTED` -> `Refund`; partial work uses TrustPlane milestone split against `artifacts[]`.
5. **Dispute** hook: `AUTH_REQUIRED` or a custom `trustplane.dispute` metadata key kicks a TrustPlane dispute, paused task until resolved off-band.

This layering matches how AP2 + x402 coexist with A2A today: A2A finds + talks, TrustPlane authorizes + settles. Stoke does not need to implement AP2/x402 itself on day one -- just expose TrustPlane as a named extension and gate state transitions on its receipts. Later, a thin `a2a-x402` adapter gives interop with Coinbase/Google-side payers.

## 9. Open questions / risks

- **Registry API is not standardized.** For Stoke's "find an agent" flow, we must either hardcode trusted card URLs, ship a Stoke-internal registry, or integrate AWS Agent Registry (preview, provider-agnostic, supports both MCP+A2A). Recommend: start hardcoded, then AWS preview behind a flag.
- **Signed cards (v1.0) require key management.** Reuse `internal/trustplane/dpop` key and publish JWKS at `/.well-known/jwks.json`.
- **`a2a-go` is on `v2.x` module path** (`/v2`) even though protocol is v1.0 -- SDK versioning is independent; pin carefully.
- **gRPC binding exists but REST+JSON-RPC is the common denominator.** Stoke should ship JSON-RPC first (simpler curl-ability, better ecosystem parity).
- **No cost field in the card.** Pricing must live in a TrustPlane extension sub-document; do not try to add a `pricing` field to the core card -- it will break signature verification by non-Stoke clients.

## Sources

- A2A spec v1.0: https://a2a-protocol.org/latest/specification/
- Agent discovery topic: https://a2a-protocol.org/latest/topics/agent-discovery/
- A2A GitHub org: https://github.com/a2aproject
- A2A Go SDK: https://github.com/a2aproject/a2a-go
- LF press (1-yr milestone, 2026-04-09): https://www.linuxfoundation.org/press/a2a-protocol-surpasses-150-organizations-lands-in-major-cloud-platforms-and-sees-enterprise-production-use-in-first-year
- LF A2A launch: https://www.linuxfoundation.org/press/linux-foundation-launches-the-agent2agent-protocol-project-to-enable-secure-intelligent-communication-between-ai-agents
- Google donation: https://developers.googleblog.com/en/google-cloud-donates-a2a-to-linux-foundation/
- A2A x402 extension: https://github.com/google-agentic-commerce/a2a-x402
- AP2 announcement: https://cloud.google.com/blog/products/ai-machine-learning/announcing-agents-to-payments-ap2-protocol
- AWS Agent Registry (preview, April 2026): https://www.infoq.com/news/2026/04/aws-agent-registry-preview/
- MCP vs A2A framing: https://www.digitalocean.com/community/tutorials/a2a-vs-mcp-ai-agent-protocols
