# Lead UX Engineer — Frontend Audit
**Date:** 2026-04-01
**Scope:** ember/devbox web frontend + backend error contracts
**Filter:** Issues that block or confuse user workflows only. Visual preferences excluded.

---

## CRITICAL

- [ ] [CRITICAL] [Dashboard.tsx:117-118] **Start/stop have no error feedback.** `doStart` and `doStop` fire `await post(...)` with no try/catch and no user-visible error state. If Fly returns 502 (backend returns `{ error: "Failed to start on Fly" }`) the call rejects silently — the user sees the machine stay in its current state with zero explanation. — fix: wrap both in try/catch, surface a toast or inline error message. — effort: small

- [ ] [CRITICAL] [Dashboard.tsx:119] **Delete failure is silent and leaves machine in error state.** `doDel` has no error handling. The backend sets `state = "error"` on Fly cleanup failure and returns 502, but the UI catches nothing, closes the menu, and calls `load()` — the user sees the machine flip to an error dot with no explanation of what happened or what to do next. — fix: catch the rejection, keep the menu open or show a modal error, explain the machine is in an error state and they may need to contact support. — effort: small

- [ ] [CRITICAL] [NewMachineModal.tsx:67-92] **`handleBuySlot` calls `handleCreate()` after a successful immediate-provisioning billing response, but `handleCreate` uses stale form state (name, size, etc.) and a NEW invocation of the async function — there is no guard preventing the modal from being dismissed by `onCreated()` inside `handleCreate` while `handleBuySlot` still holds `setBuyingSlot(true)` state.** More critically, if the Stripe redirect path (`safeRedirect`) is reached, the `return` statement exits but `setBuyingSlot(false)` is never called in that branch — so on any page re-visit (e.g. back button from Stripe), the button is permanently frozen. — fix: call `setBuyingSlot(false)` before `safeRedirect`, or restructure so `handleBuySlot` does not call `handleCreate` directly. — effort: small

- [ ] [CRITICAL] [api.ts:22-29] **401 redirect loop for `/verify-email`.** The `api()` helper redirects to `/login` on any 401. `VerifyEmail.tsx` is an unprotected route but calls `post("/api/account/verify-email", ...)` — if the token is expired and the backend returns 401, the user is hard-navigated away mid-verification with no explanation, losing the error context. — fix: add `/verify-email` to the passthrough list in the 401 guard, same as the auth routes. — effort: trivial

---

## HIGH

- [ ] [HIGH] [Login.tsx:13] **Authenticated user redirect races auth loading state.** `if (user) { navigate("/"); return null; }` fires synchronously before `loading` is checked. On first render, `user` is `null` and `loading` is `true`, so this is fine — but if the session expires mid-visit and the user manually navigates to `/login`, the `useAuth` hook may briefly return `user = null, loading = false` then a race with `refresh()` causes a momentary flicker. The larger issue: `loading` is never consulted here, so a fast network can redirect the user before they intend to reach `/login`. — fix: guard with `if (!loading && user) { navigate("/") }` and render nothing (or a spinner) while `loading` is true. — effort: trivial

- [ ] [HIGH] [Register.tsx:14] **Same loading-race issue as Login.tsx.** Same pattern: `if (user) { navigate("/"); return null; }` without checking `loading`. — fix: same as Login fix. — effort: trivial

- [ ] [HIGH] [Dashboard.tsx:117-118] **`doStart` and `doStop` use a hardcoded 2-second `setTimeout(load, 2000)` after Fly API calls.** If Fly is slow, the machine list refreshes with stale state. The backend sets DB state optimistically on success, but the 2s timer is arbitrary. More importantly, there is no visual indication that a start/stop is in progress — the machine dot does not change, no spinner appears, and the menu closes immediately. Users will click repeatedly or think the action failed. — fix: set the machine's local state to a transitional value immediately after the action (e.g., optimistic "started"/"stopped"), then load to reconcile. — effort: small

