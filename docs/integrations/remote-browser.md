# Remote Browser Integration

Spec: [`specs/browser-remote-sandbox.md`](../../specs/browser-remote-sandbox.md) (C6).
Status: Done (2026-05-12).

## 1. Why remote-browser exists

R1's hosted SaaS surfaces on `r1.run` run inside a tenant-isolated Cloud Run network. Any tool that initiates outbound TCP from inside that boundary is an implicit trusted-origin endpoint — running real Chromium there means a malicious task plan could pivot through it to private subnets. Remote-browser sandboxing breaks that pivot:

- Chromium lives in a different network (Browserless's, or a dedicated VPC behind `browser.r1.run`).
- The R1 process talks to it strictly over a CDP WebSocket with an explicit allowlist of destination URLs.
- Per-tenant incognito contexts make cookie/storage isolation a property of the protocol, not of operator hygiene.

The Cross-Product Contract Bible (2026-04-16) treats R1's hosted-SaaS network as a tenant-isolated boundary; remote-browser sandboxing keeps the bible's invariant intact.

## 2. Choosing a provider

R1 ships three implementations of `internal/browser/Provider`, picked at construction time from `browser.provider`:

| Provider | When to use | Pros | Cons |
| --- | --- | --- | --- |
| `local` | CLI / developer laptop / self-hosted single-binary mode. | Zero ops, no extra services. | Egress originates from the operator's host — unfit for shared hosted-SaaS. |
| `browserless` | Default for hosted SaaS. | Lowest operational burden, Browserless's network is the trust boundary, mature self-host story. | Cloud tier is shared infra; Scale-tier licensing ($250/mo). |
| `inhouse` | Tenants that need network-policy guarantees the public Browserless cloud can't offer (VPC-peered egress to a customer's internal site). | Full network control. | +Cloud Run cost, +operator burden, +deploy plumbing. |

The conformance suite makes the swap a config change, not a refactor (every provider passes `browser.ProviderConformance` byte-identically).

## 3. Sizing the in-house service

Single-Chromium-per-Cloud-Run-instance is the load-bearing isolation property. From the standing rules in `cloudbuild-r1-browser.yaml`:

- `concurrency=1`
- `memory=2Gi` (Chromium + the supervisor)
- `cpu=1`
- `min-instances=1` (1 idle instance per region to absorb the cold-start)
- `max-instances=50`
- `timeout=300s`

Expected cost: ~$8/mo idle + ~$0.30/hr active per instance. A mission averaging two 30-second browser sessions runs at <$0.01 per mission in steady state.

## 4. Network policy

`NetworkPolicy` is enforced at two layers:

1. **URL pre-check inside `Navigate`**: the host is decoded from the URL, glob-matched against the policy, and denied requests never leave the R1 process. Returns `*NetworkDeniedError`.
2. **CDP `Network.setRequestInterception`**: every in-page subresource fetch is intercepted by Chromium and matched against the same policy. Denied subresources reply with `AccessDenied` to the page.

In-house deployments may additionally layer a defense-in-depth squid/tinyproxy as `HTTP_PROXY` inside the container — see `docs/operations/r1-browser-service.md`.

### Three worked examples

```yaml
# Public docs site only — the lowest-trust safe default.
browser:
  network_policy:
    mode: deny_by_default
    allow:
      - docs.anthropic.com
      - "*.r1.run"
    deny:
      - "169.254.169.254"
      - "metadata.google.internal"
```

```yaml
# RelayOne MSP-internal — adds the MSP API surface to the allowlist.
browser:
  network_policy:
    mode: deny_by_default
    allow:
      - "*.r1.run"
      - "*.relayone.com"
      - docs.anthropic.com
    deny:
      - "169.254.169.254"
      - "metadata.google.internal"
```

```yaml
# Customer-VPC-only — strictest. Used with the inhouse provider
# in a VPC-peered Cloud Run deployment.
browser:
  network_policy:
    mode: deny_by_default
    allow:
      - "internal.customer.example"
      - "auth.customer.example"
    deny:
      - "169.254.169.254"
      - "metadata.google.internal"
      - "*.r1.run"            # don't pivot back into our own infra
```

## 5. Troubleshooting

| Symptom | First check |
| --- | --- |
| WS disconnect immediately after dial | Token mismatch — set `BROWSERLESS_TOKEN` or check `cfg.TenantTokens[<tenantID>]`. |
| `ErrAuthFailed` on Open | Browserless rejected the token; rotate via the Browserless dashboard. |
| `*NetworkDeniedError{Host: …}` on Navigate | The host is not on the policy's allow list. Add it OR switch the test to a host that is. |
| Cloud Run cold-start adds 5s to first Open | Expected (Chromium startup); subsequent Opens reuse the warm instance until `idle_timeout`. |
| `ErrBudgetExhausted` mid-mission | Per-mission Unit cap was hit — raise `browser.budget_per_mission_units` for that mission's plan or skip browser-requiring steps. |

## 6. The no-cookies rule

R1's remote browser tool MUST NOT accept user-supplied cookies. If a task needs authenticated access to a customer service, the integration must be at R1's tool layer (`internal/tools/`), outside the browser session — the browser sees only the resulting public URLs.

The lint test `internal/browser/lint_cookies_test.go` enforces this at CI gate: any reference to `document.cookie`, `localStorage`, `sessionStorage`, or `setCookie` outside the conformance assertions fails the build.

Worked example for "I need the browser to render a customer's authenticated page": authenticate at the tool layer, retrieve a short-lived URL (signed token in query string, server-side session, or one-time link), then hand the URL to the browser tool. The browser never sees the credential.
