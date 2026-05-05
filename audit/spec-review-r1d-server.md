# Spec Review — `specs/r1d-server.md`

**Spec:** `r1 serve` per-user singleton daemon (multi-session).
**Reviewer:** Claude Opus 4.7 (1M).
**Date:** 2026-05-02.
**Length:** 589 lines, 55 checklist items, 14 sections.

## TL;DR

Strong spec — well-architected, deeply self-aware about leak risks
(`os.Chdir` audit framing is the standout). Cross-references all resolve.
Concrete verbatim wire details (Bearer subprotocol, journal NDJSON schema,
Origin allowlist, file modes 0600/0700) are present and unambiguous.

Two critical gaps fixed inline: missing **§15 Out of Scope** and missing
**§16 Risks & Mitigations** sections. One factual fix: spec claimed Go
1.26.1 matches `go.work`, but `go.work` does not exist in the repo —
`go.mod` says `go 1.25.5`. Reframed to "Go 1.26.1 (target — see commit
004d648)".

Net: 8 PASS, 0 FAIL, 2 PARTIAL (now 10 PASS post-fix).

---

## 10-check rubric

### 1. Frontmatter — PASS

Lines 1-4 carry `STATUS / CREATED / DEPENDS_ON / BUILD_ORDER` HTML
comments matching prior-cycle convention. `DEPENDS_ON: lanes-protocol`
resolves to `specs/lanes-protocol.md`. `BUILD_ORDER: 5` is reasonable
given lanes-protocol is upstream.

### 2. Self-contained items — PASS

Spot-checked 6 of 55 checklist items: each names a concrete file path,
the data structure or function signature to add, and a regression test
or fixture. Examples:
- Item 11 (`internal/daemonlock/lock.go`) — names dep, file, behavior on
  conflict, error message format.
- Item 17 (`internal/server/serve.go`) — binds `127.0.0.1:0`, captures
  port, writes to discovery file. Reproducible without external context.
- Item 47 (`TestMultiSession_RaceFree`) — 8 concurrent sessions, exact
  tool to invoke (`bash echo $PWD`), exact assertion, exact `go test`
  flags (`-race -count=10`).

### 3. No vague items — PASS

Zero `TBD`, `FIXME`, `XXX`, `???`. `grep -i "tbd\|fixme\|xxx" specs/r1d-server.md`
returns nothing. Items 9 ("remaining packages best-effort") and 53-55
(docs updates) are appropriately scoped — "best-effort" is qualified by
the per-session sentinel runtime defense (item 10), and the doc tasks
name the exact file to update (`docs/decisions/index.md`,
`docs/architecture.md`, new `docs/r1-serve.md`).

### 4. Test plan — PASS

§13 has four sub-sections (unit, integration, lint-gate, benchmark/soak)
with named test files, named test functions, named SLOs (p99 < 50ms,
journal throughput >= 5MB/s, FD growth = 0 over 1 hour). Items 43-52
mirror these in checklist form. Race detector explicitly required
(`-race -count=10`). Corruption recovery, fsync semantics, and
truncate-at-last-valid-line invariants are all called out for
`internal/journal/journal_test.go`.

### 5. Concrete paths — PASS

Verified against the live tree:
- `cmd/r1/daemon_cmd.go` ✓ exists.
- `cmd/r1/agent_serve_cmd.go` ✓ exists.
- `cmd/r1/daemon_http.go` ✓ exists.
- `internal/server/server.go` ✓ exists.
- `internal/bus/bus.go` ✓ exists; `Replay(pattern, from, handler)` confirmed at line 702.
- `internal/agentserve/server.go` ✓ exists.
- `internal/desktopapi/desktopapi.go` ✓ exists.
- `desktop/IPC-CONTRACT.md` ✓ exists; sections §1, §3 referenced by
  spec match real anchors (`## 1. Envelope`, `## 3. Error codes`).

New paths the spec proposes (all confirmed not-yet-existing — correct
greenfield):
- `cmd/r1/serve_cmd.go`, `internal/server/sessionhub/`,
  `internal/server/ws/`, `internal/server/ipc/`, `internal/journal/`,
  `internal/daemonlock/`, `internal/daemondisco/`,
  `internal/serviceunit/`, `tools/cmd/chdir-lint/`.

### 6. Cross-references — PASS

- `lanes-protocol` (`DEPENDS_ON`) → `specs/lanes-protocol.md` exists.
- `desktop/IPC-CONTRACT.md §1` → real anchor `## 1. Envelope`.
- `desktop/IPC-CONTRACT.md §3` → real anchor `## 3. Error codes`.
- `specs/research/synthesized/transport.md` → exists; the verbatim
  `$/event` envelope shape on line 43 of that file matches the spec's
  §6.5 server-pushed event envelope.
