# mobile-platform

> Cross-platform mobile patterns: deep linking, push notifications, app store, and native integrations

<!-- keywords: mobile, ios, android, platform specific, deep linking, push notifications, app store -->

## Critical Rules

1. **Deep links must work when the app is not installed.** Deferred deep links store the target URL and redirect after install. Without this, marketing campaigns lose attribution.

2. **Never assume push notification permission is granted.** Ask at a contextually relevant moment (not on first launch). Explain the value before the system prompt. Respect denial permanently.

3. **Test on real devices, not just simulators.** Memory limits, GPS behavior, camera access, and background task killing differ dramatically between simulator and hardware.

4. **Handle all app lifecycle states.** Your app can be active, backgrounded, suspended, or terminated at any moment. Persist critical state early and restore gracefully.

5. **Respect platform conventions.** iOS users expect swipe-to-go-back and bottom tab bars. Android users expect the system back button and top navigation. Fighting platform norms frustrates users.

## Deep Linking

### Universal Links (iOS) / App Links (Android)
- Host a verification file on your domain: `/.well-known/apple-app-site-association` (iOS) or `/.well-known/assetlinks.json` (Android).
- Register the associated domain in your app configuration.
- The OS verifies domain ownership at install time. No fallback to the browser if verification fails.
- Handle the incoming URL in your app's entry point and route to the correct screen.

### Deferred Deep Links
- User clicks a link, app is not installed, goes to app store, installs, opens app, lands on the linked content.
- Implementation: store link data server-side keyed by device fingerprint or use a service (Branch, Firebase Dynamic Links).
- Match on first launch using device fingerprint, IP, clipboard, or install referrer (Android).

### URL Scheme Design
- Use universal/app links (`https://myapp.com/invite/abc123`) as the primary mechanism. Custom schemes (`myapp://`) are a fallback.
- Custom schemes are not verified and can be hijacked by other apps.
- Parse parameters defensively. Treat deep link input as untrusted user input.

## Push Notifications

### Architecture
- iOS uses APNs, Android uses FCM. App registers, receives device token, sends token to your server. Server pushes via platform API.
- Store tokens per device, not per user. Tokens can change; update on every app launch.

### Permission Strategy
- Never request permission on first launch. Show an in-app pre-prompt explaining the value first.
- If denied, provide a settings deep link to re-enable. Segment by type (messages, marketing, updates).

### Payload Best Practices
- Keep payloads small (max 4KB). Send a notification ID and fetch details from your API.
- Use silent pushes for data sync. Visible notifications for user-actionable events only.

## App Store Submission Checklist

### iOS (App Store)
- App icons at all required sizes, screenshots for all device sizes, privacy nutrition labels accurate.
- App Transport Security enforced (HTTPS only), no private API usage, purpose strings for all permissions.

### Android (Play Store)
- Target SDK meets current requirements, data safety form completed, app signing by Google Play enabled.
- 64-bit support required, deobfuscation mapping files uploaded, content rating questionnaire completed.

## Biometric Authentication

- Use platform APIs (FaceID/TouchID on iOS, BiometricPrompt on Android). Store tokens in keychain/keystore gated behind biometrics.
- Always provide a fallback (PIN, password). Re-authenticate for sensitive actions even if the session is active.
- Never store biometric data yourself. Use OS-level APIs exclusively.

## Background Task Management

- **iOS:** Use BGTaskScheduler for deferred work. Background fetch has ~30 seconds. Very limited.
- **Android:** Use WorkManager for reliable background work. Doze mode throttles activity.
- Background location requires clear justification. Audio/VoIP/navigation have special background modes.

## In-App Purchases and Subscriptions

- Use the platform's billing system (StoreKit 2 on iOS, Google Play Billing on Android). Side-loading payments violates store policies.
- Validate receipts server-side. Never trust client-side receipt validation.
- Handle all subscription states: active, expired, grace period, billing retry, paused, revoked.
- Implement restore purchases. Users switching devices must recover their purchases.
- Subscription offers (free trial, introductory price) have platform-specific rules. Test all flows in sandbox.

## Crash Reporting and Analytics

- Integrate crash reporting from day one (Crashlytics, Sentry, Bugsnag). Upload dSYM/mapping files for symbolication.
- Track key metrics: crash-free rate, app start time, screen render time, API latency.
- Log breadcrumbs (recent user actions) for crash context. Batch analytics events to respect battery life.

## App Update Strategies

- **Force update:** Block the app with a full-screen modal when the API version is incompatible. Provide a direct store link.
- **Soft update:** Show a dismissable banner for non-critical updates. Remind periodically but don't block.
- Check version against a server-side config (remote config or API header) on each app launch.
- Use in-app updates (Android Play Core) or SKStoreReviewController prompt for gentle nudges.
- Maintain backward compatibility for at least 2 previous app versions. Users update slowly.

## Common Gotchas

- **Permissions requested too early:** Requesting camera, location, and notifications on first launch guarantees denials.
- **Ignoring app review times:** iOS review takes 1-7 days. Plan releases accordingly. Expedited review is for critical fixes only.
- **Hardcoded API URLs:** Use a remote config for API base URLs. Allows switching servers without app update.
- **Ignoring accessibility:** Both platforms have strong accessibility frameworks (VoiceOver, TalkBack). Test with them. It is often a legal requirement.
- **Not testing poor network:** Use network link conditioner (iOS) or emulator throttling (Android) to test on 3G.
