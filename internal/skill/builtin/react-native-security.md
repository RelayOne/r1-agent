# react-native-security

> Security hardening for React Native apps covering storage, network, tamper detection, and obfuscation

<!-- keywords: react-native security, keychain, certificate pinning, jailbreak -->

## Secure Storage

1. **Use `react-native-keychain`** for secrets (tokens, passwords, API keys). It stores in iOS Keychain and Android Keystore, both hardware-backed on modern devices:
   ```tsx
   import * as Keychain from 'react-native-keychain';
   // Store
   await Keychain.setGenericPassword('auth', accessToken, {
     accessible: Keychain.ACCESSIBLE.WHEN_UNLOCKED_THIS_DEVICE_ONLY,
     securityLevel: Keychain.SECURITY_LEVEL.SECURE_HARDWARE,
   });
   // Retrieve
   const credentials = await Keychain.getGenericPassword();
   if (credentials) { useToken(credentials.password); }
   ```

2. **`WHEN_UNLOCKED_THIS_DEVICE_ONLY`** is the strictest accessibility level. Data is not included in backups and not accessible when the device is locked. Use it for auth tokens.

3. **For biometric-gated secrets**, set `accessControl: Keychain.ACCESS_CONTROL.BIOMETRY_CURRENT_SET`. This requires Face ID/Touch ID/fingerprint each time the value is read.

4. **Never store secrets in AsyncStorage, MMKV, or UserDefaults.** These are plaintext storage. A compromised device or backup extraction exposes everything.

5. **Rotate tokens on the server.** Short-lived access tokens (15 min) with refresh tokens stored in Keychain. If a device is compromised, exposure is time-limited.

## Certificate Pinning

1. **Pin the server's public key hash**, not the certificate itself. Certificates rotate; public keys can persist across rotations. Use `react-native-ssl-pinning` or `TrustKit`:
   ```tsx
   import { fetch as pinnedFetch } from 'react-native-ssl-pinning';
   const response = await pinnedFetch(url, {
     method: 'GET',
     sslPinning: {
       certs: ['sha256/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA='],
     },
   });
   ```

2. **Include a backup pin.** Pin both the current and next key so certificate rotation does not brick the app. Without a backup pin, a cert change locks out all deployed versions.

3. **OTA pin updates**: ship new pins via a signed configuration endpoint using a separate trust anchor. This lets you update pins without an app store release.

4. **Debug builds should skip pinning** to allow proxy tools (Charles, mitmproxy) during development. Use `__DEV__` to conditionally disable.

## Jailbreak and Root Detection

1. **`react-native-jail-monkey`** detects jailbreak (iOS) and root (Magisk/SuperSU on Android). Check at app launch and on resume:
   ```tsx
   import JailMonkey from 'jail-monkey';
   if (JailMonkey.isJailBroken()) {
     // Warn or restrict sensitive features
   }
   ```

2. **Detection is not prevention.** Skilled attackers bypass jailbreak checks. Use detection as a signal — degrade functionality (disable payments, hide sensitive data) rather than hard-blocking. Log the event for server-side risk scoring.

3. **Android SafetyNet/Play Integrity**: use `react-native-google-play-integrity` to verify device integrity server-side. The attestation token is validated on your backend, not in the app. This is harder to bypass than local checks.

4. **Debug/emulator detection**: check `JailMonkey.isDebuggedMode()` and `JailMonkey.isOnExternalStorage()`. Block sensitive operations in debug builds on production servers.

## Code Obfuscation

1. **Hermes bytecode** is the first line of defense. Hermes compiles JS to bytecode at build time, which is harder to read than plain JS. It is not encryption — determined reverse engineers can decompile it — but it raises the bar.

2. **ProGuard/R8** for Android: enable in `android/app/build.gradle` with `minifyEnabled true` and `shrinkResources true`. This obfuscates Java/Kotlin code and removes unused classes.

3. **`react-native-obfuscating-transformer`** obfuscates JS source before Hermes compilation. It renames variables and strips comments. Configure exclusions for library code that relies on reflection.

4. **Never embed secrets in JS code.** No amount of obfuscation protects a hardcoded API key. Use a server-side proxy or secure enclave for secrets that the client must use but should not see.

## Preventing Reverse Engineering

1. **Strip debug symbols** from release builds. For iOS, set `DEBUG_INFORMATION_FORMAT = dwarf` (not dwarf-with-dsym) in release scheme, or upload dSYMs only to your crash reporter.

2. **Disable React Native dev menu** in production: verify `__DEV__` is false. The dev menu exposes the JS bundle URL, remote debugging, and element inspector.

3. **Tamper detection**: compute a checksum of the JS bundle at startup and compare against a known hash from your server. If the bundle was modified, refuse to run.

4. **Code push integrity**: if using OTA updates (CodePush, expo-updates), enforce code signing. Both support RSA signature verification so tampered bundles are rejected.

## Secure API Communication

1. **Always use HTTPS.** React Native enforces this by default on iOS (App Transport Security). On Android, set `android:usesCleartextTraffic="false"` in `AndroidManifest.xml`.

2. **Short-lived JWTs** with server-side refresh. Never send long-lived tokens. Include `iat`, `exp`, and `aud` claims. Validate all three on the server.

3. **Request signing**: for sensitive operations (payments, account changes), sign the request body with an HMAC using a per-session key. The server verifies the signature before processing.

4. **Rate limiting and anomaly detection** belong on the server, not the client. Client-side rate limiting is trivially bypassed. Use device fingerprinting and behavioral analysis server-side.

5. **Sensitive data handling**: never log tokens or PII. Use `console.warn` guards in production. Strip logs with `babel-plugin-transform-remove-console` in release builds.