- [ ] [HIGH] [NewMachineModal.tsx:116] **Enter key submits the form even when `needsSlot` is true.** `onKeyDown={e => e.key === "Enter" && !needsSlot && handleCreate()}` is only on the name input. If focus moves to the size select and user hits Enter while `needsSlot` is shown, the native `<select>` behavior fires but no creation is blocked at the form level. More critically, if `needsSlot` becomes true after the user has already typed the name and presses Enter quickly, `handleCreate` can be called concurrently with the billing flow. — fix: disable the name input's Enter handler entirely while `loading || buyingSlot`, and ensure no form element can trigger submission during billing. — effort: trivial

- [ ] [HIGH] [Credits.tsx:35-51] **Purchase error is silently swallowed.** `handlePurchase` catches errors and calls `setPurchasing(null)` but shows no error message to the user — the button just re-enables. The user has no way to know whether their checkout attempt failed. — fix: add an `error` state and display a message on catch. — effort: trivial

- [ ] [Settings.tsx:45-48] **`openPortal` swallows errors silently.** The empty `catch {}` means if the billing portal request fails (network error, 500), the user clicks "Fix Payment" or "Manage Billing" and nothing happens. — fix: show an error message in the catch block. — effort: trivial

- [ ] [HIGH] [Settings.tsx:33-43] **`saveProfile` and `saveScript` reuse the same `saved` state string for both success and error messages.** Errors are set to `"Save failed"` and success to `"Profile saved"` — both use the same green success-styled banner (`color: "var(--success)"`). Error text is displayed with success styling, confusing users. — fix: add a separate `saveError` state with danger styling, or pass a type flag to the feedback component. — effort: trivial

- [ ] [HIGH] [Admin.tsx:14-17] **Admin page has no error handling and no loading state.** `get("/api/admin/stats")` and `get("/api/admin/users")` are called without try/catch and without any loading indicator. If either fails (network error, 403 mid-session), the page silently renders empty — admins have no way to distinguish "zero users" from a failed fetch. — fix: add try/catch with error state, and a loading indicator while fetches are in flight. — effort: small

- [ ] [HIGH] [Admin.tsx:25-29] **`giveCredit` has no error handling and no success feedback.** If the POST fails, nothing happens — no error shown, no confirmation. The credit amount field clears (on success path) but because there is no try/catch, an exception leaves the form in a dirty state with no message. — fix: wrap in try/catch, show success/error feedback. — effort: trivial

- [ ] [HIGH] [Dashboard.tsx:46] **`isMobile` is computed once at mount using `window.innerWidth` and never updated.** Resizing the window (or rotating a device) does not recompute the layout mode. Users who open the app in a narrow window then resize to desktop (or vice versa) are stuck in the wrong layout until they reload. — fix: use a `useEffect` with a `resize` listener, or a CSS media query approach. — effort: small

---

## MEDIUM

- [ ] [MEDIUM] [VerifyEmail.tsx:12] **Email verification is triggered automatically on mount with no way to retry.** If the `POST /api/account/verify-email` call fails transiently (network timeout), the user sees "Verification failed" with a link to settings. There is no retry button on the page itself — users must navigate away, resend, click a new email link, and start over. — fix: add a "Try again" button that re-fires the verification POST with the same token while the token is still valid. — effort: trivial

- [ ] [MEDIUM] [ResetPassword.tsx:15-22] **Missing token shows error state with no context about what happened.** The "Invalid reset link" screen is rendered immediately with no explanation of why the link is invalid (expired, already used, missing token). The backend can distinguish these cases. — fix: accept an optional `reason` query param from the backend redirect, or improve the copy to say "This link may have expired or already been used." — effort: trivial

- [ ] [MEDIUM] [NewMachineModal.tsx:19] **`ghConnected` initializes to `true` (assume connected).** The comment says "assume connected until proven otherwise," but the consequence is that the GitHub connect prompts are hidden until both async calls complete. If both `/api/github/app-status` and `/api/github/repos` fail (network error), `ghConnected` stays `true` and the user sees a text input with no prompt to connect GitHub. The user will type a private repo URL and get a silent clone failure at machine creation time. — fix: initialize `ghConnected` to `false` and set to `true` only on confirmed connection, OR initialize to `null` and show a loading state. — effort: small

