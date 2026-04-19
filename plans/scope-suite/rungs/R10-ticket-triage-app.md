# R10 — Ticket triage app (two services + real state)

A small internal tool: submitters open tickets, triagers assign +
resolve them. Two services (web + worker), a real data layer (SQLite
via better-sqlite3), and a queue that survives restarts. Tests whether
the harness can hold a vertical slice that crosses a process boundary.

Assumes R09 passed — this adds persistence + a second service.

## Scope

**types package** (`packages/types/src/tickets.ts`):
- Zod schemas for `Ticket`, `TicketEvent`, `TicketStatus`, `TicketSeverity`
- `CreateTicketInput`, `AssignTicketInput`, `ResolveTicketInput`
- A `ticketsShared` const array of the severities so both services
  reference the same source of truth

**db package** (`packages/db/src/index.ts`):
- Opens a better-sqlite3 connection from `DATABASE_URL` or the default
  `./tickets.db`
- Exposes `getDB(): Database` singleton
- `migrate()` creates `tickets` + `ticket_events` tables on first call
- Prepared-statement helpers: `insertTicket`, `updateStatus`,
  `appendEvent`, `listTickets(query)`, `getTicket(id)`

**api-client package** (`packages/api-client/src/tickets.ts`):
- Typed methods for each web API endpoint below
- Every method narrows response via Zod parse (no `as any`)

**Web service** (`apps/web`):
- `POST /api/tickets` — creates ticket + initial event, 201
- `GET /api/tickets` — lists, honors status/severity/assignee filters
- `GET /api/tickets/[id]` — fetches ticket + all events
- `PATCH /api/tickets/[id]` — assigns or resolves (validates transition)
- `/tickets` page — server component lists open tickets
- `/tickets/[id]` page — detail view + action panel

**Worker service** (`apps/worker`):
- Long-running Node process (`pnpm --filter worker start`)
- Polls `tickets` table every 2s for status=`new`
- Auto-assigns `severity=critical` tickets to the on-call rotation
  (round-robin over a hardcoded 3-name list)
- Writes a `TicketEvent` to record the auto-assignment
- Exposes a `/health` HTTP endpoint on port 8787 so tests can probe it

**Tests**:
- `packages/types/__tests__/tickets.test.ts` — schemas + transitions
- `packages/db/__tests__/migrate.test.ts` — migrate creates tables
  idempotently; inserts + selects roundtrip
- `apps/web/app/api/tickets/__tests__/route.test.ts` — happy paths +
  invalid-transition rejection (400)
- `apps/worker/__tests__/assign.test.ts` — feed it a critical-severity
  new ticket, assert an auto-assign event appears and the ticket's
  status changes to `assigned` within 5 seconds

## Acceptance

- All declared files exist, `pnpm build` succeeds
- `pnpm test` passes every case (≥15 test cases)
- After `pnpm --filter worker start` in the background, POSTing a
  critical ticket auto-assigns it within 5s
- `DELETE FROM tickets; DELETE FROM ticket_events;` followed by a fresh
  migrate leaves both tables empty with correct schemas (migrate is
  idempotent)

## What NOT to do

No auth (R09 covers that), no email notifications, no websockets, no
real on-call schedule. The 3-name round-robin is the whole assignment
logic. SQLite on disk; no Postgres, no Redis. Keep worker as a plain
`setInterval`; no BullMQ / message queue. The point is proving two
services + persistence + typed contract across a process boundary.
