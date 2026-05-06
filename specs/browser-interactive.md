<!-- STATUS: done -->
<!-- BUILD_COMPLETED: 2026-04-22 -->
<!-- CREATED: 2026-04-21 -->
<!-- DEPENDS_ON: executor-foundation (Task 19), browser-research-executors (existing spec Part 1), research-orchestrator (optional consumer) -->
<!-- BUILD_ORDER: 17 -->

# Browser Interactive (go-rod backend) — Implementation Spec

## 1. Overview

Task 21 part 1 shipped the stdlib `net/http` MVP of `internal/browser/`: fetch a URL, strip HTML, run `VerifyContains` / `VerifyRegex`. That covers "does this URL return 2xx with expected text" but cannot click, type, wait for a selector, wait for network-idle, or take a screenshot. The executor currently short-circuits with `ErrExecutorNotWired{FollowUp: "Task 21 part 2 (go-rod backend)"}` whenever `plan.Extra["interactive"] = true`. Part 2 — this spec — adds a `browser.RodClient` that wraps `github.com/go-rod/rod`, a pool that reuses one headless Chromium across many tasks, and the small set of interactive action types (navigate / click / type / wait-for-selector / wait-for-network-idle / screenshot / extract-text / extract-attribute) needed for real end-to-end verification.

Both backends satisfy a new `browser.Backend` interface; selection happens at construction time. `browser.NewClient()` returns the stdlib fetcher (today's default, no Chromium required). `browser.NewRodClient(cfg)` returns the go-rod-backed client and only compiles under the `stoke_rod` build tag, so single-binary users who never want a Chromium dependency keep a clean `go build ./cmd/r1`. Vision-model screenshot diffing (when an AC says "screenshot matches baseline") is delegated to the existing reasoning provider — this spec wires in the path that feeds PNG bytes to the diff AC but does NOT design the vision prompt itself (that's operator-ux or a later spec).

## 2. Dependency choice — go-rod

RT-01 (`specs/research/raw/RT-01-playwright-go.md`) evaluated chromedp, go-rod, Playwright/Node, and Puppeteer/Node against Stoke's constraints (single-binary distribution, parallel sessions, MIT/Apache-2.0 license, pure-Go). go-rod wins because it:

- Ships `rod.NewBrowserPool(limit)` and `rod.NewPagePool(limit)` first-class — matches Stoke's N-parallel-worker model; chromedp has known single-event-loop and fixed-buffer pitfalls under parallel load.
- Auto-downloads a pinned Chromium to `$HOME/.cache/rod/browser` on first run via `lib/launcher` (zero-config local dev; CI can pre-cache).
- Uses remote-object IDs rather than DOM node IDs → fewer SPA/iframe brittleness bugs than chromedp.
- MIT license; compatible with Stoke's Apache-2.0. `go get` adds only two modules: `github.com/go-rod/rod` and its gson dep `github.com/ysmood/gson`. Both bring in a small transitive set under `github.com/ysmood/*` (~3 MB of Go deps). Chromium itself is an out-of-process download, not linked.

**Version pin.** Last tagged release is v0.116.2 (Jul 2024) but the `main` branch has commits through Feb 2026; pin to a specific 2026 SHA in `go.mod` with a comment: `// pinned: upstream v0.116.2 predates 2026 main; revisit when a new tag is cut`.

**Chromium binary.** go-rod's default path is automatic download via `launcher.NewBrowser().MustGet()`. Operators who want to override (airgapped CI, custom Chrome path, pre-baked Docker image) set `CHROME_PATH=/absolute/path/to/chromium`. When `CHROME_PATH` is set, `RodClient` bypasses the launcher's download path entirely and uses `launcher.New().Bin(os.Getenv("CHROME_PATH"))`.

**Rejected alternatives.** chromedp (concurrency pitfalls, no built-in pool), Playwright-via-Node (detonates single-binary story, adds Node runtime to every CI job), Puppeteer-via-Node (same as Playwright, Chrome-only, no wins). RT-01 is the source of record — do NOT re-litigate in this spec.

## 3. Architecture

### 3.1 New files

