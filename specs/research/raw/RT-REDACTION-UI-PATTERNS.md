# RT-REDACTION-UI-PATTERNS

## Topic

How should the r1-server-ui-v2 ledger waterfall and 3D graph render nodes
whose content has been redacted by a signed `redaction` ledger node? We need
patterns from established observability/trace tooling for: glyph choice,
color treatment, hover/audit affordance, side-panel placeholder copy, and
accessible announcement to screen readers. Spec section
"Redacted-node rendering" enumerates 6 deliverables (helper `isRedacted`,
waterfall lock slot, side-panel placeholder, `redaction-events.html`
partial backed by `ledger.Store.RedactionsFor(nodeID)`, and 3D graph
desaturate + lock sprite). This file collects external prior art and
distills a concrete recommendation.

## How established tools handle it

### Datadog APM (Sensitive Data Scanner)
Datadog redacts sensitive matches **server-side, before indexing**, so the
span is still visible in the waterfall but the offending attribute value
is replaced. The "Redact" action substitutes a placeholder; "Partially
redact" preserves length/shape (e.g. last 4 of a card number); a separate
"Mask" action exists for logs only. There is **no global lock badge on
the span itself** — the redaction shows as in-place value substitution.
A `Data Scanner Unmask` permission lets privileged viewers de-obfuscate.
Audit/lineage is delivered through the rule that produced the match
(rule name + event timestamp), not via an inline UI affordance on the
span. Color/iconography is intentionally muted — the rendered string
itself ("\*\*\*\*-\*\*\*\*-\*\*\*\*-1234") is the entire signal.
[Datadog Data Security](https://docs.datadoghq.com/tracing/configure_data_security/)
| [Sensitive Data Scanner](https://docs.datadoghq.com/security/sensitive_data_scanner/)

### Honeycomb
Honeycomb's published patterns mostly cover **dropped** rather than
**scrubbed** spans: when a trace is partial because of rate limits or
sampling, the trace view surfaces a **warning banner with a link to
troubleshooting docs** and a "Reload Trace" button. The span sidebar
("trace sidebar") is the canonical place auxiliary information about a
span lives — fields, span events, links — and would be the natural slot
for a redaction reason. Honeycomb does not appear to ship a first-class
"this attribute was scrubbed" glyph; its philosophy is
"missing-on-purpose data is rare; missing data is a bug to fix."
[Explore Traces](https://docs.honeycomb.io/investigate/analyze/explore-traces/)
| [Trace troubleshooting](https://intercom.help/honeycomb/en/articles/2130084-traces-appear-incomplete-or-aren-t-showing-up-in-ui)

### Sentry (data scrubbing + Session Replay)
Sentry is the most directly comparable. Scrubbed fields are replaced
with the **literal string `[Filtered]`** in the event detail UI. A
**tooltip on the filtered field** explains *why* it was stripped, e.g.
"This data was scrubbed since it included content that looked like a
credit card number" or "...the string 'password'." Every event also
exposes a **`JSON` link** so users can see exactly what the scrubber
saw — important because UI field names and JSON keys can differ. In
Session Replay specifically, **redacted text nodes render as asterisks**
and **redacted images render as static gray boxes** — a deliberately
flat, low-information placeholder that signals "something was here" but
not its shape. There is an open Sentry feature request (#3050) asking
for richer "data scrubbing reasons" on the event detail page, which
matches what r1-server-ui-v2 already plans to ship via a redaction-
events list.
[Why am I seeing \[Filtered\]](https://sentry.zendesk.com/hc/en-us/articles/24501815773595-Why-am-I-seeing-Filtered-in-my-event-data)
| [Server-side scrubbing](https://docs.sentry.io/security-legal-pii/scrubbing/server-side-scrubbing/)
| [Session Replay privacy](https://docs.sentry.io/security-legal-pii/scrubbing/protecting-user-privacy/)
| [Issue #3050: scrubbing reasons](https://github.com/getsentry/sentry/issues/3050)

### Jaeger / Zipkin
Neither Jaeger nor Zipkin ships a native redaction UI. The community
pattern is to **scrub at the OpenTelemetry Collector** (attribute
processor with `delete` / `hash` actions) **before** spans reach the
backend. Net effect on the UI: the attribute is simply absent from the
tag list — there is no glyph, badge, hover, or audit trail in the trace
viewer. This is the *worst* of the four for our use case: it leaks
nothing, but also tells the operator nothing about *why* a tag is
missing.
[Zipkin](https://zipkin.io/) | [Jaeger vs Zipkin (SigNoz)](https://signoz.io/blog/jaeger-vs-zipkin/)

### Synthesis
Among shipped products, **Sentry's `[Filtered]` + tooltip-with-reason +
JSON-link pattern is the closest match** to what the spec calls for.
Datadog contributes the idea of in-place shape preservation
(partial redaction). Honeycomb contributes the trace-sidebar slot for
auxiliary metadata. Jaeger/Zipkin demonstrate the failure mode to
avoid: silent absence.

## Recommendation for r1-server-ui-v2

### Glyph + color
- **Glyph: lock (U+1F512 🔒) rendered as an SVG, not the emoji**, so we
  control color, stroke weight, and accessibility name. Emoji rendering
  varies wildly across platforms and screen readers tend to announce the
  full Unicode name ("locked").
- **Color: desaturated neutral** (one step lighter than the row's normal
  fill; e.g. `--color-redacted: #9aa4b2` on dark theme,
  `#6b7280` on light). Avoid red/yellow — those are reserved for
  *failure* and *warning* status in the existing waterfall, and
  redaction is neither. Avoid `text-decoration: line-through` on the
  span title; strikethrough connotes deletion/cancellation, not
  intentional concealment, and is poorly handled by some screen readers.
- **Reserved warning glyph (⚠) only when redaction is itself anomalous**
  — e.g. a redaction was applied *without* a matching policy node, or
  the signing key is unverified. Visual hierarchy: 🔒 = expected,
  🔒 + ⚠ overlay = "redacted but the redaction record is suspicious."

### Waterfall row
- Replace the node's normal title/preview slot with the lock glyph
  followed by **`[content redacted]`** (Sentry's `[Filtered]` is the
  precedent; we use `redacted` because it matches our ledger node type
  name, which reduces operator cognitive load).
- Keep the row's **time bar at full opacity and original color** —
  redaction conceals payload, not timing. The waterfall's primary job
  is showing temporal/causal structure; that data is not sensitive and
  should remain legible.
- **Do not collapse or hide redacted rows by default.** Transparent
  absence beats silent absence. Operators must be able to count and
  locate redactions even if they can't read them.

### Side panel
Layout, top to bottom:
1. **Header**: node ID (always visible — content-addressed IDs are
   non-sensitive), node type, and a `🔒 Redacted` pill.
2. **Placeholder block**: a single line `[content redacted]` in
   monospace inside a flat gray box. Mirrors Sentry Session Replay's
   "static gray box" pattern for redacted images.
3. **Redaction events list** (rendered from
   `partials/redaction-events.html` driven by
   `ledger.Store.RedactionsFor(nodeID)`):
   - One row per redaction node referencing this nodeID.
   - Columns: timestamp, reason (free-text from the redaction node, e.g.
     `"retention-policy-90d"`, `"PII-scan:email"`,
     `"operator-request:ticket-1234"`), signer (key fingerprint, short),
     and a link to the redaction node itself in the ledger explorer.
   - If `len(events) == 0` but `isRedacted(n)` returned true, render a
     `⚠ redaction record missing` row — this is the "redaction without
     policy" anomaly above.
4. **JSON link** at the bottom: open the raw ledger node JSON. The
   payload field will contain the placeholder hash, not the original
   content. This mirrors Sentry's "JSON" link affordance for letting
   operators see exactly what the scrubber stored.

### Reason-badge wording
Prefer **specific cause over generic verb**:
- Good: `"redacted by retention policy (90d)"`,
  `"redacted: PII scan matched email"`,
  `"redacted: operator request"`.
- Acceptable fallback when the redaction node carries no reason:
  `"redacted"`.
- Avoid: `"removed"`, `"deleted"`, `"hidden"` — each implies a
  different ledger semantics (deletion would violate append-only).
  Always use `"redacted"` so the word is consistent across waterfall,
  side panel, and 3D graph hover label.

### 3D graph
- **Desaturate the node's material**: drop saturation to ~15% and
  multiply value/lightness by 0.7. This keeps the node *findable*
  (still occupies its geometric position) but visually recedes.
- **Lock sprite**: a small flat SVG-as-texture sprite (always
  camera-facing) attached to the node. Sprite scale should be ~0.6×
  the node's radius so it reads as a badge, not a replacement.
- **Hover label** (the WebGL tooltip): `Node {id8} — redacted`. On
  click, the existing side-panel mechanism opens with the layout above.
- **Edge handling**: edges *to/from* a redacted node remain at normal
  opacity. Redaction conceals payload, not topology — and topology is
  often the most operationally useful signal ("which loop is this stuck
  in?").

### Accessibility
- Wrap the lock glyph in `<span aria-hidden="true">` and provide the
  meaning via adjacent visible text (`[content redacted]`) or, when
  the glyph is the only label (e.g. waterfall icon column), use
  `aria-label="redacted"` on the wrapping element. Do **not** rely on
  the screen reader pronouncing the U+1F512 emoji — readers vary
  ("locked", "lock", or silent depending on engine + verbosity).
- The redaction-events list should be a real `<ul>`/`<li>` list (not
  div soup) so screen-reader users hear "list, 3 items, redacted by
  retention policy 90 days, ...".
- The side-panel header pill should use `role="status"` only if it
  appears asynchronously (i.e. arrives after panel paint); otherwise
  plain text is sufficient and quieter.
- Ensure desaturated 3D-graph and waterfall colors still meet WCAG AA
  contrast against their backgrounds — desaturation often pushes
  contrast below 4.5:1. Test both light and dark themes.

## Implementation patterns

### Helper

```go
// ledger/redaction.go
//
// IsRedacted reports whether any signed redaction node references nodeID.
// A redaction node is a regular ledger node of type "redaction" whose
// payload includes a "target" field equal to the queried nodeID.
//
// This is a pure read — no caching here; callers (template render,
// 3D-graph projector) should batch via RedactionMap when iterating.
func IsRedacted(s Store, nodeID ContentID) bool {
    events, err := s.RedactionsFor(nodeID)
    if err != nil {
        return false // fail open visually; renderer flags missing-record case
    }
    return len(events) > 0
}

// RedactionMap returns id → bool for a slice of node IDs in one query.
// Used by the waterfall renderer to avoid N+1 ledger reads.
func RedactionMap(s Store, ids []ContentID) map[ContentID]bool {
    out := make(map[ContentID]bool, len(ids))
    // Implementation: single SQL `WHERE target IN (...)` against the
    // edges/nodes index, group by target.
    ...
    return out
}
```

### Waterfall template branch (Go html/template)

```html
{{/* partials/waterfall-row.html */}}
<li class="waterfall-row {{if .Redacted}}is-redacted{{end}}"
    data-node-id="{{.ID}}">
  <span class="row-bar"
        style="--start:{{.StartPct}}%;--width:{{.WidthPct}}%;"></span>

  {{if .Redacted}}
    <span class="row-label" aria-label="redacted">
      <svg class="icon-lock" aria-hidden="true" focusable="false">
        <use href="#icon-lock"/>
      </svg>
      <span class="redacted-placeholder">[content redacted]</span>
    </span>
  {{else}}
    <span class="row-label">{{.Title}}</span>
  {{end}}
</li>
```

### Side-panel partial

```html
{{/* partials/redaction-events.html */}}
{{if .Events}}
  <section aria-labelledby="redaction-events-h">
    <h3 id="redaction-events-h">Redaction events</h3>
    <ul class="redaction-events">
      {{range .Events}}
        <li>
          <time datetime="{{.At.Format "2006-01-02T15:04:05Z07:00"}}">
            {{.At.Format "2006-01-02 15:04:05 MST"}}
          </time>
          <span class="reason">{{.Reason}}</span>
          <span class="signer" title="{{.SignerKey}}">
            {{.SignerShort}}
          </span>
          <a href="/ledger/{{.RedactionNodeID}}">view record</a>
        </li>
      {{end}}
    </ul>
  </section>
{{else}}
  <p class="warn">
    <svg class="icon-warn" aria-hidden="true"><use href="#icon-warn"/></svg>
    Redaction record missing for this node.
  </p>
{{end}}
```

### 3D graph projector (pseudocode)

```ts
// when building per-node materials
for (const n of nodes) {
  if (redactionMap.get(n.id)) {
    n.material = baseMaterial.clone();
    n.material.color.setHSL(h, s * 0.15, l * 0.7);   // desaturate
    n.userData.redacted = true;
    n.add(new LockSprite({ scale: n.radius * 0.6 }));  // billboard sprite
  }
}

// hover handler
function labelFor(n) {
  return n.userData.redacted
    ? `Node ${n.id.slice(0,8)} — redacted`
    : `Node ${n.id.slice(0,8)} — ${n.type}`;
}
```

### Tests to add
- `IsRedacted` returns false when store has no redaction edges for the id.
- `IsRedacted` returns true when at least one signed redaction node targets the id.
- `RedactionMap` is single-query (assert via sqlmock query count) for N ids.
- Template snapshot: redacted row renders `[content redacted]` text and a
  lock SVG; non-redacted row renders the original title.
- Template snapshot: side-panel shows the `⚠ Redaction record missing`
  branch when `IsRedacted=true` but `RedactionsFor` returned `[]`.
- Accessibility: rendered HTML passes axe-core checks (no redacted-row
  warnings about contrast or missing labels).

## Sources

Accessed 2026-05-05.

- [Datadog: Tracing Data Security](https://docs.datadoghq.com/tracing/configure_data_security/)
- [Datadog: Sensitive Data Scanner](https://docs.datadoghq.com/security/sensitive_data_scanner/)
- [Datadog: Sensitive Data Scanner — Telemetry](https://docs.datadoghq.com/security/sensitive_data_scanner/setup/telemetry_data/)
- [Datadog blog: Identify and redact sensitive data](https://www.datadoghq.com/blog/identify-sensitive-data-leakage-in-apm-rum-with-sensitive-data-scanner/)
- [Honeycomb: Explore Traces](https://docs.honeycomb.io/investigate/analyze/explore-traces/)
- [Honeycomb: Traces appear incomplete](https://intercom.help/honeycomb/en/articles/2130084-traces-appear-incomplete-or-aren-t-showing-up-in-ui)
- [Honeycomb: Interpreting the Trace View](https://www.honeycomb.io/resources/intro-to-o11y-topic-7-interpreting-honeycombs-trace-view)
- [Sentry: Why am I seeing \[Filtered\]?](https://sentry.zendesk.com/hc/en-us/articles/24501815773595-Why-am-I-seeing-Filtered-in-my-event-data)
- [Sentry: Server-side scrubbing](https://docs.sentry.io/security-legal-pii/scrubbing/server-side-scrubbing/)
- [Sentry: Advanced data scrubbing](https://docs.sentry.io/security-legal-pii/scrubbing/advanced-datascrubbing/)
- [Sentry: Session Replay privacy](https://docs.sentry.io/security-legal-pii/scrubbing/protecting-user-privacy/)
- [Sentry develop: PII and Data Scrubbing](https://develop.sentry.dev/backend/application-domains/pii/)
- [Sentry issue #3050: data scrubbing reasons in event detail](https://github.com/getsentry/sentry/issues/3050)
- [Zipkin](https://zipkin.io/)
- [SigNoz: Jaeger vs Zipkin](https://signoz.io/blog/jaeger-vs-zipkin/)
- [MDN: aria-hidden](https://developer.mozilla.org/en-US/docs/Web/Accessibility/ARIA/Reference/Attributes/aria-hidden)
- [MDN: ARIA status role](https://developer.mozilla.org/en-US/docs/Web/Accessibility/ARIA/Reference/Roles/status_role)
- [Level Access: ARIA labels and accessible names](https://www.levelaccess.com/blog/aria-labels-and-accessible-names-a-developers-guide/)
- [Orange a11y: Accessible hiding and aria-hidden](https://a11y-guidelines.orange.com/en/articles/accessible-hiding/)
- [NN/g: Indicators, Validations, and Notifications](https://www.nngroup.com/articles/indicators-validations-notifications/)
- [NN/g: Visual Treatments that Improve Accessibility](https://www.nngroup.com/videos/visual-treatments-accessibility/)
- [Carbon Design System: Status indicator pattern](https://carbondesignsystem.com/patterns/status-indicator-pattern/)
