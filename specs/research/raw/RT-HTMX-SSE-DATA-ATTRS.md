# RT-HTMX-SSE-DATA-ATTRS

## Topic

htmx 2.x integration patterns for the `r1-server-ui-v2` chrome:

- htmx 2 stable status, 1.x → 2.x breaking changes.
- `htmx-ext-sse` Last-Event-ID / resume semantics on `/api/session/:id/events/stream`.
- The "migration to data-\* attributes" the spec calls out (§2.1, §8).
- `hx-swap-oob` for "one response, multiple targets".
- Authoring partials in Go `html/template` that drop into `hx-target`.
- CSRF / origin pinning when an htmx page issues `hx-post`.

Spec ref: `r1-server-ui-v2` §2.1 (chrome stack), §8 (no build step, partial swaps via
`hx-get`/`hx-post` + `hx-target`/`hx-swap`, SSE via `htmx-ext-sse`).

## htmx 2 status

- **Current stable: htmx 2.0.x.** htmx 2.0.0 GA was tagged 2024-06-17. The 2.x line
  has been the recommended stable line throughout 2024–2026.
- htmx 4 ("the fetchening") is announced and in active design but is **not** the
  stable line and is not what `htmx-ext-sse` is built against today. r1-server-ui-v2
  should pin to **htmx 2.x** — explicit version, not "latest".
- Breaking changes from 1.x relevant to this build:
  1. **IE dropped.** Not a concern for us.
  2. **Extensions are no longer bundled.** Each extension (including
     `htmx-ext-sse`) ships as a separate `<script>`. The 1.x `hx-sse` /
     `hx-ws` *attributes* are gone — you must use the extension attributes
     (`sse-connect`, `sse-swap`, `sse-close`) and load the extension via
     `<script src=".../htmx-ext-sse.js">` and `hx-ext="sse"` on the
     ancestor element.
  3. **`htmx.config.selfRequestsOnly` defaults to `true`.** htmx 2 will refuse
     cross-origin AJAX out of the box. Fine for r1d (same origin) but worth
     pinning explicitly so a future config flip doesn't open a hole.
  4. **`htmx.config.scrollBehavior` default is now `'instant'`** (was `'smooth'`).
  5. **`hx-on` syntax narrowed** — only the `hx-on:<event>` form is supported.
     The legacy whole-attribute form is gone.
  6. **DELETE** now sends params as URL query, not form-encoded body.
  7. **`htmx.makeFragment()`** always returns `DocumentFragment` — matters only
     if we touch the JS API.

None of (3)–(7) bite us as long as we pin a version, use `hx-on:<event>`, and
keep all server endpoints same-origin.

## SSE Last-Event-ID handling

Short version: **htmx-ext-sse does NOT forward `Last-Event-ID` reliably across
its own reconnects.** Treat it as best-effort. The server cannot rely on the
header to do gap-replay; the server MUST also accept a query-param fallback.

### Why

The extension uses the standard `EventSource` API:

```js
function createEventSource(url) {
  return new EventSource(url, { withCredentials: true })
}
```

The browser's `EventSource` automatically tracks `lastEventId` and re-sends it
as the `Last-Event-ID` header **when the browser itself transparently reconnects
the same EventSource instance** (e.g. after a server-sent retry hint or a brief
network blip). That part works.

The catch: htmx-ext-sse layers its **own** exponential-backoff reconnect on top.
On certain failure paths (explicit close, swap-target removed, visibility/wake),
the extension calls `createEventSource(url)` again — i.e. constructs a **new**
`EventSource`. A new `EventSource` starts with `lastEventId = ""`, so the
`Last-Event-ID` header will not be sent on the first request after that kind
of reconnect.

Source evidence (htmx-extensions `src/sse/sse.js`):

```js
// custom backoff path
retryCount = Math.max(Math.min(retryCount * 2, 128), 1)
var timeout = retryCount * 500
window.setTimeout(function () { ensureEventSourceOnElement(elt, retryCount) }, timeout)
```

`ensureEventSourceOnElement` ultimately calls `createEventSource(url)` afresh,
which is what loses the ID.

### What to do server-side

Two-pronged. The server SHOULD honor whichever it sees first:

