# supabase

> Supabase: Postgres + Auth + Storage + Realtime. One SDK handles all. Row-Level Security (RLS) is load-bearing — never ship a table without policies.

<!-- keywords: supabase, postgres, rls, row level security, supabase auth, supabase storage, realtime, jwt -->

**Official docs:** https://supabase.com/docs  |  **Verified:** 2026-04-14 via web search.

## Install + client

```bash
pnpm add @supabase/supabase-js
```

```ts
import { createClient } from "@supabase/supabase-js";
const supabase = createClient(
  process.env.NEXT_PUBLIC_SUPABASE_URL!,
  process.env.NEXT_PUBLIC_SUPABASE_ANON_KEY!,
);
```

Two keys:
- **Anon key** (`NEXT_PUBLIC_SUPABASE_ANON_KEY`): client-side; RLS policies enforce what it can read/write.
- **Service role key**: server-side only; bypasses RLS. NEVER send this to the browser.

## Auth

Email + password:

```ts
await supabase.auth.signUp({ email, password });
await supabase.auth.signInWithPassword({ email, password });
```

Magic link:

```ts
await supabase.auth.signInWithOtp({ email, options: { emailRedirectTo: `${SITE_URL}/auth/callback` } });
```

OAuth (GitHub, Google, etc.):

```ts
await supabase.auth.signInWithOAuth({ provider: "github", options: { redirectTo: `${SITE_URL}/auth/callback` } });
```

Session shape: access token (JWT, short-lived 5-60 min) + refresh token (single-use, rotating). SDK auto-refreshes.

```ts
const { data: { session } } = await supabase.auth.getSession();
const { data: { user } } = await supabase.auth.getUser();  // verifies JWT against server
```

**Always use `getUser()`, not `getSession()`, to verify authentication on the server.** `getSession()` trusts the local cookie without hitting Supabase.

## Database queries

```ts
// Read
const { data, error } = await supabase
  .from("orders")
  .select("id, total, customer:customers(*)")
  .eq("status", "paid")
  .limit(20);

// Write
await supabase.from("orders").insert({ customer_id: userId, total: 42 });
await supabase.from("orders").update({ status: "shipped" }).eq("id", 1);
await supabase.from("orders").delete().eq("id", 1);
```

`.select("*, relation(*)")` enables embedded joins via foreign-key discovery.

## Row-Level Security (RLS) — REQUIRED

Enable on every table:

```sql
alter table orders enable row level security;

create policy "users see own orders" on orders
  for select using (auth.uid() = customer_id);

create policy "users insert own orders" on orders
  for insert with check (auth.uid() = customer_id);
```

Without policies, the anon key can't read or write anything (closed by default). Forgetting to enable RLS on a table is the #1 Supabase security footgun — it ships with the anon key able to read everything.

## Realtime

```ts
const channel = supabase
  .channel("orders-changes")
  .on("postgres_changes",
      { event: "*", schema: "public", table: "orders", filter: `customer_id=eq.${userId}` },
      (payload) => console.log(payload))
  .subscribe();
```

Events: `INSERT`, `UPDATE`, `DELETE`, `*`. Filter server-side to minimize client work.

## Storage

```ts
await supabase.storage.from("avatars").upload(`users/${userId}.png`, file);
const { data } = supabase.storage.from("avatars").getPublicUrl(`users/${userId}.png`);
// For private buckets:
const { data: signed } = await supabase.storage.from("private").createSignedUrl("path", 60);
```

RLS applies to storage too — configure bucket policies in the dashboard.

## Server-side (Next.js App Router)

```ts
import { createServerClient } from "@supabase/ssr";
import { cookies } from "next/headers";

const supabase = createServerClient(URL, ANON_KEY, {
  cookies: {
    get(name) { return cookies().get(name)?.value; },
    set(name, value, options) { cookies().set({ name, value, ...options }); },
  },
});
```

## Common gotchas

- **Forgetting to enable RLS** → anon key exposes everything.
- **Using service role in browser code** → full DB compromise.
- **Magic link redirect_to not whitelisted** → Supabase rejects silently.
- **JWT audience mismatch in custom auth integrations** → use `aud: "authenticated"` for signed-in users.

## Key reference URLs

- Auth overview: https://supabase.com/docs/guides/auth
- JWTs: https://supabase.com/docs/guides/auth/jwts
- Sessions: https://supabase.com/docs/guides/auth/sessions
- RLS: https://supabase.com/docs/guides/database/postgres/row-level-security
- Server-side auth: https://supabase.com/docs/guides/auth/server-side
- Realtime: https://supabase.com/docs/guides/realtime
