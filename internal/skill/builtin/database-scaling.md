# database-scaling

> Database scaling patterns, connection management, and query optimization

<!-- keywords: database, postgres, mysql, connection, pool, migration, index, query, sql, transaction, deadlock, replication -->

## Critical Rules

1. **Always use connection pooling.** Direct connections exhaust DB limits. Use `pgxpool` (Postgres), connection pool middleware, or PgBouncer. Set `max_connections` based on `(cores * 2) + effective_spindle_count`.

2. **Never hold transactions open across network calls.** Transaction holds locks. If you make an HTTP call mid-transaction, you're holding locks for seconds. Compute first, transact atomically.

3. **Every migration must be reversible.** Include both `up` and `down`. Non-reversible migrations (drop column, rename table) need the expand/contract pattern: add new → migrate data → remove old.

4. **Indexes are not free.** Each index costs write performance and storage. Compound indexes follow leftmost prefix rule. `CREATE INDEX CONCURRENTLY` for production.

5. **`SELECT *` is never acceptable in production code.** Enumerate columns. Schema changes silently break `SELECT *`.

## Connection Exhaustion Patterns

- **N+1 queries:** Loop fetching related records. Fix: `JOIN` or batch `WHERE id IN (...)`.
- **Connection leak:** Missing `defer rows.Close()` or `defer tx.Rollback()`. Every `Query()` must have a `Close()`.
- **Pool starvation:** Long-running queries block the pool. Set `statement_timeout` and `idle_in_transaction_session_timeout`.
- **Prepared statement leak:** `db.Prepare()` without `stmt.Close()`. Each leaks a server-side resource.

## Scaling Patterns

### Read Replicas
- Route reads to replicas, writes to primary
- Accept eventual consistency (typically <100ms lag)
- Never read-after-write from replica without explicit consistency requirement

### Partitioning
- **Range partitioning:** Time-series data by month/quarter
- **Hash partitioning:** Even distribution for high-cardinality keys
- **List partitioning:** Known categories (region, tenant)
- Partition pruning only works when query includes partition key

### BigInt Safety
- JSON `number` overflows at 2^53. Use string representation for IDs > 9007199254740991.
- Go `int` is 64-bit but JS/JSON is not. Always `json:"id,string"` for large IDs.

## Common Gotchas

- **Float money:** Never use `FLOAT` or `DOUBLE` for currency. Use `DECIMAL(19,4)` or integer cents.
- **Timezone:** Store all timestamps as UTC (`TIMESTAMP WITH TIME ZONE`). Convert at display layer.
- **NULL handling:** `NULL != NULL`. Use `IS NULL`/`IS NOT NULL`. In Go, use `sql.NullString` etc.
- **Implicit locks:** `SELECT ... FOR UPDATE` locks rows. `ALTER TABLE` locks the entire table. Plan accordingly.
- **Bulk insert:** Individual `INSERT` in a loop is O(n) round trips. Use `COPY` (Postgres) or multi-value `INSERT`.
