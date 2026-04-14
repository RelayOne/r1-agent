# plaid

> Plaid: bank-account linking + transactions + balance. `/link/token/create` → Link UI → `/item/public_token/exchange` → permanent `access_token`. Don't store credentials — Plaid does.

<!-- keywords: plaid, banking, bank link, ach, transactions, open banking, plaid link -->

**Official docs:** https://plaid.com/docs  |  **Verified:** 2026-04-14 via web search.

## Environments + auth

- Sandbox: `https://sandbox.plaid.com`
- Development: `https://development.plaid.com` (live data, limited items, no cost)
- Production: `https://production.plaid.com`

Auth: `client_id` + `secret` (per-env, different for each) in request body:

```json
{
  "client_id": "...",
  "secret": "sandbox-xxx-xxx-xxx",
  ...
}
```

SDKs: `plaid` (Node), `plaid` (Python), `github.com/plaid/plaid-go`.

## Full flow (Link → access_token → data)

### 1. Server creates link_token (4h expiry)

```ts
const resp = await client.linkTokenCreate({
  user: { client_user_id: userId },
  client_name: "Your App",
  products: ["transactions", "auth"],
  country_codes: ["US"],
  language: "en",
  webhook: "https://yourapp/webhooks/plaid",
  transactions: { days_requested: 730 },       // required when transactions in products
});
// { link_token: "link-sandbox-xxx" }
```

### 2. Client initializes Link with link_token

```tsx
import { usePlaidLink } from "react-plaid-link";
const { open, ready } = usePlaidLink({
  token: linkToken,
  onSuccess: async (public_token, metadata) => {
    await fetch("/api/plaid/exchange", { method: "POST", body: JSON.stringify({ public_token }) });
  },
});
```

### 3. Server exchanges public_token for access_token

```ts
const { access_token, item_id } = await client.itemPublicTokenExchange({ public_token });
// access_token is permanent (until user revokes) — store encrypted in your DB, keyed on user_id
```

`public_token` expires in 30 minutes. `access_token` doesn't expire unless user disconnects.

## Fetch accounts

```ts
const { accounts } = await client.accountsGet({ access_token });
// accounts[].account_id, type, subtype, balances.{available, current}, mask
```

## Fetch transactions (use `/transactions/sync` — incremental)

```ts
let cursor = lastCursor ?? undefined;   // from DB
const { added, modified, removed, next_cursor, has_more } = await client.transactionsSync({
  access_token,
  cursor,
});
// Persist next_cursor; next call starts from there.
```

Cursor-based sync is preferred over `/transactions/get` date-range (old API). Handles pagination and change detection automatically.

## Webhooks

```json
{
  "webhook_type": "TRANSACTIONS",
  "webhook_code": "SYNC_UPDATES_AVAILABLE",
  "item_id": "..."
}
```

Important webhook codes:
- `TRANSACTIONS:SYNC_UPDATES_AVAILABLE` — call `/transactions/sync` now
- `ITEM:ERROR` — item needs re-auth
- `ITEM:PENDING_EXPIRATION` — access token about to expire (OAuth banks)
- `ITEM:USER_PERMISSION_REVOKED` — user disconnected; delete access_token

Verify webhook with JWK — Plaid publishes keys at `https://sandbox.plaid.com/verification` and fetches keys by `key_id` in `plaid-verification` JWT header.

## Common gotchas

- **Never store bank credentials** yourself. Plaid handles. You store only `access_token` (encrypted at rest).
- **Sandbox vs Production**: different `client_id` AND different `secret`. Wrong env → opaque errors.
- **Transactions can take up to 24h to backfill** after initial link — don't block UX on full history; poll `/transactions/sync` until `has_more=false` first time.
- **Re-auth flow for OAuth banks**: use Link in "update mode" with a link_token created with `access_token` set — refreshes credentials without a new Item.
- **Geography**: `country_codes` must include every country you want banks from. US-only unless you specify.

## Key reference URLs

- Link API: https://plaid.com/docs/api/link/
- Link overview: https://plaid.com/docs/link/
- Transactions sync: https://plaid.com/docs/api/products/transactions/#transactionssync
- Webhooks: https://plaid.com/docs/api/webhooks/
- Link token migration: https://plaid.com/docs/link/link-token-migration-guide/
