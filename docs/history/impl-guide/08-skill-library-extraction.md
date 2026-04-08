# 08 — Phase 8: Skill Library Extraction (parallel)

This is independent of the code phases. It can run on a separate Claude Code session at any time. The output is content (markdown files), not code.

## What this is

Eric has 61 deep-research files covering specific engineering domains:
- Postgres connection pooling, replication, partitioning
- Kafka operations, consumer groups, schema registry
- GraphQL federation, persisted queries
- Stripe billing, webhooks, metering
- Multi-tenant SaaS architecture
- Cloudflare Workers patterns
- React Native bridges, native modules
- iOS / Swift / SwiftUI patterns
- Tauri / Electron desktop
- Hedera / blockchain protocols
- Postgres + pgvector
- Authentication patterns (OAuth, SSO, MFA)
- Authorization (RBAC, ABAC, ReBAC, OPA, Spicedb)
- Rate limiting, circuit breaking, retries
- Distributed tracing, OpenTelemetry
- Vector databases (Pinecone, Weaviate, Qdrant)
- ... and many more

Each file is 150–800 lines of distilled production wisdom. Together they're hundreds of pages of high-quality content. Your job in this phase is to convert each file into a SKILL.md following the Trail of Bits standard so Stoke can use them as its skill library.

## The format you're converting to

From research [TOB], every SKILL.md must have:

1. **YAML frontmatter** with `name`, `description`, optional `triggers`, optional `allowed-tools`
2. **A "When to Use" section** with concrete scenarios
3. **A "When NOT to Use" section** with scenarios where another approach is better
4. **Body content** under 500 lines, under 2,000 words, providing **behavioral guidance** (not reference dumps)
5. **A "Gotchas" section** with explicit anti-patterns and rebuttals
6. **(For security/critical topics) A "Rationalizations to Reject" section** preempting LLM dismissals
7. **Optional `references/` subdirectory** for progressive disclosure (one level deep only)

The skill should teach Claude **how to think** about a category of work, not provide step-by-step scripts.

## Mapping research files to skill names

For each research file, the skill name is in kebab-case, gerund form preferred. Examples:

| Research file | Skill name |
|---|---|
| Postgres connection pooling deep dive | `postgres` (general), with `references/connection-pooling.md` |
| Kafka consumer groups, rebalancing, offset management | `kafka` |
| GraphQL federation, persisted queries, subscriptions | `graphql` |
| Stripe billing webhooks idempotency | `stripe` |
| React Native bridges, JSI, TurboModules | `react-native` |
| Multi-tenant SaaS isolation patterns | `multi-tenant-saas` |
| Cloudflare Workers durable objects, queues | `cloudflare-workers` |
| Hedera consensus service, token service | `hedera` |
| pgvector + Postgres semantic search | `pgvector` |
| OAuth 2.0 + PKCE + token rotation | `oauth` |
| OpenTelemetry instrumentation Go | `opentelemetry` |
| Distributed tracing with Tempo / Jaeger | `distributed-tracing` |
| Rate limiting with Redis sliding window | `rate-limiting` |
| Circuit breakers, bulkheads, retries with backoff | `resilience-patterns` |
| Vector databases comparison Pinecone Weaviate | `vector-databases` |
| Tauri commands, IPC, security | `tauri` |
| Electron security best practices | `electron` |
| iOS background tasks, push notifications | `ios` |
| SwiftUI state management | `swiftui` |

Generally one research file → one skill, but sometimes a single research file should become multiple skills (e.g., a 700-line "complete authentication and authorization" file might become both `oauth` and `rbac`).

## Conversion procedure

### Step 1: Read the research file in full

Don't skim. The whole file matters because you're extracting the parts that change agent behavior, not the parts that explain background.

### Step 2: Identify what's actionable vs. background

From research [P63]: "Over 60% of skill body content is non-actionable — preamble, verbose explanation, and redundant context that dilutes the model's attention without improving behavior."

When reading the source file, mentally tag each paragraph as one of:

- **Behavioral** — tells the agent what to do or not do (KEEP)
- **Decision criteria** — helps the agent choose between approaches (KEEP)
- **Gotcha** — non-obvious failure modes (KEEP, this is the highest-value content)
- **Background** — explains the topic to a human reader (DROP unless critical for understanding)
- **Reference data** — full API listings, exhaustive tables (MOVE to `references/`)

