# R04 — Single CRUD endpoint + schema + test

One HTTP route handler, one Zod schema, one happy-path integration
test. Tests whether stoke can cleanly produce a small feature across
3 coordinated files.

## Scope

Build a Next.js App Router project that implements a `/api/tasks`
endpoint:

- `GET /api/tasks` → returns `{ tasks: Task[] }`, 200 OK
- `POST /api/tasks` → accepts `{ title: string }` body, returns created
  `Task`, 201 Created. Invalid body → 400 Bad Request.
- In-memory storage (array in the route module is fine — no database).

Zod schema `packages/types/src/task.ts`:

```ts
import { z } from "zod";
export const TaskSchema = z.object({
  id: z.string().uuid(),
  title: z.string().min(1).max(200),
  createdAt: z.string().datetime(),
});
export type Task = z.infer<typeof TaskSchema>;
```

Test `app/api/tasks/__tests__/route.test.ts` verifies:
- GET returns empty array initially
- POST with `{ title: "first" }` succeeds, returns 201 with Task
- GET after POST returns the created Task
- POST with empty body returns 400

## Acceptance

- `app/api/tasks/route.ts` exports `GET` and `POST` handlers
- `packages/types/src/task.ts` exports `TaskSchema` and `Task`
- Test runs via `npm test` (vitest preferred) and all 4 cases pass
- `next build` succeeds without type errors

## What NOT to do

No auth, no database, no UI, no cross-route concerns. One endpoint.