1. **`Last-Event-ID` HTTP header** — set automatically by the browser on
   in-EventSource reconnects.
2. **Query-param fallback** — `?last_event_id=<id>` on the connect URL. We
   set this from the client by overriding `htmx.createEventSource`:

```html
<script>
  // Override BEFORE htmx-ext-sse loads (load order matters: this <script>
  // first, then htmx-ext-sse).
  (function () {
    let lastId = null
    document.body.addEventListener('htmx:sseMessage', (e) => {
      if (e.detail && e.detail.lastEventId) lastId = e.detail.lastEventId
    })
    window.htmx = window.htmx || {}
    window.htmx.createEventSource = function (url) {
      const u = new URL(url, window.location.href)
      if (lastId) u.searchParams.set('last_event_id', lastId)
      return new EventSource(u.toString(), { withCredentials: true })
    }
  })()
</script>
<script src="/static/htmx.min.js"></script>
<script src="/static/htmx-ext-sse.js"></script>
```

Server side (Go), in the SSE handler:

```go
lastID := r.Header.Get("Last-Event-ID")
if lastID == "" {
    lastID = r.URL.Query().Get("last_event_id")
}
// then replay events from the bus with id > lastID before going live.
```

### SSE wire-up shape on the page

```html
<div hx-ext="sse"
     sse-connect="/api/session/{{.ID}}/events/stream"
     sse-swap="message"
     hx-target="#session-log"
     hx-swap="beforeend">
  <div id="session-log"></div>
</div>
```

- `sse-connect` opens the stream.
- `sse-swap="<event-name>"` controls which named SSE events trigger a swap.
  Use named events on the server (`event: session.message\n`), not the default
  `message`, so different channels (log, status, tool-call) can target
  different DOM nodes from the same stream.
- Each event SHOULD set `id: <monotonic>\n` on the server so the browser's
  reconnect path can populate `Last-Event-ID`.

## data-\* migration

What the spec means: htmx accepts **two equivalent attribute spellings**:

- `hx-get="/foo"` — short form, technically not a valid HTML5 attribute
  (HTML5 only blesses `data-*` for unknown attrs), but universally supported
  by browsers because they ignore unknown attributes.
- `data-hx-get="/foo"` — the HTML5-spec-compliant form. Identical behavior.

From the htmx docs:

> "It's worth mentioning that, if you prefer, you can use the `data-` prefix
> when using htmx: `<a data-hx-post=\"/click\">Click Me!</a>`"

Both forms have been supported since 1.x and remain supported in 2.x. The
"migration to data-\*" the spec calls out is **a project convention choice**, not
a library-mandated 1.x→2.x change. The benefits:

1. **Validates as HTML5.** `htmlcheck`, vnu, and editor linters stop screaming.
2. **Plays nicely with strict CSPs and template tooling** that whitelists
   `data-*` (some Go template linters and CSP analysers treat unknown attrs
   as suspect).
3. **Self-documenting in mixed codebases** — search for `data-hx-` and you
   know it's an htmx hook, not a vendor-specific attr.

The only cost is verbosity. Since r1-server-ui-v2 is server-rendered Go templates
with no JS framework, the verbosity is paid once at template authoring time.

### Recommended convention

Use `data-hx-*` for **all** htmx attributes site-wide. Use the unprefixed
extension attributes (`sse-connect`, `sse-swap`, `sse-close`) for the SSE
extension because those are extension attributes, not core htmx attributes —
htmx itself only documents the `data-` alias for its core `hx-*` set. The SSE
extension still recognises both `sse-connect` and `data-sse-connect` in
practice (it scans for both), but to avoid surprises, use the form the
extension docs use.

Pick one, enforce in CI with a one-line grep:

```bash
# Fail if anyone reintroduces unprefixed hx-* in templates
git grep -nE '(^|[[:space:]])hx-(get|post|put|patch|delete|target|swap|trigger|vals|headers|on)' -- 'templates/**'
```

## Recommendation for r1-server-ui-v2

### Stack pins

```
htmx                 2.0.x  (pinned, served from /static)
htmx-ext-sse         latest 2.x-compatible (pinned)
htmx-ext-response-targets  optional, for 4xx/5xx target routing
```

