# build-plan.md — r1-server-ui-v2-handlers-and-routes (Spec 4 of 5)

**Branch:** build/r1-server-ui-v2-handlers-and-routes (off Spec 1's tip)
**Started:** 2026-05-05

22 items per spec §10. Where multiple very-small tasks share a file
(e.g. tracebundle headers + handler), they're combined into one
commit to avoid trivial diffs. Spec 5's tests will exercise the full
HTTP surface end-to-end.

## Tasks

- [x] T1  ui_v2_flag.go — V2Config + LoadV2Config + tests
- [x] T2  migrate v2Enabled() callsites + grep guard test
- [x] T3  cmd/r1-server/README.md feature-flag table
- [x] T4  ui/web/index.html (instance list)
- [x] T5  ui/web/session.html (waterfall + side panel + scrubber)
- [x] T6  ui/web/session-stream.html + serveStreamView handler
- [x] T7  ui/web/memories.html + extend serveMemories
- [x] T8  ui/web/share.html + serveShare template + R1_SERVER_SHARE_ENABLED
- [x] T9  ui/web/diff.html + migrate serveDiff to template
- [x] T10 partials/memory-side-panel.html + serveMemorySidePanel
- [x] T11 memory graph view (extend serveGraphIndex with memory_id)
- [x] T12 partials/filter-chips.html + CSS
- [x] T13 share R1_SERVER_SHARE_ENABLED gate test
- [x] T14 share banner source-order test
- [x] T15 tracebundle.go — handler + tar.gz streaming
- [x] T16 tracebundle_test.go — round-trip
- [x] T17 tracebundle headers (Cache-Control + Content-Disposition)
- [x] T18 sse.go — last_event_id query fallback
- [x] T19 sse.go — event:resync frame on pruned cursor
- [x] T20 ui/web/js/htmx-sse-shim.js
- [x] T21 partials/instance-row.html + memory-card.html + memory-group.html (referenced by T4 + T7)
- [x] T22 partials/node-side-panel-empty.html + memory-side-panel-empty.html (referenced by T5 + T7)
