<!-- STATUS: done -->
<!-- BUILD_COMPLETED: 2026-05-12 -->
<!-- CREATED: 2026-05-11 -->
<!-- DEPENDS_ON: -->
<!-- BUILD_ORDER: 45 -->

# C6 — Browser Tool Sandboxed Under Remote Browser

## 1. Overview

R1's `internal/browser/` ships two backends today (per `specs/browser-interactive.md`): a stdlib `*Client` for read-only fetches and a `*RodClient` (build-tag `stoke_rod`) that drives a locally-launched headless Chromium via go-rod. Both backends run inside the same OS as the R1 process: any DNS lookup, TCP connect, cookie jar, or filesystem write originates from the customer's host. For self-hosted CLI use that is correct; for the hosted SaaS surfaces on `r1.run` (per README §"Hosted SaaS surfaces"), it is unacceptable — a customer task that invokes the browser tool against `internal.acme.example` would attempt egress from R1's Cloud Run network, and any auth cookies set during the session would persist in a shared Chromium user-data-dir.

This spec adds a third backend — a **remote browser provider** — and refactors the existing local code behind a small `Provider` interface so the executor can swap implementations at construction time without touching call sites. Two concrete remote providers ship:

1. **Browserless** (`internal/browser/browserless/`) — connects over WebSocket CDP to either the hosted `wss://chrome.browserless.io?token=…` endpoint or to a self-hosted Browserless container at `wss://browserless.r1.run?token=…`. Picked first because Browserless is the only mature CDP-over-WS service with a documented self-host story; the 2026 cloud free tier (2 concurrent, 1k Units/month per their pricing page) is enough to validate the wire format before any in-house investment.
2. **In-house Cloud Run** (`internal/browser/inhouse/`) — a stateless headless Chromium service `r1-browser` that joins the existing 9-service Cloud Run footprint at `browser.r1.run`. Same Provider interface; same CDP-over-WS wire. Used by tenants that need network-policy guarantees the public Browserless cloud can't offer (e.g., VPC-peered egress to a customer's internal site).

Provider selection is **operator-driven** via `browser.provider: browserless|inhouse|local` in the R1 config. No customer-facing flag — the choice is a deployment property of the R1 instance, not a per-task option.

**Why this matters now (sec-arch perspective).** The Cross-Product Contract Bible (2026-04-16) treats R1's hosted-SaaS network as a tenant-isolated boundary. Any tool that initiates outbound TCP from inside that boundary is implicitly a trusted-origin endpoint; running real Chromium there means a malicious task plan can pivot through it to private subnets. Remote-browser sandboxing breaks that pivot — Chromium is in a *different* network (Browserless's, or a dedicated VPC behind `browser.r1.run`), and the R1 process talks to it strictly over a CDP WebSocket with an explicit allowlist of destination URLs.

## 2. Stack & Versions