```
internal/browser/
  backend.go            # new: Backend interface (both http + rod satisfy)
  action.go             # new: Action + ActionResult types
  rod.go                # new: RodClient — go-rod-backed Backend impl; build tag stoke_rod
  pool.go               # new: browser-instance pool (acquire/release/cleanup)
  errors.go             # new: taxonomy (ElementNotFound / Timeout / NavigationFailed / ChromeLaunchFailed)
  rod_test.go           # new: unit tests against mock + rod.Browser.Test helpers; build tag stoke_rod
  pool_test.go          # new: pool lifecycle + SIGINT cleanup; build tag stoke_rod
  action_test.go        # new: action marshaling, stdlib — no build tag
  rod_integration_test.go  # new: real-Chrome against httptest; build tag stoke_rod_integration
```

### 3.2 Modified files

- `internal/browser/browser.go` — add `Backend` interface; keep existing `Client` struct, add a method set so it satisfies `Backend` for the non-interactive subset. `NewClient()` unchanged (returns stdlib). Add `NewRodClient(cfg)` constructor (build-tag-gated stub + real impl).
- `internal/executor/browser.go` — extend `Execute`: when `plan.Extra["interactive"]==true`, unwrap `plan.Extra["actions"]` as `[]browser.Action`, require a RodClient, run each action via the backend, accumulate per-action `ActionResult` onto the deliverable. When `interactive==false`, keep the existing stdlib fetch path unchanged.
- `cmd/r1/main.go` — `stoke browse` subcommand gains `--action` repeatable flag (see §5).
- `go.mod` / `go.sum` — add `github.com/go-rod/rod` (pinned SHA) + `github.com/ysmood/gson`.

### 3.3 `Backend` interface (new, in `browser.go`)

```go
// Backend is the executor-facing contract. Both the stdlib http
// Client and the go-rod RodClient satisfy it. Selection is made at
// construction time in NewClient / NewRodClient.
type Backend interface {
    // Fetch is the stdlib-compatible path: GET url + extract text.
    // The rod backend implements Fetch by doing navigate + extract.
    Fetch(ctx context.Context, url string) (FetchResult, error)

    // RunActions executes the interactive action list in order.
    // Stdlib backend returns ErrInteractiveUnsupported for any
    // action that requires a real browser (click / type / wait /
    // screenshot). Rod backend implements all of them.
    RunActions(ctx context.Context, actions []Action) ([]ActionResult, error)

    // Close releases resources (pool shutdown for rod; no-op for http).
    Close() error
}
```

The existing `Client` struct keeps its method set and gains `RunActions` (returns `ErrInteractiveUnsupported` for any non-navigate action) and `Close` (no-op). The new `RodClient` lives in `rod.go`.

### 3.4 `Action` + `ActionResult` (new, in `action.go`)

```go
// ActionKind enumerates the eight interactive primitives.
type ActionKind string

const (
    ActionNavigate          ActionKind = "navigate"
    ActionClick             ActionKind = "click"
    ActionType              ActionKind = "type"
    ActionWaitForSelector   ActionKind = "wait_for_selector"
    ActionWaitForNetworkIdle ActionKind = "wait_for_network_idle"
    ActionScreenshot        ActionKind = "screenshot"
    ActionExtractText       ActionKind = "extract_text"
    ActionExtractAttribute  ActionKind = "extract_attribute"
)

type Action struct {
    Kind      ActionKind
    URL       string          // navigate
    Selector  string          // click / type / wait / extract_*
    Text      string          // type
    Attribute string          // extract_attribute
    OutputPath string         // screenshot (optional; bytes always in result)
    Timeout   time.Duration   // per-action; 0 → default (10s)
}

type ActionResult struct {
    Kind          ActionKind
    OK            bool
    Err           error
    Text          string       // extract_text / extract_attribute
    Attribute     string       // extract_attribute
    ScreenshotPNG []byte       // screenshot — raw bytes, caller writes if it wants
    URL           string       // navigate (final URL after redirects)
    DurationMs    int64
}
```

### 3.5 Pool (new, in `pool.go`)

```go
type Pool struct {
    Max int          // default 3, configurable via RodConfig.PoolSize
    // internal: channel of *rod.Browser, mu, closed bool, launcher config
}

func NewPool(cfg RodConfig) (*Pool, error)
func (p *Pool) Acquire(ctx context.Context) (*rod.Browser, error)
func (p *Pool) Release(b *rod.Browser)
func (p *Pool) Close() error  // shuts down all browsers, kills launcher subprocess
```

Constructed once per `RodClient`. SIGINT/SIGTERM handler installed at pool construction to call `Close()` — prevents zombie Chromium processes if the CLI is killed mid-run.

