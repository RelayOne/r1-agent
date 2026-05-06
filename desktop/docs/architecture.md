# R1 Desktop — Architecture

> Scaffold doc. Final form ships alongside R1D-11 polish.
> Source of truth for scope: `plans/work-orders/work-r1-desktop-app.md` §5.

---

## 1. Process model

The desktop app is **three tiers of process**, communicating over two
distinct IPC channels:

```
┌──────────────────────────────────────────────────────────────┐
│  Tier 1: WebView (TypeScript + React, system-provided engine)│
│    - UI rendering, local UI state (Zustand)                  │
│    - Async data fetching (TanStack Query)                    │
│    - No direct process access, no direct FS access           │
│                                                              │
│                   ↕ Tauri IPC (typed invoke + event)         │
│                                                              │
│  Tier 2: Rust host (Tauri core + sidecar code)               │
│    - Subprocess supervision (r1 per session)                 │
│    - Stdout JSON event parsing + fanout                      │
│    - OS keychain access, notifications, tray, scheduling     │
│    - Credential gatekeeper (never forwards raw secrets       │
│      to the WebView)                                         │
│                                                              │
│                   ↕ stdin/stdout JSON RPC + event stream     │
│                                                              │
│  Tier 3: r1 subprocess(es)                                   │
│    - One per active session                                  │
│    - Unchanged from the CLI runtime                          │
│    - Reads config + ledger + memory from user data dir       │
└──────────────────────────────────────────────────────────────┘
```

### Why a subprocess (not an embedded runtime)?

The desktop app does **not** embed an R1 runtime. It launches the `r1`
binary as a child process and speaks the same stdin/stdout JSON protocol the
CLI and CloudSwarm Temporal workers already speak.

- Zero code duplication between CLI and GUI.
- Every R1 feature the CLI gets is instantly available to the GUI.
- Upgrade path decoupled: ship the binary separately from the GUI; operator
  can upgrade either.
- Crash isolation: an R1 panic does not crash the GUI.
- Reuses the battle-tested supervisor and process-group patterns (SIGTERM →
  SIGKILL, `Setpgid: true`).

### WebView security

- `contextIsolation` enforced via Tauri's capability allowlist
  (`tauri.conf.json` → `app.security.csp`).
- Per-command allowlist in `tauri.conf.json` (Tauri v2 capabilities), not
  blanket invoke access.
- Credentials never cross the WebView boundary; the Rust host signs requests
  on behalf of the WebView and hands results back.
- CSP locked to `script-src 'self'` with an explicit Monaco worker
  exception.

---

## 2. Data sources

### 2.1 Local R1 binary subprocess

Primary data source. The Rust host spawns `r1` (bundled as a Tauri sidecar;
see §6 of the work order) with `--one-shot` or equivalent mode per session.
Events stream out of stdout as NDJSON and fan out to the WebView via
Tauri's typed event bus.

**Bundled binary.** Ships inside the app bundle. Version pinned to the app
release. Operator can override with `settings.advanced.r1_binary_path`.

**Lifecycle.** One r1 process per session. Process group isolation (same
`Setpgid: true` pattern the CLI uses). Supervisor reaps on session close or
app quit.

### 2.2 Local ledger

The desktop app reads the local ledger directly (via r1 RPC, not direct DB
access) for the Ledger browser, verify-chain, and crypto-shred operations.

- **Storage.** SQLite (WAL) + filesystem blobs, exactly as the CLI uses.
- **Location.** User data dir (`~/Library/Application Support/r1/ledger.db`
  on macOS, `%APPDATA%/r1/ledger.db` on Windows, `~/.local/share/r1/ledger.db`
  on Linux).
- **Migration.** On first launch, detects a project-local `.stoke/` dir and
  offers one-click migration to `.r1/` (non-destructive symlink + dual-path
  resolution per `work-r1-rename.md` §S1-5).

### 2.3 Local memory bus

Same transport as the ledger. Read via the `memory.scan(scope)` RPC.
5 scopes: `Session`, `Worker`, `AllSessions`, `Global`, `Always`.

### 2.4 Cloud-sync option (v1.1, out of scope for v1)

**v1: single-operator, all local.** No cloud sync.

**v1.1 (stretch):** optional connection to a remote R1 instance
(team-shared, RelayGate-fronted R1 server). Auth via bearer token from
RelayGate's tenant config. Remote mode is **read-only + send-prompts** — no
remote ledger mutation. Gated on its own follow-up work order; not covered
by this scaffold.

### 2.5 OS keychain (credentials only)

- macOS: Keychain Services.
- Windows: Windows Credential Manager.
- Linux: Secret Service API (GNOME Keyring / KWallet).

Credentials stored under a stable account prefix (`r1.desktop.<credential
name>`). The Rust host is the only tier with keychain access; the WebView
sees credential names only.

---

## 3. UI panel inventory

Matches work order §3. Priorities here reflect build order, not UI
prominence.

