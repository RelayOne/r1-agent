# R05-rn — Login screen with form validation + mocked API

Build a React Native login screen that POSTs to a backend, displays
errors, and navigates on success. All network calls are mocked via
`jest.mock`, so this stays a pure JS/component test.

## Scope

Files:
- `App.tsx` renders `<LoginScreen />` wrapped in a minimal navigator
  shim (can be a plain conditional render if the screen emits a
  `onLoginSuccess` callback).
- `src/LoginScreen.tsx` — form with:
  - Email input (`testID="email"`)
  - Password input (`testID="password"`, `secureTextEntry`)
  - Submit button (`testID="submit"`)
  - Error text (`testID="error"`, hidden when empty)
  - Calls `POST /login` via a `login()` helper in `src/api.ts`.
  - On 200: calls `onLoginSuccess(token)`.
  - On 401: sets error text to "invalid credentials".
  - On 400: sets error text to "check your input".
- `src/api.ts` — exports `async function login(email, password): Promise<{token}>`.
- `__tests__/LoginScreen.test.tsx`:
  - Mocks `fetch` globally via `jest.spyOn(global, 'fetch')`.
  - Asserts 200 path (resolves, onLoginSuccess called with token).
  - Asserts 401 path (error text appears).
  - Asserts 400 path.

## Acceptance

- `pnpm install` + `pnpm test` exit 0.
- Three tests pass: happy, 401, 400.
- `testID` attributes present on all interactive elements.
- No real HTTP — `global.fetch` is mocked.

## What NOT to do

- No real navigation library. Callback is enough.
- No persistent storage (AsyncStorage).
- No biometrics.
- No styling beyond minimal `StyleSheet.create`.