### 3.6 Executor wiring (`internal/executor/browser.go`)

```go
type BrowserExecutor struct {
    Client    *browser.Client      // stdlib (always present)
    RodClient *browser.RodClient   // may be nil; only constructed when interactive plans are routed here
}

func (e *BrowserExecutor) Execute(ctx, p Plan, _ EffortLevel) (Deliverable, error) {
    url := strings.TrimSpace(p.Query)
    if url == "" { return nil, errors.New("browser: empty URL") }

    interactive, _ := p.Extra["interactive"].(bool)
    if !interactive {
        // unchanged: stdlib fetch + BrowserDeliverable
    }

    // interactive path
    if e.RodClient == nil {
        return nil, &ErrExecutorNotWired{
            Type:     TaskBrowser,
            FollowUp: "construct executor with browser.NewRodClient(cfg) (stoke_rod build tag required)",
        }
    }
    actions, ok := p.Extra["actions"].([]browser.Action)
    if !ok { return nil, errors.New("browser: plan.Extra[\"actions\"] missing or wrong type") }

    // If no explicit navigate at head, synthesize one from plan.Query.
    if len(actions) == 0 || actions[0].Kind != browser.ActionNavigate {
        actions = append([]browser.Action{{Kind: browser.ActionNavigate, URL: url}}, actions...)
    }

    results, err := e.RodClient.RunActions(ctx, actions)
    if err != nil { return nil, fmt.Errorf("browser run: %w", err) }

    return BrowserInteractiveDeliverable{
        URL:            url,
        Actions:        actions,
        Results:        results,
        ExpectedText:   str(p.Extra["expected_text"]),
        ExpectedRegex:  str(p.Extra["expected_regex"]),
        ScreenshotAC:   str(p.Extra["screenshot_baseline"]),
    }, nil
}
```

`BuildCriteria` extends to emit `BROWSER-ACTION-SUCCESS-<i>-<KIND>` per action + (optional) `BROWSER-SCREENSHOT-MATCH` when a baseline is configured.

## 4. Implementation checklist

Each item is self-contained. File paths are absolute from repo root. Tests land in a sibling `_test.go` unless otherwise noted.

1. [ ] **Add go-rod to `go.mod`.** Run `go get github.com/go-rod/rod@<2026-SHA>` and `go get github.com/ysmood/gson`. Append `// pinned: upstream v0.116.2 predates 2026 main; revisit when a new tag is cut` next to the rod require. Commit both `go.mod` and `go.sum`. Verify: `go build ./...` (untagged) still passes — rod imports must be guarded by build tag yet.

2. [ ] **Create `internal/browser/errors.go`.** Define `ErrElementNotFound`, `ErrNavigationFailed`, `ErrActionTimeout`, `ErrChromeLaunchFailed`, `ErrInteractiveUnsupported` as typed structs with `Error()` + `Unwrap()` methods. Each carries `Selector`, `URL`, `Cause` fields as appropriate. Test: errors.As roundtrip for each type.

3. [ ] **Create `internal/browser/action.go`.** Define `ActionKind` consts + `Action` + `ActionResult` structs exactly per §3.4. No build tag. Include a `(Action).Validate() error` method that rejects empty `URL` for navigate, empty `Selector` for click/type/wait/extract_*, and empty `Text` for type. Test: `TestActionValidate` covers every kind's happy + required-field-missing case.

4. [ ] **Add `Backend` interface to `internal/browser/browser.go`.** Insert exactly the interface from §3.3 above the existing `Client` struct. Add `RunActions` method to existing `*Client` that returns `(nil, ErrInteractiveUnsupported{Kind: ...})` for any non-navigate action, or performs `Fetch` + returns a single `ActionResult{Kind: ActionNavigate}` for a lone navigate. Add a no-op `Close() error` to `*Client`. Add `var _ Backend = (*Client)(nil)` compile-time assertion. Test: `TestClientRunActionsNavigateOnly` + `TestClientRunActionsRejectsInteractive`.

5. [ ] **Create `internal/browser/rod.go` with `//go:build stoke_rod` tag.** Declare `RodConfig` struct: `PoolSize int` (default 3), `HeadlessMode bool` (default true), `Timeout time.Duration` (default 30s), `ChromePath string` (default `os.Getenv("CHROME_PATH")`), `UserAgent string`, `Logger func(string, ...any)`. Declare `RodClient` struct holding a `*Pool` + `RodConfig`. Declare constructor `NewRodClient(cfg RodConfig) (*RodClient, error)`. Stub `Fetch`, `RunActions`, `Close` (bodies land in subsequent items).

