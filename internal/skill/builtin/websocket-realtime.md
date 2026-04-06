# websocket-realtime

> Real-time communication patterns: WebSockets, SSE, connection management, and scaling

<!-- keywords: websocket, socket.io, server-sent events, sse, realtime, pubsub, presence -->

## Critical Rules

1. **Always implement reconnection with exponential backoff.** Connections drop. Mobile networks switch. The client must reconnect automatically with jitter to avoid thundering herd.

2. **Heartbeat both directions.** Server sends ping, client responds pong (or vice versa). Detect dead connections within 30-60 seconds. Without heartbeats, half-open connections leak resources.

3. **Never trust client-sent timestamps or ordering.** Assign sequence numbers and timestamps server-side. Clients can have clock drift of minutes.

4. **Authenticate on connection, not per message.** Validate the token during the WebSocket handshake or first message. Reject unauthenticated connections immediately.

5. **Design for exactly-once delivery at the application level.** The transport guarantees at-most-once. Use idempotency keys or sequence numbers to deduplicate on the client.

## Technology Decision Matrix

| Criterion | WebSocket | SSE | Long Polling |
|-----------|-----------|-----|-------------|
| Direction | Bidirectional | Server-to-client | Server-to-client |
| Protocol | ws:// or wss:// | HTTP/2 stream | HTTP request/response |
| Browser support | Universal | Universal (except IE) | Universal |
| Reconnection | Manual | Built-in (EventSource) | Built-in (by design) |
| Binary data | Yes | No (text only) | Yes |
| HTTP/2 multiplexing | No (upgrades from HTTP/1.1) | Yes | Yes |
| Proxy/CDN friendly | Sometimes problematic | Yes | Yes |

- **Use SSE** when you only need server-to-client push (notifications, live feeds, progress updates).
- **Use WebSocket** when you need bidirectional communication (chat, collaborative editing, gaming).
- **Use long polling** as a fallback when WebSocket and SSE are blocked by infrastructure.

## Connection Lifecycle

### Client-Side Reconnection
- Connect with initial backoff of 1s. On disconnect, wait backoff plus random jitter (0-500ms), double backoff (cap at 30s), attempt reconnect.
- On successful connect, reset backoff to 1s, send last received sequence number, server replays missed messages.

### Server-Side Connection Management
- Track connections in a concurrent map keyed by user ID or session ID.
- Set read/write deadlines on the socket. A connection that stops reading is dead.
- Graceful shutdown: send a close frame, wait briefly, then terminate.
- Limit connections per user (e.g., max 5) to prevent resource exhaustion.

## Room and Channel Patterns

- **Rooms:** Group connections by topic (chat room, document, game lobby). Messages broadcast to all room members.
- **Join/leave events:** Track membership explicitly. Broadcast join/leave to other room members.
- **Server-side filtering:** Subscribe to events server-side. Never send all events and filter on the client.
- **Hierarchical channels:** Use dot-separated namespaces (`chat.room.123`, `notifications.user.456`) for flexible routing.

## Presence Tracking

- Presence answers "who is online right now" across servers. Use Redis with TTL keys (`presence:user:123`, TTL 60s) refreshed by heartbeats.
- Publish presence changes to a pub/sub channel. Other servers relay to connected clients.
- Clean disconnects send a "going offline" message. Crashes rely on TTL expiry.

## Message Ordering and Delivery

### Guarantees
- **Causal ordering:** Messages from the same sender arrive in order. Use per-sender sequence numbers.
- **Total ordering:** All clients see messages in the same order. Requires a central sequencer (database, Kafka partition).
- **Delivery confirmation:** Server sends ACK with message ID. Client resends unACKed messages after timeout.

### Message Format
- Include `id` (dedup), `seq` (ordering), `type` (routing), `payload`, and `timestamp` in every message.
- Keep payloads small. Send references (IDs) not full objects for large data.

## Scaling WebSockets

### Horizontal Scaling
- WebSocket connections are stateful and sticky. A load balancer must route reconnections to the same server (sticky sessions) or use a shared state layer.
- **Redis pub/sub:** Each server subscribes to relevant channels. When a message arrives, it broadcasts to its local connections.
- **Kafka or NATS:** For higher throughput. Each server consumes from topic partitions.

### Connection Limits
- A single server handles 100K-1M connections with tuning. Each idle WebSocket costs ~10-50KB. Tune `ulimit -n` and `net.core.somaxconn`.

## Binary Protocols

- For high-throughput needs, use Protocol Buffers or MessagePack over WebSocket binary frames.
- Binary is 2-10x smaller and faster to parse than JSON. Worth it for gaming, streaming, and IoT.

## Rate Limiting and Abuse Prevention

- **Per-connection rate limit:** Max N messages per second per connection. Drop or disconnect abusers.
- **Message size limit:** Enforce maximum frame size (e.g., 64KB). Reject oversized messages.
- **Authentication expiry:** Tokens expire. Periodically revalidate auth on long-lived connections (e.g., every 15 minutes).
- **Input validation:** Every incoming message must be validated against a schema. Malformed messages are dropped silently.
- **Flood protection:** Detect rapid identical messages (spam) and throttle or ban the sender.

## Common Gotchas

- **Buffered messages on slow clients:** If the server sends faster than the client reads, messages buffer in memory. Set write deadlines and drop slow consumers.
- **Connection storms after deploy:** All clients reconnect simultaneously. Use jittered reconnection delays and rolling deploys.
- **Proxy timeouts:** Many proxies (nginx, AWS ALB) close idle connections after 60s. Heartbeat interval must be shorter.
- **Memory leaks from uncleaned connections:** Always remove connections from maps and unsubscribe from channels on disconnect.
- **Mixed HTTP and WS auth:** Ensure the same auth mechanism works for both. Don't have separate session stores.