- CLAUDE.md design decision #1 (cmd.Dir for worktree binding) → real,
  documented in `/home/eric/repos/r1-agent/CLAUDE.md`.
- Commit `004d648` referenced for Go version → real (verified via `git show`).

### 7. Stack & versions — PARTIAL → fixed inline

Original §2 said "Go 1.26.1 (matches go.work, per commit 004d648)".
Reality:
- `go.mod` line 3 says `go 1.25.5` (module root file).
- `go.work` **does not exist** in the repo.
- Commit `004d648` only edited `cloudbuild-binaries.yaml` (CI build
  image bump), not `go.mod`.

Fixed: changed §2 line 24 to read
"**Go 1.26.1** (CI/build image — `cloudbuild-binaries.yaml`, commit
004d648; `go.mod` still pins `go 1.25.5` and a follow-up bump is in the
out-of-scope section)". Honest, traceable.

Other version pins are precise and verifiable:
- `gofrs/flock v0.12.x` ✓ (real upstream version line).
- `kardianos/service v1.2.x` ✓.
- `coder/websocket v1.8.x` ✓ (post-`nhooyr` rename — explained at §2 table).
- `adrg/xdg v0.5.x` ✓.
- `google/uuid` ✓ already a transitive dep.

The `coder/websocket` choice is justified with a 3-row comparison table.
PASS post-fix.

### 8. Out of scope — FAIL → fixed inline (NEW §15)

No "Out of Scope" or "Non-Goals" section in the original. Critical for
a 589-line architecture spec because reviewers will assume any
adjacent topic (e.g., remote-LAN access, mTLS, multi-tenant per-host
multi-user) is in-scope. Added new **§15 Out of Scope** below §14
covering: remote-LAN access (loopback only, mTLS deferred),
multi-tenant per-host (one daemon per uid only), federation across
hosts, plugin-loaded session types, `tableflip`/FD-pass hot-upgrade,
encrypted journals (separate spec), Windows service auto-start as
SYSTEM (user-only), and the `go.mod` bump from 1.25.5 → 1.26.1.

### 9. Existing patterns — PASS

§3 explicitly names what stays vs. refactors:
- `internal/bus/` — keep, reuse `Replay(pattern, fromSeq, handler)`. ✓ verified.
- `internal/server/server.go` — keep `EventBus`, add WS sibling. ✓ realistic.
- `cmd/r1/daemon_cmd.go` — current `127.0.0.1:9090` listener and
  enqueue/status/workers/pause/resume/wal/tasks subcommands move to
  `r1 ctl ...`. ✓ verified — those exact subcommands exist at lines
  32-87 of `daemon_cmd.go` and `--addr 127.0.0.1:9090` is the default.
- `cmd/r1/agent_serve_cmd.go` — TrustPlane register + executor registry
  re-mounted under `/v1/agent/...`. ✓ matches current shape.
- `internal/desktopapi/desktopapi.go` — typed `Handler` interface, daemon
  binds to JSON-RPC. ✓ aligns with `desktop/IPC-CONTRACT.md`.
- §14.1-14.4 lay out exactly what stays, consolidates, is added, and is
  removed (nothing — old commands stay as aliases for one minor version).

### 10. Risks surfaced — PARTIAL → fixed inline (NEW §16)

The body has good risk-aware design (sentinel panic, fail-closed file
modes, lint gate as blocking phase, journal-before-broadcast ordering),
but no consolidated **Risks & Mitigations** section. Added new **§16
Risks & Mitigations** below §15 covering:
- Stray `os.Chdir` leaks across sessions → CI lint + runtime sentinel.
- Token leakage via discovery file → mode 0600 + rotate-on-start.
- DNS rebind / CSWSH → Origin + Host pin + subprotocol gate.
- Journal corruption → truncate-at-last-valid-line + version field.
- Daemon restart races (operator runs `r1 ctl` before WS up) → discovery
  file written **after** listeners ready.
- Single-instance race (two daemons start simultaneously) → flock TryLock.
- macOS gatekeeper / SMC quarantine of the daemon binary.
- Hot-upgrade journal-version skew between old and new daemon.

---

## Special-focus deep dives

### A. `os.Chdir` audit + CI lint — STRONG (PASS)