6. [ ] **Add build-tag stub `internal/browser/rod_stub.go` with `//go:build !stoke_rod`.** Declares the same `RodClient` type as an opaque struct and `NewRodClient(cfg RodConfig) (*RodClient, error)` that returns `nil, ErrChromeLaunchFailed{Cause: errors.New("stoke built without stoke_rod tag")}`. Ensures callers compile unchanged whether or not the tag is set. Test (no tag): `TestNewRodClientWithoutTagReturnsError`.

7. [ ] **Create `internal/browser/pool.go` with `//go:build stoke_rod`.** Implement `Pool` struct + `NewPool(cfg) (*Pool, error)`. Construct the launcher: when `cfg.ChromePath != ""` use `launcher.New().Bin(cfg.ChromePath).Headless(cfg.HeadlessMode)`; else `launcher.New().Headless(cfg.HeadlessMode)` (auto-download). Save the launch URL; spawn `cfg.PoolSize` browsers eagerly. Store them in a buffered channel. Test: `TestPoolSizeHonored` (stoke_rod tag, uses `rod.New().Client(&cdp.Client{...}).MustConnect()` OR a mock transport).

8. [ ] **Implement `Pool.Acquire(ctx)`.** Non-blocking read from the buffered channel; if empty, block on ctx. Returns `*rod.Browser`. Test: `TestPoolAcquireBlocksWhenExhausted` (acquire PoolSize + 1 with short ctx → ctx.DeadlineExceeded).

9. [ ] **Implement `Pool.Release(b)`.** Push back onto channel; if channel full (should not happen), close the browser and log. Test: `TestPoolReleaseRestoresCapacity`.

10. [ ] **Implement `Pool.Close()`.** Close each browser in the channel, call `launcher.Cleanup()` to kill the subprocess group, mark pool closed. Idempotent. Test: `TestPoolCloseIdempotent`.

11. [ ] **Install SIGINT/SIGTERM handler at `NewPool`.** Use `signal.Notify` in a goroutine that calls `p.Close()` on first signal. Test: send `syscall.SIGTERM` via `syscall.Kill(os.Getpid(), ...)` inside a subtest with a fake pool; assert Close was invoked.

12. [ ] **Implement `RodClient.Fetch(ctx, url)`.** Acquire a browser, open a page, `Navigate(url)`, `WaitLoad()`, grab `page.HTML()` + `page.MustInfo().Title`, release. Translate to a `FetchResult` compatible with the stdlib `Client.Fetch` shape (same field set). Cap body at 1MB like the stdlib path. Test: `TestRodFetch` against httptest (integration tag).

13. [ ] **Implement `RodClient.RunActions(ctx, actions)`.** Acquire one browser + one page for the whole list. Iterate actions; dispatch to per-kind helper; accumulate `ActionResult`; on first hard error, record the error in that result but CONTINUE to the next action ONLY if `action.Kind` is `ActionScreenshot` or `ActionExtract*` (best-effort evidence collection). For navigate/click/type/wait, abort the loop on error. Release page+browser in defer. Test: `TestRunActionsOrdering` with 5-step plan.

14. [ ] **Implement `actionNavigate(page, a)`.** `page.Context(ctx).Navigate(a.URL)` then `page.WaitLoad()`. Honor `a.Timeout` via `page.Timeout(a.Timeout)`; default 30s. Populate `ActionResult.URL` from `page.MustInfo().URL`. On rod.ErrNavigation-class errors → return `ErrNavigationFailed{URL: a.URL, Cause: err}`. Test: happy + 404 + DNS-fail.

15. [ ] **Implement `actionClick(page, a)`.** `page.Timeout(a.Timeout).Element(a.Selector).Click(proto.InputMouseButtonLeft, 1)`. On selector miss → `ErrElementNotFound{Selector: a.Selector}`. Test: click a `<button id="go">` wired to set `document.body.innerText = "clicked"` and assert via extract.

16. [ ] **Implement `actionType(page, a)`.** `page.Timeout(a.Timeout).Element(a.Selector).Input(a.Text)`. Test: type into `<input id="q">` and assert value via `extract_attribute` with attribute=`value`.

