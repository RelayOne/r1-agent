<!-- STATUS: done -->
<!-- BUILD_COMPLETED: 2026-04-22 -->
<!-- CREATED: 2026-04-21 -->
<!-- DEPENDS_ON: memory-bus (emits memory_stored/recalled nodes), ledger-redaction (redacted-node rendering), retention-policies (settings page HTTP handlers) -->
<!-- BUILD_ORDER: 27 -->

# r1-server UI v2 retrofit

## 1. Overview

The MVP `r1-server` UI (`cmd/r1-server/ui/index.html` + `app.js` + `style.css` + `graph.html/js/css`) ships as a vanilla-JS SPA with a polling refresher and a dedicated 3D ledger visualizer powered by Three.js + `3d-force-graph` pulled from unpkg. Deep research against real observability workflows flags four architectural mistakes in that shape: (4) per-node `THREE.Mesh` allocation collapses past ~1000 nodes; (5) the CDN-fetched script-tag approach breaks offline use and fights sensible caching; (6) a 3D graph is the wrong *default* view — every user who comes from Honeycomb, Datadog, Jaeger, Sentry, or LangSmith expects a waterfall + indented-tree; (7) a memory/knowledge explorer needs a grouped-list default, not a graph.

v2 rearchitects the UI around three grounded ideas: an **htmx + SSE shell** (server-rendered templates, `hx-*` attributes for partial swaps, SSE for live updates, zero CDN) serving chrome at human cadence; a **waterfall + indented tree** as the default session view with a side panel, timeline scrubber, filter, and text search; and **vanilla-JS islands** only where interactivity actually requires JS (the 3D graph, the stream tail, the scrubber). The 3D graph is retained as a secondary tab and rebuilt around `THREE.InstancedMesh` + a Web-Worker-hosted `d3-force-3d` simulation + aggregation-based time scrubbing so it scales to ~3000 nodes. New features layer on: a memory explorer at `/memories` (grouped list default per correction 7), skill load/unload ledger emission and visualization (RS-8), redacted-node rendering honoring the ledger-redaction spec, cryptographic verification badges (`✔/⚠/✗`), content-addressed share links, `.tracebundle` export, and a minimum-viable run diff. The whole thing ships behind `R1_SERVER_UI_V2=1` for two weeks, then becomes default; the MVP stays one more release before removal.

## 2. Stack migration (correction 5)

### 2.1 Stack choices

- **htmx 2.x** for chrome: instance list, session panels, memory explorer, settings. All partial swaps via `hx-get` / `hx-post` with `hx-target` / `hx-swap="outerHTML"`. No client-side router, no virtual DOM, no build pipeline.
- **Go `html/template`** for every rendered page and partial. A page is the full document (extends `base.html`); a partial is a block matching an `hx-target`.
- **SSE (`text/event-stream`)** for live updates (`/api/session/:id/live`). One-way, server-authored, human cadence. Auto-reconnect with `retry: 2000` + `Last-Event-ID` resume. **No WebSocket** — the traffic is strictly server-to-client.
- **Vanilla JS islands + ES module import maps** for the 3D graph, the SSE stream tail, and the timeline scrubber. Each island is a single `<script type="module">` hanging off a marked container (`data-island="graph"`, etc.).
- **`embed.FS` vendored assets**: Three.js (`three.module.js`), `three/addons/controls/OrbitControls.js`, `3d-force-graph.js`, `three-spritetext.js`, `htmx.min.js`, `htmx-ext-sse.js` all shipped inside the r1-server binary. Budget: ≤250 KB gzipped total.
- **No CDN at all** — the UI works air-gapped. A vendor step (one-time script) pulls the pinned versions into `cmd/r1-server/ui/web/vendor/` at build-tooling time, never at runtime.

### 2.2 New file layout under `cmd/r1-server/ui/`