### Step 3: Write the SKILL.md following this template

```markdown
---
name: <skill-name-in-kebab-case>
description: <one sentence in third-person voice describing when to use this skill, including trigger phrases>
triggers:
  - <natural language phrase that should activate this skill>
  - <another phrase>
  - <a third phrase>
---

# <Title — same as the topic, properly capitalized>

<2-3 sentence summary of what this skill teaches the agent.>

## When to Use

- When working with <specific scenario>
- When implementing <specific feature>
- When debugging <specific class of problem>
- When choosing between <option A> and <option B>

## When NOT to Use

- When the task is purely <unrelated thing> — use the `<other-skill>` skill instead
- When working in a <context where this doesn't apply>

## Core Patterns

<The actionable content. 2-5 H3 sections, each 5-30 lines.>

### <Pattern 1>

<2-5 sentences explaining the pattern, with code or config snippets where they significantly aid clarity. Snippets should be minimal — full examples go in references/.>

### <Pattern 2>

...

## Gotchas

<This is the highest-value section. List specific failure modes the agent should avoid.>

- **<Anti-pattern name>** — <what the agent might do wrong> — <why it's wrong> — <what to do instead>
- **<Another anti-pattern>** — ...

## Rationalizations to Reject

<Only for security/critical/correctness-sensitive skills. Lists rationalizations the agent might use to dismiss the gotchas above.>

- **"This is just a prototype, we don't need to handle <X>."** — In practice, prototypes ship to production. Do it right the first time.
- **"The framework handles this for us."** — Verify, don't assume. Read the framework's actual behavior under your specific conditions.

## See Also

- references/<advanced-topic>.md — for deep-dive on <topic>
- references/<another>.md — for <other deep-dive>
- The `<related-skill>` skill — for <related but separate concern>
```

### Step 4: Move overflow content to references/

If the skill body exceeds 500 lines or 2,000 words, the excess goes to `references/<topic>.md` files. Each reference file:

- Has its own descriptive title
- Is also under 500 lines
- Does NOT link to other reference files (one-level-deep rule)
- Is loaded only when the SKILL.md explicitly requests it

### Step 5: Validate the SKILL.md

Run through this checklist for every skill you produce:

1. ✅ YAML frontmatter parses (no tab indentation, valid YAML)
2. ✅ `name` is kebab-case, ≤64 characters
3. ✅ `description` is third-person, includes trigger phrases
4. ✅ "When to Use" has 3+ concrete scenarios
5. ✅ "When NOT to Use" has 1+ scenarios
6. ✅ Body is under 500 lines, under 2,000 words
7. ✅ "Gotchas" section has 3+ entries with rebuttals
8. ✅ Body provides behavioral guidance, not just reference material
9. ✅ No reference chain longer than one level deep
10. ✅ No content that Claude already knows from training (no need to teach what `psql` is)

## Where to put the skill files

