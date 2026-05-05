# RT-WATERFALL-DENSITY

## Topic

Virtualization / windowing strategies for the **r1-server-ui-v2 waterfall view** —
a tree-indented, ledger-node-per-row "trace waterfall" that is the **default**
session view (spec §1 corrections 6+7) and must stay smooth when a single
session emits **1,000–10,000 events**.

Constraints from the spec (§4 Row rendering, §4.4 filter+search+scrubber):

- Render stack: htmx 2 partials + Go `html/template` + vanilla JS islands.
- **No React, no framework runtime.** Bundle additions are metered in KB, not MB.
- Each row is a ledger node (typed: `task.start`, `tool.use`, `bus.event`, …).
- Rows are indented (parent/child via `parent_id`); collapse/expand allowed.
- Filter, full-text search, and a time scrubber must remain interactive.
- Target: 60 FPS scroll on 5k rows on a 2020-era laptop (Intel i5-1035G1 / M1 baseline).

We need to pick a windowing strategy that is small, compatible with htmx,
and degrades gracefully if JS fails to load.

---

## Strategies considered

| # | Strategy                                                          | Bundle cost  | Framework req | Smoothness @ 5k rows | Complexity | Works without JS |
|---|-------------------------------------------------------------------|--------------|---------------|----------------------|------------|------------------|
| A | **Pure DOM** (render all 5k–10k `<tr>`)                           | 0            | none          | 1/5 (jank, slow init) | trivial    | yes              |
| B | **CSS-only**: `content-visibility:auto` + `contain-intrinsic-size`| 0            | none          | 3/5 up to ~2–3k; degrades after | trivial | yes              |
| C | **Clusterize.js**                                                  | ~2.3 KB gz   | none (vanilla)| 4/5 reliably to 50k+ | low        | partial (fallback list) |
| D | **`virtual-scroller` web component (holmberd)**                    | ~6–8 KB gz   | none (custom element) | 4/5            | medium     | partial          |
| E | **IntersectionObserver hand-roll** (lazy-mount sentinel rows)      | ~1–2 KB gz   | none          | 3.5/5 (scroll-jump risk) | medium-high | partial      |
| F | **htmx infinite scroll** (`hx-trigger="revealed"`, append-only)    | 0 (htmx already) | htmx       | 2.5/5 — DOM grows unboundedly past ~3k | low | yes |
| G | **Hybrid: B + F + aggregation**                                    | 0–2 KB gz    | htmx + vanilla| 4.5/5 expected       | medium     | yes (B+F both degrade) |
| H | **Hybrid: C inside htmx-loaded chunks**                            | ~2.3 KB gz   | htmx + vanilla| 5/5 expected to 10k+ | medium     | partial          |

Notes on the ratings:

- **A** dies on initial render: Lawson 2024 measured ~3 s render on 20k DOM
  nodes even on a fast machine. Our 5k–10k upper bound would still cost
  hundreds of ms of style+layout, blowing the 60 FPS budget on first paint.
- **B** alone gives **~15% Chrome / ~5% Firefox** initial-load improvement on
  the Lawson benchmark, rising to **~40–45%** when paired with
  `IntersectionObserver`. CSS-Tricks/web.dev confirm scrolling triggers
  *continuous* paint as `:auto` regions enter/leave the viewport — works to
  ~1–3k rows, then CPU spikes during fast scroll.
- **C** Clusterize is **2.3 KB gzipped**, last release v1.0.0 (3 yrs ago, but
  stable; no known correctness bugs). Maintains a fixed-size DOM window
  (~50 rows by default) regardless of data size; tested by users at 500k+ rows.
- **D** `virtual-scroller` web component is heavier (~6–8 KB) and supports
  variable-height rows, but it requires `customElements` registration and a
  shadow DOM dance that complicates htmx swap targets.
- **E** hand-rolled IO works but has well-documented pitfalls: scroll-jump on
  height changes, hard to unit-test (DraftKings Eng), and requires careful
  `contain-intrinsic-size` placeholders to keep CLS stable.