```
web/
  base.html                  # htmx layout with SSE hookup + import map
  index.html                 # instance list (replaces SPA index)
  session.html               # waterfall+tree default session view
  session-graph.html         # 3D graph (secondary, behind Views tab)
  session-stream.html        # raw event stream
  memories.html              # grouped-list memory explorer
  settings.html              # retention settings (from retention-policies spec)
  share.html                 # read-only session snapshot
  diff.html                  # run diff view
  partials/
    instance-row.html        # one row in instance list (hx-swap target)
    waterfall-node.html      # one tree row + expand/collapse
    node-side-panel.html     # side panel body (full JSON or [redacted])
    memory-card.html         # one grouped-list card
    memory-group.html        # one group heading + cards
    redaction-events.html    # redaction audit list fragment
    sse-event.html           # event rendered as an htmx-sse target
  vendor/
    three.module.js
    three/addons/controls/OrbitControls.js
    3d-force-graph.js
    three-spritetext.js
    htmx.min.js
    htmx-ext-sse.js
  css/
    style.css                # shared shell + waterfall/tree styling
    graph.css                # 3D viewport
    memories.css
    settings.css
  js/
    graph.js                 # 3D island entry
    graph-worker.js          # d3-force-3d layout in a Web Worker
    stream.js                # SSE stream island
    scrubber.js              # timeline scrubber island
```

### 2.3 Migration path (one release cycle of parallel code)

- Existing files in `cmd/r1-server/ui/` (`index.html`, `app.js`, `style.css`, `graph.html`, `graph.js`, `graph.css`) stay in place, unchanged, as the MVP.
- New templates live under `cmd/r1-server/ui/web/**`.
- `mountUI` inspects `R1_SERVER_UI_V2` env var:
  - unset (default for first two weeks) → existing `uiFS` serves unchanged (`ui/index.html` + `ui/app.js` + `ui/graph.*`); v2 routes `/memories`, `/settings/retention`, `/share/:hash`, `/diff/:a/:b` 404.
  - `R1_SERVER_UI_V2=1` → new `webFS = mustSubFS(embeddedUI, "ui/web")`; v2 serves `session.html` at `/session/:id`, 3D moves to `/session/:id/graph` (preserves the MVP path), memories at `/memories`, settings at `/settings/retention`.
- After two weeks of flagged use, default flips (the unset case serves v2); MVP files stay for one more release then are deleted.

### 2.4 htmx layout (`base.html`) at a glance

- `<html>` has an `<meta name="htmx-config" content='{"defaultSwapStyle":"outerHTML"}'>`.
- `<head>` contains a JSON import map mapping `three`, `three/addons/`, `3d-force-graph`, `three-spritetext` to the corresponding `/ui/vendor/...` URLs.
- `<script src="/ui/vendor/htmx.min.js" defer>` and `<script src="/ui/vendor/htmx-ext-sse.js" defer>`.
- A top nav with the toggle bar `[Waterfall] [3D Graph] [Stream] [Memories]` (wired as normal `<a>` tags with `hx-boost="true"`).
- A `<main hx-ext="sse">` container; nested partials opt in with `sse-connect="/api/session/{{.ID}}/live"` + `sse-swap="event"` on the elements that should live-update.

## 3. Correction 4 — 3D graph performance

### 3.1 InstancedMesh refactor

- One `THREE.BufferGeometry` per node *shape* (16 shapes today: sphere, cube, diamond, octahedron, cone, icosahedron, cylinder, plane, torus, hex_prism, ring — plus variants). For each, one `THREE.InstancedMesh(geometry, material, MAX_INSTANCES=8192)` sharing a single `MeshLambertMaterial` per color bucket.
- `matrix4` per node baked from `{x,y,z, scale, rotation}`; on simulation tick the worker posts a Float32Array of positions, main thread writes into `InstancedMesh.instanceMatrix` and flips `.needsUpdate = true`.
- Per-instance color via `InstancedMesh.setColorAt`; pass/fail verification variant, redacted desaturation, loaded/unloaded skill opacity all expressed as color-at-index + `instanceMatrix` scale.
- Labels (three-spritetext) only for nodes within camera-near-frustum OR selected — never all nodes at once.

### 3.2 Web Worker layout

- `web/js/graph-worker.js`: imports `d3-force-3d` directly from `/ui/vendor/`. Receives `{nodes, edges}`, runs the simulation to cooled (`alpha() < 0.02`), posts the final `positions: Float32Array` (length `3*nodes.length`) and `links` with indices. Main thread reads into the `InstancedMesh`.
- Main thread posts layout requests; worker posts progress ticks every N iterations so the UI can show a fill bar.
- On streaming insert: main thread sends `{kind:'add', node, neighbors}` to the worker; worker inserts at mean-of-neighbors position, restarts sim at `alpha(0.3)` (never `alpha(1).restart()`).

### 3.3 Time scrubber — aggregation, not re-simulation

