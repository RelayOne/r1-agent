# build-plan.md — r1-server-ui-v2-3d-perf (Spec 2 of 5)

**Spec:** specs/r1-server-ui-v2-3d-perf.md (BUILD_ORDER 29)
**Mode:** FEATURE
**Branch:** build/r1-server-ui-v2-3d-perf
**Started:** 2026-05-05
**DEPENDS_ON:** r1-server-ui-v2-foundation (shipped on build/r1-server-ui-v2-foundation, PR #154)

11 tasks per spec §7. Each is one task = one commit.

## Tasks

- [x] T1 — `web/js/graph-worker.js`: 9-message protocol, transferable ArrayBuffer, d3-force-3d simulation
- [x] T2 — SharedArrayBuffer fallback in worker + COOP/COEP headers in `ui_v2_foundation.go`
- [x] T3 — `web/js/graph.js`: InstancedMesh per shape (11 shapes), 8192 cap, side-table `instances[i]=nodeId`
- [x] T4 — raycaster picking in graph.js: `pointermove` → `intersectObject` → `instanceId` → htmx side-panel swap
- [x] T5 — label layer pool of 64 sprites in graph.js (frustum-cull + selected-only)
- [x] T6 — `web/js/scrubber.js`: vanilla island bound to `#timeline-scrubber`, posts visibility deltas
- [x] T7 — focused-subtree view in graph.js: shift-click BFS 1-3 hops, camera animate, fade non-focused
- [x] T8 — streaming insert: SSE `ledger.node.append` → worker `{kind:'add',node,neighbors}` at α(0.3)
- [x] T9 — `serveGraphIndex` v2 path + `web/session-graph.html` template
- [x] T10 — `cmd/r1-server/ui/web/js/graph-worker.test.js` (vitest, jsdom, 50-node fixture)
- [x] T11 — `cmd/r1-server/graph_e2e_test.go` (Playwright + chromium, `//go:build e2e`, 3k-node fixture, mean FPS ≥ 30)