- **F** htmx infinite scroll is dead simple but the htmx docs themselves warn:
  *"might not be suitable for very large datasets"* — DOM grows monotonically.
  Past ~3k rows scroll degrades because every `<tr>` is still painted.
- **G/H** combine the cheap wins. (G) is the no-dep route. (H) buys robustness
  at +2.3 KB.

---

## Recommendation for r1-server-ui-v2

**Primary: Strategy G — CSS `content-visibility` + htmx server-paged chunks +
type-aware aggregation.** Zero JS bundle cost, htmx-native, degrades to a
plain paginated table when JS is off.

**Fallback: Strategy H — drop in Clusterize.js (2.3 KB gz)** behind a feature
flag if real-world telemetry shows scroll FPS < 50 at the p95 session size.

### Why G first

1. The 60 FPS target on 5k rows is reachable with **B+F+aggregation** because
   aggregation alone collapses ~30–60% of typical agent traces (lots of
   adjacent `bus.event` / `tool.partial` rows).
2. Adds **0 KB** to the wire weight; the spec's "vanilla JS islands only"
   rule stays clean.
3. htmx already needs `hx-trigger="revealed"` for the scrubber's lazy
   time-windows, so server pagination is wire-compatible.
4. Falls back to a real `?page=N` link list when JS is disabled — a property
   neither C nor D nor E preserves cleanly.

### Why H is the fallback, not the primary

Clusterize is small but it **owns the scroll container**, which conflicts
with the scrubber's drag-to-time-position UX (§4.4) — Clusterize would have
to be re-initialized on every scrubber jump, and its DOM-recycling makes
"open this exact row by ID" trickier. Keeping it in reserve avoids paying
the integration cost until measurements demand it.

### Performance budget (5k rows, 2020 laptop, Chrome)

| Phase              | Budget   | Strategy G expectation |
|--------------------|----------|------------------------|
| Initial HTML parse | ≤ 150 ms | server returns ~500 rows in first chunk; rest lazy |
| First paint        | ≤ 250 ms | `content-visibility:auto` skips off-screen layout |
| Scroll frame       | 16.6 ms  | only on-screen chunks paint; aggregation cuts row count |
| Filter apply       | ≤ 100 ms | server-side via htmx swap; client just replaces `<tbody>` |

If any cell exceeds budget at p95, switch primary to H.

---

## Aggregation thresholds

Real observability tools (Jaeger, Tempo, Honeycomb) don't publicly document
fixed thresholds — but the heuristics distilled from the search results and
common practice:

**Collapse adjacent rows into a single summary row when ALL of:**

1. **Same node type** (e.g., 5 consecutive `bus.event` of the same `kind`).
2. **Same parent** (`parent_id` identical).
3. **Time gap between consecutive rows < 50 ms** (below human-perceptible
   distinct-event threshold; matches Chrome DevTools' default flame-chart
   "minor frame" collapse).
4. **Run length ≥ 3** (don't collapse 2 rows — the visual saving isn't worth
   the loss of detail).
5. **No row in the run has a non-OK status** (errors must always be visible).

**Collapsed row rendering:**
- Display: `▸ 7× tool.partial in 312 ms` with the same indent as the run.
- Click expands in place (htmx `hx-get="/sessions/{id}/range?from=…&to=…"`).
- Aggregation is computed **server-side** during template render so the
  collapse is part of the initial HTML — no client-side fold/unfold loop.

**Hard caps (always render, never collapse):**
- `task.start`, `task.end`, `mission.*`, `consensus.*`, `error.*`,
  `verify.*`, `merge.*`, `snapshot.*` — these are decision points the user
  is scrolling to find.

**Soft caps (eligible for collapse):**
- `bus.event`, `tool.partial`, `stream.chunk`, `log.line`, `cache.*`,
  `prompt.token`, `model.heartbeat`.

This typically drops a 5k-row trace to 2–3k visible rows with no information
loss for debugging.

