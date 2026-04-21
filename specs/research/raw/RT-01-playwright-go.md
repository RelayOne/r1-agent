# RT-01: Headless Browser Options for Stoke `internal/browser/`

**Topic:** Best way for a Go CLI to drive a headless browser in 2026 for visual verification (screenshots + DOM extraction + console log capture).
**Author:** Research agent
**Date:** 2026-04-20
**Target package:** `internal/browser/` in Stoke (Apache-2.0 Go CLI agent)

---

## Use cases recap

- (a) Post-session visual verification: navigate `localhost:<port>`, screenshot, DOM, console errors.
- (b) Research claim verification: fetch URL, extract readable text, compare to claim.
- (c) Deployment verification: GET URL, assert 200, check console.

Stoke runs multiple sessions in parallel, so the driver must be safe under concurrent use.

---

## Option 1 — `github.com/go-rod/rod` (pure Go CDP)

- **Install burden:** One `go get`. Rod auto-downloads a matching Chromium into `$HOME/.cache/rod/browser` on first run via `lib/launcher`. No Node, npm, or system Chrome required for dev; CI can pre-install Chromium to avoid the download hop. (Source 1, 5)
- **Maintenance:** MIT. ~6.9k stars. Last tagged release on GitHub is v0.116.2 (Jul 12 2024), but the `main` branch shows commits into Feb 2026; project is maintained without formal release cadence. (Source 1, 2)
- **API ergonomics:** Fluent, thread-safe, auto-waiting (`MustWaitVisible`, `MustWaitStable`). Screenshot is one call (`page.Screenshot` or `MustScreenshot`). Text via `page.MustElement(sel).MustText()`. Console capture via `page.EachEvent(func(e *proto.RuntimeConsoleAPICalled) { ... })`. Fewer wrapper layers than chromedp; tracebacks readable. (Source 5, 8)
- **License:** MIT — compatible with Apache-2.0. (Source 1)
- **Concurrency:** First-class. `rod.NewBrowserPool(limit)` and `rod.NewPagePool(limit)` are built-in, thread-safe, with per-page contexts for cancellation. Rod uses remote-object IDs (like Puppeteer) instead of DOM node IDs, so parallel work on iframes/SPAs is less brittle than chromedp. (Source 5, 8)
- **Binary impact:** Adds ~3 MB to a Go binary (`ysmood/*` deps, CDP proto); Chromium is an out-of-process download, not linked.
- **Notable users:** No ADOPTERS.md; README lists "sponsored by many organizations." Rod advertises TestMu AI partnership. Ecosystem adoption is smaller than chromedp's but consistently cited as the Puppeteer-flavored Go client. (Source 6)

## Option 2 — `github.com/chromedp/chromedp` (the dominant Go CDP wrapper)

- **Install burden:** `go get`. Requires a Chromium/Chrome binary on `$PATH` or via `chromedp/headless-shell` Docker image (multi-arch, daily builds). Not auto-downloaded. (Source 2, 4)
- **Maintenance:** MIT. ~13k stars, 867 forks. Latest release v0.15.1 on 2026-03-23 per pkg.go.dev (some public listings lag); imported by ~2,179 known projects — the de facto standard. (Source 2, 4, 11)
- **API ergonomics:** Task-list DSL (`chromedp.Run(ctx, chromedp.Tasks{...})`). APIs: `CaptureScreenshot`, `FullScreenshot`, `Screenshot(sel, …)`, `Text(sel, &out)`, `InnerHTML`, `WaitVisible`, `WaitReady`. Console capture is manual: `ListenTarget(ctx, func(ev any){ switch ev.(type){ case *runtime.EventConsoleAPICalled, *runtime.EventExceptionThrown:…}})`. Verbose but explicit. (Source 4)
- **License:** MIT — compatible. (Source 2)
- **Concurrency:** Known weaknesses. Fixed-size event buffer can deadlock under high fan-out; single event loop means one slow listener blocks others. Mitigations: one `AllocatorContext` per session, `RemoteAllocator` for a long-running Chrome, explicit `NewContext` per page. Workable but requires care. (Source 4, compare-chromedp docs)
- **Binary impact:** Similar to rod (~3-4 MB of Go deps). Chromium still external.
- **Notable users:** Widely used in Go scraping/testing stacks; no curated adopter list, but import count + star count dwarf alternatives. (Source 2, 11)

