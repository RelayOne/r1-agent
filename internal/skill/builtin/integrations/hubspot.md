# hubspot

> HubSpot: CRM + marketing + sales + service. Rich REST API (v3/v4). Private Apps (scoped tokens) have replaced API keys. Webhooks, CRM Object API, Marketing Email, Forms, Workflows.

<!-- keywords: hubspot, crm, hubspot api, contacts, deals, marketing automation -->

**Official docs:** https://developers.hubspot.com/docs/api/overview  |  **Verified:** 2026-04-14.

## Auth

- **Private App token** (recommended): Settings → Integrations → Private Apps → create, select scopes, copy token. `Authorization: Bearer <token>`.
- **Legacy API keys (hapikey)**: fully deprecated (shut off Nov 2022). Migrate if you still have them.
- **OAuth2** for public apps distributed via Marketplace.

## Base URL

`https://api.hubapi.com/`

## CRM Objects API (v3)

Unified CRUD across contacts, companies, deals, tickets, products, line items, custom objects:

```ts
// Create contact
await fetch("https://api.hubapi.com/crm/v3/objects/contacts", {
  method: "POST",
  headers: { Authorization: `Bearer ${TOKEN}`, "Content-Type": "application/json" },
  body: JSON.stringify({
    properties: { email: "a@b.com", firstname: "Alex", lifecyclestage: "lead" },
  }),
});

// Upsert by email (idempotency key)
// PATCH /crm/v3/objects/contacts/a@b.com?idProperty=email

// Search
await fetch("https://api.hubapi.com/crm/v3/objects/contacts/search", {
  method: "POST",
  body: JSON.stringify({
    filterGroups: [{ filters: [{ propertyName: "email", operator: "EQ", value: "a@b.com" }] }],
    properties: ["email", "firstname", "lifecyclestage"],
    limit: 10,
  }),
});
```

## Associations (v4)

Link objects (contact ↔ deal, etc.):

```
PUT /crm/v4/objects/contacts/{contactId}/associations/deals/{dealId}
    Body: [{ "associationCategory": "HUBSPOT_DEFINED", "associationTypeId": 4 }]
```

Association type IDs: `GET /crm/v4/associations/{from}/{to}/labels`.

## Batch endpoints

```
POST /crm/v3/objects/contacts/batch/create     # up to 100
POST /crm/v3/objects/contacts/batch/upsert
POST /crm/v3/objects/contacts/batch/read
```

Much more rate-efficient than per-record calls.

## Forms

```
POST https://api.hsforms.com/submissions/v3/integration/submit/{portalId}/{formGuid}
     { fields: [{ name: "email", value: "a@b.com" }], context: { pageUri, pageName } }
```

Submission adds contact + triggers form workflow. Unauthenticated endpoint.

## Webhooks (Public Apps only — private apps use Workflows instead)

Subscriptions: `contact.creation`, `contact.propertyChange` + `propertyName`, `deal.creation`, etc. Configure in app settings.

Verify signature v3: HMAC-SHA256, header `X-HubSpot-Signature-v3`:

```ts
const signed = `${method}${uri}${rawBody}${timestamp}`;
const expected = crypto.createHmac("sha256", CLIENT_SECRET).update(signed).digest("base64");
// also check X-HubSpot-Request-Timestamp is within 5 min
```

For private apps, use **Workflows** → "Send webhook" action to hit your endpoint on object changes. Simpler, no app-registration needed.

## Rate limits

- Per Private App: 100 req/10s burst, 250k/day on Starter+; higher on Enterprise.
- 429s include `Retry-After` header. Batch endpoints count as 1 request regardless of payload size — use them aggressively.

## Common gotchas

- **API keys (`hapikey`) are gone** — anyone with old code still using them is broken. Migrate to Private Apps.
- **v1/v2 endpoints deprecated but still responding** for some objects. Always use v3/v4 for CRM.
- **Custom properties are internal-name-based** — dashboard label ≠ API name. Fetch via `GET /crm/v3/properties/contacts`.
- **Timeline events** (custom activity in a contact's timeline) require a separate "timeline integration" setup, not just `events` endpoints.
- **Deals require stage and pipeline IDs** — fetch from `GET /crm/v3/pipelines/deals` rather than guessing string values.

## Key reference URLs

- CRM objects v3: https://developers.hubspot.com/docs/api/crm/understanding-the-crm
- Associations v4: https://developers.hubspot.com/docs/api/crm/associations
- Private Apps: https://developers.hubspot.com/docs/api/private-apps
- Webhooks: https://developers.hubspot.com/docs/api/webhooks
- Rate limits: https://developers.hubspot.com/docs/api/usage-details