- **Layout once, freeze positions**. Positions on disk once simulation converges.
- Slider cursor = timestamp; set each instance's scale to `0` if `created_at > cursor` else its styled size. Instances stay at their frozen `(x,y,z)`; only visibility (scale/alpha) changes.
- Redacted-at transitions: cursor ≥ `redacted_at` desaturates color + raises lock sprite.
- Skill load/unload: cursor between `skill_loaded` and subsequent `skill_unloaded` → opacity 1.0; after `skill_unloaded` → opacity 0.3.
- This replaces the MVP's "filter node set + feed to 3d-force-graph which re-simulates" pattern. No re-simulation during scrub; O(N) visibility update per frame.

### 3.4 Focused subtree view

- Clicking a node with shift-held (or via a "Focus subtree" side-panel button) enters focus mode: BFS 1–3 hops from the selected node, full `InstancedMesh` interactivity, camera zooms in, non-focused nodes reduced to 0.15 opacity.
- Implements the Obsidian "local graph vs. global graph" split. A top toolbar button re-enters global mode.

## 4. Correction 6 — Waterfall + tree default

### 4.1 Route and template

- `GET /session/:id` (v2 flag on) → renders `session.html` with:
  - Top bar: `[Waterfall ✓] [3D Graph] [Stream] [Memories]` toggle (current tab highlighted).
  - Left pane: hierarchical tree (indented list with ▸/▾ chevrons).
  - Right pane: side panel (initially empty — "select a node").
  - Footer: filter-by-type selector, text search input, timeline scrubber.
- Tree is rendered server-side from `ledger.Store.List` grouped by mission/task parent-child edge (same edges the MVP graph uses). Partials stream in as the ledger grows via SSE (`sse-swap="waterfall-node"`).

### 4.2 Row rendering (per node)

Each row shows, inline:

| Slot | Content |
|---|---|
| Indent | `▸` / `▾` chevron (collapses descendants) |
| Icon | emoji by type (🧩 task, 🗳 decision, ✅ verify-pass, ❌ verify-fail, 🙋 hitl, ⚖ judge, 🧬 skill, 📖 research, 🧠 memory, …) |
| Title | `node.name` or first `raw.summary`, truncated to 80 chars |
| Duration | ms/s between `created_at` and child terminal (if task) |
| Cost | `cost_usd` if present (from stoke cost bus events) |
| Status badge | `✔ verified` (green) / `⚠ unsigned` (amber) / `✗ tampered` (red) — from `ledger.Store.Verify` hash check |
| Redacted | 🔒 if content tier wiped (see §9) |

### 4.3 Side panel (partial `partials/node-side-panel.html`)

- Loaded by `hx-get="/api/session/:id/node/:node_id"` triggered on row click, swapping the right pane.
- Shows the structural header (id, type, created_at, created_by, mission, parent_hash, edges in/out) always.
- Content area: full `raw` JSON pretty-printed — or `[content redacted]` placeholder if the content tier was wiped, followed by the `Redaction events` list (see §9).
- If node type is `agent_io` (LLM I/O), render each prompt + response as a chat-style message bubble (one per turn), not as raw JSON.

### 4.4 Filter + text search + scrubber

- Filter: `<select multiple>` of observed types; change fires `hx-get="/session/:id/waterfall?types=…"` swapping the tree.
- Text search: debounced `hx-get` with `hx-include="[name='types']"` so filter + search compose.
- Scrubber: a range input island (`web/js/scrubber.js`) that sets a `data-cursor` attribute on each row based on `created_at`; CSS hides post-cursor rows via `tr[data-cursor="future"] { display:none }`. No server round-trip during scrub.

## 5. Differentiator features

### 5.1 Cryptographic verification UI

- Every node row shows `✔ / ⚠ / ✗` based on a call to `ledger.Store.Verify(node.id)` (hash re-compute vs. stored `parent_hash`).
- Hover shows the computed hash, stored hash, and "signed by" (if `signature` present).

### 5.2 True data-flow edges

- The ledger already distinguishes edge types (see MVP `graph.js` EDGE_STYLES: `supersedes`, `depends_on`, `contradicts`, `extends`, `references`, `resolves`, `distills`). v2 adds rendering for **`produces` / `consumes`** edges (output-of-N → input-of-M) introduced by the memory-bus and workflow specs. Waterfall view shows them as small "↳ uses output of #N7" inline hints; 3D renders them as a distinct particle-stream color.

### 5.3 Content-addressed share route

