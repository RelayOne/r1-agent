# R09 — User admin slice (auth + CRUD + UI, stitched)

A small but complete end-to-end vertical slice: authenticated admin
can list/create/edit/delete users in a mini directory. Tests whether
the harness can hold a coherent contract across auth, data, API, and
UI layers simultaneously.

Assumes R05 (login flow) + R06 (cross-package contracts) passed —
this stitches those ideas into one feature.

## Scope

**types package** (`packages/types/src/users.ts`):
- Zod schema `User` with `id`, `email`, `name`, `role: "admin"|"member"`
- `CreateUserInput` omits `id`; `UpdateUserInput` makes all but `id` partial
- `UserListQuery` with optional `search`, `role`, `limit`, `offset`

**api-client package** (`packages/api-client/src/users.ts`):
- `listUsers(query?: UserListQuery): Promise<User[]>`
- `getUser(id): Promise<User>`
- `createUser(input): Promise<User>`
- `updateUser(id, input): Promise<User>`
- `deleteUser(id): Promise<void>`
- All methods throw `UserError` with `code` + `message` on failure

**API routes** (Next.js App Router under `apps/web/app/api/v1/users`):
- `GET /` — lists users, honors query params, `admin` role required
- `POST /` — creates user, `admin` role required, returns 201
- `GET /[id]` — fetches user, `admin` role required
- `PATCH /[id]` — updates user, `admin` role required
- `DELETE /[id]` — deletes user, `admin` role required, returns 204
- All routes validate session cookie via a shared `requireAdmin()` helper
- In-memory storage is fine (a singleton map is OK)

**UI** (`apps/web/app/admin/users/page.tsx` + subroutes):
- Server component fetches list using api-client
- Client component renders table with row-level edit + delete
- `/admin/users/new` has a create form
- `/admin/users/[id]` has an edit form
- Forms call api-client, show success + redirect on 200/201

**Tests**:
- `packages/types/__tests__/users.test.ts` — schemas accept/reject as documented
- `apps/web/app/api/v1/users/__tests__/route.test.ts` — GET+POST happy + auth rejection
- `apps/web/app/api/v1/users/[id]/__tests__/route.test.ts` — GET/PATCH/DELETE happy + 404
- At least one test per endpoint proves the `requireAdmin` gate rejects a `member` session

## Acceptance

- All declared files exist, `pnpm build` succeeds across the monorepo
- `pnpm test` passes every case (≥10 test cases)
- The API rejects a non-admin session on every write endpoint with 403
- Client-visible types match server-returned shapes exactly (no `as any`
  escape hatches in the api-client)

## What NOT to do

No database, no email, no real session store — in-memory maps are
sufficient. No role hierarchy beyond `admin|member`. No pagination UI
(the server can honor query params, but the admin page can render the
first 20 rows). The point is contract coherence across six layers.
