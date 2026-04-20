# R10-rn — Ticket triage app (multi-screen with filter + detail + actions)

Heaviest RN rung. Multi-screen app with list + filter + detail +
status actions. All API mocked; tests exercise full user flows.

## Scope

Structure:
- `App.tsx` → NavigationContainer + stack nav (TicketList →
  TicketDetail).
- `src/api/tickets.ts`:
  - `listTickets(filter?: {status?: string}): Promise<Ticket[]>`
  - `getTicket(id): Promise<Ticket>`
  - `updateTicketStatus(id, status): Promise<Ticket>`
- `src/screens/TicketList.tsx`:
  - Filter bar with "All" / "Open" / "In Progress" / "Closed" buttons
    (`testID="filter-all|open|inprogress|closed"`), active filter bolded.
  - Fetches on mount + on filter change.
  - List rendered, tappable rows navigate to detail.
  - Loading + error + empty states.
- `src/screens/TicketDetail.tsx`:
  - Full ticket display: title, description, status, assignee, created.
  - Three action buttons: "Start" (to in-progress), "Close" (to closed),
    "Reopen" (to open). Only show relevant ones for current status.
  - Status update calls `updateTicketStatus` then refreshes ticket.

`Ticket = { id, title, description, status: "open"|"in_progress"|"closed", assignee: string, createdAt: string }`.

## Acceptance

- `pnpm install` + `pnpm test` exit 0.
- At least 6 tests: list rendering, each filter, detail rendering,
  status transition (Start then Close).
- `global.fetch` always mocked.
- No real navigation deps beyond the installed nav library.

## What NOT to do

- No persistence.
- No real backend.
- No optimistic UI updates (just refresh after mutation).
- No pagination / infinite scroll.