- [ ] [MEDIUM] [NewMachineModal.tsx:116] **Name field has no validation feedback for the character constraint.** The backend schema rejects names that don't match `/^[a-zA-Z0-9-]+$/` with a 400, but the modal only validates `!name.trim()` before enabling the Create button. A user who types "my machine" (with a space) or "api_server" (underscore) will hit a 400 error that surfaces as `"Creation failed"` with no hint about what is wrong. — fix: add inline validation on the name field that checks against the same regex and shows a hint like "Letters, numbers, and hyphens only." — effort: trivial

- [ ] [MEDIUM] [Dashboard.tsx:104-107] **Layout load error is silently swallowed.** `get("/api/settings/layout").catch(() => {})` means if the layout endpoint returns an error, the dashboard silently falls back to auto-layout. This is acceptable for most cases, but combined with the fact that `load()` (machine fetch) also has no try/catch, a completely broken initial load renders an empty dashboard with no error state — just blank panes and a sidebar with no machines. — fix: catch `load()` errors and show a recoverable error state with a reload button. — effort: small

- [ ] [MEDIUM] [Dashboard.tsx:158] **Mobile layout: no empty state when there are no running machines.** `am` resolves to `undefined` when `live` is empty. The terminal iframe slot renders nothing (`{am && <TerminalIframe ticket={tickets[am.id]} />}`), and the machine select shows an empty dropdown. There is no guidance to create a machine. — fix: show an empty state message with a "New Machine" CTA when `live.length === 0` on mobile. — effort: trivial

- [ ] [MEDIUM] [SessionView.tsx:14] **SessionView uses raw `fetch` instead of the shared `api()` helper.** This bypasses the 401 redirect logic and centralized error handling. More importantly, it polls on a 3-second interval with no backoff — if the session endpoint is down or returns repeated errors, the client fires requests every 3s indefinitely. — fix: use the `api()` helper and implement exponential backoff or a max-retry cap on polling errors. — effort: small

- [ ] [MEDIUM] [Credits.tsx:22-33] **Credits page uses raw `fetch` instead of the shared `api()` helper.** Same issue as SessionView — bypasses 401 redirect and centralized error handling. If the user's session expires while on the Credits page, they see a loading spinner that resolves to an empty balance with no prompt to sign in. — fix: use `get()` from `../lib/api` to get consistent 401 handling. — effort: trivial

- [ ] [MEDIUM] [MachineMenu.tsx:18-33] **Delete confirmation has no loading/disabled state after "Delete" is clicked.** Clicking Delete fires `onDelete(m.id)` which is `doDel` in Dashboard — an async operation with no feedback. The modal closes immediately (via `setMenu(null)` in `doDel`), but if the delete fails, the user is back at the dashboard with no indication of what happened. — fix: pass a deleting state to MachineMenu and disable the button while the delete is in progress, and only close the modal on success. — effort: small

- [ ] [MEDIUM] [App.tsx:17] **Auth loading state has no timeout.** If `/api/auth/me` hangs indefinitely, the entire app renders "loading..." forever. There is no timeout, no retry, and no fallback that allows the user to navigate to login. — fix: add a timeout (e.g., 10s) after which `loading` is forced to `false` and `user` to `null`, allowing the redirect to `/login`. — effort: small

- [ ] [MEDIUM] [Dashboard.tsx:326] **"Reconnect" button (↻) in pane header clears the ticket and calls `onRefresh` but provides no feedback while the new ticket is being fetched.** The pane transitions from "Loading terminal..." (while ticket is null) back to the iframe once the ticket arrives, but there is no visible indicator that a reconnect is in progress — the pane just goes blank briefly. — fix: show a "Reconnecting..." message in the `Msg` component while ticket is null and a refresh is pending. — effort: trivial

- [ ] [MEDIUM] [Register.tsx:49] **Name field has `type` omitted** (defaults to `type="text"`), while the email and password fields are correctly typed. More critically, there is no `autocomplete` attribute on any field in the registration or login forms, preventing password managers from correctly filling credentials and marking new account registrations. — fix: add `autocomplete="name"`, `autocomplete="email"`, `autocomplete="new-password"` / `autocomplete="current-password"` to the relevant inputs. — effort: trivial

