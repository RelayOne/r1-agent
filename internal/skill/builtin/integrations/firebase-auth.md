# firebase-auth

> Firebase Authentication: drop-in auth UI + backend for email/password, OAuth providers, phone, anonymous. Tight integration with Firestore/RTDB security rules. Identity Platform is the paid tier with SAML/OIDC/MFA.

<!-- keywords: firebase auth, firebase authentication, google identity platform, oauth, phone auth -->

**Official docs:** https://firebase.google.com/docs/auth  |  **Verified:** 2026-04-14.

## Setup

Firebase Console → Auth → enable providers (Email, Google, Apple, GitHub, Phone, Anonymous, custom OIDC/SAML on paid tier).

## Client SDK (web)

```ts
import { initializeApp } from "firebase/app";
import { getAuth, signInWithEmailAndPassword, onAuthStateChanged } from "firebase/auth";
const app = initializeApp(firebaseConfig);
const auth = getAuth(app);

await signInWithEmailAndPassword(auth, email, password);
onAuthStateChanged(auth, user => { if (user) console.log(user.uid); });

const idToken = await auth.currentUser.getIdToken();   // send to your backend
```

## OAuth providers

```ts
import { GoogleAuthProvider, signInWithPopup } from "firebase/auth";
const result = await signInWithPopup(auth, new GoogleAuthProvider());
```

Providers: Google, Apple, Facebook, Twitter/X, GitHub, Microsoft, Yahoo. Each requires client ID registration in Firebase Console.

## Backend: verify ID token

Every request from client should carry `Authorization: Bearer <id_token>`. Backend verifies with Admin SDK:

```ts
import admin from "firebase-admin";
admin.initializeApp({ credential: admin.credential.cert(serviceAccount) });

const decoded = await admin.auth().verifyIdToken(idToken);
const uid = decoded.uid;
const email = decoded.email;
```

Verifying checks signature + expiration + audience. Do this on every request — tokens expire every 1h.

## Admin SDK user management

```ts
await admin.auth().createUser({ email, password, displayName: "Alex" });
await admin.auth().updateUser(uid, { emailVerified: true });
await admin.auth().setCustomUserClaims(uid, { role: "admin" });   // claims appear in ID token
await admin.auth().deleteUser(uid);
```

Custom claims flow into `decoded.role` in security rules + backend — use them for RBAC. Claims are in ID token so re-login (or force refresh) after change.

## Phone auth

Client calls `signInWithPhoneNumber(auth, "+14155551234", recaptchaVerifier)` → Firebase sends SMS → user enters code → `confirmationResult.confirm(code)`. Requires reCAPTCHA v2 (visible or invisible) to prevent SMS bombing.

## Security rules integration

Firestore/RTDB rules reference `request.auth.uid` + custom claims:

```
match /users/{userId} {
  allow read, write: if request.auth.uid == userId;
}
match /admin/{doc} {
  allow write: if request.auth.token.role == "admin";
}
```

## Identity Platform upgrade

For MFA, SAML, OIDC, blocking functions, anonymous user linking: upgrade project to Identity Platform. Same API, extra features, paid.

## Common gotchas

- **ID tokens expire every 1h** — clients auto-refresh via `onIdTokenChanged`; backend must `verifyIdToken` fresh, not cache.
- **`setCustomUserClaims` doesn't update active tokens** — claims appear only on the NEXT token refresh. Call `auth.currentUser.getIdToken(true)` on client to force.
- **Phone auth without reCAPTCHA = abuse vector** — Firebase will rate-limit/block you if you skip it.
- **Anonymous auth creates real users** — uid, token, rules all work. Link to permanent account via `linkWithCredential` to preserve data when user signs up.
- **Password reset emails are customizable** — but the action URL domain must be an authorized domain in Firebase Console.

## Key reference URLs

- Client SDK: https://firebase.google.com/docs/auth/web/start
- Admin SDK verify ID token: https://firebase.google.com/docs/auth/admin/verify-id-tokens
- Custom claims: https://firebase.google.com/docs/auth/admin/custom-claims
- Identity Platform MFA: https://cloud.google.com/identity-platform/docs/web/mfa