| # | Panel | Priority | Package dependencies |
|---|---|---|---|
| 3.1 | **Session view** — chat transcript + composer, SOW tree left, context right | P1 | `plan/`, `intent/`, `workflow/`, `hub/`, `ledger/`, `skill/`, `costtrack/`, `branch/` |
| 3.2 | **Skill catalog browser** — faceted filter, manifest detail drawer | P2 | `skill/`, `skillmfr/`, `skillselect/` |
| 3.3 | **Ledger browser** — session list, timeline, node drawer, verify-chain, crypto-shred | P3 | `ledger/`, `ledger/nodes/`, `ledger/loops/` |
| 3.4 | **Verification descent panel** — T1..T8 grid per AC with evidence drill-down | P4 | `verify/`, `convergence/`, `taskstate/`, `failure/`, `baseline/` |
| 3.5 | **Memory bus viewer** — 5-scope tabs, K/V table, drill-down history | P5 | `memory/`, `wisdom/` |
| 3.6 | **Skills marketplace** — discovery + install for packs (e.g. Actium Studio) | P6 | `skillmfr/`, `skill/` |
| 3.7 | **MCP servers panel** — add/remove/test MCP connections | P7 | `mcp/` |
| 3.8 | **Settings** — profile, providers, credentials, data, privacy, updates, advanced | P7 | `config/`, OS keychain |
| 3.9 | **Observability dashboard** — cost, latency, tokens, errors | P8 | `costtrack/`, `metrics/`, `telemetry/`, `failure/` |

Each panel cites the R1 package(s) that produces its data so a developer
working on a panel can find the ground-truth backing code immediately.

### Layout skeleton (day 1)

```
┌────────────┬────────────────────────────────┬────────────┐
│            │                                │            │
│ Session    │   Chat transcript              │  Active    │
│ list +     │   (tool-call renderings,       │  context   │
│ SOW tree   │    verification gates)         │  panel     │
│            │                                │            │
│            │                                │  Skills    │
│            │                                │  injected, │
│            │                                │  model,    │
│            │                                │  tokens    │
│            │                                │            │
│            ├────────────────────────────────┤            │
│            │   Composer (prompt, mode,      │            │
│            │   attach, submit)              │            │
└────────────┴────────────────────────────────┴────────────┘
```

Other panels (Ledger, Memory, Skills, MCP, Observability, Settings) open as
full-window routes, not sidebars. Keyboard shortcut: `Cmd/Ctrl+K`
switches routes.

---

## 4. RPC surface (initial set)

The Rust host and the WebView agree on a typed RPC over Tauri's `invoke`.
The same verbs round-trip to the r1 subprocess over stdin/stdout JSON.

| Verb | Direction | Notes |
|---|---|---|
| `session.create(opts)` → `session_id` | WebView → host → r1 | Spawns a fresh r1 subprocess |
| `session.send(session_id, prompt)` | WebView → host → r1 | Streams results back as events |
| `session.cancel(session_id)` | WebView → host → r1 | SIGTERM, then SIGKILL after grace period |
| `skill.list()` → `Skill[]` | WebView → host → r1 | Reads `skill.Registry.List()` |
| `skill.get(name)` → `Manifest` | WebView → host → r1 | Reads `skillmfr.Registry` |
| `skill.invoke(session_id, name, input)` | WebView → host → r1 | Isolated test invocation |
| `ledger.list(filter)` → `NodeSummary[]` | WebView → host → r1 | Paginated |
| `ledger.get_node(hash)` → `Node` | WebView → host → r1 | 22 node types from `ledger/nodes/` |
| `ledger.verify_chain(session_id)` → `VerifyResult` | WebView → host → r1 | Re-hashes from genesis |
| `memory.scan(scope)` → `Entry[]` | WebView → host → r1 | 5 scopes |
| `memory.delete(scope, key)` | WebView → host → r1 | Scoped delete |
| `config.get()` → `Config` | WebView → host | No r1 round-trip; host owns config |
| `config.set(patch)` | WebView → host | Persists to `config.json` |

Verbs wire-match `cmd/r1/ctl_cmd.go` IPC where possible; new verbs are
additive and ship behind an `X-R1-RPC-Version: 1` header for future
compatibility.

---

## 5. State + storage layout

```
~/Library/Application Support/r1/      (macOS)
%APPDATA%/r1/                          (Windows)
~/.local/share/r1/                     (Linux)
  ├── config.json                      Operator config (providers, defaults, UI prefs)
  ├── credentials.db                   OS-keychain-backed encrypted store
  ├── ledger.db                        SQLite (WAL mode), shared across sessions
  ├── ledger/                          Filesystem blob store for large nodes
  ├── memory.db                        SQLite + FTS5 across 5 scopes
  ├── schedules.json                   Recurring-task definitions (R1D-10.3)
  ├── sessions/
  │   └── <session_id>/                Per-session subdir
  │       ├── transcript.ndjson
  │       ├── events.log
  │       └── state.json
  └── logs/
      └── desktop.log                  Rust host log + WebView error capture
```

Project-local state still honors `.r1/` (legacy `.stoke/`) per project. The
Desktop app surfaces a project picker and migration prompt on startup.

---

## 6. Open questions (inherited from work order §9)

None resolved in this scaffold. Re-state here for reference; each needs a
decision before the relevant phase ships.

- **§9.1 Licensing** — default Option A (open source, free, no gate). Revisit
  after MAU threshold.
- **§9.2 Telemetry** — opt-in, default off. Honest disclosure in onboarding.
- **§9.3 Competitive tracking** — monthly parity review in `docs/`.
- **§9.4 Remote R1 timing** — local-only v1; team-mode follow-up work order.
- **§9.5 Plugin ecosystem** — panel plugins OUT for v1; skill packs IN.
- **§9.6 Data residency** — offline-mode toggle needed in Settings for v1.
- **§9.7 OS minimums** — macOS 11+, Windows 10 1809+ (WebView2), Linux
  glibc 2.31+.

---

## 7. What this document is not

- Not a design system spec — that ships as part of R1D-2.
- Not a wire protocol spec — r1's existing stdin/stdout JSON protocol is
  the ground truth.
- Not a distribution guide — that ships under R1D-11/R1D-12.

See `plans/work-orders/work-r1-desktop-app.md` for the full scope.
