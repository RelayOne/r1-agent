# build-plan.md — r1-server-ui-v2-event-rendering (Spec 3 of 5)

**Branch:** build/r1-server-ui-v2-event-rendering (off Spec 1's tip)
**Started:** 2026-05-05

12 tasks. NOTE on cross-spec coupling: T9/T10 reference `web/js/graph.js`
which lives on Spec 2's branch. We ship them as a standalone
`web/js/graph-layers.js` module here so this branch can build atop
Spec 1 only. Spec 2's renderer wires them via `window.__GRAPH_RENDERER__`.

NOTE on ledger surface: `Store.RedactionsFor(id) []Redaction` doesn't
exist; the actual surface is `Store.IsRedacted(id) (bool, error)` and
the per-event `RedactionRecord` returned by `Store.Redact()` is not
persisted as a queryable history. T1 ships what's possible against the
real surface and documents the per-node redaction-event log as future
work.

- [x] T1  redaction.go — RedactionMap + LoadRedactionMap + IsRedacted/Events
- [x] T2  skills.go — SkillEventMap + LoadSkillEventMap + IsActiveAt
- [x] T3  skill_loaded emission in internal/hub/builtin/skill_injector.go
- [x] T4  skill_unloaded emission in internal/microcompact/compact.go
- [x] T5  scope-exit emission for SkillUnloaded
- [x] T6  waterfall-row.html partial + icon-lock svg
- [x] T7  node-side-panel.html + redaction-events.html + skill-loaded-detail.html + skill-unloaded-detail.html
- [x] T8  redaction CSS in web/css/base.css
- [x] T9  graph-layers.js applyRedactionLayer
- [x] T10 graph-layers.js applySkillLayer
- [x] T11 integration test: redacted node renders lock + side panel placeholder
- [x] T12 integration test: skill load/unload cycle renders 🧬 + side panel events