## Option 3 — Shell out to Node + Playwright

- **Install burden:** Heaviest. Requires Node 18+, `npm i @playwright/test`, then `npx playwright install` (downloads Chromium + Firefox + WebKit ~300 MB). Every CI job, every dev machine, every container. (Source 3, 7)
- **Maintenance:** Apache-2.0. ~86.9k–94k stars range (Playwright repo itself ~86.9k; puppeteer 94.2k). Releases every 1–3 weeks; latest v1.59.1 (2026 train). Microsoft-maintained, aggressive release cadence. (Source 7, 9)
- **API ergonomics (via IPC):** Not Stoke's API — you'd either drive a Node script per call (process-spawn overhead 300-800 ms) or keep a long-running Node sidecar speaking JSON over stdio. Screenshots, text, console, and auto-waiting are all first-class on the Node side, but you pay a marshalling tax for each op.
- **License:** Apache-2.0 — identical to Stoke, trivially compatible. (Source 7)
- **Concurrency:** Playwright's `browser.newContext()` is the best-in-class isolation primitive; sidecar model scales well if you pool contexts. But you now own a Node runtime lifecycle and crash recovery.
- **Binary impact:** Go binary unchanged, but distribution jumps from "one static binary" to "binary + Node runtime + npm tree + browsers." Kills the single-binary story.
- **Notable users:** VS Code, GitHub, Bing, Microsoft 365, Shopify testing infra. (Source 3, 7)

## Option 4 — Shell out to Node + Puppeteer

- **Install burden:** Same cost class as Playwright — Node + `npm i puppeteer` (bundles Chromium ~170 MB). (Source 3, 9)
- **Maintenance:** Apache-2.0. ~94.2k stars. `puppeteer-core` v24.42.0 on 2026-04-20; Google-maintained, weekly cadence. (Source 9)
- **API ergonomics:** Same sidecar pattern as Playwright. Puppeteer is Chrome-only (with experimental Firefox). Less auto-waiting than Playwright. Still an IPC hop away from Go. (Source 3)
- **License:** Apache-2.0. (Source 9)
- **Concurrency:** Fine, but same architectural cost as Playwright.
- **Binary impact:** Same as Playwright — breaks the single-binary story.
- **Notable users:** Google internal, Netlify, Chrome team dogfood. (Source 3)

---

## Trade-off matrix

| Criterion | go-rod | chromedp | Playwright (Node) | Puppeteer (Node) |
|---|---|---|---|---|
| Install burden | Low (auto-DL) | Low-med (chrome on PATH) | High (Node+npm+browsers) | High (Node+npm+browsers) |
| License | MIT | MIT | Apache-2.0 | Apache-2.0 |
| Stars | 6.9k | 13k | 86.9k | 94.2k |
| Last release | main active Feb 2026 | v0.15.1 (Mar 2026) | v1.59.1 (2026) | v24.42.0 (Apr 2026) |
| Concurrency primitives | BrowserPool/PagePool built-in | Manual; known pitfalls | Excellent via contexts | Good via contexts |
| API ergonomics | Fluent, auto-wait | Task-DSL, verbose | Best-in-class | Good |
| Single-binary | Yes | Yes | No | No |
| Cross-browser | Chromium only | Chromium only | Chromium+Firefox+WebKit | Chromium |

---

## Recommendation: **`go-rod`**