17. [ ] **Implement `actionWaitForSelector(page, a)`.** `page.Timeout(a.Timeout).Element(a.Selector)` — rod auto-waits for presence. Timeout → `ErrActionTimeout{Kind: "wait_for_selector", Selector: a.Selector}`. Test: selector appears after 100ms via setTimeout in the test page.

18. [ ] **Implement `actionWaitForNetworkIdle(page, a)`.** `page.Timeout(a.Timeout).WaitRequestIdle(500*time.Millisecond, nil, nil)`. Default timeout 10s. Test: page with an XHR that completes at 200ms; wait returns cleanly after.

19. [ ] **Implement `actionScreenshot(page, a)`.** `page.Screenshot(true, &proto.PageCaptureScreenshot{Format: proto.PageCaptureScreenshotFormatPng})`; full-page if `a.Selector == ""`, else `element.Screenshot(...)`. Populate `ActionResult.ScreenshotPNG` with raw bytes. If `a.OutputPath != ""`, also write bytes to that path (0644). Test: assert first 8 bytes == PNG magic (`\x89PNG\r\n\x1a\n`) + len > 1KB.

20. [ ] **Implement `actionExtractText(page, a)`.** If `a.Selector == ""` → `page.MustElement("body").MustText()`; else `page.Timeout(a.Timeout).Element(a.Selector).Text()`. Populate `ActionResult.Text`. Selector miss → `ErrElementNotFound`. Test: extract `<h1>Hello</h1>` via `h1` selector returns "Hello".

21. [ ] **Implement `actionExtractAttribute(page, a)`.** `el := page.Timeout(a.Timeout).Element(a.Selector)`; `val, err := el.Attribute(a.Attribute)`. Populate `ActionResult.Text` AND `ActionResult.Attribute` with the attribute value (nil → empty string, not an error). Test: extract `href` from `<a href="/next">` returns `/next`.

22. [ ] **Implement `RodClient.Close()`.** Delegate to `pool.Close()`. Idempotent. Test: `TestRodClientClose`.

23. [ ] **Update `internal/browser/browser.go`: add `NewRodClient` exported alias.** Thin wrapper that calls into the rod.go constructor. When build tag absent, uses the stub from item 6. No import of rod from browser.go — keep the tag boundary clean. Callers do `browser.NewRodClient(cfg)`.

24. [ ] **Extend `internal/executor/browser.go` struct to hold `RodClient *browser.RodClient`.** Add `NewInteractiveBrowserExecutor(rc *browser.RodClient) *BrowserExecutor` constructor. The existing `NewBrowserExecutor()` stays as-is (stdlib only). Compile-time assert both satisfy `Executor`.

25. [ ] **Extend `BrowserExecutor.Execute` per §3.6.** Keep the non-interactive path byte-identical to today's MVP. Add the interactive branch that runs actions and builds `BrowserInteractiveDeliverable`. Test: `TestExecuteInteractiveRequiresRodClient` (no rod client → ErrExecutorNotWired); `TestExecuteInteractiveSyntheticNavigate` (actions list missing leading navigate → synthesizes from plan.Query).

26. [ ] **Add `BrowserInteractiveDeliverable` type in `internal/executor/browser.go`.** Fields: `URL`, `Actions []browser.Action`, `Results []browser.ActionResult`, `ExpectedText`, `ExpectedRegex`, `ScreenshotAC` (baseline path for vision diff). Implement `Summary()` and `Size()` (sum of result text + screenshot bytes).

27. [ ] **Extend `BuildCriteria` for interactive deliverables.** For each action result, emit an AC with ID `BROWSER-ACTION-SUCCESS-<i>-<KIND>` (e.g. `BROWSER-ACTION-SUCCESS-2-CLICK`), description = "action i (<kind>) succeeded", `VerifyFunc` returns `(r.OK, r.Err.Error() or "ok")`. Test: 5-action plan with one failure produces 5 ACs, one failed.

28. [ ] **Add `BROWSER-SCREENSHOT-MATCH` AC when `ScreenshotAC != ""`.** `VerifyFunc` loads the baseline PNG + latest screenshot bytes, calls out to the reasoning provider via an injected `VisionDiffFunc func(ctx, baseline, candidate []byte) (bool, string, error)`. When no provider is wired (field nil), AC returns `(false, "vision diff not wired")`. **Do NOT design the prompt here** — that's deferred. Test: wired provider stub returns true; nil provider returns the expected "not wired" reason.

