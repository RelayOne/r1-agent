<!-- STATUS: done -->
<!-- BUILD_COMPLETED: 2026-04-22 -->
<!-- CREATED: 2026-04-20 -->
<!-- DEPENDS_ON: spec-2 cloudswarm-protocol (S-0 stream-to-file), spec-3 executor-foundation (eventlog/ledger infrastructure) -->
<!-- BUILD_ORDER: 15 -->
<!-- SOURCE: user-provided r1-server-work.md (2026-04-20) -->

# r1-server — Visual Execution Trace Server

## Overview

A separate `r1-server` binary (one per machine, port 3948) that discovers running Stoke/R1 instances by scanning for `r1.session.json` signature files, ingests their stream.jsonl + ledger DAG + checkpoint timeline, and exposes a web dashboard with an embedded 3D force-directed visualizer of the ledger.

**The thesis:** R1/Stoke already produces a content-addressed Merkle-chained reasoning ledger (28 node types, 7 edge types, append-only filesystem store at `.stoke/ledger/`). Every decision, task, verification, HITL request, escalation, research query, skill invocation is already a node in a DAG with typed relationships. **Nobody currently renders this data.** r1-server makes R1's internal reasoning visible — the "glass box" to every other agent's black box.

**Protocol name:** STOKE — Strong Traceable Observable Knowledge Executor. Every event carries `ledger_node_id` (content-addressed SHA-256) + `trace_parent` (W3C Trace Context).

## Stack & Versions