Stoke is a single-binary Go CLI; adding a Node runtime for browser verification detonates the distribution story and the CI matrix. Between the two pure-Go CDP options, go-rod wins for Stoke's access pattern: it ships a built-in `BrowserPool`/`PagePool` that matches Stoke's "many sessions in parallel" model, avoids chromedp's single-event-loop and fixed-buffer concurrency pitfalls, auto-downloads a pinned Chromium on first run (great for zero-config local dev; CI can pre-cache), and its remote-object-ID architecture handles iframes and SPAs without the brittleness chromedp exhibits. The MIT license is clean with Apache-2.0. The one risk is that go-rod has ~half chromedp's star count and no formal v1 — mitigate with a narrow `Browser` interface (below) so we can swap to chromedp in a week if needed. For use case (b) — research-claim text extraction — rod's `page.MustElement("body").MustText()` plus a Readability-style DOM snippet is simpler than chromedp's `Text(sel, &out)` DSL. (Sources 1, 5, 6, 8, 10)

### Proposed Stoke interface (swap-able)

```go
package browser

import (
    "context"
    "time"
)

// Browser is the driver-neutral contract. Default impl: rodDriver.
type Browser interface {
    Navigate(ctx context.Context, url string) error
    Screenshot(ctx context.Context, path string) error          // full-page PNG
    ExtractText(ctx context.Context) (string, error)            // readable body text
    ConsoleErrors(ctx context.Context) ([]string, error)        // drains since Navigate
    WaitForSelector(ctx context.Context, selector string, timeout time.Duration) error
    Close() error
}

// Pool hands out Browser instances for concurrent session use.
// Implemented with rod.NewBrowserPool(limit) under the hood.
type Pool interface {
    Acquire(ctx context.Context) (Browser, error)
    Release(Browser)
    Close() error
}
```

Implementation notes:

- Construct one `*rod.Browser` per Stoke session worker via `rod.NewBrowserPool(N)` where `N == subscriptions.MaxParallel`.
- Console capture: register `page.EachEvent(func(e *proto.RuntimeConsoleAPICalled){...})` immediately after `Navigate`; stash entries on a per-page ring buffer flushed by `ConsoleErrors`.
- `ExtractText`: prefer `page.MustElement("body").MustText()` for speed; for research use case (b) layer a Readability port or innerText-of-`article,main`.
- Timeouts: thread `ctx` into every op; rod honors `ctx.Done()` via its sleeper abstraction.
- Tests: rod has a first-class test harness (`rod/lib/devices`, `launcher.Headless(true)`); gate CI browser jobs behind a `-tags browser` build tag so `go test ./...` stays fast.

---

## Sources

1. [go-rod/rod — GitHub repo](https://github.com/go-rod/rod) — accessed 2026-04-20
2. [chromedp/chromedp — GitHub repo](https://github.com/chromedp/chromedp) — accessed 2026-04-20
3. [Playwright vs Puppeteer: Which to choose in 2026? — BrowserStack](https://www.browserstack.com/guide/playwright-vs-puppeteer) — accessed 2026-04-20
4. [chromedp — pkg.go.dev (v0.15.1, 2026-03-23)](https://pkg.go.dev/github.com/chromedp/chromedp) — accessed 2026-04-20
5. [rod — pkg.go.dev](https://pkg.go.dev/github.com/go-rod/rod) — accessed 2026-04-20
6. [Why rod? — go-rod docs](https://github.com/go-rod/go-rod.github.io/blob/main/why-rod.md) — accessed 2026-04-20
7. [Microsoft Playwright — GitHub repo](https://github.com/microsoft/playwright) — accessed 2026-04-20
8. [Golang Headless Browser best tools — Latenode](https://latenode.com/blog/web-automation-scraping/headless-browser-overview/golang-headless-browser-best-tools-for-automation) — accessed 2026-04-20
9. [puppeteer/puppeteer — GitHub repo (puppeteer-core v24.42.0, 2026-04-20)](https://github.com/puppeteer/puppeteer) — accessed 2026-04-20
10. [Chromedp: Golang Headless Browser Tutorial 2026 — ZenRows](https://www.zenrows.com/blog/chromedp) — accessed 2026-04-20
11. [chromedp/docker-headless-shell — GitHub](https://github.com/chromedp/docker-headless-shell) — accessed 2026-04-20