Vendored locally, not from a CDN — no build step, but reproducible.

### Page chrome

- One root `<body>` with site-wide config:

  ```html
  <body data-hx-headers='{"X-CSRF-Token":"{{.CSRFToken}}"}'
        data-hx-ext="sse,response-targets">
  ```

- Pin htmx config in a `<meta>` tag rather than inline JS:

  ```html
  <meta name="htmx-config"
        content='{"selfRequestsOnly":true,"defaultSwapStyle":"innerHTML","historyCacheSize":0}'>
  ```

  `historyCacheSize:0` matters — htmx's history cache is XSS-relevant if any
  partial ever renders untrusted content; r1d session content is not trusted.

### Partials swap pattern (Go html/template)

One template file per page, fragments named via `{{define}}`. Detect partial
requests by the `HX-Request: true` header.

```go
// templates/session.tmpl
{{define "session"}}
<!doctype html>
<html><body>
  <main id="session" data-hx-target="this" data-hx-swap="outerHTML">
    {{template "session-body" .}}
  </main>
</body></html>
{{end}}

{{define "session-body"}}
  <h1>{{.Title}}</h1>
  <div id="session-log"
       data-hx-ext="sse"
       sse-connect="/api/session/{{.ID}}/events/stream"
       sse-swap="message"
       data-hx-swap="beforeend"></div>
  <form data-hx-post="/api/session/{{.ID}}/messages"
        data-hx-target="#session-log"
        data-hx-swap="beforeend">
    <textarea name="text"></textarea>
    <button type="submit">send</button>
  </form>
{{end}}
```

Handler:

```go
func (s *Server) sessionHandler(w http.ResponseWriter, r *http.Request) {
    data := s.loadSession(r)
    name := "session"
    if r.Header.Get("HX-Request") == "true" {
        name = "session-body" // fragment only
    }
    s.tpl.ExecuteTemplate(w, name, data)
}
```

This is the htmx-canonical "template fragments" pattern (Carson Gross,
*Template Fragments* essay) — single file, locality of behavior, no separate
partial-template files.

### Single-response, multi-target updates

Use OOB swaps. The primary swap goes to `hx-target`; everything else is
tagged `hx-swap-oob`:

```html
<!-- response body to POST /api/session/123/messages -->
<div class="msg">user said: hi</div>           {{/* goes to hx-target */}}
<div id="status-bar" hx-swap-oob="true">
  busy: 1 in flight
</div>
<ul id="memory-list" hx-swap-oob="beforeend:#memory-list">
  <li>new memory pinned</li>
</ul>
```

Match by `id`. The OOB element is detached from the response and re-targeted
client-side. Multiple OOB blocks per response are fine.

For instance-list / memory-explorer cross-cutting updates (where the action
happens in the session panel but the sidebar also needs to refresh), prefer
OOB over emitting a custom event + second `hx-trigger` round-trip — one HTTP
response, atomically applied.

### SSE on `/api/session/:id/events/stream`

- Server emits events with `id:`, `event:`, and `data:` fields. `id` is a
  monotonic per-session sequence (or the bus offset).
- Server reads `Last-Event-ID` header **and** `last_event_id` query param,
  preferring whichever is non-empty, and replays from that offset before
  going live.
- Client uses the `htmx.createEventSource` override above to thread
  `last_event_id` into the URL on extension-driven reconnects.
- Heartbeat: send a `: ping\n\n` SSE comment every 15s to keep proxies open.

### CSRF / origin pinning

