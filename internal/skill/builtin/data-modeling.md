# data-modeling

> Data modeling, schema design, migration strategies, and temporal data patterns

<!-- keywords: data model, schema, migration, normalization, entity relationship -->

## Entity-Relationship Modeling

1. **Start with nouns from the domain.** User, Order, Product, Invoice -- each becomes a table. Verbs become join tables or foreign keys.
2. **Every table gets a surrogate primary key.** Use UUIDs for distributed systems, auto-increment bigint for single-database setups. Never use natural keys (emails change, SSNs get reissued).
3. **Name foreign keys consistently.** `user_id` references `users.id`. Never `uid`, `usr`, or `user_fk`.
4. **Add `created_at` and `updated_at` to every table.** Use database-level defaults (`DEFAULT NOW()`). These cost nothing and save hours of debugging.

## Normalization vs Denormalization

**Normalize first, denormalize for performance.** Start at 3NF. Denormalize only when you have measured query performance problems.

When to denormalize:
- **Read-heavy dashboards.** Materialize aggregates into summary tables updated by background jobs.
- **Avoiding expensive JOINs.** Store `user_name` on `orders` if you show it on every order list and users table has millions of rows.
- **Counters.** Maintain `comments_count` on `posts` instead of `COUNT(*)` every time.

Always keep the normalized source of truth. Denormalized data is a cache -- treat it that way.

## Schema Migration Strategies

### Expand-Contract (Recommended)
1. **Expand:** Add the new column/table. Old code ignores it. Deploy.
2. **Migrate:** Backfill data. Dual-write from application code.
3. **Contract:** Remove old column/table after all readers are updated.

This enables zero-downtime deployments. Never rename a column in one step.

### Migration Safety Rules
- Never hold locks on large tables. Use `pt-online-schema-change` (MySQL) or `CREATE INDEX CONCURRENTLY` (Postgres).
- Every migration must be reversible. Write the `down` migration before the `up`.
- Test migrations against production-sized data. A migration that takes 2ms on dev can lock production for 20 minutes.

## Soft Deletes vs Hard Deletes

**Soft deletes** (`deleted_at TIMESTAMP NULL`):
- Pros: Audit trail, easy undo, referential integrity preserved
- Cons: Every query needs `WHERE deleted_at IS NULL`, indexes bloat, GDPR complicates it
- Add a partial index: `CREATE INDEX idx_active_users ON users(id) WHERE deleted_at IS NULL`

**Hard deletes** with audit log:
- Pros: Clean data, simpler queries, smaller tables
- Cons: Requires separate audit table, no easy undo
- Write to `audit_log(table_name, record_id, action, old_data, timestamp)` before deleting

**Recommendation:** Use soft deletes for user-facing data (orders, accounts). Use hard deletes with audit log for internal operational data.

## Temporal Data Patterns

### Effective Dating
```sql
CREATE TABLE prices (
  product_id  BIGINT NOT NULL,
  amount_cents INT NOT NULL,
  effective_from TIMESTAMP NOT NULL,
  effective_to   TIMESTAMP, -- NULL = currently active
  EXCLUDE USING gist (product_id WITH =, tsrange(effective_from, effective_to) WITH &&)
);
```
- Use exclusion constraints (Postgres) to prevent overlapping periods
- Query current price: `WHERE effective_from <= NOW() AND (effective_to IS NULL OR effective_to > NOW())`

### Audit Trails
Append-only event table. Never update, never delete.
```sql
CREATE TABLE entity_history (
  id BIGSERIAL, entity_type TEXT, entity_id BIGINT,
  field_name TEXT, old_value TEXT, new_value TEXT,
  changed_by BIGINT, changed_at TIMESTAMP DEFAULT NOW()
);
```

## Polymorphic Associations

**Avoid the `type + id` anti-pattern** (`commentable_type = 'Post'`, `commentable_id = 123`). No foreign key constraints possible.

**Better alternatives:**
1. **Separate join tables.** `post_comments`, `video_comments` -- each with proper FK constraints.
2. **Shared parent table.** `commentables(id)` as parent, `posts` and `videos` both FK to it. Comments FK to `commentables`.

## Multi-Tenancy Data Isolation

| Strategy | Isolation | Complexity | Cost |
|----------|-----------|------------|------|
| Shared table + `tenant_id` | Low | Low | Low |
| Schema per tenant | Medium | Medium | Medium |
| Database per tenant | High | High | High |

For shared tables: enforce `tenant_id` via Row-Level Security (Postgres) or application middleware. Never trust application code alone -- one missed `WHERE` clause leaks data.
```sql
ALTER TABLE orders ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON orders USING (tenant_id = current_setting('app.tenant_id')::int);
```

## JSON Columns vs Normalized Tables

Use JSON columns for:
- **Truly schemaless data.** User preferences, form responses, third-party webhook payloads.
- **Data you never filter or join on.** If you query by a field, it belongs in a column.

Use normalized tables for:
- **Anything you WHERE, JOIN, or aggregate on.** JSON extraction is slow and unindexable (without expression indexes).
- **Data with referential integrity needs.** JSON cannot enforce FK constraints.

If you must query JSON, add a generated column or expression index:
```sql
ALTER TABLE events ADD COLUMN event_type TEXT GENERATED ALWAYS AS (payload->>'type') STORED;
CREATE INDEX idx_event_type ON events(event_type);
```