- Go 1.25 (matches existing `services/r1-coord-api/Dockerfile`).
- `nhooyr.io/websocket` v1.8+ for the CDP-over-WS client. Rejected: `gorilla/websocket` (maintenance mode), `github.com/coder/websocket` (renamed `nhooyr` fork; we pin the canonical name to match the rest of the repo).
- `github.com/chromedp/cdproto` for typed CDP request/response structs. Rejected `mafredri/cdp` — chromedp's `cdproto` is already pulled in by go-rod's transitive set; sharing it keeps the dep graph flat.
- Browserless v2 image `ghcr.io/browserless/chrome:v2.x` for the in-house container baseline (cherry-pick Browserless's own Docker image when running the inhouse provider as a thin wrapper) OR upstream `chromedp/headless-shell:latest` for a tighter Cloud-Run-native build. Decision deferred to T10 — both paths documented.
- Existing `internal/cloud/` for token persistence (mirror the `cloud.json` pattern under `~/.r1/browser.json`).
- Existing `internal/costtrack/` for per-tenant cost rollups.
- Existing `internal/coderadar/` for telemetry emission (this is the bus referenced in `audit/scan-test-quality.md` for B3-style events).

## 3. Architecture

### 3.1 Package layout

```
internal/browser/
  provider.go              # new — Provider interface + Open/Navigate/WaitFor/Screenshot/Eval/Close
  provider_local.go        # new — wraps the existing *RodClient behind Provider
  provider_local_test.go   # new — conformance suite, run for every provider
  conformance.go           # new — exported test suite ProviderConformance(t, factory)
  browserless/
    client.go              # new — CDP-over-WS connect, token auth, incognito contexts
    client_test.go         # new — mock CDP server + protocol fuzzing
    network_policy.go      # new — Browserless Network API allow/deny enforcer
    cost.go                # new — Unit counting (30s blocks) per Browserless docs
  inhouse/
    service/               # new — Cloud Run service source (separate go.mod under services/)
    client.go              # new — same wire as browserless/, different auth (SA → ID token)
    client_test.go
  fallback.go              # new — wraps a primary Provider + optional local fallback
  fallback_test.go         # new
  events.go                # new — emit browser.session_*, browser.navigate, browser.error
docs/
  integrations/remote-browser.md   # new — operator runbook
  operations/r1-browser-service.md # new — in-house Cloud Run sizing + deploy
services/
  r1-browser/                      # new — sibling of r1-coord-api / r1-docs / r1-admin
    Dockerfile
    main.go                         # tiny supervisor: launches Chromium + forwards CDP WS
    go.mod
    cloudbuild-deploy.yaml          # piggybacks the existing services/cloudbuild-deploy.yaml pattern
configs/
  browser.example.yaml              # new — operator-facing schema
```

### 3.2 Modified files

- `internal/browser/backend.go` — keep `Backend` interface; add a thin shim so `Provider` is the new canonical contract and `Backend` forwards to it. Existing callers of `*Client` and `*RodClient` stay byte-identical.
- `cmd/r1/browse_cmd.go` — read `browser.provider` from config (or `R1_BROWSER_PROVIDER` env) and construct the right Provider. Default stays `local` so unmodified `r1 browse <url>` invocations keep working.
- `internal/executor/browser.go` — accept a `Provider` instead of holding `*Client` + `*RodClient` directly. Both old types now satisfy `Provider` via shims.
- `internal/oneshot/` — reject `--one-shot` invocations that would route to a remote provider (latency-critical path; see boundary §11). Hard error at command init.
- `go.mod` / `go.sum` — add `nhooyr.io/websocket`. `cdproto` is already transitively present.

### 3.3 `Provider` interface (new, in `provider.go`)

```go
package browser

import (
    "context"
    "time"
)

// Provider is the executor-facing browser contract. Local rod, Browserless,
// and the in-house Cloud Run service all satisfy it. Selection happens at
// construction time from config (browser.provider: browserless|inhouse|local).
type Provider interface {
    Open(ctx context.Context, opts SessionOpts) (Session, error)
    Close(ctx context.Context) error  // shuts the provider; existing Sessions become invalid
    Name() string                      // "local" | "browserless" | "inhouse"
}

// Session is one browser context — incognito-isolated, single-tenant, single-task.
type Session interface {
    Navigate(ctx context.Context, url string) (NavigateResult, error)
    WaitFor(ctx context.Context, selector string, timeout time.Duration) error
    Screenshot(ctx context.Context) ([]byte, error)
    Eval(ctx context.Context, script string) (any, error)
    Close(ctx context.Context) error
    ID() string                        // opaque; logged with every event
}

type SessionOpts struct {
    TenantID       string             // required when Provider != local
    UserAgent      string
    Viewport       Viewport
    NetworkPolicy  NetworkPolicy      // resolved by the caller from operator config
    IdleTimeout    time.Duration      // close after no activity (default 5m)
    HardTimeout    time.Duration      // absolute lifetime cap (default 30m)
}

type NavigateResult struct {
    FinalURL string
    Status   int
    Title    string
}

type NetworkPolicy struct {
    Allow []string   // glob: ["docs.anthropic.com", "*.relayone.com"]
    Deny  []string   // takes precedence over Allow
    Mode  PolicyMode // PolicyDenyByDefault | PolicyAllowByDefault
}
```

Three notes on the shape:

- `Eval` is intentionally typed `any` not `string` — Browserless and in-house both return CDP `Runtime.evaluate` results that carry JS primitives (number, bool, object). Forcing JSON-string at this layer would re-serialize and lose precision. Callers cast.
- `Open` returns a `Session` rather than a flat token because each task wants its own incognito context. Multiple `Open` calls against the same `Provider` allocate independent contexts. The Provider is the *connection*; the Session is the *tab*.
- `Close(ctx)` on Provider is idempotent and final. The fallback wrapper (T8) takes advantage of this to release a primary provider before promoting a secondary.

### 3.4 Refactor sequence

T1 (the very first task) refactors the *existing local* code behind `Provider` and `Session`. The remote providers (T2/T3) implement the same interface — no executor change between T1 and T13. This guarantees we never end up with an executor that "knows" it's talking to a remote browser; the interface is the only contract.

## 4. Implementation checklist

Each item is self-contained. File paths absolute from repo root. Tests live in a sibling `_test.go` unless noted.

1. [x] **T1: Provider abstraction & local refactor.** Create `internal/browser/provider.go` with `Provider`, `Session`, `SessionOpts`, `NavigateResult`, `NetworkPolicy`, `PolicyMode` (per §3.3). Create `internal/browser/provider_local.go` — a `localProvider` struct that wraps the existing `*RodClient` and adapts its `RunActions` calls into the new `Open`/`Navigate`/`WaitFor`/`Screenshot`/`Eval`/`Close` shape. The translation: `Open` ⇒ acquire a rod page; `Navigate` ⇒ `RunActions` with a single `ActionNavigate`; `WaitFor` ⇒ `ActionWaitForSelector`; `Screenshot` ⇒ `ActionScreenshot`; `Eval` ⇒ a new internal helper that calls `page.Eval(script)`; `Close` ⇒ release page + decrement rod pool. Compile-time assert `var _ Provider = (*localProvider)(nil)`. Test: `provider_local_test.go` runs the full conformance suite (item 14) against `localProvider` and the existing rod_real_test scenarios must still pass under the new wrapper.

2. [x] **T1b: Provider conformance suite.** Create `internal/browser/conformance.go` exporting `func ProviderConformance(t *testing.T, factory func() (Provider, error))`. The suite covers, in order: Open returns distinct Session IDs across two calls; Navigate to httptest URL returns Status=200 + FinalURL; WaitFor selector that appears after 200ms succeeds; WaitFor missing selector returns deadline error after the configured timeout; Screenshot bytes start with PNG magic `\x89PNG`; Eval returns the expected JS primitive (`Eval("1+2")` → 3 numeric); Close is idempotent. Every provider's `*_test.go` calls `ProviderConformance(t, factory)`. This is the keystone — it is the single thing that proves both Browserless and in-house honor the contract identically (acceptance criterion §10).

3. [x] **T2: Browserless client — package skeleton.** Create `internal/browser/browserless/client.go` with a `Client` struct holding the endpoint URL, token, `*websocket.Conn` pool (max=cfg.MaxConcurrent), and a per-tenant context cache. Constructor `NewClient(cfg Config) (*Client, error)` validates the endpoint scheme (`wss://` required; `ws://` rejected unless `cfg.AllowInsecure=true` for local-docker development) and resolves the token from `BROWSERLESS_TOKEN` env or `cfg.Token`. No network IO at construction — defer to first `Open`. Compile-time assert `var _ browser.Provider = (*Client)(nil)`.

4. [x] **T2a: Browserless connect over CDP-over-WS.** Implement `Client.connect(ctx)` — dials `wss://<endpoint>?token=<TOKEN>&headless=true&blockAds=true&stealth=false&trackingId=<TENANT>` using `nhooyr.io/websocket.Dial`. On success returns a `*cdpConn` that wraps the WS conn and a JSON-RPC method counter. Read pump goroutine reads each WS frame, parses `{id, method, params}`, dispatches to per-id channels for responses and to a fanout for events. Test: `client_test.go` starts an `httptest.NewServer` that upgrades to WS and replays canned CDP responses; assert connect succeeds with a synthetic token, fails fast on 401 with `ErrAuthFailed`.

5. [x] **T2b: Browserless Open — incognito context per call.** Implement `Client.Open(ctx, opts)`. Sends `Target.createBrowserContext` (CDP) — Browserless honors this to allocate a fresh incognito profile. Capture the returned `browserContextId`. Then `Target.createTarget` inside that context. Store both IDs in a `*browserlessSession` value. Compile-time assert `var _ browser.Session = (*browserlessSession)(nil)`. The session.ID() returns `targetId` (CDP-defined; opaque). Test: mock CDP server responds to both methods; assert two consecutive Open calls return distinct `browserContextId`s and ID() strings.

6. [x] **T2c: Browserless Navigate / WaitFor / Screenshot / Eval / Close.** Each maps to a single CDP method on the session's `targetId`:
   - Navigate → `Page.navigate` + `Page.loadEventFired` await. Returns FinalURL from `frameNavigated` event + status from concurrent `Network.responseReceived`.
   - WaitFor → `Runtime.evaluate` polling `document.querySelector(sel) !== null` every 100ms until timeout (rod-equivalent semantics; CDP does not natively offer wait-for-selector).
   - Screenshot → `Page.captureScreenshot {format:"png"}`; base64-decode the result.
   - Eval → `Runtime.evaluate {expression: script, returnByValue: true}`; unwrap `result.value` per type.
   - Close → `Target.disposeBrowserContext`. The incognito context disposal evicts cookies/localStorage by construction — this is the load-bearing isolation primitive.
   Tests in `client_test.go` cover happy-path + one wire error each (timeout, navigation aborted, JS throw).

7. [x] **T2d: Browserless token resolution & rotation.** Implement `cfg.Token` resolution chain: explicit field > `BROWSERLESS_TOKEN` env > `~/.r1/browser.json:browserless_token` (new file, mirrors `cloud.json` from `internal/cloud/config.go`). If all three are empty AND `cfg.Provider=="browserless"` then return `ErrNoToken` at construction (T1 fast-fail). On 401/403 from Browserless during a session, emit `browser.token_invalid` event and tear down all sessions for that tenant; do NOT retry with the same token.

8. [x] **T3: In-house Cloud Run service — `services/r1-browser/`.** Mirror `services/r1-coord-api/`. `main.go` is a tiny supervisor: launches `chromium --headless --remote-debugging-port=9222 --no-sandbox --disable-gpu --disable-dev-shm-usage --user-data-dir=/tmp/userdata-${RANDOM}` per-request, then reverse-proxies the inbound WS to localhost:9222's CDP WebSocket. Token check: validates a Google-issued ID token for the service account `r1-browser-invoker@<project>.iam`. Concurrency=1 per Cloud Run instance (one Chromium per container) so isolation is enforced by Cloud Run itself; horizontal scaling handles parallel sessions. Dockerfile: distroless+chromium is impractical (Chromium needs a libc); use `gcr.io/distroless/cc-debian12` with chromium installed via a builder stage that copies `/usr/bin/chromium` + its shared libs (`ldd` walk in the build script). Fall back to `ghcr.io/browserless/chrome:v2.x` if the distroless path proves too fragile in T13 testing — document the decision in `docs/operations/r1-browser-service.md`.

9. [x] **T3a: In-house client — `internal/browser/inhouse/client.go`.** Same shape as `browserless/client.go` but with a different auth header: each WS dial sends `Authorization: Bearer <ID-TOKEN>` where the ID token is minted by the metadata server (`https://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/identity?audience=https://browser.r1.run`). Token cache: 30-minute TTL with proactive refresh at 25min. The CDP-over-WS layer is shared code — extract `internal/browser/cdpconn/` if duplication exceeds 100 LOC after T2c lands; otherwise leave it inline. Test: stand up an httptest WS server that asserts the Bearer header is present and well-formed.

10. [x] **T3b: Cloud Run deploy config.** Add `services/r1-browser/cloudbuild-deploy.yaml` that builds the Dockerfile, pushes to `gcr.io/<project>/r1-browser:<sha>`, and deploys to Cloud Run `r1-browser` in `us-central1`. Settings: `min-instances=1`, `max-instances=50`, `concurrency=1`, `timeout=300s`, `memory=2Gi`, `cpu=1`. Append the new service to the README §"Hosted SaaS surfaces" inventory (raise the count from 9 to 10 if applicable; otherwise treat as a sibling that lives on Cloud Run but isn't customer-facing — `browser.r1.run` is internal-only, no public DNS).

11. [x] **T4: Tenant isolation — per-session incognito.** In every provider (`Open`), unconditionally enable incognito: Browserless via `Target.createBrowserContext`, in-house via the `--user-data-dir=/tmp/userdata-${UUID}` per-launch arg (T8 already does this; T4 is the contract enforcement). Add a test `TestNoCrossSessionCookies(t)` in `conformance.go`: open session A, navigate to a server that sets `Set-Cookie: a=1`, close A; open session B against the same server, assert the cookie is absent from the request. This runs against every provider via the conformance harness. Failure here is non-negotiable — block release if red.

12. [x] **T4a: Tenant credential plumbing.** Extract tenant from the executor's invocation context: re-use A4 SSO claims (per `services/r1-coord-api/internal/auth/`) to derive `SessionOpts.TenantID`. When R1 runs outside the hosted SaaS (CLI / `r1 browse` on a developer laptop), TenantID defaults to `"local"`. Browserless tokens are scoped per-tenant via the operator config map `browser.tenant_tokens: {tenantA: "<token1>", tenantB: "<token2>"}`. Missing tenant → fall back to `browser.default_token` if set, else `ErrNoTenantToken`. Test: two configured tenants get distinct WS endpoints; an unknown tenant errors.

13. [x] **T5: Network egress policy — config schema.** Define `NetworkPolicy` config under `browser.network_policy:` in `configs/browser.example.yaml`:
    ```yaml
    browser:
      provider: browserless
      network_policy:
        mode: deny_by_default     # or: allow_by_default
        allow:
          - docs.anthropic.com
          - "*.relayone.com"
          - "*.r1.run"
        deny:
          - "169.254.169.254"     # GCE metadata
          - "metadata.google.internal"
    ```
    Loader in `internal/browser/policy_config.go`. Hard-fail at boot if `mode: deny_by_default` AND `allow:` is empty (operator footgun guard).

14. [x] **T5a: Network policy enforcement — Browserless path.** Use Browserless's `--rejectResourceTypes` is too coarse; instead, after `Target.createBrowserContext`, attach a `Network.setRequestInterception` patterns list to every target. On `Network.requestIntercepted`, match the request URL host against the policy: deny → `Network.continueInterceptedRequest {errorReason: "AccessDenied"}` + emit `browser.network_denied` event; allow → `Network.continueInterceptedRequest`. Glob matching via `path.Match` with a `*.` prefix special-case. Test in `browserless/network_policy_test.go`: a mock CDP server that simulates request-intercepted callbacks asserts the right action for each rule.

15. [x] **T5b: Network policy enforcement — in-house path.** Same `Network.setRequestInterception` CDP wiring as Browserless (the policy code is provider-agnostic — that's the point of the conformance suite). Additionally, layer a defense-in-depth nginx allowlist in `services/r1-browser/`: the Chromium subprocess sees an `HTTP_PROXY=localhost:3128` that points at a `tinyproxy` or `squid` with the same allow/deny rules. This catches the case where CDP interception is somehow bypassed by a future Chromium version. Optional in v1 if T13's in-house tests show CDP interception is sufficient; document the rationale either way in `docs/operations/r1-browser-service.md`.

16. [x] **T5c: Default policies per environment.** In `configs/browser.example.yaml`, document three environment templates:
    - `prod`: `deny_by_default`, allow only `*.r1.run` + tenant-customizable extension.
    - `staging`: `deny_by_default`, allow `*.r1.run` + `*.example.com` + tenant customization.
    - `dev`: `allow_by_default`, deny `169.254.169.254` + `metadata.google.internal` only.
    Loader resolves which template is in force from `R1_ENV` env (existing convention).

17. [x] **T6: Credential isolation — codify the no-cookie rule.** Add a static-analysis check `internal/browser/lint_cookies.go` (a `go vet` analyzer is overkill; a unit test that greps for forbidden patterns is enough): scan all `internal/browser/**/*.go` for `Cookie`, `setCookie`, `document.cookie`, `localStorage`, `sessionStorage` references and fail the test if any survive outside `conformance.go` (which uses them in negative-test assertions). Document in `docs/integrations/remote-browser.md`: "R1's remote browser tool MUST NOT accept user-supplied cookies. If a task needs authenticated access to a customer service, the integration must be at R1's tool layer (`internal/tools/`) outside the browser session — the browser sees only the resulting public URLs."

18. [x] **T7: Latency targets — measurement harness.** Add `internal/browser/bench_test.go` (`-tags=bench`) that runs N=100 navigate-and-WaitFor cycles per provider against an httptest server. Assert p50/p99 hard limits:
    | Provider | p50 | p99 |
    | --- | --- | --- |
    | local | 500ms | 2s |
    | browserless (cloud) | 2s | 8s |
    | browserless (self-host, same region) | 1s | 4s |
    | inhouse (same region) | 1s | 4s |
    The numbers come from Browserless's documented 1-2s cold-start + 50-200ms network call latency per the May 2026 web search. The bench is opt-in (build-tag-gated); CI runs it nightly. Test failure = release blocker only when the p99 regresses by >25% vs the most recent green run (use `gs://resolute-parity-484218-g1-r1-bench-reports/` per README §"Nightly benchmark cron").

19. [x] **T8: Fallback wrapper.** Create `internal/browser/fallback.go` with a `FallbackProvider` that wraps a primary + optional secondary. Config: `browser.fallback: local | none` (default `none` for prod). On primary `Open` returning a transient error (WS dial timeout, 5xx, `Target.createBrowserContext` failure with retryable code), the wrapper emits `browser.fallback_used` to coderadar + bus and retries against the secondary. Permanent errors (ErrNoToken, network-policy-denied) DO NOT fall back. Test: mock primary that errors twice then succeeds; mock secondary always succeeds; assert fallback fires on first two calls then primary recovers.

20. [x] **T8a: Fallback off in prod — config gate.** When `R1_ENV=prod`, refuse to load any config that sets `browser.fallback: local` unless `browser.fallback_allow_in_prod: true` is also set. This is an explicit operator opt-in for break-glass scenarios. Hard-fail at boot with a clear message: "browser.fallback: local in prod requires browser.fallback_allow_in_prod: true — falling back to local Chromium leaks customer egress to the R1 host network."

21. [x] **T9: Session-bound browsers.** A new R1 session (per `internal/session/`) gets exactly one `Provider.Open` call at first browser tool invocation; the resulting `Session` is stored in the session's context and reused for every subsequent browser tool call in that R1 session. On R1 session end (`session.Close` hook), call `browserSession.Close(ctx)`. Add idle-timeout enforcement: if the browser tool has been silent for `SessionOpts.IdleTimeout` (default 5m), the wrapper closes the browser session and the next tool call lazily opens a fresh one. Hard-cap absolute lifetime at `SessionOpts.HardTimeout` (default 30m). Test: timer-driven test that asserts close fires at idle + hard cap.

22. [x] **T10: In-house service — Cloud Run deployment.** Wire `services/r1-browser/` into `services/cloudbuild-deploy.yaml`'s service list (same pattern as r1-admin / r1-docs). IAM: create service account `r1-browser-runtime@<project>.iam` with no permissions other than `roles/run.invoker` on itself (defense in depth — it should not even read its own logs). Create `r1-browser-invoker@<project>.iam` with `roles/run.invoker` granted to it; R1 backend services (r1-coord-api) authenticate as the invoker. Deploy steps in `docs/operations/r1-browser-service.md` with copy-pasteable `gcloud` commands. Smoke test: post-deploy curl-style probe that opens a CDP session against `wss://browser.r1.run` and navigates to `https://example.com`.

23. [x] **T11: Observability — events.** Add `internal/browser/events.go` with constants:
    ```go
    const (
        EvtSessionOpened   = "browser.session_opened"
        EvtSessionClosed   = "browser.session_closed"
        EvtNavigate        = "browser.navigate"
        EvtError           = "browser.error"
        EvtFallbackUsed    = "browser.fallback_used"
        EvtNetworkDenied   = "browser.network_denied"
        EvtTokenInvalid    = "browser.token_invalid"
    )
    ```
    Every Provider emits these via the existing event-log + CodeRadar (`internal/coderadar/`) pipeline. Each event carries `tenant_id`, `provider` (`local|browserless|inhouse`), `session_id`, `endpoint_host` (e.g., `chrome.browserless.io`), `duration_ms`. Test: a fake event sink captures all emissions during a navigate-and-close cycle and asserts the expected event sequence.

24. [x] **T12: Cost tracking.** Browserless bills by "Unit" = 30 seconds of browser-time per session per their pricing page. Track via `internal/browser/browserless/cost.go`: open-time stamp on Open; on Close compute `units = ceil(duration / 30s)`; emit to `internal/costtrack/` with `Category="browser-remote"`, `Provider="browserless"`, `Quantity=units`, `TenantID=opts.TenantID`. For inhouse, track Cloud Run vCPU-seconds via the existing Cloud Run cost ingester (already present per README §"PostHog + Customer.io + CodeRadar"); add a `Category="browser-inhouse"` tag at session close. Tenant attribution is non-negotiable — every cost record carries the tenant. Test: simulate three 45-second sessions across two tenants; assert costtrack receives 3×2 units for Browserless (45/30 ceil = 2) attributed to the right tenants.

25. [x] **T12a: Per-Mission cost budget.** A "Mission" (per `cmd/r1/mission_cmd.go`) is the unit operators care about. Add a `browser.budget_per_mission_units: 60` config (default: 60 units = 30 minutes of Browserless time = ~$0.50 at 2026 Scale-plan pricing). When a mission's cumulative browser cost crosses budget, `Provider.Open` returns `ErrBudgetExhausted` and the mission's plan is forced to skip browser-requiring steps. Document the rationale: at 60 units/mission and 1000 missions/month a tenant burns ~60k units, well inside the Scale-tier 500k cap. Test: stub costtrack returns OverBudget=true; assert Open errors with the budget error.

26. [x] **T13: Tests — provider conformance.** Wire `internal/browser/provider_local_test.go`, `internal/browser/browserless/client_test.go`, `internal/browser/inhouse/client_test.go` to each call `ProviderConformance(t, factory)` from T1b. The local factory uses the existing rod stub; Browserless and inhouse factories use mock CDP-over-WS servers (a single shared `testutil/cdpmock` package — extract if more than ~150 LOC of mock server appears in both files). Hard requirement: **all three providers pass the same byte-identical conformance suite**.

27. [x] **T13a: Tests — integration against real Browserless.** A separate file `internal/browser/browserless/integration_test.go` (`-tags=browserless_live`) runs against the real `wss://chrome.browserless.io` using a test-only token from `BROWSERLESS_TEST_TOKEN`. CI gates this behind the build tag; the nightly bench cron (T7) runs it. Skip with `t.Skip` if the env var is absent so local `go test ./...` stays green for contributors without a Browserless account.

28. [x] **T13b: Tests — integration against local in-house container.** `services/r1-browser/integration_test.go` (`-tags=inhouse_live`) starts the r1-browser Docker container locally via `testcontainers-go`, dials its CDP-over-WS endpoint, and runs the full conformance suite. Mirrors the Browserless live test but against the operator-controlled image. CI gates with the build tag; nightly bench cron runs it.

29. [x] **T13c: Tests — tenant isolation negative test.** A dedicated `internal/browser/isolation_test.go` (no build tag) that runs through every provider via `ProviderConformance`: opens tenant A's session, navigates to an httptest endpoint that sets `Set-Cookie: tracker=A`, captures the cookie via `Eval("document.cookie")`, closes; opens tenant B's session against the same endpoint, asserts `document.cookie` is empty. This is the load-bearing security test — it stays in the default `go test ./...` path, no build tag, no opt-in. If this is ever red, deployment is blocked.

30. [x] **T13d: Tests — network policy denial.** `internal/browser/policy_test.go` (no build tag, mock provider): policy = `mode: deny_by_default, allow: ["example.com"]`. Open session, navigate to `http://attacker.com` — assert the call returns `ErrNetworkDenied` with the destination host in the error. Navigate to `http://example.com` — assert success. Repeats against every provider via conformance.

31. [x] **T14: Docs — operator runbook.** Write `docs/integrations/remote-browser.md` covering, in order: (a) why remote-browser exists (the sec-arch summary from §1); (b) choosing a provider (decision table: cloud Browserless = lowest ops + Browserless's network = trust boundary; self-host Browserless = same code, customer-controlled = +$250/mo license; in-house = full control, +Cloud Run cost, +operator burden); (c) sizing in-house (concurrency=1, mem=2Gi, min-instances=1 means 1 idle instance per region, scale to N instances under load; expect ~$8/mo idle + $0.30/hr active); (d) network policy authoring (the YAML schema from T5 with three worked examples — public docs site, RelayOne MSP-internal, customer-VPC-only); (e) troubleshooting (WS disconnect → check token, check Cloud Run logs, check network policy denied counter); (f) the no-cookies rule from T6 with a worked example of how to integrate auth at the tool layer instead.

32. [x] **T14a: Docs — in-house service operations.** Write `docs/operations/r1-browser-service.md` covering: container build (the distroless+chromium tradeoff from T8), Cloud Run config tuning, IAM grants (the two-SA pattern from T10), CDP debugging (how to attach DevTools to a live session for incident response — answer: don't, the container is single-tenant and ephemeral; instead, replay the failing event sequence in a dev environment).

33. [x] **T14b: Add `configs/browser.example.yaml`.** The canonical operator-facing config schema with annotated examples for all three providers and all three environment templates from T5c. This file is loaded by `r1 doctor` (existing diagnostic command) for syntax validation.

## 5. Provider config (operator-facing)

```yaml
# configs/browser.yaml
browser:
  # one of: local | browserless | inhouse
  provider: browserless

  # Browserless-specific
  browserless:
    endpoint: wss://chrome.browserless.io
    # token resolution: this field > BROWSERLESS_TOKEN env > ~/.r1/browser.json
    token: ""
    max_concurrent: 5
    tenant_tokens:
      acme-corp: ${BROWSERLESS_TOKEN_ACME}
      globex:   ${BROWSERLESS_TOKEN_GLOBEX}

  # In-house Cloud Run
  inhouse:
    endpoint: wss://browser.r1.run
    audience: https://browser.r1.run    # for ID token minting

  # Network egress policy (applies to every provider)
  network_policy:
    mode: deny_by_default
    allow:
      - "*.r1.run"
      - "docs.anthropic.com"
    deny:
      - "169.254.169.254"
      - "metadata.google.internal"

  # Fallback — prod must explicitly opt in
  fallback: none        # local | none
  fallback_allow_in_prod: false

  # Session limits
  session:
    idle_timeout: 5m
    hard_timeout: 30m

  # Budget
  budget_per_mission_units: 60
```

## 6. CLI behavior

- `r1 browse <url>` — picks the configured provider; unchanged surface.
- `r1 browse <url> --provider local` — operator-only override; refuses in prod with a clear error referencing `browser.fallback_allow_in_prod`.
- `r1 --one-shot browse <url>` — hard-rejected with `oneshot+remote-browser is not supported (latency-sensitive); rebuild with browser.provider: local or use the long-running session entry point`.

## 7. Boundaries — what NOT to do

- **Do NOT pass user PII or credentials to the remote browser ever.** This is enforced by T6's lint test + the documented operator runbook. Violations are release blockers.
- **Do NOT enable the remote provider in `--one-shot` mode.** One-shot is for stateless, sub-100ms verbs (per `internal/oneshot/`). Remote browser adds 1-2s minimum (per the Browserless docs cited in §2). The CLI hard-rejects this combination.
- **Do NOT couple the executor to a specific provider.** Every executor call site goes through `Provider`. Adding a fourth provider (e.g., Browserbase, Steel) must require zero executor changes — only a new package under `internal/browser/<name>/`.
- **Do NOT roll a custom remote-browser service before validating Browserless cloud.** The in-house path (T3/T8/T10/T13b) is real, but Browserless ships first. The conformance suite makes the swap a config change, not a refactor; that ordering keeps T1-T7 (the abstraction + cloud path) on a critical path of ~2 weeks and leaves T8-T14 (in-house + polish) as the back half.
- **Do NOT enable `browser.fallback: local` by default in prod.** T8a refuses the config; the operator must explicitly opt in with a separate flag whose name (`fallback_allow_in_prod`) is intentionally awkward to type.
- **Do NOT cache CDP-over-WS connections across tenants.** Each tenant gets a dedicated WebSocket; closing the last session for that tenant closes the underlying WS. This is what makes incognito-context isolation actually load-bearing — the WS itself never sees two tenants' traffic interleaved.
- **Do NOT depend on Browserless-specific CDP extensions.** Browserless adds `/function`, `/screenshot`, `/scrape` REST shortcuts — ignore them. Everything we use is vanilla CDP that an in-house Chromium speaks identically. This is what lets the conformance suite be byte-identical across providers.

## 8. Data models

### `Session` (internal state)
| Field | Type | Notes |
| --- | --- | --- |
| ID | string | CDP `targetId`; opaque to caller |
| TenantID | string | from `SessionOpts.TenantID` |
| OpenedAt | time.Time | for idle/hard timeout enforcement |
| LastActiveAt | time.Time | updated on every method call |
| Provider | string | `local`/`browserless`/`inhouse` |
| EndpointHost | string | logged with every event |
| BrowserContextID | string | Browserless/inhouse only; empty for local |

### `NavigateResult`
| Field | Type | Notes |
| --- | --- | --- |
| FinalURL | string | after redirects |
| Status | int | HTTP status of the top-level navigation |
| Title | string | from `Page.getNavigationHistory` or empty |

## 9. Error handling

| Failure | Strategy | Resulting state |
| --- | --- | --- |
| WS dial timeout to Browserless | T8 fallback (if configured) → secondary; else `ErrProviderUnreachable` | Session not opened; mission step fails AC |
| 401/403 from Browserless | `ErrAuthFailed` + emit `browser.token_invalid`; tear down tenant tokens | All in-flight sessions for that tenant closed; mission halts |
| `Target.createBrowserContext` 5xx | retry once with backoff; on second failure → `ErrProviderUnreachable` | Session not opened |
| Network policy denial during nav | `ErrNetworkDenied{Host: "...", Rule: "deny"}` + emit event | Session stays open; next navigate may succeed if policy is per-request |
| Session idle timeout | close session silently; lazy re-open on next call | Transparent to caller; cost record finalized |
| Session hard timeout | close session + emit `browser.session_closed{reason:"hard_timeout"}` | Caller must Open a fresh one |
| Cost budget exhausted | `ErrBudgetExhausted` at Open; existing sessions continue | Mission planner skips browser steps |
| Cloud Run cold start (inhouse) | first Open is allowed up to 5s; subsequent Open in same R1 session is sub-1s | Logged but not failed; counts toward p99 budget |

## 10. Acceptance criteria — bash

```
# Build
go build ./...
go vet ./...

# Conformance — all three providers
go test ./internal/browser/...                    # provider_local + isolation + policy
go test -tags browserless_live ./internal/browser/browserless/...    # requires BROWSERLESS_TEST_TOKEN
go test -tags inhouse_live ./internal/browser/inhouse/...            # requires docker

# Tenant isolation — non-negotiable
go test -run TestTenantIsolation ./internal/browser/...

# Network policy — denial path
go test -run TestNetworkPolicy ./internal/browser/...

# Cost tracking
go test -run TestCostTracking ./internal/browser/browserless/...
go test -run TestBudgetExhausted ./internal/browser/...

# Fallback
go test -run TestFallback ./internal/browser/...
go test -run TestFallbackRefusedInProd ./internal/browser/...

# CLI
./r1 browse https://example.com    # picks configured provider
./r1 --one-shot browse https://example.com 2>&1 | grep -q 'oneshot+remote-browser is not supported'

# Latency bench (nightly cron)
go test -tags bench ./internal/browser/...

# Service deploy (operator gate)
gcloud run services describe r1-browser --region us-central1 --format='value(status.url)'
```

**Measurable acceptance summary:**

- Both Browserless and in-house providers pass `ProviderConformance` byte-identically — same test file, two factories.
- `TestTenantIsolation` passes against every provider: cookies/localStorage set in tenant A's session are invisible to tenant B's session against the same target server.
- `TestNetworkPolicy` passes: denied destinations return `ErrNetworkDenied` with the host name; allowed destinations succeed.
- `TestFallbackRefusedInProd` passes: `R1_ENV=prod` + `browser.fallback: local` (without the explicit opt-in flag) → hard error at boot.
- Browserless cost per Mission stays under `browser.budget_per_mission_units` (default 60 units ≈ 30 min ≈ ~$0.50 at 2026 Scale-tier pricing). Documented target: <$1.00/mission at 95th percentile.
- p99 navigate-and-WaitFor latency stays under the table in T7; 25% regression vs last green nightly = release blocker.
- The lint test in T6 finds zero `Cookie`/`document.cookie`/`localStorage` references outside `conformance.go` and `isolation_test.go`.

## 11. Rollout

1. T1 lands first as a pure refactor — no remote provider, no behavior change. Merge under a feature flag `R1_BROWSER_PROVIDER_API=v2` so CI gets a chance to flake before the second wave.
2. T2/T2a/T2b/T2c/T2d ship Browserless against the cloud free tier — green conformance + green isolation + green policy.
3. T3-T3b add the in-house Cloud Run service and deploy it to `staging.browser.r1.run` first; soak for a week; promote to prod.
4. T4-T9 (isolation, policy, fallback, session lifecycle) land alongside T2 since they live in shared code.
5. T10 (Cloud Run prod deploy), T11-T12 (observability + cost), T13-T13d (test surface), T14-T14b (docs) follow.

Estimated calendar: 2 weeks for T1-T7 (cloud Browserless live), 2 weeks for T8-T14 (in-house + polish + docs). Matches the SOW's 4-week estimate.
