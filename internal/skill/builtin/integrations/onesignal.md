# onesignal

> OneSignal: multi-platform push (iOS, Android, Web), email, SMS, in-app. Abstracts FCM/APNs credentials — you upload keys once, OneSignal handles per-device tokens + segmentation.

<!-- keywords: onesignal, push notifications, cross-platform push, web push, in-app messaging -->

**Official docs:** https://documentation.onesignal.com  |  **Verified:** 2026-04-14.

## Auth

- **REST API Key** (server-side): `Authorization: Basic <REST_API_KEY>`. Scoped to one app.
- **User Auth Key** (organization-wide): for creating apps via API. Rarely needed.
- **App ID**: public, identifies your OneSignal app. Sent in every call.

## Send notification (REST)

```ts
await fetch("https://onesignal.com/api/v1/notifications", {
  method: "POST",
  headers: {
    Authorization: `Basic ${REST_API_KEY}`,
    "Content-Type": "application/json",
  },
  body: JSON.stringify({
    app_id: APP_ID,
    contents: { en: "You have a new message" },
    headings: { en: "New DM" },
    include_external_user_ids: ["user_42"],    // your internal user ID (preferred)
    // OR include_player_ids: ["subscription-id"]  // OneSignal's device token ID
    // OR included_segments: ["Subscribed Users"]
    data: { deep_link: "/messages/42" },
    ios_badgeType: "Increase",
    ios_badgeCount: 1,
  }),
});
```

## Client SDKs

- Web: `OneSignalDeferred.push(async OneSignal => { await OneSignal.init({ appId }); })`
- React Native: `react-native-onesignal`
- iOS Swift, Android Kotlin, Flutter, Unity — all official SDKs.

Subscribe + associate external ID:

```js
await OneSignal.login(userId);         // links device to your userId
await OneSignal.User.addTag("plan", "pro");
```

## Segments

Define in dashboard: "Users with tag plan=pro AND last_session < 7 days". Use `included_segments` in API call. Fast — precomputed.

## External user IDs (CRITICAL for multi-device users)

Call `OneSignal.login(userId)` on every client after login. One `external_user_id` can span many subscriptions (phone + laptop). `include_external_user_ids` sends to all of them.

## Webhooks

Dashboard → Settings → Webhooks. Events: `notification.clicked`, `notification.displayed`, `notification.dismissed`.

OneSignal POSTs to your URL; no signing by default — IP-allowlist or verify a custom header you add.

## Common gotchas

- **REST API Key is secret — never ship to client.** The client uses App ID only.
- **`include_player_ids` vs `include_external_user_ids`**: the first targets specific subscriptions (deprecated-ish), the second targets logical users. Use external IDs.
- **Rate limit**: 2,000 notifications/sec sustained; bulk operations via `/notifications` with segment targeting, not per-user loops.
- **Free tier cap**: 10k web subscribers, unlimited mobile subscribers (as of 2025). Priced tiers add API throughput + advanced targeting.
- **iOS Focus modes + notification summaries** can suppress displayed events — clicked events remain reliable.

## Key reference URLs

- REST API: https://documentation.onesignal.com/reference/create-notification
- Web SDK: https://documentation.onesignal.com/docs/web-push-quickstart
- React Native SDK: https://documentation.onesignal.com/docs/react-native-sdk-setup
- External user IDs: https://documentation.onesignal.com/docs/external-user-ids
