# fcm

> Firebase Cloud Messaging HTTP v1 API (legacy `/fcm/send` is sunset; use `/v1/projects/{id}/messages:send`). OAuth2 service-account auth, per-device or topic-based delivery.

<!-- keywords: fcm, firebase cloud messaging, push, android push, notification, messaging, firebase push -->

**Official docs:** https://firebase.google.com/docs/cloud-messaging  |  **Verified:** 2026-04-14 via web search.

## Base URL + auth

- Endpoint: `POST https://fcm.googleapis.com/v1/projects/{PROJECT_ID}/messages:send`
- Auth: OAuth2 Bearer token derived from a service-account JSON key. Scope: `https://www.googleapis.com/auth/firebase.messaging`.
- Use `google-auth-library` (Node) / `google-auth` (Python) to mint short-lived access tokens from the service account; DO NOT use the legacy server key — legacy `/fcm/send` was retired.

```js
import { GoogleAuth } from "google-auth-library";
const auth = new GoogleAuth({
  keyFile: "service-account.json",
  scopes: ["https://www.googleapis.com/auth/firebase.messaging"],
});
const client = await auth.getClient();
const { token } = await client.getAccessToken();
```

## Send payload

```js
await fetch(`https://fcm.googleapis.com/v1/projects/${PROJECT_ID}/messages:send`, {
  method: "POST",
  headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
  body: JSON.stringify({
    message: {
      token: DEVICE_REGISTRATION_TOKEN,                   // single device
      // OR topic: "news",
      // OR condition: "'news' in topics && 'sports' in topics",
      notification: { title: "Hello", body: "World" },    // handled by SDK
      data: { deep_link: "/orders/42" },                  // string→string only
      android: { priority: "high", notification: { click_action: "OPEN_ORDER" } },
      apns: { payload: { aps: { sound: "default", badge: 1 } } },
      webpush: { notification: { icon: "/icon.png" } },
    },
  }),
});
```

`data` fields MUST be string → string. No nested objects. Deserialize on the client.

## Multicast / batch

Firebase Admin SDK exposes `sendEachForMulticast` and `sendMulticast` for up to 500 tokens in one call. Direct HTTP requires one POST per message (v1 API has no batch endpoint; use `:batchSend` with multipart/mixed for pseudo-batching).

## Device token lifecycle

Tokens are issued per install per device. Store them server-side keyed on user. Handle:

- Token refresh (client calls `getToken()` → new token; push to server; remove old).
- Uninstall / disabled: FCM returns `NotRegistered` or `InvalidRegistration` → delete token from DB.
- Invalid token errors: response body has `error.status: "NOT_FOUND"` or `"INVALID_ARGUMENT"`.

## Topics

Subscribe/unsubscribe via client SDK: `messaging.subscribeToTopic("news")`. Server subscribes bulk via `POST /iid/v1:batchAdd`. Topics are strings `[a-zA-Z0-9-_.~%]+`.

## Rate limits + errors

- Per-project quota; upgrade via Firebase console for scale.
- `QUOTA_EXCEEDED` → backoff with jitter.
- `UNREGISTERED` / `NOT_FOUND` → token invalid, delete.
- `INVALID_ARGUMENT` → payload malformed, log and fix, don't retry.

## iOS gotcha

Even with FCM SDK on iOS, APNs is the actual delivery channel. You must still configure APNs auth key in the Firebase console; FCM just proxies to APNs.

## Key reference URLs

- Send v1 API: https://firebase.google.com/docs/cloud-messaging/send/v1-api
- Migrate from legacy: https://firebase.google.com/docs/cloud-messaging/migrate-v1
- REST reference: https://firebase.google.com/docs/reference/fcm/rest/v1/projects.messages
- Message types: https://firebase.google.com/docs/cloud-messaging/customize-messages/set-message-type