- Go 1.25 (matches Stoke binary)
- SQLite (already a dep via `github.com/mattn/go-sqlite3`)
- `embed.FS` for Web UI bundling (same pattern as RelayGate's admin dashboard)
- Three.js + `three-forcegraph` (or `d3-force-3d`) via CDN or bundled, for 3D visualizer

## Existing Patterns to Follow

- NDJSON emitter: `internal/streamjson/emitter.go:187` (already exists, already Claude-Code-compatible)
- Ledger: `internal/ledger/` (Node + Edge, filesystem store at `.stoke/ledger/nodes/` + `.stoke/ledger/edges/`)
- Checkpoint timeline: `internal/checkpoint/timeline.go` (append-only JSONL at `.stoke/checkpoints/timeline.jsonl`)
- Bus WAL: `internal/bus/wal.go` (append-only NDJSON at `.stoke/bus/events.log`)
- Embedded UI precedent: router-core's admin dashboard (single HTML + `embed.FS`)

## Boundaries — What NOT To Do

- Do NOT require r1-server to run. Stoke + API key must continue to work without it.
- Do NOT hard-couple. r1-server discovers instances passively (file scan + optional POST register). If r1-server isn't running, Stoke silently skips registration (1-second timeout).
- Do NOT push events. R1 writes to local filesystem; r1-server reads. This preserves crash recovery and avoids IPC complexity.
- Do NOT modify R1's ledger or session state. r1-server is read-only.
- Do NOT bundle heavy frontend frameworks. Single HTML + Three.js + vanilla JS. Same philosophy as RelayGate admin.

## Implementation Checklist

### RS-1: Session signature file + stream-to-file (1 day)

1. [ ] **Create `internal/session/signature.go`** with `Signature` type + `WriteSignature(repoRoot, cfg)` + `(*Signature).Close()` + background `heartbeat()` goroutine that updates `updated_at` every 30s.
2. [ ] **Signature schema** per r1-server-work.md §RS-1: `{version, pid, instance_id, started_at, repo_root, mode, sow_name, model, status, stream_file, ledger_dir, checkpoint_file, bus_wal, updated_at}`. Write atomically to `<repo>/.stoke/r1.session.json` via tmp+rename.
3. [ ] **Instance ID** generation: `r1-` + 8-hex-char short random ID.
4. [ ] **Call `WriteSignature` at R1 entry points**: `cmd/stoke/main.go` after `absRepo` is resolved, before SOW parse. `defer sig.Close()`. Covers: `sowCmd`, `chatCmd`, and any future entry points.
5. [ ] **On `Close()`**: set `status = "completed"` or `"failed"` (based on exit path), write final `updated_at`.
6. [ ] **Auto-register with r1-server** (best-effort): `(*Signature).registerWithServer()` POSTs to `http://localhost:3948/api/register` with 1s timeout. Silent failure on refused connection (r1-server not running = OK).
7. [ ] **Stream file**: modify `internal/streamjson/emitter.go` so events always write to `.stoke/stream.jsonl` (file), AND optionally to stdout when `--output stream-json`. Use `io.MultiWriter` at construction in `cmd/stoke/main.go:1476`. This lets r1-server tail the file regardless of whether CloudSwarm is consuming stdout.
8. [ ] **Tests**: `internal/session/signature_test.go` — WriteSignature creates file with correct fields, Close updates status, registerWithServer times out gracefully if no server.

### RS-2: `cmd/r1-server/main.go` binary (2-3 days)

1. [ ] **New binary** `cmd/r1-server/main.go` — package `main`, separate from `cmd/stoke/`.
2. [ ] **Global data dir**: XDG-aware `globalDataDir()` — `~/Library/Application Support/r1` on macOS, `${XDG_DATA_HOME}/r1` on Linux, `%APPDATA%/r1` on Windows. Override via `R1_DATA_DIR` env.
3. [ ] **SQLite DB** at `<data_dir>/server.db` with WAL mode. Schema:
   ```sql
   CREATE TABLE IF NOT EXISTS sessions (
       instance_id TEXT PRIMARY KEY,
       pid INTEGER,
       repo_root TEXT,
       mode TEXT,
       sow_name TEXT,
       model TEXT,
       status TEXT,
       stream_file TEXT,
       ledger_dir TEXT,
       started_at TEXT,
       updated_at TEXT,
       ended_at TEXT
   );
   CREATE TABLE IF NOT EXISTS session_events (
       id INTEGER PRIMARY KEY AUTOINCREMENT,
       instance_id TEXT REFERENCES sessions(instance_id),
       event_type TEXT,
       data TEXT,
       timestamp TEXT
   );
   CREATE INDEX idx_events_instance ON session_events(instance_id);
   CREATE INDEX idx_events_type ON session_events(event_type);
   ```
4. [ ] **HTTP server on port 3948** with `net/http.ServeMux`. Port configurable via `R1_SERVER_PORT`.
5. [ ] **API endpoints**:
   - `POST /api/register` — ingest signature from running R1 instance
   - `GET /api/health` — liveness probe (200 OK + version)
   - `GET /api/sessions` — list all discovered sessions (paginated, filter by status)
   - `GET /api/session/{id}` — signature + metadata
   - `GET /api/session/{id}/events?after={id}&limit={n}` — cursor-paginated stream events
   - `GET /api/session/{id}/ledger` — full ledger DAG (nodes + edges)
   - `GET /api/session/{id}/checkpoints` — checkpoint timeline
6. [ ] **Graceful shutdown**: SIGINT/SIGTERM → close DB, drain in-flight requests, exit 0.
7. [ ] **Single-instance guard**: if port 3948 is already bound, print a clear error and exit 1 (another r1-server is running).
8. [ ] **Logging**: structured logs via `log/slog` to `<data_dir>/r1-server.log`, rotated at 10MB.

### RS-3: Instance scanner (included in RS-2 effort)

9. [ ] **Background scanner** goroutine in `cmd/r1-server/scanner.go` that runs every 60s.
10. [ ] **Scan paths**: `$HOME`, `$HOME/code`, `$HOME/projects`, `$HOME/dev`, `$HOME/repos`, `$HOME/src`, `$HOME/work`, plus any `filepath.Dir(previously_seen_repo_root)` from prior scans.
11. [ ] **Skip descent into**: `.git`, `node_modules`, `vendor`, `target`, `.cache`, `dist`, `build`.
12. [ ] **On finding `r1.session.json`**: parse, `ingestSignature(path)` — upsert into `sessions` table.
13. [ ] **Liveness**: for sessions with `status=running`, check `processAlive(pid)`. If dead, mark `status=crashed`.
14. [ ] **Event tailing**: for each known session, maintain a goroutine that tails `stream_file` using `fsnotify`; parse each new line as JSON; insert into `session_events`.
15. [ ] **Ledger scan**: on first discovery of a session, walk `ledger_dir/nodes/*.json` + `edges/*.json` once, load into DB for fast query. Watch `ledger_dir` for new files via fsnotify.
16. [ ] **Tests**: scanner discovers a fixture signature file; dead PID marked crashed; new events tailed correctly.

### RS-4: Web dashboard (2-3 days basic, +3-5 days for 3D visualizer)

17. [ ] **Embedded UI** via `embed.FS` in `cmd/r1-server/ui/`: single `index.html`, `app.js`, `style.css`. Reject heavy frameworks; vanilla JS + fetch.
18. [ ] **Route `/`** — Instance list page:
    - List all `sessions` rows with status emoji (🟢 running / ⚫ completed / 🔴 crashed / ⏸ paused)
    - Per row: repo_root, mode, model, sow_name, time elapsed, cost (if available in latest event)
    - Links: [Stream] → `/session/:id` (live-tail view) | [Visualizer] → `/session/:id/graph` (3D DAG)
19. [ ] **Route `/session/:id`** — Stream view:
    - Live-tailing via EventSource (SSE) from `/api/session/:id/events/stream`
    - Event cards color-coded by type (session.start/task.start/descent.tier/ac.result/cost/hitl_required/etc.)
    - Filter dropdown by event type
    - Auto-scroll toggle
    - Copy-as-JSON per event
20. [ ] **Route `/session/:id/graph`** — 3D execution graph:
    - Fetch `/api/session/:id/ledger` → `{nodes, edges}`
    - Render using Three.js + `three-forcegraph` (CDN-loaded)
    - Node shapes per r1-server-work.md §RS-4: task=cube/blue, decision=sphere/purple, verification=diamond(green/red), hitl_request=octahedron/orange, escalation=cone/red, judge_verdict=icosahedron/gold, research=cylinder/teal, skill=hex-prism/cyan, supervisor=ring/white
    - Edge styles: supersedes=solid-gray-arrow, depends_on=dashed-blue, contradicts=zigzag-red, extends=solid-green, references=dotted-light, resolves=thick-gold, distills=thin-purple
    - Interactions:
      - OrbitControls (scroll-zoom, drag-rotate, right-click-pan)
      - Click a node → side panel shows full JSON + `ledger_node_id`
      - Hover → tooltip (type, created_at, created_by)
      - **Time scrubber**: slider that animates node appearance in creation order — scrub left to see state at past moments, scrub right to see future state building
      - Filter by node type (checkbox list)
      - Text search across node content
      - Cluster by `mission_id` (multiple missions = multiple sub-graphs)
21. [ ] **Graceful degradation**: if SSE unsupported, fall back to polling `/api/session/:id/events` every 2s. If WebGL unavailable for the 3D graph, fall back to a 2D SVG force-directed view.

**CDN + offline note (RS-4 item 20):** `graph.js` loads Three.js
(`three@0.160.0`) and `3d-force-graph@1.73.0` from unpkg at
request-time, pinned to specific versions to avoid silent breakage.
The UI therefore requires internet access on first visit (or a
browser cache warmed on an earlier visit). When WebGL is unavailable
— or the CDN scripts fail to load — the page renders a fallback
banner pointing back at the 2D live-stream view at
`/session/:id`.

### RS-5: Auto-launch r1-server from Stoke (1 hour)

22. [ ] **In `cmd/stoke/main.go`, add `ensureServerRunning()`** called before `WriteSignature`. Behavior:
    - `http.Get("http://localhost:3948/api/health")` with 200ms timeout; if 200 OK, return
    - Otherwise `exec.LookPath("r1-server")`; if not found, return silently (zero dep)
    - If found: spawn detached (`Setsid: true`, `cmd.Start()`), sleep 500ms, return
23. [ ] **Do not block R1 startup** if r1-server launch fails or times out. Continue without it.
24. [ ] **Environment bypass**: `STOKE_NO_R1_SERVER=1` disables auto-launch entirely.

### RS-6: STOKE protocol event spec (1 day)

25. [ ] **Document the STOKE event envelope** in `docs/stoke-protocol.md`:
    ```json
    {
      "stoke_version": "1.0",
      "type": "stoke.descent.tier",
      "ts": "2026-04-20T21:05:32.123456Z",
      "instance_id": "r1-a3f7b2c4",
      "session_id": "sess-b8e2f1a9",
      "trace_parent": "00-abcdef1234567890-1234567890abcdef-01",
      "ledger_node_id": "node-sha256-...",
      "data": { ... }
    }
    ```
26. [ ] **Modify `(*Emitter).EmitStoke`** (added in spec-2) to populate `stoke_version`, `instance_id`, `trace_parent`, and `ledger_node_id` automatically when a ledger Node exists for the event. Call sites pass `ledger_node_id` via a new optional arg or via context.
27. [ ] **Ledger integration point**: every emit call that corresponds to a ledger write should pass the freshly-computed node ID. Plumb via context or a small helper.
28. [ ] **W3C Trace Context**: generate a `traceparent` header-format string per RFC on session.start; propagate down the session's events. Optional dependency: `go.opentelemetry.io/otel/trace` — or hand-roll (it's an 8-byte trace ID + 8-byte span ID in a specific ASCII format).
29. [ ] **Compatibility**: the new envelope fields are additive. Existing consumers (CloudSwarm `execute_stoke.py`) see unchanged behavior — they read `type` + `ts` + `data` and don't look at the new fields. Verify with a contract test.

