# no-e2e-in-ac

> Browser-based E2E tests (Playwright, Cypress, Puppeteer) cannot run as acceptance criteria in automated builds — they need browsers, display servers, and setup the build agent doesn't have.

<!-- keywords: playwright, cypress, puppeteer, e2e, end-to-end, acceptance criteria, browser test -->

## The rule

NEVER emit acceptance criteria that run browser-based E2E test frameworks:
- `playwright test`
- `cypress run`
- `puppeteer`
- Any command that opens a browser or requires `DISPLAY` / headless Chrome

These require:
1. Browser binaries installed (chromium, firefox)
2. OS-level dependencies (libX11, libgbm, etc.)
3. A display server or `--headless` flag that still needs the binaries
4. Playwright's `npx playwright install` setup step

None of these exist in the automated build environment.

## What to use instead

For acceptance criteria that verify UI behavior:

| Instead of | Use |
|---|---|
| `playwright test tests/e2e/login.spec.ts` | `pnpm --filter @sentinel/web build` (verifies the code compiles) |
| `cypress run --spec dashboard.cy.ts` | `vitest run` (unit tests with jsdom verify component logic) |
| "verify the dashboard updates via SSE" | `grep -r 'EventSource\|useSSE' apps/web/hooks/` (structural check that SSE code exists) |
| "verify auth redirects work" | `tsc --noEmit` (type-checks the middleware) + `vitest run` (unit tests for the redirect logic) |

## When E2E tests ARE appropriate

E2E tests belong in:
- CI pipelines with browser setup (GitHub Actions + `playwright install`)
- Manual QA sessions
- A dedicated `test:e2e` script that the developer runs locally

They do NOT belong in acceptance criteria that gate automated SOW session transitions.

## The failure mode this prevents

Without this rule, the LLM generates ACs like:
```
pnpm --filter @sentinel/web exec playwright test tests/e2e/dashboard.spec.ts
```

The AC runner executes this command, playwright isn't installed, the command fails with "playwright: not found" or "No tests found", and the session enters the repair loop. The repair agent then:
1. Tries to install playwright (downloads 200MB of browsers)
2. Runs the test, which fails because there's no display server
3. Gets stuck in a loop trying to fix a fundamentally unsolvable problem

The correct fix is to never emit the AC in the first place.