- [ ] [MEDIUM] [ForgotPassword.tsx:31-36] **Success state shows the submitted email in the confirmation message** ("If an account exists for {email}...") which is correct anti-enumeration behavior — but the email value is displayed as raw text with no sanitization. While React escapes text content, if the email somehow contains unusual characters from state manipulation, this could render confusingly. More importantly, there is no "resend" option on the success screen — if the user didn't receive the email, they must navigate back and resubmit. — fix: add a "Resend" button on the success screen that resets `sent` to false or re-fires the request. — effort: trivial

---

## Accessibility Gaps (blocking or confusing for keyboard/screen reader users)

- [ ] [HIGH] [MachineMenu.tsx:68-74] **`MenuBtn` renders `<button>` elements with no `type="button"` attribute inside what could be perceived as a form context, and no `aria-label` for icon-only actions.** The text content uses Unicode symbols (■, ▶, ✕) that screen readers will announce literally ("black square", "black right-pointing triangle", "multiplication x"). — fix: add `type="button"` (already present on some) and `aria-label` with plain-language descriptions on all action buttons. — effort: trivial

- [ ] [HIGH] [Dashboard.tsx:449-451] **`HdrBtn` pane header buttons have `title` for tooltip but no `aria-label`.** `title` is not reliably announced by screen readers. Icon-only buttons (↻, ↗, ⋯, ×, ⚡, 📎) are completely opaque to assistive technology. — fix: replace or supplement `title` with `aria-label` on every `HdrBtn`. — effort: trivial

- [ ] [MEDIUM] [NewMachineModal.tsx / MachineMenu.tsx] **Both modal overlays trap neither focus nor keyboard navigation.** A keyboard user who opens either modal can tab out of it into the obscured background content. There is no focus trap, no `aria-modal="true"`, and no `role="dialog"`. The Escape key does not close the modals (Overlay's `onClose` is only bound to a mouse click on the backdrop). — fix: add `role="dialog"`, `aria-modal="true"`, a focus trap (e.g., `focus-trap-react` or manual implementation), and an Escape key handler. — effort: medium

- [ ] [MEDIUM] [Login.tsx / Register.tsx] **OAuth buttons are `<a>` elements with `btn` className but no `role="button"` or keyboard activation guarantee.** An `<a>` without `href` would not be keyboard-focusable, but these do have `href` — however, they are styled as buttons and semantically should have `role="button"` or be actual `<button>` elements that do a programmatic navigation/POST. Screen readers will announce them as "link, Continue with GitHub" which is acceptable, but if the OAuth endpoint ever changes to a POST flow, this becomes a real issue. — fix: acceptable as-is for now, but add `aria-label="Sign in with GitHub"` / `aria-label="Sign in with Google"` to distinguish them from generic "link" announcements. — effort: trivial

- [ ] [MEDIUM] [Settings.tsx:94] **Success/error feedback banner (`saved`) is not announced to screen readers.** The banner appears and disappears after 2 seconds but has no `role="status"` or `aria-live` region, so users relying on screen readers never hear the feedback. — fix: add `role="status"` and `aria-live="polite"` to the feedback element, and ensure it is always rendered in the DOM (empty string when not active) so the live region is pre-registered. — effort: trivial

---

## Summary

| Severity | Count |
|----------|-------|
| CRITICAL | 4 |
| HIGH | 12 |
| MEDIUM | 15 |
| **Total** | **31** |

### Top priorities for a single sprint:
1. **Silent failures on start/stop/delete** (Dashboard.tsx) — users have no idea actions failed
2. **`handleBuySlot` billing state leak** (NewMachineModal.tsx) — button can freeze permanently
3. **401 redirect eating verify-email errors** (api.ts) — breaks email verification flow
4. **Error styling on success banner** (Settings.tsx) — errors shown in green, confusing
5. **No focus trap in modals** — keyboard users cannot use the app safely