## Acceptance Criteria

- `go build ./cmd/r1-server` produces `r1-server` binary
- `r1-server --version` prints a version string
- `curl http://localhost:3948/api/health` returns 200 OK with JSON
- Running `stoke ship --sow ...` creates `<repo>/.stoke/r1.session.json` with correct shape
- r1-server discovers the instance within 60s and lists it at `/api/sessions`
- Browsing to `http://localhost:3948` shows the instance
- Browsing to `/session/:id` shows live-tailed events
- Browsing to `/session/:id/graph` renders the ledger as a 3D force-directed graph
- Time scrubber animates node appearance correctly in creation order
- Stoke continues to work when r1-server is not installed (silent fallback)
- `STOKE_NO_R1_SERVER=1 stoke ship ...` skips all r1-server interactions

## Testing

- `go test -race -count=1 ./internal/session/` — signature write/close/heartbeat
- `go test -race -count=1 ./cmd/r1-server/` — scanner discovery + liveness + API handlers
- Manual: smoke-run `stoke ship` on a fixture SOW, then `curl` the API endpoints, then browse the UI
- Visual: load a completed SOW's ledger into the 3D graph, verify node shapes/colors/edges
- Contract test: CloudSwarm's `test_execute_stoke.py` fixtures still parse Stoke stdout events correctly after STOKE envelope additions

## Rollout

1. RS-1 and RS-2 land first (signature + binary). No UI, no visualizer. Just the API.
2. RS-3 scanner comes with RS-2.
3. RS-5 auto-launch lands with RS-2 (the pieces are independent but the ergonomic value requires both).
4. RS-4 stream view lands as shippable milestone (first user-visible win).
5. RS-4 3D visualizer lands as Phase 2 of RS-4 (the wow factor).
6. RS-6 STOKE protocol envelope lands alongside RS-4 (or before, if CloudSwarm wants early access).

**MVP cut:** RS-1 + RS-2 + RS-3 + RS-4 stream view + RS-5 = ~5-7 days. The 3D visualizer is the headline feature; budget another week.

**Total: ~2 weeks to full delivery, ~1 week to MVP stream view.**