- Keep `htmx.config.selfRequestsOnly: true` (it's the 2.x default — pin it
  in the meta tag anyway so a config edit can't silently disable it).
- Issue a CSRF token in a server-side cookie on first GET, and require it on
  every unsafe method. Inject into every page via `<body data-hx-headers>`:

  ```html
  <body data-hx-headers='{"X-CSRF-Token":"{{.CSRFToken}}"}'>
  ```

  htmx merges `data-hx-headers` from ancestors, so every `hx-post`/`hx-put`/
  `hx-patch`/`hx-delete` originating inside `<body>` carries the header.

- Server side: standard double-submit-cookie or `gorilla/csrf` middleware.
  Validate **method-and-Origin**: reject any `POST/PUT/PATCH/DELETE` whose
  `Origin` (or, fallback, `Referer`) doesn't match the expected host. htmx
  sets `Origin` on AJAX automatically.
- For SSE: the stream is GET, so it's not protected by CSRF. Authenticate it
  via the session cookie, and require an `Origin` header check on the GET to
  prevent EventSource-from-other-origin attacks (yes — `EventSource` does
  send `Origin`, and same-origin policy applies, but a hardened server
  double-checks).
- **Do not** put the CSRF token in `hx-vals` — that puts it in the request
  body, which means a logging proxy/middleware will capture it. Headers only.

### Anti-footguns

- Don't use `hx-boost` site-wide. It hijacks every link/form, including ones
  that need to navigate (auth, downloads). Opt-in per element instead.
- Don't enable `htmx.config.allowEval` (default false). Keep it false, write
  custom logic in event handlers, not in `hx-vals: js:` strings.
- All template output must auto-escape (Go `html/template` does this by
  default — never reach for `template.HTML` on user content). htmx swaps raw
  HTML, so a single un-escaped substitution is XSS.
- `historyCacheSize: 0` — htmx's history cache stores prior partials in
  `localStorage`. For an authenticated UI showing session/memory data,
  that's a leak. Disable.
- Pin extension versions. The htmx-extensions repo has had silent behavior
  changes between minor bumps (specifically around SSE retry backoff).

## Sources

Accessed 2026-05-05.

- [htmx 2.0.0 release announcement](https://htmx.org/posts/2024-06-17-htmx-2-0-0-is-released/)
- [htmx 1.x → 2.x Migration Guide](https://htmx.org/migration-guide-htmx-1/)
- [htmx CHANGELOG (master)](https://github.com/bigskysoftware/htmx/blob/master/CHANGELOG.md)
- [htmx 2.0 roadmap discussion #2198](https://github.com/bigskysoftware/htmx/discussions/2198)
- [htmx SSE extension docs (htmx.org)](https://htmx.org/extensions/sse/)
- [htmx SSE extension docs (four.htmx.org / 4.x preview)](https://four.htmx.org/extensions/sse/)
- [htmx SSE extension source — bigskysoftware/htmx-extensions src/sse/sse.js](https://github.com/bigskysoftware/htmx-extensions/blob/main/src/sse/sse.js)
- [htmx Issue #522: How to reconnect to SSE](https://github.com/bigskysoftware/htmx/issues/522)
- [htmx hx-swap-oob attribute reference](https://htmx.org/attributes/hx-swap-oob/)
- [htmx hx-select-oob attribute reference](https://htmx.org/attributes/hx-select-oob/)
- [htmx Template Fragments essay (Carson Gross)](https://htmx.org/essays/template-fragments/)
- [htmx + Go tutorial (DEV / Calvin McLean)](https://dev.to/calvinmclean/how-to-build-a-web-application-with-htmx-and-go-3183)
- [htmx Web Security Basics essay](https://htmx.org/essays/web-security-basics-with-htmx/)
- [htmx CSRF gist (1cg / Carson Gross)](https://gist.github.com/1cg/05feb956f9de8ecee02dd74acee5ffe1)
- [django-htmx tips: CSRF pattern](https://django-htmx.readthedocs.io/en/latest/tips.html)
- [htmx hx-headers attribute reference](https://htmx.org/attributes/hx-headers/)
- [htmx Documentation — data-\* prefix support](https://htmx.org/docs/)
- [MDN: data-\* global attributes](https://developer.mozilla.org/en-US/docs/Web/HTML/Reference/Global_attributes/data-*)
- [WHATWG HTML spec: attr-data-\*](https://html.spec.whatwg.org/multipage/dom.html#attr-data-*)
- [HTML5 Doctor: HTML5 Custom Data Attributes](http://html5doctor.com/html5-custom-data-attributes/)
- [JetBrains Guide: OOB swaps with htmx](https://www.jetbrains.com/guide/dotnet/tutorials/htmx-aspnetcore/out-of-band-swaps/)
- [htmx 4.0 "The Fetchening" — context for why we pin to 2.x](https://htmx.org/essays/the-fetchening/)