29. [ ] **Extend `BuildEnvFixFunc` to classify rod errors.** On `ErrChromeLaunchFailed` → return true (transient; next attempt re-launches). On `ErrActionTimeout` with `Kind==wait_for_network_idle` or `navigate` → true. On `ErrElementNotFound` → false (permanent; selector is stale). On `ErrNavigationFailed` with network patterns (existing keyword list) → true. Test: table-driven `TestEnvFixClassifies`.

30. [ ] **`BuildRepairFunc` remains nil for the interactive path in this spec.** Repair (re-click after wait, retry with different selector) is out of scope for part 2. Comment in code: `// TODO: selector-repair strategy lands with screenshot-diff spec`. No test change.

31. [ ] **Add `stoke browse` `--action` flag parsing in `cmd/r1/main.go`.** `--action` is `[]string` (repeatable). Parse each entry via `parseActionFlag(s string) (browser.Action, error)` with format rules in §5. Accumulate into `[]browser.Action`. Pass via `plan.Extra["actions"]` + `plan.Extra["interactive"]=true`. Test: `TestParseActionFlag` table-driven over 8 example inputs (one per Kind).

32. [ ] **`parseActionFlag` format.** `navigate:URL`, `click:SEL`, `type:SEL:TEXT` (TEXT may contain `:`; split on first two only), `wait:SEL[:TIMEOUT]` (TIMEOUT as Go duration, default 10s), `wait_idle[:TIMEOUT]`, `screenshot[:OUT.png]` (OUT optional), `extract:SEL`, `extract_attr:SEL:ATTR`. Reject unknown prefixes with a clear error. Test: TestParseActionFlag covers every prefix + malformed input.

33. [ ] **Add `--screenshot-baseline` + `--rod-pool-size` + `--chrome-path` flags to `stoke browse`.** Wire into `RodConfig`. Only consumed when `--action` is present OR `--interactive` is explicitly set. Test: integration-style CLI test via `exec.Command` in a subtest.

34. [ ] **Update `cmd/r1/main.go` help text** to list the new action syntax. One paragraph; examples: `stoke browse https://example.com --action click:#submit --action wait:.result:5s --action screenshot:out.png`. No test, manual verify.

35. [ ] **Ensure `go build ./cmd/r1` (no tag) passes without rod in the binary.** Achieved by items 5/6: the rod-using files all carry `//go:build stoke_rod`; without the tag, `NewRodClient` hits the stub from item 6 and returns `ErrChromeLaunchFailed`. Test: `TestBuildWithoutRodTag` via `go build -tags=` invocation (CI gate).

36. [ ] **Add `//go:build stoke_rod` guard at top of every rod-importing file.** Files touched: `rod.go`, `pool.go`, `rod_test.go`, `pool_test.go`, `rod_integration_test.go`. No rod import outside these files. Manual grep gate.

37. [ ] **Wire `internal/browser/rod_test.go` (tag `stoke_rod`).** Unit tests that use rod's in-process mock helpers where possible (`rod.New().NoDefaultDevice()` against a `cdp.Client` stub). Tests: constructor, pool size, action-kind dispatch, result marshaling, error taxonomy. Do NOT require a real Chrome — use rod's test browser helpers OR mock the page-level interface behind a thin wrapper in `rod.go`.

38. [ ] **Wire `internal/browser/rod_integration_test.go` (tag `stoke_rod_integration`).** Launches real headless Chrome, starts `httptest.NewServer` with a 3-form test page (button + input + wait-triggered div + `<a href>`), runs the full action set, asserts: PNG magic on screenshot, extracted text "Hello", extracted `href` "/next", click-triggered body text change. Excluded from default `go test ./...` by the build tag. Gate the entire file on `// +build stoke_rod_integration` (old syntax ALSO for compatibility with Go tooling that still reads the first line).

39. [ ] **Wire `internal/browser/pool_test.go` (tag `stoke_rod`).** Tests per items 7/8/9/10/11. Uses a fake browser factory injected into `NewPool` for unit tests (add a hidden `newBrowserFn func() (*rod.Browser, error)` field in `Pool` that defaults to the real launcher; tests override).

40. [ ] **Wire `internal/browser/action_test.go` (no build tag).** `TestActionValidate` + `TestActionKindConstants` (stringer-style). Runs on every `go test ./...`.