- `GET /share/:chain_root_hash` → renders a read-only session snapshot from the chain-tier metadata (no content tier, no live updates).
- Served without authentication when the flag `r1_server.share_enabled=true` in the config (default: false).
- Template: `share.html` — waterfall only, no edit, no SSE, banner "Read-only snapshot as of `created_at`".

### 5.4 `.tracebundle` export

- `GET /api/session/:id/export.tracebundle` → `application/gzip` of:
  - `manifest.json` (session metadata + chain root hash)
  - `chain.ndjson` (chain-tier nodes, one per line)
  - `edges.ndjson`
  - `content/` directory of content-tier JSON blobs (skipped for redacted nodes, with a `redacted.json` sidecar listing those IDs + reasons)
- Another r1-server can import with `POST /api/sessions/import` (out of scope for v2; noted as "import handler is next sprint").

### 5.5 Deterministic replay

- Noted but **not** in this spec. A future spec will cover re-running a session against a recorded prompt→response log.

### 5.6 Run diff view (minimum viable)

- `GET /diff/:session1/:session2` → `diff.html` side-by-side tree.
- v2 scope: show nodes added / removed / changed (by `type+name` key only). Full content-diff is future work.
- Backend: `diffSessions(a, b) []diffRow` walks both trees in order and emits rows tagged `added` / `removed` / `changed-type` / `changed-status`.

## 6. Correction 7 — Memory explorer (RS-11)

### 6.1 Route + layout

- `GET /memories` → `memories.html`, default view = grouped list.
- Top bar: `[Grouped List ✓] [Graph]` + `[+ Add Memory]` button (opens a form partial in the right pane).
- Groups rendered in this fixed order:
  1. **Permanent** (`scope=permanent`)
  2. **Always** (`scope=always`)
  3. **Global** (`scope=global`)
  4. **This Session** (`scope=session`, filtered to current active session if one is selected; otherwise skipped)
  5. **Older Sessions** (`scope=session`, `session_id != current`)

### 6.2 Card contents (`partials/memory-card.html`)

- Key (code-styled)
- Truncated content preview (200 chars)
- Scope badge (pill, color per group)
- Write-count + read-count (`writes: 3 · reads: 17`)
- Created-by (agent name)
- Expires-at (when set, else hidden)
- 🔒 indicator when `content_encrypted` populated AND the keyring is unavailable (server-side check); card body shows "[encrypted — unlock keyring]" instead of content preview
- Actions row: `[Promote]` (session → persistent; submits `PUT /api/memories/:id` with scope change), `[Edit]` (admin only; RBAC check server-side), `[Delete]` (with an htmx-confirm dialog: `hx-confirm="Delete memory X?"`)

### 6.3 Filters + search

- Scope `<select multiple>`
- `memory_type` `<select multiple>`
- Repo filter (text input; matches `repo_root` prefix)
- FTS5 text search (backed by the sqlite FTS5 index from memory-bus spec): `/api/memories?q=…`

### 6.4 Graph tab (secondary)

- `/memories/:id/graph` — **not** a graph of all memories; it's the read/write trail of a single memory inside the 3D visualizer: the `memory_stored` write node, all `memory_recalled` read nodes, the agents that touched them, connected by read/write edges. Uses the same `graph.js` island with a filtered dataset.

### 6.5 API

| Method | Path | Body | Response |
|---|---|---|---|
| GET | `/api/memories` | — | `{memories: [...], count: N}` |
| GET | `/api/memories/:id` | — | `{memory: {...}}` |
| POST | `/api/memories` | `{key, content, scope, memory_type, expires_at?}` | `{id, ...}` (201) |
| PUT | `/api/memories/:id` | partial memory | updated memory |
| DELETE | `/api/memories/:id` | — | 204 |

All handlers live in `cmd/r1-server/memories.go` and call into the memory-bus-provided service interface; r1-server itself is still read-mostly and writes are gated by the same RBAC check used for settings.

## 7. RS-8 — Skill load/unload emission + viz

### 7.1 Emission (backend — Stoke core, not r1-server)

- In `internal/hub/builtin/skill_injector.go`, at the end of `handle()` after skills are selected and injected: for each selected skill emit:
  - A `skill_loaded` ledger node (`ledger/nodes/skill_loaded.go`): `{skill_id, skill_name, version, selected_by, prompt_tokens_injected}`.
  - A STOKE event on the hub: `stoke.skill.loaded` with the same payload.