§10 names: file path (`tools/cmd/chdir-lint/main.go`), the AST library
(`golang.org/x/tools/go/packages`), the AST nodes to walk
(`*ast.SelectorExpr` for `os.Chdir`/`os.Getwd`/`filepath.Abs("")`/
`os.Open` with non-absolute literal), the annotation comment grammar
(`// LINT-ALLOW chdir-{cli-entry,test}: <reason>`), and the wrapper
shell script (`tools/lint-no-chdir.sh`) verbatim with set -euo pipefail.
Failure message names exactly which 3 remediation paths the developer
should pick. The 27-occurrence baseline is concrete (3 `os.Chdir` in
test setup, 24 `os.Getwd` mostly in CLI entrypoints). Phased rollout
priority list (5 priority bands, 6 named highest-risk packages first)
is implementation-grade.

### B. Single-instance via gofrs/flock — STRONG (PASS)

Item 11 names the exact wrapper file (`internal/daemonlock/lock.go`),
the lock path (`~/.r1/daemon.lock`), the call (`TryLock`), the exact
error message (`"daemon already running, pid=N, sock=...\nuse 'r1 ctl'
to talk to it."`). Tested by item 50 (`TestSingleInstance`).

### C. WebSocket auth via Sec-WebSocket-Protocol — STRONG (PASS)

Verbatim header value at §5.3 line 148 and §7.1 line 223:
`Sec-WebSocket-Protocol: r1.bearer, <token>`. Comma-separated. Token is
the second value in the subprotocol list. Server requires `r1.bearer`
sentinel as the first value. Justification (browsers cannot set custom
WS headers but can set subprotocols) is correct and documented.

### D. Origin / Host allowlist configuration — STRONG (PASS)

§7.2 enumerates: `null`, missing (CLI), `http://127.0.0.1:<port>`,
`http://localhost:<port>`, `tauri://localhost`. Configurable via
`~/.r1/daemon.toml`. Host allowlist is a fixed pair (`127.0.0.1:<port>`,
`localhost:<port>`) — defeats DNS rebind. State-changing methods
(POST/PUT/DELETE) and WS upgrade gated; idempotent GET reads not gated
(allows `r1 doctor` health checks without Origin). Items 19-20 wire
two named middleware functions.

### E. Session lifecycle HTTP+WS bodies — STRONG (PASS)

§5.2 has a full CRUD table with exact request/response bodies. §6.1 has
JSON-RPC method equivalents with exact `params` / `result` JSON shapes.
§8 has 7 sub-sections (Create / Attach / Detach / Resume / Pause-Resume
/ Kill / Daemon-restart-resume) each with numbered step lists. Verbatim
state names: `running`, `paused`, `ended`, `paused-reattachable`. Every
mutation flushes journal before broadcast (consistency invariant
documented in §8.1 step 4 and item 24).

### F. Migration plan from existing daemon_cmd.go + agent_serve_cmd.go — PASS

§14 has dedicated subsections (stays / consolidates / new files /
removed). Concrete mapping table at §14.2 (5 rows: old command → new
command + notes). Critical compat preserved: old `/api/...` paths and
old `r1 daemon` / `r1 agent-serve` subcommands stay aliased for one
minor version with a `Deprecation: true` header. **§14.4 explicitly
says nothing is removed** in this iteration — clean migration story.

### G. Hot-upgrade journal replay protocol — PASS

§11 has 7 steps in order (update → graceful drain → re-spawn → scan
sessions-index → re-open journals → rebuild Sessions → broadcast
`daemon.reloaded`). Protocol-version handshake via WS subprotocol
`r1.proto.v1`/`r1.proto.v2` with WS close code 1002 + `migration_hint`
close reason. The `r1 doctor` sub-command detects "installed binary
newer than running daemon" and prompts restart. Journal version field
`v: 1` is enforced; older versions are refused with a `migration_hint`
URL.

---

## Inline fixes applied

1. **§2 line 24** — corrected Go version claim. `go.work` does not
   exist; `go.mod` says 1.25.5. Reframed honestly.
2. **NEW §15 Out of Scope** — 8 items naming what is NOT addressed.
3. **NEW §16 Risks & Mitigations** — 8 risk rows with mitigation
   pointing to spec section or item number.
4. Renumbered the original "Implementation Checklist" header (was the
   trailing block with no number) is unchanged — it lives below the
   new sections.

---

## Verdict

**APPROVE for build** post-inline-fixes. Spec is implementation-grade.
Build can start at Phase A (the os.Chdir audit + lint gate); Phase E
(multi-session enable) blocked until the lint is green. The 55-item
checklist is appropriately sized for a 9-phase rollout (avg ~6 items
per phase).