41. [ ] **Verify existing `internal/browser/browser_test.go` still passes unchanged.** The MVP tests (VerifyContains, VerifyRegex, ExtractText, ExtractTitle, Fetch against httptest) must not change. CI gate.

42. [ ] **Update `internal/executor/browser_test.go`** to cover the two new execute branches (no rod wired → ErrExecutorNotWired with correct FollowUp; rod wired with mock Backend → results flow through). Use an in-package `fakeBackend` that implements `browser.Backend`.

43. [ ] **Add integration test `internal/executor/browser_integration_test.go` (tag `stoke_rod_integration`).** Build a real `BrowserExecutor` with a real `RodClient`, run against httptest, assert `BuildCriteria` produces the expected ACs, each VerifyFunc returns true.

44. [ ] **Document the `stoke_rod` tag in `STATUS.md`** under a new "Build tags" section (one line). No separate docs file.

45. [ ] **Run the full AC block (§6) locally.** `go build ./cmd/r1` (untagged) + `go build -tags stoke_rod ./cmd/r1` + `go vet ./...` + `go test ./...` + `go test -tags stoke_rod ./internal/browser/...` + `go test -tags stoke_rod_integration ./internal/browser/... ./internal/executor/...`. Fix any red; do not suppress.

46. [ ] **Kill-switch sanity check.** Build untagged binary, invoke `stoke browse https://example.com --action click:#foo` — must fail fast with a clear message: "interactive actions require the stoke_rod build tag; rebuild with `go build -tags stoke_rod ./cmd/r1`". Wire the message at the executor layer where the stub returns `ErrChromeLaunchFailed`. No test (manual verify).

47. [ ] **Ensure `go.sum` is committed.** `go mod tidy` with `-tags stoke_rod` to ensure all rod transitive deps are captured. Without the tag, the sum file still lists the pinned modules (required, not optional) so CI can verify.

## 5. CLI extensions

```
stoke browse URL [flags]

  --action STRING        Interactive action (repeatable; executes in order).
                         Formats:
                           navigate:URL
                           click:SELECTOR
                           type:SELECTOR:TEXT
                           wait:SELECTOR[:TIMEOUT]
                           wait_idle[:TIMEOUT]
                           screenshot[:OUT.png]
                           extract:SELECTOR
                           extract_attr:SELECTOR:ATTR
  --screenshot-baseline PATH     PNG baseline for vision-diff AC (optional)
  --rod-pool-size N              default 3
  --chrome-path PATH             override rod's auto-downloaded Chromium
  --interactive                  force the rod backend even with zero --action flags
  --expected-text STRING         existing MVP flag; still honored
  --expected-regex STRING        existing MVP flag; still honored
```

Multiple `--action` flags execute in strict argv order. If the first action is not `navigate`, one is synthesized from the positional URL (or the command errors if URL is empty). Example:

```
stoke browse https://example.com \
  --action click:#login \
  --action type:#username:alice \
  --action type:#password:hunter2 \
  --action click:button[type=submit] \
  --action wait:.dashboard:10s \
  --action screenshot:after-login.png \
  --action extract:.welcome-banner
```

Without the `stoke_rod` build tag, any `--action` other than `navigate` fails with: `interactive actions require the stoke_rod build tag; rebuild with 'go build -tags stoke_rod ./cmd/r1'`.

## 6. Acceptance criteria

```
# Untagged build — stdlib-only, no Chromium, no rod linked.
go build ./cmd/r1
go vet ./...
go test ./...

# Tagged build — rod linked, Chromium required for integration tests.
go build -tags stoke_rod ./cmd/r1
go vet -tags stoke_rod ./...
go test -tags stoke_rod ./internal/browser/...
go test -tags stoke_rod ./internal/executor/...

# Real-Chrome integration (opt-in; build tag gates CI without Chrome).
go test -tags stoke_rod_integration ./internal/browser/... ./internal/executor/...

# CLI smoke (untagged binary, non-interactive path unchanged).
./stoke browse https://example.com --expected-text 'Example Domain'

# CLI smoke (tagged binary, interactive path).
./stoke browse https://example.com \
    --action click:h1 \
    --action screenshot:/tmp/stoke-browse.png \
    --action extract:h1
test -s /tmp/stoke-browse.png
file /tmp/stoke-browse.png | grep -q 'PNG image data'

# Kill-switch: interactive action without rod tag → clear error, non-zero exit.
./stoke browse https://example.com --action click:h1 2>&1 | grep -q 'stoke_rod build tag'
```

