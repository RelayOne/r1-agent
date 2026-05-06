# `cmd/r1-server`

The HTTP daemon that serves the r1 dashboard, SSE event streams, and
the read-only public surfaces (memories, share view, trace bundles).

## Feature flags

The dashboard ships a single htmx + Go-templates surface (the "v2"
surface; the legacy vanilla-JS SPA was deleted by Spec D —
D-UI2-7). The flags below toggle a few orthogonal opt-ins on top
of that always-on baseline.

Strict-equality semantics: every flag here treats only the literal
string `"1"` as on. Anything else — `"true"`, `"yes"`, `"on"`, even
`" 1"` (with leading space) — reads as off. This is deliberate: ops
scripts thinking "true" is on should not accidentally flip a
customer-visible surface. The grep-guard in
`no_direct_env_test.go` enforces that env reads happen only in
`ui_v2_flag.go`.

| Variable | Default | What it controls | Audit semantics |
|---|---|---|---|
| `R1_SERVER_SHARE_ENABLED` | unset | Required for `/share/{hash}` to render the read-only snapshot view. Off → 404. | Operator opt-in for the read-only public-link surface. Audit log records who flipped it on. |
| `R1_SERVER_TRACE_STUB` | unset | When on, the trace waterfall fabricates demo spans for sessions with zero events. Ops/development only — never in prod. | Guarded by `traceStubEnabled()` in `trace.go`. |
| `R1_MEMORIES_PASSPHRASE` | unset | When set, memory writes whose scope is `"always"` require the same passphrase supplied via the JSON body. Off → no passphrase required. | Anti-foot-gun for the global-scope memory-bus tier. Legacy alias `STOKE_MEMORIES_PASSPHRASE` still honoured. |

The Go-side struct that reads these is `V2Config` in `ui_v2_flag.go`;
every consumer should call `LoadV2Config()` rather than `os.Getenv`
directly. `Renderable()` (always true post-Spec-D) and
`CanServeShare()` are the predicate helpers most handlers want.

(The previous `R1_SERVER_UI_V2` umbrella toggle was removed in Spec
D — D-UI2-7. The v2 surface had been opt-in for two release cycles
per spec §2.3; once the legacy SPA was deleted there was no
fallback to flip back to. `LoadV2Config().Enabled` is now hardcoded
true; existing callsites of `v2Enabled()` / `traceV2Enabled()`
compile unchanged but are dead-fallback-only.)

## Routes (v2 surface)

| Method | Path | Handler | Notes |
|---|---|---|---|
| GET  | `/` | `serveHTMLIndex` | v2 dashboard index |
| GET  | `/session/{id}` | `serveTraceWaterfall` | Waterfall view, htmx server-paged via `hx-trigger="revealed"` |
| GET  | `/session/{id}/tree` | `serveTraceTree` | Tree view of the same session |
| GET  | `/session/{id}/graph` | `serveSessionGraph` | 3D ledger graph (InstancedMesh + WebWorker) |
| GET  | `/session/{id}/stream` | `serveStreamView` | Raw SSE event stream view |
| GET  | `/memories` | `serveMemories` | Grouped memory explorer |
| POST | `/api/memories` | `serveMemoryCreate` | Create memory (passphrase if scope=always) |
| PUT  | `/api/memories/{id}` | `serveMemoryUpdate` | Update memory |
| DELETE | `/api/memories/{id}` | `serveMemoryDelete` | Delete memory |
| GET  | `/diff/{a}/{b}` | `serveDiff` | Run-diff side-by-side |
| GET  | `/api/session/{id}/export.tracebundle` | `serveTracebundle` | tar.gz export |
| GET  | `/share/{hash}` | `serveShare` | Read-only snapshot — requires `R1_SERVER_SHARE_ENABLED=1` |
| GET  | `/settings` | `serveSettings` | Read-only config viewer |
| GET  | `/ui/...` | `http.FileServer(uiFS)` | Static assets (templates, css, js, vendor) |

## Building

```bash
go build ./cmd/r1-server
go test ./cmd/r1-server/...
```

## Vendor blobs

Frontend dependencies (htmx, three.js, d3-force-3d, ...) are
pinned, content-addressed copies under
[`ui/vendor/`](ui/vendor/README.md). Bumping a version is a
4-line review-and-commit (script URL + SRI + sri_test count + this
README); CI runs `scripts/vendor-ui.sh --check` to verify the
on-disk blobs match the SRI table without network access.