- On context compaction — find the compactor callsite by grepping `microcompact` / `context/compact*` packages, hooking the exit path. For each skill dropped from the active set emit:
  - A `skill_unloaded` ledger node: `{skill_id, skill_name, reason="context_compaction"}`.
  - A STOKE event `stoke.skill.unloaded`.
- Deterministic — multiple emits for the same skill within one session are allowed (reload/unload cycles). Dedup key = `(skill_id, created_at)`.

### 7.2 Viz (this spec, UI side)

- Waterfall: `skill_loaded` rows render with 🧬 icon; `skill_unloaded` render with 🧬❌; both grouped under a "Skills" subtree by default.
- 3D graph: skill nodes keep their RS-4 hexagonal-prism shape; instance color opacity drops to 0.3 after the matching `skill_unloaded`. Time scrubber cursor (§3.3) toggles this live.
- Side panel for a skill node: `loaded_at`, `unloaded_at` (if any), tokens injected, selected_by reason.

### 7.3 Tests

- Fixture session writes a `skill_loaded` then a `skill_unloaded` 2 seconds later; an httptest client subscribes to SSE and asserts both events arrive; waterfall template render test asserts the `🧬❌` row exists and the skill node side-panel lists both timestamps.

## 8. SSE endpoint

### 8.1 Spec for `/api/session/:id/live`

