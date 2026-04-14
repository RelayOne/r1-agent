# clerk

> Clerk auth for Next.js, React, Expo. Short-lived (60s) session JWTs with auto-refresh; `clerkMiddleware()` for route protection; `auth()` / `currentUser()` for server components.

<!-- keywords: clerk, clerkauth, user management, next.js auth, session, jwt, clerkmiddleware, useauth -->

**Official docs:** https://clerk.com/docs  |  **Verified:** 2026-04-14 via web search.

## Install + setup (Next.js App Router)

```bash
pnpm add @clerk/nextjs
```

`.env.local`:

```
NEXT_PUBLIC_CLERK_PUBLISHABLE_KEY=pk_test_...
CLERK_SECRET_KEY=sk_test_...
```

Wrap app:

```tsx
// app/layout.tsx
import { ClerkProvider } from "@clerk/nextjs";
export default function RootLayout({ children }) {
  return <ClerkProvider>{children}</ClerkProvider>;
}
```

## Middleware (protect routes)

```ts
// middleware.ts
import { clerkMiddleware, createRouteMatcher } from "@clerk/nextjs/server";

const isProtected = createRouteMatcher(["/dashboard(.*)", "/admin(.*)"]);

export default clerkMiddleware(async (auth, req) => {
  if (isProtected(req)) await auth.protect();
});

export const config = { matcher: ["/((?!.*\\..*|_next).*)", "/", "/(api|trpc)(.*)"] };
```

`auth.protect()` redirects unauthenticated users to `/sign-in` (the Clerk-hosted page by default).

## Server-side auth (App Router)

```ts
// server component or route handler
import { auth, currentUser } from "@clerk/nextjs/server";

const { userId, sessionId, orgId } = await auth();
if (!userId) return NextResponse.redirect("/sign-in");

const user = await currentUser();  // full Backend User object (email, name, metadata)
```

`auth()` is cheap (reads the session cookie). `currentUser()` fetches the user from Clerk's API.

## Client-side hooks

```tsx
import { useAuth, useUser, SignInButton, UserButton } from "@clerk/nextjs";

const { isSignedIn, userId, getToken } = useAuth();
const { user } = useUser();

// Call your backend with a Clerk-issued JWT
const token = await getToken({ template: "my-api-template" });
fetch("/api/private", { headers: { Authorization: `Bearer ${token}` } });
```

Session JWTs are short-lived (60s default) — SDK refreshes automatically. Don't cache the token in long-lived storage.

## Verifying tokens in a non-Next.js backend

```ts
import { verifyToken } from "@clerk/backend";
const jwt = req.headers.authorization?.replace("Bearer ", "");
const payload = await verifyToken(jwt, { secretKey: process.env.CLERK_SECRET_KEY });
// payload.sub is the Clerk user ID
```

Templates let you customize JWT claims per audience (configure in Clerk Dashboard → JWT Templates).

## Webhooks (user lifecycle → your DB)

Configure Endpoint URL in Clerk Dashboard → Webhooks. Events: `user.created`, `user.updated`, `user.deleted`, `session.created`, `organization.created`, etc.

Verify with `svix`:

```ts
import { Webhook } from "svix";
const wh = new Webhook(process.env.CLERK_WEBHOOK_SECRET);
const evt = wh.verify(rawBody, {
  "svix-id": req.headers["svix-id"],
  "svix-timestamp": req.headers["svix-timestamp"],
  "svix-signature": req.headers["svix-signature"],
});
```

Use `user.created` to sync to your own DB; don't rely on `currentUser()` reads hitting Clerk on every request.

## Key reference URLs

- Next.js quickstart: https://clerk.com/docs/quickstarts/nextjs
- clerkMiddleware: https://clerk.com/docs/reference/nextjs/clerk-middleware
- auth() / currentUser(): https://clerk.com/docs/reference/nextjs/app-router/auth
- useAuth: https://clerk.com/docs/nextjs/reference/hooks/use-auth
- Backend SDK: https://clerk.com/docs/reference/backend/overview
- Webhooks: https://clerk.com/docs/webhooks/overview