**Non-negotiables.**
- `go build ./cmd/r1` (no tag) MUST continue to work with zero network access and zero Chromium binary — the single-binary distribution story is the whole point of the build-tag split.
- `go test ./internal/browser/...` (no tag) MUST still pass the existing MVP tests unchanged. Any regression there is a bug in part 2.
- The tagged-build integration tests MUST exit gracefully (not fail) when no Chrome is available — detect missing binary and return early with a clear diagnostic; do not fail the test run.

## 7. Testing

One test file per new go file (items 37–40, 42–43). Strategy:

- **`action_test.go` (no tag)** — pure struct tests; always in CI.
- **`rod_test.go` (`stoke_rod`)** — uses rod's `rod.New().MustConnect()` against a mocked CDP transport where feasible; for rod internals that refuse a fake transport, factor a `browserFactory` interface in `pool.go` and inject a test double. Always runs when the tag is set; no Chrome required.
- **`pool_test.go` (`stoke_rod`)** — lifecycle + SIGINT; uses the same test double.
- **`rod_integration_test.go` (`stoke_rod_integration`)** — real Chrome via `rod.New().MustConnect()`; starts `httptest.NewServer` with canned HTML; runs the full action matrix; asserts real PNG bytes + real DOM text. Build-tag-gated so CI without Chrome does not run the file.
- **`browser_integration_test.go` in `internal/executor/` (`stoke_rod_integration`)** — end-to-end BuildCriteria + AC verification against real Chrome.

Mirror the existing pattern in `internal/browser/browser_test.go`: table-driven where possible, one focused test per named behavior. Use `t.Parallel()` everywhere safe (pool tests may not parallel within a single subtree).

## 8. Rollout — build tag strategy

**Default: `go build ./cmd/r1` stays stdlib-only.** No Chromium dep, no go-rod linked into the binary. The existing MVP continues to work for all read-only verification use cases. This is the 95% path and it stays trivial.

**Opt-in: `go build -tags stoke_rod ./cmd/r1`.** Adds go-rod to the link set; `NewRodClient` constructs a real pool + launcher; interactive actions work. First invocation downloads Chromium to `$HOME/.cache/rod/browser` unless `CHROME_PATH` is set. CI that wants the interactive-path tests runs with the tag and pre-installs Chromium (or sets `CHROME_PATH`).

**Optional integration: `go build -tags stoke_rod,stoke_rod_integration`.** Unlocks the real-browser tests. Separate tag so unit tests can run under `stoke_rod` without a working Chrome.

**Tradeoff documented in `STATUS.md` Build tags section:**
- Pro (default): single static binary, zero runtime deps, fast `go build`.
- Pro (tagged): full interactive automation for operator-driven verification, deploy smoke tests, research claim verification with live pages.
- Con (tagged): +3 MB of Go deps linked in, +pin maintenance on rod SHA, Chromium is a runtime requirement.

**Migration to "tagged by default" is an operator decision later.** This spec does NOT flip the default. Existing users must explicitly opt in by rebuilding with the tag — matches our "no surprise dependencies" convention from H-91 onwards.

## 9. Boundaries — what NOT to do

- Do NOT redesign Part 1 (http backend). `internal/browser/browser.go`'s existing `Client`, `Fetch`, `VerifyContains`, `VerifyRegex`, `ExtractText`, `ExtractTitle` stay byte-identical. Only additions (Backend interface, `RunActions`/`Close` methods on `Client`) land.
- Do NOT design the vision-model screenshot-diff prompt. The `VisionDiffFunc` field exists as a seam; prompt engineering is a separate spec (likely operator-ux).
- Do NOT re-evaluate chromedp or Playwright. RT-01 chose go-rod; that decision is locked.
- Do NOT add selector-repair / re-click strategies in `BuildRepairFunc`. That's part of a follow-up that pairs with screenshot diffing.
- Do NOT change the `Executor` interface. Part 2 stays fully compatible with the Task 19 foundation.
- Do NOT default `stoke_rod` on. Single-binary users must stay unaffected.
- Do NOT introduce a second browser pool type elsewhere in the codebase. Pool lives in `internal/browser/pool.go`; other consumers (future Deploy executor, Research executor) take a `*RodClient` and reuse its pool.
