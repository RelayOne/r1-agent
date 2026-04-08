# postgres-connection-pooling

> PgBouncer configuration, pool sizing formulas, exhaustion detection, and Go/Node.js driver tuning for PostgreSQL

<!-- keywords: pgbouncer, connection pool, pgx, postgres, postgresql, pool exhaustion, prepared statement, transaction mode, max_connections -->

## Critical Rules

1. **Use PgBouncer in transaction mode for stateless workloads.** Transaction mode assigns a server connection only for the duration of a transaction, enabling 100 DB connections to serve 5,000+ clients. Session mode offers minimal benefit (1:1 proxy).

2. **Smaller pools perform better.** The canonical formula: `connections = (cpu_cores * 2) + effective_spindle_count`. For SSDs: `cpu_cores * 2 + 1`. An 8-core server with SSD needs only 17 active connections. Oracle demonstrated a 50x improvement by reducing a pool from 2,048 to 96.

3. **Never increase max_connections beyond 200-300.** Each PostgreSQL connection is a forked OS process consuming ~10MB RAM. `GetSnapshotData()` must scan all processes at transaction start. Citus Data showed a single query slowed by >2x with 5,000 idle connections.

4. **Every Query() must have a Close().** Missing `defer rows.Close()` or `defer tx.Rollback()` leaks connections. In pgx, a checked-out connection holding `rows` open while another query needs a connection from a full pool causes deadlock.

## Transaction Mode Breaks Session State

PgBouncer transaction mode breaks features that depend on session state:

- **Prepared statements**: Cause `prepared statement "X" does not exist`. Fix: set `max_prepared_statements = 200` in PgBouncer 1.21+, or use `default_query_exec_mode=simple_protocol` in pgx. In node-postgres: `{ prepare: false }`.
- **SET commands**: Session state vanishes when the connection returns to pool. Fix: use `SET LOCAL` inside a transaction.
- **Advisory locks**: `pg_advisory_lock()` persists at session level. Use `pg_advisory_xact_lock()` instead (releases at transaction end).
- **LISTEN/NOTIFY**: Requires a persistent connection. Run a separate session-mode pool on a different port.
- **Temporary tables**: Only `ON COMMIT DROP` works. `ON COMMIT PRESERVE ROWS` breaks.

## Essential PgBouncer Configuration

```ini
pool_mode = transaction
default_pool_size = 25
min_pool_size = 5
reserve_pool_size = 5
reserve_pool_timeout = 3
max_client_conn = 5000
max_db_connections = 100
server_idle_timeout = 300
server_lifetime = 3600
query_wait_timeout = 60
idle_transaction_timeout = 30
max_prepared_statements = 200
auth_type = scram-sha-256
```

`max_client_conn` controls connections to PgBouncer (~2KB each). `default_pool_size` controls connections to PostgreSQL (~10MB each). The multiplexing ratio is `max_client_conn / pool_size`.

## Pool Sizing for Multi-Instance Deployments

If N app instances each have pool size P, total `N * P` must not exceed `max_connections - superuser_reserved_connections` (default 3). With PgBouncer: each app connects to PgBouncer with a larger pool; PgBouncer maintains the smaller optimal pool to PostgreSQL.

Real-world: 8-core DB, 4 app instances, SSD:
- Formula: (8 * 2) + 1 = 17 connections to PostgreSQL
- Per-instance pool: 17 / 4 = 4 connections each
- With PgBouncer: `default_pool_size = 20`, `max_client_conn = 5000`, each app gets 50 to PgBouncer

Deadlock avoidance formula (HikariCP): `pool_size = Tn * (Cm - 1) + 1` where Tn = max threads, Cm = max simultaneous connections per thread.

## Monitoring and Alerting

Run `SHOW POOLS` on the PgBouncer admin console. Critical fields:

| Alert Condition | Severity | Meaning |
|---|---|---|
| `cl_waiting > 0` sustained >30s | Warning | Pool saturated |
| `cl_waiting > 10` sustained >2m | Critical | Severe exhaustion |
| `maxwait > 5s` | Warning | Clients waiting too long |
| `sv_active == pool_size` with `cl_waiting > 0` | Critical | Fully consumed |

Export to Prometheus: `pgx_pool_acquired_conns` (gauge), `pgx_pool_empty_acquire_total` (counter), `pgx_pool_canceled_acquire_total` (counter). Alert when pool utilization exceeds 80% for 5 minutes.

## Pool Exhaustion Behavior by Driver

- **pgx**: `pool.Acquire(ctx)` blocks until available or context deadline, returns `context.DeadlineExceeded`.
- **database/sql**: Default unlimited connections (`SetMaxOpenConns(0)`) creates connections until PostgreSQL returns `too many clients already`.
- **node-postgres**: Without `connectionTimeoutMillis`, `pool.connect()` hangs indefinitely. Always set it.
- **Prisma**: Throws error P2024: `Timed out fetching a new connection from the connection pool`.

## PgBouncer vs PgCat

PgCat (Rust, multi-threaded) adds built-in read/write splitting, load balancing, and sharding. At >100 connections, PgCat achieves 2x throughput (59K TPS vs PgBouncer 44K TPS). However: PgCat has no 1.0 release, sparse documentation, no SCRAM-SHA-256 client auth, and its creator has left the project. Stick with PgBouncer unless you specifically need built-in query routing.
