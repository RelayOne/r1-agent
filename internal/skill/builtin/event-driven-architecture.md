# event-driven-architecture

> Outbox pattern, saga orchestration, event sourcing, CDC relay, and distributed transaction patterns for microservices

<!-- keywords: outbox, saga, event sourcing, cdc, debezium, choreography, orchestration, compensation, distributed transaction, eventual consistency, idempotency -->

## Critical Rules

1. **The outbox pattern is the only correct way to atomically commit a database write and publish a Kafka event.** Write business data and an outbox row in the same PostgreSQL transaction, then relay the outbox row to Kafka separately. Dual-write (write DB then publish event) is fundamentally broken -- if the publish fails after DB commit, data diverges permanently.

2. **Every saga step must have a compensating action.** Compensating transactions are new forward transactions that semantically undo business effects: CreateOrder -> CancelOrder, ReserveStock -> ReleaseStock. Every compensation must be idempotent and retryable.

3. **Use orchestration for >3 steps, choreography for 2-3 linear steps.** Orchestration provides dashboard visibility, global timeouts, and conditional branching. Choreography maximizes throughput for loosely-coupled flows. Most production systems use a hybrid.

4. **Deploy consumers before producers when evolving event schemas.** Consumer deploys with new field treated as optional. Producer then starts sending the field. This phased approach requires no simultaneous deploys.

5. **Use clock_timestamp() in outbox tables, not NOW().** NOW() returns the same value for all statements within a transaction, causing ordering ambiguity. Use BIGSERIAL for reliable ordering, not timestamps.

## Outbox Table Schema

```sql
CREATE TABLE outbox_events (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    sequence_id     BIGSERIAL NOT NULL,
    aggregate_type  VARCHAR(255) NOT NULL,       -- routes to Kafka topic
    aggregate_id    VARCHAR(255) NOT NULL,        -- becomes Kafka message key
    event_type      VARCHAR(255) NOT NULL,
    payload         JSONB NOT NULL,
    headers         JSONB DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    published_at    TIMESTAMPTZ
);
CREATE INDEX idx_outbox_unpublished
    ON outbox_events (sequence_id) WHERE published_at IS NULL;
```

## CDC vs Polling for Outbox Relay

**Debezium CDC**: Reads PostgreSQL WAL with ~10-50ms latency, zero table-level DB load, perfect transaction ordering. Configure with `plugin.name=pgoutput` and the Outbox Event Router SMT. Critical risk: WAL bloat from replication slots. Set `heartbeat.interval.ms=10000`. Monitor `pg_replication_slots` for `restart_lsn` drift.

**Polling**: `SELECT ... FOR UPDATE SKIP LOCKED` claims unpublished rows. Simpler to operate (no Kafka Connect cluster). 1-5 second latency floor. Concurrent transactions can commit out of sequence order, potentially causing missed messages (noted by Martin Kleppmann).

Choose CDC for >1,000 events/sec, strict ordering, or DB load concerns. Choose polling for simpler systems or teams without CDC experience.

**Outbox cleanup**: Partition by date range, use `DROP PARTITION` instead of DELETE (avoids vacuum pressure). For CDC: INSERT and DELETE the outbox row in the same transaction. Debezium captures the INSERT from WAL; the table stays permanently empty.

## Saga Patterns

### Orchestration Saga

A saga coordinator issues commands, waits for responses, and triggers compensation on failure. Persist saga state in PostgreSQL:

```sql
CREATE TABLE saga_instances (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    saga_type       VARCHAR(255) NOT NULL,
    current_state   VARCHAR(100) NOT NULL DEFAULT 'INITIAL',
    payload         JSONB NOT NULL DEFAULT '{}',
    version         INTEGER NOT NULL DEFAULT 0,  -- optimistic concurrency
    timeout_at      TIMESTAMPTZ
);
```

Use optimistic concurrency (version column) to prevent duplicate step execution.

### Choreography Saga

Each service publishes events that trigger the next step. Compensating events undo on failure. Best for 2-3 step linear flows. Harder to trace failures across services -- require correlation IDs and distributed tracing.

### Compensation Categories

- **Compensable transactions**: Can be undone (reserve -> release inventory).
- **Pivot transactions**: Point of no return. After this, forward recovery only.
- **Retryable transactions**: Steps after the pivot that must eventually succeed via retry.

For non-reversible actions, use a **two-phase API** (reserve, then confirm/cancel with auto-cancel timeout). Register compensation before executing the action.

## Event Sourcing Essentials

Store state changes as an append-only log. Rebuild current state by replaying events. Use when you need complete audit trails or temporal queries -- not every service needs it. Store snapshots every N events to prevent slow replays. Build read-optimized projections from events. Use upcasters to transform old event formats to new on replay.

## Idempotency Patterns

Every event handler must produce the same result if delivered twice. Use an idempotency key table (store processed event IDs, check before processing, TTL cleanup), conditional writes (`INSERT ... ON CONFLICT DO NOTHING`), or naturally idempotent operations (`SET status = 'shipped'`). Scope idempotency keys to each saga step.

## Common Gotchas

- **Stale outbox rows**: One fintech discovered 2 billion uncleared outbox rows causing severe degradation. Always implement cleanup.
- **Saga timeout**: Sagas without global timeouts can hang forever. Set `timeout_at` and run periodic sweeps.
- **Change data capture ordering**: CDC preserves transaction commit order, not statement order. Two concurrent transactions may interleave in the WAL.
- **Event schema coupling**: If consumers parse the full event body, any field change is a breaking change. Use the Tolerant Reader pattern.
- **Compensation after side effects**: Emails, SMS, and external API calls cannot be compensated. Queue side effects and only execute after the saga reaches the pivot transaction.
