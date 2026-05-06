# Browser executor (Task 21)

R1's browser executor lets you fetch and verify web content as
a first-class executor type. Task 21 ships two phases:

**Part 1 (this release)** — stdlib-only fetch + extract + verify.
Covers: "does this URL return 200", "does the page contain text
X", "does the page match regex Y". No JavaScript execution.

**Part 2 (future)** — go-rod / Playwright backend for real
interactive browsing (click, type, wait, screenshot).

## CLI

```
r1 browse <url> [--expected TEXT] [--regex PATTERN] [--timeout DUR] [--text-limit N]
```

Prints status, final URL (after redirects), content-type, page
title, extracted body text (first 1000 chars by default). Optional
`--expected` runs a case-insensitive substring check; `--regex`
runs an RE2 pattern match. Either verifier failing exits 3; a
non-2xx HTTP status exits 2.

Example:

```
$ r1 browse https://pkg.go.dev/net/http --expected "Package http"
URL:          https://pkg.go.dev/net/http
Status:       200
Content-Type: text/html; charset=utf-8
Title:        http package - net/http - Go Packages
Bytes:        48203

  Package http ... [truncated at 1000 chars]

Verification:
  ✓ BROWSER-TEXT-MATCH — found "Package http" in page text
```

## Executor integration

The `BrowserExecutor` satisfies `executor.Executor`, so it plugs
into the same router / descent ladder as code, research, and
deploy. `BuildCriteria` returns a `BROWSER-LOADED` AC and optional
text / regex ACs; `BuildEnvFixFunc` treats timeouts / 5xx as
transient (retry at T5); `BuildRepairFunc` returns nil because
"retry the fetch with different selectors" requires the go-rod
backend — descent falls through to T7 / T8 until part 2 lands.

## Part 1 limitations

- No JavaScript execution — SPAs that render content client-side
  look empty to us. Use the raw API endpoint instead, or wait for
  Part 2.
- No cookies / session handling — each `Fetch` is stateless.
- No interactive actions — attempting `plan.Extra["interactive"] =
  true` returns `ErrExecutorNotWired` pointing at Task 21 part 2.
- 1MB body cap — adversarial large pages get truncated with a
  visible marker in the extracted text.

## Part 2 roadmap

- Add `internal/browser/rod.go` wrapping
  [go-rod/rod](https://github.com/go-rod/rod) behind the same
  `Fetch` interface plus new `Click`, `Type`, `Wait`, `Screenshot`
  methods.
- `BrowserExecutor` selects the backend via `BACKEND` field (http
  vs rod); callers requesting interactive actions get the rod
  backend automatically.
- Vision-model screenshot diffs for UI verification (talks to the
  reasoning provider registered in the pool).
- Local chrome install path documentation; support `CHROME_PATH`
  env override for CI.

## Security

- The fetcher does NOT follow arbitrary redirect targets outside
  HTTP/HTTPS — the stdlib `net/http` handles scheme filtering.
- No credentials are sent. If an operator needs authenticated
  fetches they pass the token in the URL or wait for Part 2's
  cookie-jar support.
- The websearch allowlist (`internal/websearch/`) does NOT apply
  to `r1 browse`. If you expose `r1 browse` over the HTTP
  hireable agent facade, add your own allowlist.