- Content-Type: `text/event-stream`
- Cache-Control: `no-cache`
- First thing on the wire: `retry: 2000\n\n` (browsers auto-reconnect on disconnect).
- Then for each new row in the event-bus read cursor (the `id` column from memory-bus spec's read-only cursor):
  ```
  id: 12345
  event: stoke.task.end
  data: {"id":12345,"type":"stoke.task.end","payload":{...}}

  ```
- Heartbeat every 30s: `: ping\n\n` (comment line keeps reverse proxies happy without firing a DOM event).
- On reconnect, the client sends `Last-Event-ID: 12345`; server resumes by querying `WHERE id > 12345` on the cursor; if the row is older than the retention window and has been pruned, server responds with an `event: resync\ndata: "full-reload-required"\n\n` frame and the client does `window.location.reload()`.
- CORS: same-origin only. No `Access-Control-Allow-Origin` header. The binary serves its own UI.

### 8.2 Go-side handler

- `func serveLive(w http.ResponseWriter, r *http.Request)` in `cmd/r1-server/sse.go`:
  - Parses `:id` from the path, `Last-Event-ID` from request headers OR the `last-event-id` URL query (htmx-sse fallback).
  - Calls `eventBus.Subscribe(ctx, sessionID, sinceID)` → a channel; for each event, writes an SSE frame with `http.Flusher.Flush()`.
  - On `r.Context().Done()` returns; the bus subscription cleans up.
  - Writes heartbeat every 30s from a `time.NewTicker`.

## 9. Redacted-node display (ledger-redaction interop)

### 9.1 Waterfall

- Row shows 🔒 in the redacted slot.
- Status badge retains `✔ verified` if the chain-tier hash still verifies (redaction is signed; the structural header is untouched).
- Click → side panel renders:
  - The structural header verbatim (id, type, created_at, created_by, mission_id, parent_hash, edge list).
  - Content area: a visible placeholder box `[content redacted]`.
  - Below: `Redaction events for this node` — a list of the signed redaction events (from the `redaction` ledger node type per ledger-redaction spec):
    - `redacted_at` timestamp
    - `reason` (text)
    - `signer` (agent/user id)
    - signature hash (short form)

### 9.2 3D graph

- The `InstancedMesh` instance for a redacted node:
  - Color desaturated (lerp 60% toward `#444`).
  - A small `three-spritetext` 🗝 lock sprite attached at `(0, size, 0)`.
- Time scrubber: if the `redacted_at` is after the current cursor, the node renders normally; at/after the cursor, it desaturates and gains the lock. This lets operators scrub back to pre-redaction state visually.

## 10. Implementation checklist

### Go-side handlers + templates

- [ ] Create `cmd/r1-server/ui/web/` directory tree per §2.2.
- [ ] Add `//go:embed ui/web/*` (alongside the existing `//go:embed ui/*`) to `ui.go`.
- [ ] `func v2Enabled() bool` reads `os.Getenv("R1_SERVER_UI_V2") == "1"`.
- [ ] Refactor `mountUI` to branch on `v2Enabled()`: unset → existing MVP handlers; set → v2 handlers.
- [ ] Parse all templates with `html/template.ParseFS(webFS, "*.html", "partials/*.html")` at init.
- [ ] `GET /{$}` (v2) → render `index.html` with instance list.
- [ ] `GET /session/{id}` (v2) → render `session.html` (waterfall + side panel placeholder).
- [ ] `GET /session/{id}/graph` (v2) → render `session-graph.html` (3D island).
- [ ] `GET /session/{id}/stream` (v2) → render `session-stream.html` (live tail).
- [ ] `GET /session/{id}/waterfall` (htmx partial) → re-render the tree with filter+search params.
- [ ] `GET /api/session/{id}/node/{node_id}` → `partials/node-side-panel.html`.
- [ ] `GET /api/session/{id}/live` → SSE endpoint (see SSE section below).
- [ ] `GET /memories` → render `memories.html`.
- [ ] `GET /memories/groups` (htmx partial) → render `partials/memory-group.html` repeated.
- [ ] `GET /memories/:id/graph` → render 3D island filtered to that memory's trail.
- [ ] `GET /settings/retention` → render `settings.html` (depends on retention-policies spec handler).
- [ ] `GET /share/:chain_root_hash` → render `share.html` (read-only); 404 when `share_enabled=false`.
- [ ] `GET /diff/:session1/:session2` → render `diff.html`.
- [ ] `GET /api/session/:id/export.tracebundle` → stream tar.gz.

### embed.FS vendor files + vendor script

- [ ] `scripts/vendor-ui.sh`: curl pinned versions of `three.module.js`, `OrbitControls.js`, `3d-force-graph.js`, `three-spritetext.js`, `htmx.min.js`, `htmx-ext-sse.js` into `cmd/r1-server/ui/web/vendor/`.
- [ ] Commit vendored files (they're source for the single-binary build).
- [ ] CI check: total `web/vendor/**` gzipped size ≤ 260 KB (hard fail above).
- [ ] Pin versions in a `VENDOR.md` inside `web/vendor/` (human-readable source of truth).
- [ ] Add a smoke test `TestVendorAssetsPresent` that asserts each expected file exists under `webFS` and is nonzero length.

### htmx layout + data-attributes

- [ ] Build `base.html` with `<meta name="htmx-config">`, import map, `<script src="/ui/vendor/htmx.min.js">`, `<script src="/ui/vendor/htmx-ext-sse.js">`.
- [ ] Top nav tabs are plain `<a hx-boost="true">` so full-page transitions happen without fetch-based partials.
- [ ] Waterfall row `<tr>` has `hx-get="/api/session/:id/node/:nid"` + `hx-target="#side-panel"` + `hx-swap="innerHTML"`.
- [ ] Filter form uses `hx-trigger="change delay:150ms, keyup delay:300ms"` + `hx-include="[name='q'], [name='types']"`.
- [ ] `[+ Add Memory]` opens form partial into `#side-panel`; form `hx-post="/api/memories"` with `hx-target="#memory-list"` + `hx-swap="afterbegin"` to prepend new card.

### SSE endpoint with Last-Event-ID resume

- [ ] `cmd/r1-server/sse.go` implements `serveLive`.
- [ ] Parses `Last-Event-ID` header or URL fallback.
- [ ] Writes `retry: 2000\n\n` once.
- [ ] Subscribes to event bus; emits SSE frames with `id:` / `event:` / `data:`.
- [ ] 30s heartbeat via ticker; writes `: ping\n\n`.
- [ ] Handles bus cursor pruning: emits `event: resync` frame when the client's `Last-Event-ID` is below the bus retention floor.
- [ ] Calls `http.Flusher.Flush()` after each write.
- [ ] Test: `TestSSE_ResumeFromLastEventID` with httptest + a fake bus that has seen IDs 1..10.
- [ ] Test: `TestSSE_Heartbeat` with a manual clock.
- [ ] Test: `TestSSE_ResyncOnPrunedCursor`.

### 3D graph worker + InstancedMesh refactor

- [ ] `web/js/graph-worker.js` imports `d3-force-3d` from `/ui/vendor/`.
- [ ] Worker message protocol: `{kind:'init', nodes, edges}`, `{kind:'tick', positions}`, `{kind:'done', positions}`, `{kind:'add', node, neighbors}`, `{kind:'progress', alpha}`.
- [ ] `web/js/graph.js` spawns the worker; allocates one `InstancedMesh` per node shape with `MAX_INSTANCES=8192`.
- [ ] `setMatrixAt` / `setColorAt` per node; `.instanceMatrix.needsUpdate = true` + `.instanceColor.needsUpdate = true` on tick.
- [ ] Raycaster logic switched to `InstancedMesh.raycast` returning `{instanceId, meshIndex}`, mapped back to node id.
- [ ] Time scrubber: freeze positions post-layout; on scrub, iterate instances and toggle scale via `setMatrixAt` (scale=0 for hidden).
- [ ] Streaming insert at mean neighbor position + `alpha(0.3)`; never `alpha(1).restart()`.
- [ ] BFS 1–3 hop focused subtree mode + "Focus subtree" side-panel button.
- [ ] Desaturate + lock sprite for redacted nodes.
- [ ] Opacity 0.3 for skill nodes after `skill_unloaded`.
- [ ] Performance test: synthetic 3000-node fixture loads and hits interactive frame rate on a laptop-class GPU (qualitative stretch goal; documented in `docs/perf-notes.md`).

### Memory explorer CRUD handlers

- [ ] `cmd/r1-server/memories.go` with `ListMemories`, `GetMemory`, `CreateMemory`, `UpdateMemory`, `DeleteMemory`.
- [ ] RBAC check on write methods (edit/delete) via the same `rbac` middleware used for settings.
- [ ] Group-order helper `groupedMemories(memories, currentSession) []memoryGroup`.
- [ ] Template `memories.html` + partials `memory-group.html` + `memory-card.html`.
- [ ] FTS5 search wired through memory-bus service's `SearchMemories(q string)`.
- [ ] Encrypted indicator: template branches on `m.ContentEncrypted && !keyringUnlocked`.
- [ ] Tests: handler test per method; golden-HTML test for grouped render.

### Skill-loaded / skill-unloaded emission + rendering

- [ ] `ledger/nodes/skill_loaded.go` with struct + `NodeType()` method.
- [ ] `ledger/nodes/skill_unloaded.go` same.
- [ ] `internal/hub/builtin/skill_injector.go`: emit both node + bus event on load.
- [ ] Locate compactor callsite (grep `microcompact` + `context/compact*`) and emit `skill_unloaded` for each dropped skill.
- [ ] Fixture session test `TestSkillLoadUnloadEmission` in `internal/hub/builtin/` — asserts both events land on the bus.
- [ ] Waterfall icon + label wiring in `partials/waterfall-node.html`.
- [ ] 3D graph opacity handling in `graph.js`.
- [ ] SSE end-to-end test: fixture session emits load → unload; httptest SSE client receives both; scrubber shows transition.

### Redacted-node rendering

- [ ] Helper `isRedacted(n ledgerNode) bool` (checks whether a signed `redaction` node references this node).
- [ ] Waterfall template branch for 🔒 slot.
- [ ] Side-panel template branch: `[content redacted]` placeholder + redaction events list.
- [ ] `partials/redaction-events.html` rendered from `ledger.Store.RedactionsFor(nodeID)`.
- [ ] 3D graph desaturate + lock sprite.
- [ ] Test: golden render of a redacted-node side panel.

### Run diff view (minimum viable)

- [ ] `func diffSessions(a, b sessionID) []diffRow` — tree walk both, emit added/removed/changed-type/changed-status.
- [ ] `GET /diff/:a/:b` handler + `diff.html` template (side-by-side trees).
- [ ] Test: fixture two ledgers, expected diff rows.
- [ ] Document "content-diff is future work" in the template footer.

### Content-addressed share route

- [ ] `GET /share/:hash` handler + `share.html`.
- [ ] Config key `r1_server.share_enabled=false` default; 404 until flipped.
- [ ] No auth required when enabled; banner "Read-only snapshot" on page.
- [ ] Test: happy path + disabled-404 case.

### .tracebundle export route

- [ ] `GET /api/session/:id/export.tracebundle` writes tar.gz (stdlib `archive/tar` + `compress/gzip`).
- [ ] Includes manifest, chain.ndjson, edges.ndjson, content/ dir (skips redacted), redacted.json sidecar.
- [ ] Test: export + untar roundtrip; redacted node excluded from content/.

### Feature flag `R1_SERVER_UI_V2=1`

- [ ] Single helper `v2Enabled()`; all new handlers gated by it.
- [ ] README snippet documenting the flag + roll-out plan.
- [ ] Test asserting both paths (`unset` serves MVP, `=1` serves v2) via httptest.

### Tests per file

- [ ] `ui.go` — route mounting branches on flag.
- [ ] Each handler has a table-driven httptest test.
- [ ] Templates have golden-HTML tests using goldmark-style normalization (strip whitespace variance, sort attributes).
- [ ] SSE frame parser test with canned wire bytes.
- [ ] Memory explorer CRUD — happy + RBAC-denied + encrypted-content cases.
- [ ] Optional Playwright E2E behind `//go:build e2e` tag with a smoke flow: instance list → session → 3D tab → memory tab → add memory → delete memory → share link.

## 11. Acceptance criteria

- `go build ./cmd/r1-server` clean.
- `go vet ./...` clean.
- `go test ./...` clean (including `cmd/r1-server/...`).
- With `R1_SERVER_UI_V2` **unset**: the MVP UI serves exactly as it does today. `index.html`, `app.js`, `style.css`, `graph.html`, `graph.js`, `graph.css` reachable at `/`, `/session/:id`, `/session/:id/graph`. `/memories`, `/settings/retention`, `/share/*`, `/diff/*` return 404.
- With `R1_SERVER_UI_V2=1`: default `/session/:id` renders the **waterfall**, not the SPA stream view. `/session/:id/graph` serves the v2 3D page (preserving the MVP path). `/memories` serves the grouped-list explorer. `/settings/retention` serves the retention page.
- SSE `/api/session/:id/live` survives a network blip: disconnect, client reconnects with `Last-Event-ID`, server resumes without duplicating frames.
- 3D graph renders a 3000-node fixture on a mid-range laptop at an interactive frame rate (stretch; documented, not CI-gated).
- A redacted node renders in the waterfall with `[content redacted]` placeholder and a visible list of signed redaction events; in 3D with desaturated color + 🗝 lock sprite.
- A `skill_loaded` ledger node and a subsequent `skill_unloaded` emit from the skill-injector + compactor callsites; waterfall shows both rows; 3D graph opacity transitions on scrub.
- Memory explorer lists memories in the `Permanent → Always → Global → This Session → Older Sessions` order with correct cards, filter, FTS5 search, Add/Edit/Delete/Promote actions, and 🔒 indicator for encrypted-and-locked content.

## 12. Testing

- **Go handler tests** per route: happy, 404, auth-denied where applicable.
- **Template golden tests**: render each template against a canned fixture; compare normalized HTML (sort attrs, collapse whitespace) to a checked-in `.golden.html`.
- **SSE frame parsing test**: spin up the handler via `httptest.NewServer`, consume the stream with a buffered scanner, assert frame structure + `Last-Event-ID` resume.
- **htmx partial render tests**: request with `HX-Request: true` header, assert the response is the partial not the full layout.
- **Fixture ledger tests** for the 3D data endpoint `/api/session/:id/ledger` (shape only; Three.js is browser-side).
- **Optional Playwright E2E** behind `//go:build e2e` tag running against a real binary with the flag set. Not part of the default `go test ./...` run.
- **Performance notes** (not gated): manual check that 3000-node fixture renders smoothly; documented in `docs/perf-notes.md`.

## 13. Rollout

1. **Week 0** — merge v2 behind `R1_SERVER_UI_V2=1`. Default unchanged. MVP UI serves at all the same paths.
2. **Weeks 1–2** — team uses `R1_SERVER_UI_V2=1` in daily work. Collect feedback on waterfall ergonomics + 3D perf. File follow-up issues.
3. **Week 3** — flip default: `v2Enabled()` returns true unless `R1_SERVER_UI_V2=0` is set explicitly. Keep MVP files in `cmd/r1-server/ui/` (non-`web/`) so the escape hatch still exists.
4. **Next release cycle** — delete the MVP files and the flag. Consolidate `uiFS` to only point at `ui/web/`. A final commit removes `app.js`, old `index.html`, old `style.css`, old `graph.html/js/css`.

## 14. Out of scope

- Deterministic replay of a session's LLM I/O.
- Full content-diff between two sessions (v2 is key-level diff only).
- `.tracebundle` **import** handler (export only in v2).
- A graph view of *all* memories — only the per-memory trail is in v2.
- WebSocket transport — SSE is the only live channel.
- Redesigning the ledger schema or memory-bus storage layer.
- Provider-pool, router-core, or any non-UI Stoke system.
