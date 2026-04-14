# apns

> Apple Push Notification Service — HTTP/2 provider API with JWT (p8 key) auth. One connection per bundle ID. Dev vs prod environments use different hosts.

<!-- keywords: apns, apple push, ios push, push notification, p8, jwt push, apns2 -->

**Official docs:** https://developer.apple.com/documentation/usernotifications  |  **Verified:** 2026-04-14 via web search.

## Endpoints

- **Production:** `https://api.push.apple.com:443`
- **Development (sandbox):** `https://api.sandbox.push.apple.com:443`

Send via `POST /3/device/{deviceToken}` (the 64-char hex device token, not the registration ticket).

## Token-based auth (preferred — `.p8` JWT)

Token auth replaces the older cert-based path. Signing key (`.p8` file) + Key ID + Team ID comes from the Apple Developer Account → Certificates, Identifiers & Profiles → Keys. One key works for both environments and doesn't expire (but can be revoked).

JWT claims:

```json
{
  "iss": "TEAM_ID",       // Apple Team ID (10 chars)
  "iat": 1713100000       // issued-at epoch seconds; MUST be within last hour
}
```

JWT header:

```json
{ "alg": "ES256", "kid": "KEY_ID" }  // KEY_ID is 10-char Key ID from Apple
```

Sign with ES256 using the `.p8` private key. Cache the JWT and reuse for up to ~55 minutes; APNs rejects tokens older than one hour with 403 `ExpiredProviderToken`.

## Send request

HTTP/2 required (use `h2` client library; `curl --http2` for testing):

```
POST /3/device/{hex-device-token} HTTP/2
Host: api.push.apple.com
authorization: bearer <jwt>
apns-push-type: alert                  # or background, voip, complication, fileprovider, mdm
apns-topic: com.yourapp.bundle         # bundle ID
apns-priority: 10                       # 10 immediate, 5 conserve power
apns-expiration: 0                      # 0 = discard if undeliverable; unix ts otherwise
apns-collapse-id: order-42              # optional; replaces older notification with same id

{ "aps": { "alert": { "title": "...", "body": "..." }, "sound": "default", "badge": 1 } }
```

Payload max: **4096 bytes** (was 2KB legacy). `aps` is reserved; custom fields go at top level alongside `aps`.

## Payload shape

```json
{
  "aps": {
    "alert": { "title": "New message", "body": "Hello", "subtitle": "..." },
    "badge": 3,
    "sound": "chime.caf",
    "category": "MESSAGE_CATEGORY",
    "thread-id": "chat-42",
    "mutable-content": 1,           // 1 to let a Notification Service Extension modify
    "content-available": 1          // 1 for silent background pushes
  },
  "deep_link": "app://order/42"     // your custom keys
}
```

## Response codes

- **200**: delivered to Apple. Doesn't guarantee device delivery — that's APNs's internal queue.
- **400** with JSON `{reason: "BadDeviceToken"|"MissingTopic"|...}` — payload or headers wrong.
- **403** `ExpiredProviderToken` — regenerate JWT.
- **410** `Unregistered` — token invalidated (app uninstalled or reset); remove from DB.
- **429** — rate limit; backoff.
- **500/503** — retry with backoff.

## Per-connection etiquette

- One HTTP/2 connection carries many requests multiplexed. Don't open a new connection per push.
- Keep connections alive (Apple allows idle up to ~30 min); reconnect on disconnect with backoff.
- Separate connections per environment (prod vs sandbox) — can't mix.

## Common gotchas

- **Wrong topic:** use the iOS app's bundle ID. For VoIP pushes, append `.voip`. For complications, append `.complication`.
- **Silent pushes need `content-available: 1` AND `apns-push-type: background`** — OR iOS drops them.
- **Device token format:** 64 hex chars (or 160 chars for some watchOS). Not the FCM registration token — those are different.
- **Production build installed on dev device** still gets prod tokens, and vice versa — make sure your backend targets the matching environment.

## Key reference URLs

- Establishing token-based connection: https://developer.apple.com/documentation/usernotifications/establishing-a-token-based-connection-to-apns
- Sending notification requests: https://developer.apple.com/documentation/usernotifications/sending-notification-requests-to-apns
- Registering your app: https://developer.apple.com/documentation/usernotifications/registering-your-app-with-apns
- Go client example: https://github.com/sideshow/apns2
