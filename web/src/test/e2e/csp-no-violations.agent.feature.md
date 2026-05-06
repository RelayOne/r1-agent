# Feature: zero CSP violations across every route

Every shipped route must load without a single
`Content Security Policy` console.error. This is enforced
deterministically by the Playwright suite (see csp-axe.spec.ts);
the .agent.feature.md exists so the spec 8 MCP harness can drive
the same routes.

## Scenario: walk every route

```gherkin
Given the dist bundle is mounted at the daemon's web root
For each route in:
  - "/"
  - "/d/local"
  - "/d/local/sessions/seed-session"
  - "/d/local/sessions/seed-session/lanes/seed-lane"
  - "/settings"
  - "/no-such-route"
Then opening the route emits zero console.error matching /Content Security Policy/
And axe-core reports zero serious or critical findings
And the page reports a non-null navigation response
```
