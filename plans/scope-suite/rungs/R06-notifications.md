# R06 — Notification preferences across monorepo

A cross-package feature that requires the types package + api-client +
API routes + UI + tests to all stay in sync. Tests whether stoke can
hold a consistent contract across 4+ packages simultaneously.

Assumes R03 (monorepo) passed — this builds on that structure.

## Scope

**types package** (`packages/types/src/notifications.ts`):
- Zod schema `NotificationPreferences` with fields:
  - `email: boolean`, `sms: boolean`, `push: boolean`
  - `digest: "instant" | "hourly" | "daily"`
  - `quietHoursStart: string` (HH:MM format)
  - `quietHoursEnd: string` (HH:MM format)

**api-client package** (`packages/api-client/src/notifications.ts`):
- `getPreferences(userId: string): Promise<NotificationPreferences>`
- `updatePreferences(userId, prefs): Promise<NotificationPreferences>`

**API routes** (`apps/web/app/api/v1/users/[id]/notifications/route.ts`):
- `GET` returns the current preferences (in-memory storage OK)
- `PATCH` validates body against schema, updates storage, returns new

**UI** (`apps/web/app/settings/notifications/page.tsx`):
- React component reads current preferences via api-client
- Form with all fields as controlled inputs
- Submit calls `updatePreferences`, shows success message on 200

**Tests**:
- `packages/types/__tests__/notifications.test.ts` — schema accepts valid,
  rejects invalid (bad digest enum, bad HH:MM)
- `apps/web/app/api/v1/users/[id]/notifications/__tests__/route.test.ts`
  — GET returns defaults, PATCH with valid body updates, PATCH with
  invalid body returns 400

## Acceptance

- All 7 files exist as declared
- `pnpm build` succeeds cross-package
- `pnpm test` passes all test cases
- The API response shape matches exactly what the types package
  declares (no `any` escape hatches)

## What NOT to do

No database, no push service integration, no email provider. In-memory
storage is fine. The point is contract consistency across files.