---

## Implementation snippets

### 1. Go template — row with `content-visibility`

```html
<!-- templates/waterfall_row.html -->
{{ define "row" }}
<tr class="wf-row" data-node-id="{{ .ID }}" data-depth="{{ .Depth }}"
    style="--depth: {{ .Depth }};">
  <td class="wf-time">{{ .RelMs }}ms</td>
  <td class="wf-kind wf-kind-{{ .Kind }}">{{ .Kind }}</td>
  <td class="wf-label">{{ .Label }}</td>
</tr>
{{ end }}
```

```css
/* static/waterfall.css */
.wf-tbody tr {
  content-visibility: auto;
  contain-intrinsic-size: 0 28px;   /* row height; reserve space, no CLS */
}
.wf-row { padding-left: calc(var(--depth) * 14px); }
```

### 2. htmx lazy chunk loader (server-paged)

```html
<!-- last row of each chunk acts as the sentinel -->
<tr hx-get="/sessions/{{ .SessionID }}/rows?after={{ .LastSeq }}"
    hx-trigger="revealed"
    hx-swap="afterend"
    hx-select="tbody > tr"
    class="wf-sentinel">
  <td colspan="3">Loading more…</td>
</tr>
```

The Go handler returns the next ~500 rows, with the new sentinel embedded in
the last row. DOM grows linearly with how far the user has scrolled, but
`content-visibility:auto` keeps off-screen rows zero-cost during scroll.

### 3. Server-side aggregation (Go)

```go
// ledger/waterfall/aggregate.go (sketch)
func Aggregate(rows []Node) []Row {
    out := make([]Row, 0, len(rows))
    i := 0
    for i < len(rows) {
        run := runLength(rows, i)  // applies the 5 rules above
        if run >= 3 && eligible(rows[i].Kind) {
            out = append(out, summaryRow(rows[i:i+run]))
            i += run
            continue
        }
        out = append(out, Row{Node: rows[i]})
        i++
    }
    return out
}
```

### 4. Optional Clusterize fallback (only if telemetry forces H)

```html
<script src="/static/clusterize.min.js"></script>  <!-- 2.3 KB gz -->
<script>
  new Clusterize({
    rows: window.__rowsHTML,        // pre-rendered <tr> strings from Go
    scrollId: 'wf-scroll',
    contentId: 'wf-tbody',
    rows_in_block: 50,              // ~3 viewports
    blocks_in_cluster: 4,
  });
</script>
```

Note: in Strategy H, the Go template emits `<template>`-wrapped row HTML
strings that Clusterize ingests. htmx is still used for filter/search swaps
that *replace* the row array, then call `clusterize.update(newRows)`.

### 5. Telemetry hook (decide G vs H at runtime)

```js
// static/wf-perf.js  (~400 bytes)
let frames = 0, t0 = performance.now();
const tick = () => {
  frames++;
  if (performance.now() - t0 > 1000) {
    if (frames < 50) navigator.sendBeacon('/perf/wf-fps', String(frames));
    frames = 0; t0 = performance.now();
  }
  requestAnimationFrame(tick);
};
addEventListener('scroll', () => requestAnimationFrame(tick), { once: true });
```

Server logs p95 FPS during scroll; if < 50 across enough sessions, flip the
feature flag to load Clusterize.

---

## Sources

Accessed 2026-05-05.