The library lives at `~/.stoke/skills/` (the user's home directory). Each skill is its own directory:

```
~/.stoke/skills/
  postgres/
    SKILL.md
    references/
      connection-pooling.md
      replication.md
      partitioning.md
  kafka/
    SKILL.md
    references/
      consumer-groups.md
      schema-registry.md
  ...
```

The wizard (Phase 2) copies these to project-local `.stoke/skills/` based on the detected stack.

## Bulk extraction strategy

Doing 61 conversions one-at-a-time is slow. Better approach:

### Approach A: Batched extraction with a template

Write a single prompt for Claude that includes:
- The Trail of Bits SKILL.md template (as above)
- The validation checklist
- A "convert this research file to a SKILL.md" instruction

Run it against 5–10 research files at a time. Review the output for one or two before bulk-applying.

### Approach B: Two-pass extraction

Pass 1: For each research file, extract just the gotchas and decision criteria into a draft skill body. Aim for 200 lines per skill.

Pass 2: Add the When/When NOT/References sections to each draft.

This separates the high-value extraction (gotchas) from the structural work (sections).

### Approach C: Skill-improver loop

Trail of Bits has a `skill-improver` agent that takes a draft skill, runs it through a `skill-reviewer` agent, applies fixes, and repeats until quality gates pass. You can mimic this:

1. Convert the research file to a draft SKILL.md (rough)
2. Run the validation checklist as a Claude prompt: "Review this skill against the checklist below and tell me what to fix"
3. Apply the fixes
4. Re-validate
5. Stop when no more fixes are needed (typically 2–3 iterations)

This produces higher quality at the cost of more LLM calls.

## Example conversion

To make this concrete, here's how the Postgres research file would convert.

**Source (excerpt from research file):**

> PostgreSQL connection pooling is critical for production workloads. The default postmaster process can handle thousands of connections in theory but in practice each connection consumes 5-10MB of RAM and the planner overhead becomes prohibitive past 200 active connections. PgBouncer is the most common solution, supporting three pool modes: session (one client per connection for the session lifetime), transaction (one client per connection per transaction), and statement (one client per connection per statement). Transaction mode is the standard for web workloads but breaks features that rely on session state like prepared statements, advisory locks, and SET LOCAL. PgCat is a newer Rust-based alternative offering load balancing, sharding, and connection mirroring. The pool size formula from the Hikari project is connections = (cores × 2) + effective_spindle_count. ... [continues for 600 lines]

**Output SKILL.md:**

```markdown
---
name: postgres
description: Use this skill when working with PostgreSQL — schema design, queries, migrations, connection pooling, replication, performance tuning. Triggers on "postgres", "pg", "psql", "pgvector", "pgx", "pq", "GORM postgres".
triggers:
  - postgres
  - postgresql
  - pg connection pool
  - pgbouncer
  - postgres replication
  - pgvector
  - postgres performance
---

# PostgreSQL

This skill provides production patterns for PostgreSQL including connection pooling, schema design, query performance, and replication. Use it whenever working with a Postgres-backed application.

## When to Use

- When designing or modifying database schemas
- When writing queries that touch more than one table or use complex predicates
- When configuring connection pooling (PgBouncer, PgCat, application-side)
- When debugging slow queries, connection exhaustion, or lock contention
- When setting up replication, failover, or read replicas
- When choosing between Postgres native features and application-layer solutions

## When NOT to Use

- For purely DML scripting (use the standard SQL skill)
- For NoSQL data modeling (use the appropriate document/key-value skill)
- For data warehousing or columnar workloads (consider ClickHouse or BigQuery skills)

## Core Patterns

### Connection pooling

Always use a connection pool. Direct application connections to Postgres exhaust resources past ~200 concurrent connections (each consumes 5-10MB RAM and planner overhead becomes prohibitive).

For most web workloads:
- **PgBouncer in transaction pool mode** is the default. Pool size = `(cores × 2) + effective_spindle_count`.
- **PgCat** if you need load balancing across replicas, sharding, or query mirroring.
- **Application-side pooling** (HikariCP, pgx pool, SQLAlchemy pool) is acceptable for low-fanout services but doesn't scale across instances.

Transaction pool mode breaks: prepared statements, advisory locks, `SET LOCAL`, listen/notify, and any session state. If your code uses these, switch to session mode or work around them.

### Schema design

Default to `BIGINT` primary keys via `GENERATED BY DEFAULT AS IDENTITY` (Postgres 10+). Avoid SERIAL — it's deprecated in favor of identity columns. Avoid UUIDs as primary keys unless distribution or unguessability is required; UUIDs are 4× larger and slower for joins.

Use `TIMESTAMPTZ` not `TIMESTAMP`. Postgres stores timestamps in UTC internally; the difference is whether the client sees the original timezone offset.

Add `NOT NULL` to every column unless absence is genuinely meaningful. Three-valued logic from `NULL` causes more bugs than the storage cost saves.

### Indexes

Default to B-tree. Use:
- **Partial indexes** for queries that filter on a constant predicate
- **Covering indexes** (`INCLUDE`) for index-only scans
- **GIN** for `jsonb`, `tsvector`, and array columns
- **BRIN** for time-series tables that grow append-only

Run `EXPLAIN (ANALYZE, BUFFERS)` before assuming an index is being used. The query planner estimates can be wildly wrong on small tables.

### Migrations

Use a migration tool (sqlx-migrate, golang-migrate, Alembic, Atlas). Never apply schema changes via psql directly to production. Lock-aware migrations are mandatory at scale: adding a column with a default rewrites the entire table on Postgres < 11. Use `ADD COLUMN ... DEFAULT NULL` then backfill in batches, then add the constraint.

### Replication

Streaming replication is the default. For high availability use Patroni + etcd/ZooKeeper or one of the managed solutions (Crunchy Bridge, Aiven, Neon). Logical replication is the right tool for moving data between schemas, doing zero-downtime upgrades, or feeding analytics warehouses.

## Gotchas

- **Long-running transactions block VACUUM.** A transaction that stays open for hours prevents Postgres from cleaning up dead tuples, leading to bloat. Set `idle_in_transaction_session_timeout` and monitor `pg_stat_activity` for long-running transactions.

- **Transaction pool mode silently breaks `SET LOCAL`.** PgBouncer in transaction mode reuses the connection across transactions, so any session state set by one transaction persists into another client's transaction. The fix is either session pool mode or moving the setting into application code.

- **`SELECT FOR UPDATE` doesn't lock rows that don't exist yet.** Use advisory locks or unique constraints to prevent insert races. The classic "check then insert" pattern is broken without these.

- **`COUNT(*)` is O(n) on Postgres.** There's no precomputed row count. For large tables, use `pg_class.reltuples` for an estimate or maintain a counter table.

- **JSONB queries can defeat indexing if you use the wrong operator.** `->>` for text extraction defeats GIN indexes; use `@>` containment for index hits.

- **Postgres dates have a timezone surprise.** `now()` returns the timestamp in the session timezone, but if your client is set to UTC and your data was inserted by a client in another zone, comparisons can be off. Always use `TIMESTAMPTZ` and explicit timezone conversions.

- **Sequence gaps from rolled-back transactions.** Identity columns and sequences are not transactional — a rolled-back insert still consumes the sequence value. Don't rely on sequential numbering for business logic.

- **Vacuum freeze on append-only tables can pause writes.** Tables that are mostly inserts will eventually hit the `autovacuum_freeze_max_age` threshold and trigger an aggressive vacuum that can lock the table. Tune the freeze settings or use `VACUUM FREEZE` proactively in low-traffic windows.

## Rationalizations to Reject

- **"This is just for reads, we don't need to think about pooling."** Read connections still consume resources. A read-only service still needs a pool.
- **"We'll add an index later if it gets slow."** Index changes on multi-million row tables require careful migration planning. Add the right indexes during initial schema design.
- **"We can't use a migration tool because the schema is already in production."** Initialize the migration tool with a baseline. Every modern migration tool supports this.

## See Also

- references/connection-pooling.md — full PgBouncer and PgCat configuration deep dive
- references/replication.md — streaming, logical, Patroni, failover
- references/performance.md — EXPLAIN, autovacuum tuning, statistics
- references/jsonb.md — schema, indexing, query patterns for jsonb columns
- The `pgvector` skill — for semantic search and embeddings
```

That's the format. ~250 lines for the SKILL.md, with overflow content moved to references/. The Postgres research file likely contains 600+ lines of source material, but only the actionable behavioral guidance + gotchas survives the conversion. Everything else either goes to references or gets dropped because Claude already knows it.

## Quality bar

Eric will rerun the wizard against several real projects and use the resulting skill set in production. Skills that don't change agent behavior are noise that dilutes the prompt and reduces compliance with skills that do matter [P63]. So:

- **Skills that just describe the technology are useless.** Drop them.
- **Skills that just list the API are useless.** Claude knows the API.
- **Skills that capture the gotchas and decision criteria are gold.** Keep these dense.

If a research file doesn't yield at least 3 substantive gotchas, the resulting skill is probably not worth shipping. Note it in `STOKE-IMPL-NOTES.md` and skip it.

## Output checklist

When you're done, you should have:

1. `~/.stoke/skills/<n>/SKILL.md` for each successful conversion
2. `~/.stoke/skills/<n>/references/*.md` for any overflow
3. A list in `STOKE-IMPL-NOTES.md` of:
   - Skills successfully extracted
   - Research files skipped (with reasons)
   - Open questions for Eric

## Validation

Run the validation gate from Phase 1:

```bash
stoke skill list
```

Should show all the new skills.

```bash
stoke skill show postgres
```

Should print the SKILL.md content.

```bash
stoke skill select  # in a postgres-using repo
```

Should rank `postgres` near the top of matches.
