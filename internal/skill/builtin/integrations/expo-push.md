# expo-push

> Expo Push Notification Service — proxy to FCM + APNs for Expo / React Native apps. Ticket/receipt model with batching; access-token security (enable in EAS dashboard).

<!-- keywords: expo, expo push, expo notifications, react native push, push token, expopushtoken, expo-server-sdk -->

**Official docs:** https://docs.expo.dev/push-notifications/overview  |  **Verified:** 2026-04-14 via web search.

## Endpoint + auth

- `POST https://exp.host/--/api/v2/push/send`
- Auth: `Authorization: Bearer <EXPO_ACCESS_TOKEN>` (**enable in EAS Dashboard → Project settings → Push notifications** — off by default, ON is required for production security)
- SDK: `expo-server-sdk` (Node; v3.6.0+ required for access-token support)

Without access-token enabled, Expo will accept push requests from anyone who has a valid push token — don't ship to production without enabling.

## Device token shape

Expo push tokens look like `ExponentPushToken[xxxxxxxxxxxxxxxxxxxxxx]`. Client gets it via `Notifications.getExpoPushTokenAsync()`. Store server-side keyed on user + device. These are NOT raw FCM / APNs tokens — Expo proxies to those.

## Send (batched up to 100 messages)

```js
import { Expo } from "expo-server-sdk";
const expo = new Expo({ accessToken: process.env.EXPO_ACCESS_TOKEN });

const messages = [];
for (const token of tokens) {
  if (!Expo.isExpoPushToken(token)) continue; // validate client-side
  messages.push({
    to: token,
    sound: "default",
    title: "New message",
    body: "Hello!",
    data: { deepLink: "/orders/42" },
    priority: "high",               // delivered immediately (Android); iOS uses "default" or "high"
    channelId: "default",           // Android channel
    badge: 1,                       // iOS badge count
  });
}

const chunks = expo.chunkPushNotifications(messages);
const tickets = [];
for (const chunk of chunks) {
  const chunkTickets = await expo.sendPushNotificationsAsync(chunk);
  tickets.push(...chunkTickets);
}
```

## Tickets → receipts (required for reliability)

Each `send` returns one **ticket** per message. A ticket has `{status: "ok", id: "ABC-123"}` on success or `{status: "error", message, details}` on validation failure.

**Receipts** tell you whether Expo successfully forwarded to FCM/APNs. Available for ~24h; fetch in batches up to 1000 IDs:

```js
const ids = tickets.filter(t => t.status === "ok").map(t => t.id);
const idChunks = expo.chunkPushNotificationReceiptIds(ids);
for (const chunk of idChunks) {
  const receipts = await expo.getPushNotificationReceiptsAsync(chunk);
  for (const id in receipts) {
    const r = receipts[id];
    if (r.status === "error") {
      if (r.details?.error === "DeviceNotRegistered") {
        // remove token from DB
      }
      // Log r.message
    }
  }
}
```

**You MUST check receipts.** If you don't, a bad FCM/APNs config will silently drop notifications and you'll never know.

## Error codes worth handling

- `DeviceNotRegistered` — token invalid; delete from your DB.
- `MessageTooBig` — payload > 4KB; trim data field.
- `MessageRateExceeded` — per-token rate limit; backoff.
- `InvalidCredentials` — FCM/APNs creds in EAS are wrong; fix in Expo dashboard.

## Production setup

1. Generate APNs auth key (`.p8`) and upload to EAS credentials.
2. Upload FCM service-account JSON to EAS credentials.
3. Enable "enhanced push security" in EAS Dashboard and save the access token.
4. Store the token in your backend env as `EXPO_ACCESS_TOKEN`.
5. `eas build --profile production` — production build carries the right credentials.

## Key reference URLs

- Overview: https://docs.expo.dev/push-notifications/overview/
- Sending: https://docs.expo.dev/push-notifications/sending-notifications/
- Setup: https://docs.expo.dev/push-notifications/push-notifications-setup/
- FAQ / troubleshooting: https://docs.expo.dev/push-notifications/faq/
