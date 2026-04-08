# Bus: Durable WAL-Backed Event System

Package: `internal/bus/`

## Design

The bus is the runtime backbone for cross-component communication. Events are
durably written to a Write-Ahead Log before delivery, ensuring no events are
lost even on crash.

## Three Participant Types

1. **Publishers** — Any component emits events via `Publish()`
2. **Hooks** — Privileged handlers (authority="supervisor"), fire synchronously in priority order
3. **Subscribers** — Passive observers with buffered async delivery per subscriber

## Event Structure

```go
type Event struct {
    ID        string          // unique event ID
    Type      string          // dotted namespace (e.g., "worker.spawned")
    Timestamp time.Time
    EmitterID string          // who published
    Sequence  uint64          // monotonic
    Scope     Scope           // mission/branch/loop/task/stance context
    Payload   json.RawMessage
    CausalRef string          // optional: ID of causing event
}
```

## Event Namespaces

| Namespace | Events | Purpose |
|-----------|--------|---------|
| `worker.*` | spawned, action.started/completed, paused, resumed, terminated | Stance lifecycle |
| `ledger.*` | node.added, edge.added | Graph mutations |
| `supervisor.*` | rule.fired, hook.injected, checkpoint | Rule engine |
| `skill.*` | loaded, applied, extraction.requested | Skill pipeline |
| `mission.*` | started, completed, aborted | Mission lifecycle |
| `cost.*` | recorded, budget.alert | Cost tracking |
| `verify.*` | started, completed | Verification |
| `bus.*` | handler.panic, subscriber.overflow, hook.action_failed | Bus observability |

## Delivery Guarantees

- Hooks fire **synchronously** before subscribers (ensures causality)
- Injected events from hooks are deferred until the triggering event finishes subscriber delivery
- Subscriber delivery is **asynchronous** with per-subscriber goroutines
- Panic in one subscriber does not affect others

## Delayed Events

```go
bus.PublishDelayed(event, delay)  // schedule for future delivery
bus.CancelDelayed(id)            // cancel before it fires
```

Delayed events are persisted to WAL and restored on restart.

## API

```go
bus.Publish(event)                // durable publish
bus.RegisterHook(hook)            // authority-gated
bus.Subscribe(pattern, handler)   // passive observer
bus.Replay(fromSeq, handler)      // historical replay
bus.CurrentSeq()                  // global sequence number
```

## Prefix Indexing

The bus uses the first dot-segment of the event type for O(1) subscription and
hook lookup. For example, `worker.spawned` matches subscriptions registered for
`worker.*` via the `worker` prefix index.