- [Clusterize.js — official site](https://clusterize.js.org/) — accessed 2026-05-05.
- [NeXTs/Clusterize.js — GitHub repo + README](https://github.com/NeXTs/Clusterize.js/) — accessed 2026-05-05; bundle size 2.3 KB gz, last release v1.0.0.
- [holmberd/virtual-scroller — web component alt to Clusterize](https://github.com/holmberd/virtual-scroller) — accessed 2026-05-05.
- [content-visibility — MDN](https://developer.mozilla.org/en-US/docs/Web/CSS/content-visibility) — accessed 2026-05-05; cross-browser availability since Sep 2024.
- [content-visibility — web.dev (Una Kravets / Vlad Levin)](https://web.dev/articles/content-visibility) — accessed 2026-05-05; the canonical "7× rendering boost" article.
- [Improving rendering performance with CSS content-visibility — Nolan Lawson, 2024-09-18](https://nolanlawson.com/2024/09/18/improving-rendering-performance-with-css-content-visibility/) — accessed 2026-05-05; 20k-DOM-node benchmark, 15% / 45% improvement numbers, "20k is the practical ceiling" guidance.
- [content-visibility on CSS-Tricks Almanac](https://css-tricks.com/almanac/properties/c/content-visibility/) — accessed 2026-05-05; scrollbar/CLS pairing with `contain-intrinsic-size`.
- [DebugBear — Faster Rendering with content-visibility](https://www.debugbear.com/blog/content-visibility-api) — accessed 2026-05-05.
- [12 Days of Web — CSS content-visibility (2024)](https://12daysofweb.dev/2024/css-content-visibility/) — accessed 2026-05-05.
- [Calculating contain-intrinsic-size — Terluin Webdesign](https://www.terluinwebdesign.nl/en/css/calculating-contain-intrinsic-size-for-content-visibility/) — accessed 2026-05-05.
- [DraftKings Engineering — Lazy Rendering with IntersectionObserver](https://medium.com/draftkings-engineering/lazy-rendering-web-uis-with-intersectionobserver-api-bc69a4b61325) — accessed 2026-05-05; pitfalls list (scroll jump, testing, framework integration).
- [IntersectionObserver — MDN](https://developer.mozilla.org/en-US/docs/Web/API/Intersection_Observer_API) — accessed 2026-05-05.
- [htmx — Infinite Scroll example](https://htmx.org/examples/infinite-scroll/) — accessed 2026-05-05; the `hx-trigger="revealed"` sentinel pattern.
- [htmx — hx-trigger attribute reference](https://htmx.org/attributes/hx-trigger/) — accessed 2026-05-05; `revealed` vs `intersect` semantics.
- [htmx — Patterns: Infinite Scroll (htmx 4 docs)](https://four.htmx.org/patterns/infinite-scroll/) — accessed 2026-05-05; the "revealed for document, intersect once for overflow containers" guidance.
- [Hypermedia Systems — More htmx Patterns](https://hypermedia.systems/more-htmx-patterns/) — accessed 2026-05-05.
- [Loading More with Less: Infinite Scrolling in Go and HTMX — Wawandco](https://wawand.co/blog/posts/infinite-scroll-with-go-and-htmx/) — accessed 2026-05-05; Go-specific server pagination wiring.
- [How to Avoid JavaScript for Infinite Scrolling Using HTMX — DEV](https://dev.to/hexshift/how-to-avoid-javascript-for-infinite-scrolling-using-htmx-3e7e) — accessed 2026-05-05; progressive-enhancement pattern, "build on existing pagination."
- [GitHub issue #2102 — htmx — `revealed` + `delay` interactions](https://github.com/bigskysoftware/htmx/issues/2102) — accessed 2026-05-05; known sentinel-edge-case bug.
- [Chrome DevTools Performance reference — flame chart collapse / aggregation](https://developer.chrome.com/docs/devtools/performance/reference) — accessed 2026-05-05; informs the "minor frame" collapse heuristic.
- [Jaeger SPM — span aggregation for RED metrics](https://www.jaegertracing.io/docs/2.dev/architecture/spm/) — accessed 2026-05-05.
- [How to Read OpenTelemetry Trace Waterfalls — OneUptime, 2026-02-06](https://oneuptime.com/blog/post/2026-02-06-read-interpret-opentelemetry-trace-waterfalls/view) — accessed 2026-05-05; "if you see hundreds of similar spans, aggregate" rule of thumb.
- [Last9 — Traces & Spans Observability Basics](https://last9.io/blog/traces-spans-observability-basics/) — accessed 2026-05-05.
