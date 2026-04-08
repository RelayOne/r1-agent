# database-migration-safety

> Zero-downtime database migrations using expand-and-contract, lock-safe DDL, and safe rollback patterns

<!-- keywords: migration, schema, expand, contract, zero-downtime, lock, ddl, postgres, postgresql, backfill, rollback, pgroll, dual-write -->

## The Meta-Pattern

Every zero-downtime migration follows one sequence: expand the schema, migrate the data, then contract the old structure away. Never require a synchronized big-bang cutover. This applies to PostgreSQL column changes, MongoDB document evolution, Kafka schema updates, and cross-service API contracts.

## PostgreSQL Lock Danger

`ALTER TABLE` acquires an ACCESS EXCLUSIVE lock by default, blocking ALL reads and writes. Worse, PostgreSQL's lock queue is FIFO -- if ALTER TABLE is waiting for its lock, every subsequent SELECT queues behind it, causing cascading outage. Always `SET lock_timeout = '5s'` before any DDL.

### Safe vs Dangerous Operations

| Operation | Duration | Safe? |
|-----------|----------|-------|
| ADD COLUMN (no default or constant, PG11+) | Instant | Yes |
| ADD COLUMN (volatile default, e.g. `now()`) | Full rewrite | NO |
| DROP COLUMN | Instant | Yes (marks dropped) |
| RENAME COLUMN | Instant | Breaks queries using old name |
| ALTER COLUMN TYPE | Usually rewrite | NO (exceptions: VARCHAR->TEXT) |
| SET NOT NULL | Full table scan | NO (use CHECK constraint) |
| ADD FK CONSTRAINT | Full scan, locks both tables | NO |
| CREATE INDEX | Full scan, blocks writes | NO (use CONCURRENTLY) |

### Safe Alternatives

- **NOT NULL:** `ADD CONSTRAINT CHECK (col IS NOT NULL) NOT VALID` (brief lock), then `VALIDATE CONSTRAINT` (allows concurrent writes)
- **Unique constraint:** `CREATE UNIQUE INDEX CONCURRENTLY` then `ADD CONSTRAINT USING INDEX`
- **Foreign key:** `ADD CONSTRAINT FK NOT VALID` then `VALIDATE CONSTRAINT` separately
- **Index:** Always `CREATE INDEX CONCURRENTLY` -- never inside a transaction

## Expand-and-Contract for Column Changes

Renaming or retyping a column requires multiple deploys:

1. **Expand:** Add the new column (nullable, instant in PG11+)
2. **Dual-write:** Install trigger syncing old->new column on every write
3. **Backfill:** Batch-update historical rows with checkpointing
4. **Switch reads:** Feature-flag application to read from new column
5. **Contract:** Drop old column and trigger after validation period

The **pgroll** tool by Xata automates this entire sequence with versioned schema views so old and new application code run simultaneously.

## Safe Backfilling

Update in small batches using primary key ranges, committing each batch independently:

```sql
-- Use FOR UPDATE SKIP LOCKED to avoid contention with app writes
-- Start with 10,000 rows/batch, reduce for heavily indexed tables
-- Monitor replica lag: if > 1 second, increase sleep or reduce batch
-- Persist progress to a checkpoint table for resumability
UPDATE target SET new_col = compute(old_col)
WHERE id IN (SELECT id FROM target WHERE id > $last_id
             AND new_col IS NULL ORDER BY id LIMIT 10000
             FOR UPDATE SKIP LOCKED);
```

After backfill: add NOT NULL safely via `ADD CHECK NOT VALID` then `VALIDATE CONSTRAINT`.

## Rollback Strategy

Most tools advertise rollback but only handle schema rollback -- data rollback requires manual planning. The safest approach:

1. **Feature flag approach:** Stop reading/writing the new column via toggle. Observe for a sprint. Then plan the actual DROP.
2. **Compensating forward migration:** Write a new migration that reverses the change, preserving the audit trail.
3. **Shadow migration pattern (Stripe):** Keep old table synced via triggers/CDC. Enables near-instant revert with zero data loss.

Some migrations are inherently irreversible: type narrowing, column merges, lossy conversions. For these, always keep the old structure through the entire migration window and take a logical backup before the irreversible step.

## Kafka Schema Evolution

- **BACKWARD compatibility** (default): new schemas read old data -- deploy consumers first
- **FORWARD compatibility:** old schemas read new data -- deploy producers first
- **Avro:** Always add fields with defaults. Never reorder fields.
- **Protobuf:** Never change or reuse field numbers. Names are wire-safe.
- Set `auto.register.schemas=false` in production. Register schemas only through CI after compatibility checks.

## Cross-Service Coordination

When adding a field between services: deploy the consumer first with the field as optional, then the producer. Use consumer-driven contract testing (Pact) to encode this in CI. The Tolerant Reader pattern (ignore unknown fields) enables independent evolution.

## CI Integration

Test migrations against production-sized data. A migration completing in milliseconds on 100 rows may take minutes on 10 million. Clone production data to staging, run with `\timing`, monitor `pg_stat_progress_create_index` during index builds.
