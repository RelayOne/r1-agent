<!-- STATUS: done -->
<!-- CREATED: 2026-05-05 -->
<!-- BUILD_STARTED: 2026-05-05 -->
<!-- BUILD_COMPLETED: 2026-05-05 -->
<!-- DEPENDS_ON: r1-server-ui-v2-foundation -->
<!-- BUILD_ORDER: 31 -->
<!-- BLOCKED_PARTIAL: T11 (memory graph view — defers to post-merge integration with Spec 2) + T15-T17 caller wiring (serveTracebundle is ready, the production TracebundleSource implementation against ledger.Store waits on Spec 5's fixture loader). -->

# r1-server UI v2 — Handlers & Routes (filling the gaps)

## 1. Overview

Several Go-side route gaps were left when the original `r1-server-ui-v2` work shipped. This spec fills them, packaged together because they're all small Go-side handler/template work that doesn't depend on the 3D refactor or the cross-cutting redaction/skill rendering:

- **Feature flag polish (3)** — currently `R1_SERVER_UI_V2=1` is read in three different functions; centralise + document.
- **Go-side handler templates (6)** — finish the `web/` page templates that Spec 1's `base.html` will be extended by: `index.html`, `session.html`, `session-stream.html`, `memories.html`, `share.html`, `diff.html`.
- **Memory explorer remaining (4)** — memory side-panel + memory-graph view + filter+search filter chip UI + grouped memory partials.
- **Content-addressed share route polish (4)** — already shipped at the handler level; need the `share.html` template + `share_enabled` flag.
- **`.tracebundle` export (3)** — tar.gz format + `manifest.json` + redacted-content sidecar.
- **SSE polish (2)** — `last_event_id` URL-query fallback (per RT-HTMX-SSE-DATA-ATTRS finding: htmx-ext-sse resets `lastEventId=""` on reconnect) + `event: resync` frame on cursor pruning.

## 2. Stack & Versions

- Go html/template (existing)
- htmx-ext-sse 2.2.4 (vendored by Spec 1)
- archive/tar + compress/gzip stdlib for `.tracebundle`
- existing `cmd/r1-server/sse.go` for SSE handler

## 3. Architecture

```
cmd/r1-server/
├── ui/web/
│   ├── index.html              # instance list (extends base)
│   ├── session.html            # waterfall + side panel (extends base)
│   ├── session-stream.html     # raw event stream (extends base)
│   ├── memories.html           # memory explorer (extends base)
│   ├── share.html              # read-only snapshot (extends base)
│   ├── diff.html               # run diff side-by-side (extends base, replaces minimal HTML in diff.go)
│   └── partials/
│       ├── instance-row.html
│       ├── memory-card.html
│       ├── memory-group.html
│       ├── memory-side-panel.html
│       └── filter-chips.html
├── ui_v2_flag.go               # NEW: centralised v2Enabled() + v2Config()
├── trace.go                    # extended: serveTraceWaterfall renders web/session.html
├── memories.go                 # extended: side-panel + filter handlers
├── share.go                    # extended: serveShare renders web/share.html
├── tracebundle.go              # NEW: GET /api/session/:id/export.tracebundle
├── diff.go                     # extended: serveDiff renders web/diff.html
└── sse.go                      # extended: ?last_event_id= fallback + resync frame
```

## 4. Feature flag centralisation

Currently `os.Getenv("R1_SERVER_UI_V2") == "1"` is checked in `ui.go`, `share.go`, `memories.go`, etc. Move to one helper + a typed config struct:

```go
// cmd/r1-server/ui_v2_flag.go

type V2Config struct {
    Enabled       bool   // R1_SERVER_UI_V2=1
    ShareEnabled  bool   // R1_SERVER_SHARE_ENABLED=1 (for /share/*)
    HtmxSRI       string // SRI hash for vendored htmx, baked at compile time
    HtmxSseSRI    string
}

func LoadV2Config() V2Config {
    return V2Config{
        Enabled:      os.Getenv("R1_SERVER_UI_V2") == "1",
        ShareEnabled: os.Getenv("R1_SERVER_SHARE_ENABLED") == "1",
        HtmxSRI:      vendoredSRI["htmx.min.js"],
        HtmxSseSRI:   vendoredSRI["htmx-ext-sse.js"],
    }
}

func (c V2Config) Renderable() bool { return c.Enabled }

// Replaces the v2Enabled() function. All callsites migrate.
```

The SRI hashes are baked from a `vendoredSRI` map populated by Spec 1's `sri_test.go`. Compile-time injection means the runtime never needs to read the SRI file from disk.

## 5. Page templates

Each page extends Spec 1's `base.html` and provides a `{{ define "title" }}` + `{{ define "main" }}` block. Pages stay thin; partials do the heavy lifting.

### 5.1 `index.html` — instance list

```html
{{ template "base" . }}
{{ define "title" }}r1 instances{{ end }}
{{ define "main" }}
<header class="topbar">
  <h1>r1 instances</h1>
  <a href="/settings" hx-get="/settings" hx-target="#main">Settings</a>
</header>
<table data-testid="instance-list">
  <thead><tr><th>Instance</th><th>Status</th><th>Sessions</th><th>Last activity</th></tr></thead>
  <tbody hx-ext="sse" sse-connect="/api/events/instance-stream" sse-swap="instance-row">
    {{ range .Instances }}{{ template "instance-row" . }}{{ end }}
  </tbody>
</table>
{{ end }}
```

### 5.2 `session.html` — waterfall + side panel

```html
{{ template "base" . }}
{{ define "title" }}{{ .Session.Name }} — r1{{ end }}
{{ define "main" }}
<nav class="session-tabs">
  <a aria-current="page" href="/session/{{ .Session.ID }}">Waterfall</a>
  <a href="/session/{{ .Session.ID }}/graph">3D Graph</a>
  <a href="/session/{{ .Session.ID }}/stream">Stream</a>
  <a href="/memories?session={{ .Session.ID }}">Memories</a>
</nav>
<div class="split-pane">
  <section class="waterfall"
           hx-ext="sse"
           sse-connect="/api/session/{{ .Session.ID }}/events/stream?last_event_id={{ .LastEventID }}"
           sse-swap="waterfall-node">
    {{ range .Rows }}{{ template "waterfall-row" . }}{{ end }}
    <div hx-trigger="revealed"
         hx-get="/session/{{ .Session.ID }}/waterfall?after={{ .LastID }}"
         hx-swap="outerHTML"></div>
  </section>
  <aside id="side-panel">{{ template "node-side-panel-empty" . }}</aside>
</div>
<footer class="filters">
  {{ template "filter-chips" .Filters }}
  <input type="search" name="q" placeholder="Search nodes..."
         hx-get="/session/{{ .Session.ID }}/waterfall"
         hx-trigger="input changed delay:200ms"
         hx-target=".waterfall"
         hx-include="[name='types']">
  <input type="range" id="timeline-scrubber" data-island="scrubber"
         min="{{ .Session.StartedAtUnix }}" max="{{ .Session.UpdatedAtUnix }}">
</footer>
{{ end }}
```

The `hx-trigger="revealed"` infinite-scroll sentinel uses the htmx server-paged chunk pattern from RT-WATERFALL-DENSITY (strategy G). Combined with `content-visibility: auto` on each row (set in Spec 3's waterfall-row template), this is the recommended path for ≤5k rows; Clusterize.js fallback (RT strategy H) is a separate spec if telemetry shows scroll FPS < 50.

### 5.3 `session-stream.html` — raw events

Mirrors the existing `serveStreamingEvents` handler. SSE-streamed `<pre>` block, no styling tricks, deliberate "raw mode" for power users.

### 5.4 `memories.html` — memory explorer

```html
{{ template "base" . }}
{{ define "title" }}memories — r1{{ end }}
{{ define "main" }}
<header>
  <h1>Memories</h1>
  <button data-hx-get="/api/memories/new" data-hx-target="#side-panel">+ Add Memory</button>
</header>
{{ template "filter-chips" .Filters }}
<input type="search" name="q" placeholder="Search FTS5..."
       data-hx-get="/memories" data-hx-trigger="input changed delay:300ms"
       data-hx-target="#groups" data-hx-include="[name='scope']">
<div class="layout">
  <main id="groups">
    {{ range .Groups }}{{ template "memory-group" . }}{{ end }}
  </main>
  <aside id="side-panel">{{ template "memory-side-panel-empty" . }}</aside>
</div>
{{ end }}
```

### 5.5 `share.html` — read-only snapshot

```html
{{ template "base" . }}
{{ define "title" }}{{ .Snapshot.SessionName }} (read-only) — r1{{ end }}
{{ define "main" }}
<aside class="banner">Read-only snapshot of <code>{{ .Snapshot.ChainRootHash | trunc 12 }}</code>
       as of <time>{{ .Snapshot.CreatedAt.Format "2006-01-02 15:04 UTC" }}</time></aside>
<div class="waterfall waterfall--readonly">
  {{ range .Rows }}{{ template "waterfall-row" . }}{{ end }}
</div>
{{ end }}
```

No SSE, no filter, no scrubber — pure read-only. Locked down via `share_enabled=true` in config.

### 5.6 `diff.html` — side-by-side

Replaces the inline-fmt-printf HTML in `cmd/r1-server/diff.go` (shipped in PR #151) with a real template that matches base.html theming. Same DiffRow data structure.

## 6. Memory explorer extensions

### 6.1 Side panel

Loads via `hx-get="/api/memories/:id"` triggered by clicking a card; swaps `#side-panel`. Shows full content (or `[encrypted — unlock keyring]` per the existing `content_encrypted` check), write-count, read-count, edit form (admin-gated server-side), delete button (htmx-confirm).

### 6.2 Memory graph view

`/memories/:id/graph` — reuses Spec 2's 3D graph code with a filtered dataset: just the `memory_stored` write node + all `memory_recalled` reads + the agents that touched them, connected by read/write edges. This is just a different `?initial-data` query string passed to the same `web/session-graph.html` template.

### 6.3 Filter chips

Per RT-HTMX-SSE-DATA-ATTRS, the data-* migration: filter selectors render as a row of chips, each with `data-hx-get`, `data-hx-target`, `data-hx-trigger="click"`, `data-state="active|inactive"`. Multi-select compose via `hx-include="[name='scope']"`.

## 7. `.tracebundle` export

```
GET /api/session/:id/export.tracebundle
Accept: application/gzip

Response:
  Content-Type: application/gzip
  Content-Disposition: attachment; filename="<session-id>.tracebundle"
  Body: tar.gz of:
    manifest.json    {"format":"tracebundle","version":1,"session_id":...,"chain_root_hash":...}
    chain.ndjson     # one chain-tier node per line
    edges.ndjson     # one edge per line
    content/         # one JSON blob per content-tier node
    content/redacted.json  # list of {node_id, redaction_event_ids} for redacted nodes (no content)
```

Streaming: tar writes are `io.Copy`'d into a `gzip.Writer` wrapping the response. Memory cap: a single 64 KB transfer buffer; never load the full session into memory.

## 8. SSE polish

### 8.1 `last_event_id` URL-query fallback

Per RT-HTMX-SSE-DATA-ATTRS: htmx-ext-sse creates a fresh `EventSource` on reconnect, resetting `lastEventId=""`. Workaround: override `htmx.createEventSource` client-side to thread `?last_event_id=` from the cached value, AND have the Go handler accept either `Last-Event-ID` header or `last_event_id` query.

```go
// cmd/r1-server/sse.go (extended)

func handleEventsStream(...) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        sinceID := r.Header.Get("Last-Event-ID")
        if sinceID == "" {
            sinceID = r.URL.Query().Get("last_event_id")
        }
        if sinceID == "" {
            sinceID = r.URL.Query().Get("after") // legacy alias
        }
        ...
    }
}
```

### 8.2 `event: resync` on pruning

When `since_id < bus_retention_floor` (the bus has pruned the requested cursor), respond with:

```
event: resync
data: {"reason":"cursor_pruned","oldest_available":<n>}

```

Then close the connection. Client code in `web/js/htmx-sse-shim.js` (added by this spec) listens for `addEventListener('resync', ...)` and triggers `window.location.reload()`.

## 9. Boundaries

- **No new ledger node types.** All emission is read-only pull from existing types.
- **No share link mutation routes.** Shares are content-addressed and immutable; deletion via existing key-rotation in retention-policies spec.
- **No tracebundle import.** The dual route `POST /api/sessions/import` is in scope of `cmd/r1-server/import.go` (already exists for the internal session-replay use case); this spec only adds export.
- **No memory write FTS5 index updates** — that's owned by memory-bus.
- **No SSE multiplexing.** Each session+client pair gets its own subscription; if a client opens 5 tabs they get 5 connections (the daemon's expected load).

## 10. Implementation checklist (22 items — self-contained)

### Feature flag

- [ ] Write `cmd/r1-server/ui_v2_flag.go` with `V2Config` struct + `LoadV2Config()` per §4. Wire `vendoredSRI` map populated at compile time from a generated file (`go generate ./cmd/r1-server/sri_table` reads `web/vendor/sri.json` produced by Spec 1's `vendor-ui.sh`). Add `cmd/r1-server/ui_v2_flag_test.go` with 4 tests: env-unset → Enabled=false; env=1 → Enabled=true; env=true → Enabled=false (strict "1" only); ShareEnabled independent of Enabled.
- [ ] Migrate every `os.Getenv("R1_SERVER_UI_V2")` callsite to `cfg.Enabled` (passed through context or as a method receiver). Files: `ui.go`, `share.go`, `memories.go`, `trace.go`. Run `go vet ./cmd/r1-server/...` to verify; add `cmd/r1-server/no_direct_env_test.go` that grep-asserts no remaining direct calls to `os.Getenv("R1_SERVER_UI_V2")` outside `ui_v2_flag.go`.
- [ ] Document the feature-flag table in `cmd/r1-server/README.md`: `R1_SERVER_UI_V2`, `R1_SERVER_SHARE_ENABLED`, `R1_MEMORIES_PASSPHRASE` — required values, default behaviour, audit semantics.

### Page templates

- [ ] Write `cmd/r1-server/ui/web/index.html` per §5.1. Must extend `base.html`, declare a `data-testid="instance-list"` table, hook the SSE instance-stream. Add golden test in `index_test.go` that fixtures 2 instances + asserts the rendered output matches `testdata/golden/index.html`.
- [ ] Write `cmd/r1-server/ui/web/session.html` per §5.2: tabs nav, split pane, SSE waterfall feed with `last_event_id` query, infinite-scroll sentinel, filter chips, scrubber island. Include the `hx-trigger="revealed"` server-paged chunk pattern from RT-WATERFALL-DENSITY. Migrate `serveTraceWaterfall` (in `trace.go`) to render this template when `cfg.Enabled`. Golden test for the rendered tree at 50 rows.
- [ ] Write `cmd/r1-server/ui/web/session-stream.html` per §5.3: a raw-mode SSE-streamed pre-block. New handler `serveStreamView(d *DB)` registered at `GET /session/{id}/stream` (mountUI). Tests: `TestStreamView_RendersSSEHookup`, `TestStreamView_404OnUnknownSession`.
- [ ] Write `cmd/r1-server/ui/web/memories.html` per §5.4. Mounted at `GET /memories` when `cfg.Enabled` (existing `serveMemories` extended). Filter chips composable via `hx-include`. Golden test with 3 groups × 5 cards.
- [ ] Write `cmd/r1-server/ui/web/share.html` per §5.5. Mounted at `GET /share/{hash}` when `cfg.Enabled && cfg.ShareEnabled`. 404s with body "share not enabled" when share flag off; 404 with "snapshot not found" when hash unknown. Golden test for both.
- [ ] Write `cmd/r1-server/ui/web/diff.html` per §5.6. Migrate `serveDiff` from PR #151 (currently emits inline `fmt.Fprintf` HTML) to render this template. Keep the JSON branch unchanged. Test: `TestServeDiff_HTMLUsesTemplate` asserts output contains `{{ template "base" }}` markers (or rather the resulting `<title>` + `data-testid` attrs).

### Memory explorer extensions

- [ ] Write `cmd/r1-server/ui/web/partials/memory-side-panel.html` showing full content (or `[encrypted — unlock keyring]` placeholder when `Memory.ContentEncrypted != nil && keyring.Unavailable()`), write/read counts, edit form (RBAC-gated), delete button with `data-hx-confirm="Delete memory X?"`. New handler `serveMemorySidePanel(d *DB)` at `GET /api/memories/{id}/panel` returning the rendered partial when `HX-Request: true`, else the JSON shape (existing).
- [ ] Wire memory graph view: extend `serveGraphIndex` (existing) with a `memory_id` query param; when set, the handler loads only the `memory_stored` write node + all `memory_recalled` reads referencing it + the agents involved + the read/write edges, packaged as the graph's initial dataset. Mounted at `GET /memories/{id}/graph`. Test: fixture memory + 3 reads → 3D graph initial JSON has 5 nodes (write + 3 reads + 1 agent).
- [ ] Add `cmd/r1-server/ui/web/partials/filter-chips.html` per §6.3: chip-style filter selectors with `data-hx-get`, `data-hx-target`, `data-hx-trigger="click"`, `data-state` toggling. CSS in `web/css/base.css` styles `[data-state="active"]` chips. Test asserts the rendered chips for a 3-element filter list have correct active-state attributes.

### Share polish

- [ ] Add `R1_SERVER_SHARE_ENABLED=1` gate to `serveShare` (currently always 200 when v2 on). When `cfg.ShareEnabled == false`, return 404 with body "share not enabled". Add `share_enabled_test.go` with three cases: enabled+valid hash → 200; enabled+invalid hash → 404 "snapshot not found"; disabled → 404 "share not enabled".
- [ ] Banner copy: `share.html` template top renders "Read-only snapshot of `<chain_root_hash>` as of `<created_at>`". Verify the banner is the FIRST rendered element (above the waterfall) so screen readers announce read-only context first. Test asserts the banner element's source order.

### Tracebundle export

- [ ] Write `cmd/r1-server/tracebundle.go` with `serveTracebundle(d *DB)` handler. Streams a tar.gz with `manifest.json` + `chain.ndjson` + `edges.ndjson` + `content/<node_id>.json` for each non-redacted content-tier node + `content/redacted.json` summary. Buffer size ≤ 64 KB. Mounted at `GET /api/session/{id}/export.tracebundle` when `cfg.Enabled`. Imports: `archive/tar`, `compress/gzip`, `encoding/json`. NO 3rd party deps.
- [ ] Add `cmd/r1-server/tracebundle_test.go`: TestTracebundle_RoundTrip — fixture session with 5 chain nodes (1 redacted) → call handler → unzip + untar → assert manifest valid JSON + chain.ndjson has 5 lines + content/ has 4 JSON blobs + content/redacted.json lists 1 entry.
- [ ] Add Cache-Control + Content-Disposition headers to the tracebundle response. Cache-Control: `private, no-cache` (downloads should not be CDN-cached). Content-Disposition: `attachment; filename="<session_id>.tracebundle"`. Test asserts both headers are set.

### SSE polish

- [ ] Extend `cmd/r1-server/sse.go` `handleEventsStream` to accept `last_event_id` from URL query when the `Last-Event-ID` header is empty (per §8.1 + RT-HTMX-SSE-DATA-ATTRS). Test: `TestSSE_LastEventIDFromQuery` — request with `?last_event_id=42` (no header) → server resumes from 42.
- [ ] Implement `event: resync` frame per §8.2: when `sinceID < oldestAvailable` (look up via `d.OldestEventID(sessionID)`), write `event: resync\ndata: {"reason":"cursor_pruned","oldest_available":<n>}\n\n`, flush, return. Test: `TestSSE_ResyncOnPrunedCursor` fixture seeds events 100-200, prunes 100-150, requests `?last_event_id=120` → response is single `event: resync` frame.
- [ ] Write `cmd/r1-server/ui/web/js/htmx-sse-shim.js` (vanilla JS module): wraps htmx-ext-sse to (a) append `?last_event_id=<cached>` to the SSE URL on reconnect (workaround per RT-HTMX-SSE-DATA-ATTRS finding 2), and (b) listen for `event: resync` and trigger `window.location.reload()`. Loaded from `base.html` via `<script type="module" src="/ui/web/js/htmx-sse-shim.js">`. No tests in this spec — covered by Spec 5's E2E.

## 11. Acceptance

- `go build ./cmd/r1-server/...` clean.
- `go test ./cmd/r1-server/...` all 18+ new tests pass.
- Manual: `R1_SERVER_UI_V2=1 R1_SERVER_SHARE_ENABLED=1 go run ./cmd/r1-server`; load `/`, `/session/<id>`, `/session/<id>/stream`, `/memories`, `/share/<hash>`, `/api/session/<id>/export.tracebundle`. All render without console errors.
- Curl the tracebundle endpoint, untar it, manually verify every spec'd file is present.
- SSE reconnect under packet drop: kill the daemon, restart, observe the page resume the stream from `last_event_id` rather than restarting from 0 (verify in network panel that the reconnect URL has the query param).
