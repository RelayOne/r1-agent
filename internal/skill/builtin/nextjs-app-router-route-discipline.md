# nextjs-app-router-route-discipline

> What route.ts files in Next.js 14 App Router can and cannot export, and where helper functions belong

<!-- keywords: nextjs, app router, route.ts, route handler, middleware, api routes, Next.js 14 -->

## The allowed-exports rule

A file at `app/**/route.ts` (or `route.js`) is a Route Handler. Next.js 14 enforces a strict allowlist of named exports from this file. Anything else fails the build with:

```
Type error: Route "app/api/alarms/export/route.ts" does not match the required types of a Next.js Route.
  "fetchAlarmsForExport" is not a valid Route export field.
```

Allowed named exports from `route.ts`:

- HTTP verb handlers: `GET`, `POST`, `PUT`, `PATCH`, `DELETE`, `HEAD`, `OPTIONS`
- Segment config consts: `runtime`, `dynamic`, `dynamicParams`, `revalidate`, `fetchCache`, `preferredRegion`, `maxDuration`

Nothing else. Not helpers, not schemas, not types, not default exports.

## Where helpers go

If your handler needs helpers, they go in a separate file — typically `helpers.ts` alongside, or in `lib/`:

```
app/api/alarms/export/
  route.ts              # thin handler, only exports GET/POST/etc
  helpers.ts            # fetchAlarmsForExport, buildCSVRow, ...
```

```ts
// app/api/alarms/export/route.ts
import { NextRequest, NextResponse } from 'next/server'
import { fetchAlarmsForExport, buildCSVRow } from './helpers'

export async function GET(req: NextRequest) {
  const alarms = await fetchAlarmsForExport(req.nextUrl.searchParams)
  const csv = alarms.map(buildCSVRow).join('\n')
  return new NextResponse(csv, {
    headers: { 'content-type': 'text/csv' },
  })
}

export const runtime = 'nodejs'
export const dynamic = 'force-dynamic'
```

```ts
// app/api/alarms/export/helpers.ts
export async function fetchAlarmsForExport(params: URLSearchParams) { /* ... */ }
export function buildCSVRow(alarm: Alarm): string { /* ... */ }
```

## Other App Router file conventions

Each special filename has its own export contract. Don't cross the streams.

- `page.tsx`: default export is a React component (server component by default; add `'use client'` at the top to opt into the client). Optional `metadata` named export or `generateMetadata` function.
- `layout.tsx`: default export receives `{ children }`, returns a component that wraps the subtree. Optional `metadata`.
- `loading.tsx`, `error.tsx`, `not-found.tsx`: default export only.
- `middleware.ts` at repo root (or inside `src/`): default-exported `middleware` function plus optional `config` named export with `matcher`.
- Server actions: files with `'use server'` directive at the top. Typically live under `app/actions/` or colocated. Every exported function in such a file becomes a server action.

## Use `next/server` types, not `next`

Route Handlers use `NextRequest` and `NextResponse` from `next/server`. The `NextApiRequest` / `NextApiResponse` types from `next` are the Pages Router API and do not work inside App Router.

```ts
// BAD — pages-router types, will not type-check inside app/
import type { NextApiRequest, NextApiResponse } from 'next'

// GOOD — app-router types
import { NextRequest, NextResponse } from 'next/server'
```

## File layout example

Correct separation of concerns for a non-trivial endpoint:

```
app/api/reports/csv/
  route.ts              # exports GET, runtime, dynamic
  helpers.ts            # data fetching, CSV formatting
  schema.ts             # Zod request-param schema
lib/
  csv.ts                # generic CSV util shared by multiple routes
```

```ts
// route.ts — thin, only legal exports
import { NextRequest, NextResponse } from 'next/server'
import { loadReportRows } from './helpers'
import { querySchema } from './schema'
import { toCSV } from '@/lib/csv'

export async function GET(req: NextRequest) {
  const parsed = querySchema.safeParse(Object.fromEntries(req.nextUrl.searchParams))
  if (!parsed.success) return NextResponse.json({ error: parsed.error }, { status: 400 })
  const rows = await loadReportRows(parsed.data)
  return new NextResponse(toCSV(rows), { headers: { 'content-type': 'text/csv' } })
}

export const dynamic = 'force-dynamic'
```

## Gotchas

- **`route.ts` only exports HTTP verbs + config consts**. Anything else is a build error. Move helpers to a sibling file and import them.
- **No default export from `route.ts`**. Unlike `page.tsx`, Route Handlers are verb-named, not default.
- **`NextRequest`/`NextResponse` come from `next/server`**. `NextApiRequest`/`NextApiResponse` from `next` is the old Pages Router.
- **Config consts are named exports, not an object**: `export const runtime = 'nodejs'`, not `export const config = { runtime: 'nodejs' }` (that's Pages Router).
- **`'use client'` and `'use server'` must be the very first line** (after optional comments). Inside nested blocks they have no effect.
- **Server actions must be async functions** and can only be called from Server Components or form actions.
- **`middleware.ts` lives at the repo root or `src/`**, not under `app/`. It uses a default export plus `config.matcher`.
- **Dynamic segment params** arrive as `{ params }: { params: { slug: string } }` as the handler's second argument — no config needed.
