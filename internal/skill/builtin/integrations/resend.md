# resend

> Resend: modern developer-first transactional email. React Email for templates; one API key; domain-based sender identity. Simpler than SendGrid/Mailgun — fewer knobs, sensible defaults.

<!-- keywords: resend, email, transactional email, react email, resend api -->

**Official docs:** https://resend.com/docs  |  **Verified:** 2026-04-14 via web search.

## Base URL + auth

- REST base: `https://api.resend.com/`
- Auth: `Authorization: Bearer re_...` (single API key)
- SDKs: `resend` (Node), `resend` (Python), Go, Ruby, PHP.

## Send email

```ts
import { Resend } from "resend";
const resend = new Resend(process.env.RESEND_API_KEY!);

const { data, error } = await resend.emails.send({
  from: "Your App <noreply@yourdomain.com>",    // must be a verified sender domain
  to: ["user@example.com"],
  subject: "Welcome",
  html: "<p>Welcome aboard!</p>",
  text: "Welcome aboard!",
  tags: [{ name: "category", value: "welcome" }],
  headers: { "X-Entity-Ref-ID": "usr_42" },
});
```

## React Email (native template support)

```bash
pnpm add @react-email/components
```

```tsx
// emails/welcome.tsx
import { Html, Button, Heading } from "@react-email/components";
export default function WelcomeEmail({ name }: { name: string }) {
  return (
    <Html>
      <Heading>Welcome, {name}</Heading>
      <Button href="https://app/setup">Set up your account</Button>
    </Html>
  );
}
```

Send with rendered React component:

```ts
import { render } from "@react-email/render";
import WelcomeEmail from "./emails/welcome";
const html = await render(WelcomeEmail({ name: "Alex" }));
await resend.emails.send({ from, to, subject, html });
```

## Batch send

```ts
await resend.batch.send([
  { from, to: "a@x.com", subject, html },
  { from, to: "b@x.com", subject, html },
  // Up to 100 emails per batch
]);
```

## Webhooks

Configure in dashboard → Webhooks. Events: `email.sent`, `email.delivered`, `email.delivery_delayed`, `email.complained`, `email.bounced`, `email.opened`, `email.clicked`.

Verify with `svix` (Resend uses Svix for webhook delivery):

```ts
import { Webhook } from "svix";
const wh = new Webhook(process.env.RESEND_WEBHOOK_SECRET!);
const evt = wh.verify(rawBody, {
  "svix-id": req.headers["svix-id"],
  "svix-timestamp": req.headers["svix-timestamp"],
  "svix-signature": req.headers["svix-signature"],
});
```

## Domain setup

Add domain in dashboard → Domains. Resend shows the DNS records to set (SPF, DKIM, DMARC). Send requires verified domain — no `@gmail.com` allowed as sender.

## Common gotchas

- **API key permissions**: sending keys vs full-access keys. Scope down for production workloads.
- **React Email requires `render()`** — don't pass a JSX element to `send()`.
- **Rate limit**: 10 req/sec per API key on the free tier. Paid tiers scale up.
- **Attachments as base64 or URL**: `attachments: [{ filename, content: base64, contentType }]` or `{ filename, path: "https://..." }`.

## Key reference URLs

- Node quickstart: https://resend.com/docs/send-with-nodejs
- React Email: https://react.email/docs
- Webhooks: https://resend.com/docs/dashboard/webhooks/introduction
- Domains: https://resend.com/docs/dashboard/domains/introduction
