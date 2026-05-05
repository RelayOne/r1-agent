# R1 Desktop App

> **This is a SCAFFOLD.** Full implementation: see
> `plans/work-orders/work-r1-desktop-app.md`.
> **Status: not yet buildable; scoping complete.**
> Filed 2026-04-23.

R1 Desktop is a cross-platform desktop GUI wrapping R1's existing Go binary
(`stoke` → `r1`). Operator runs sessions, composes plans, inspects agent
reasoning, manages skills, and browses ledgers — all without dropping to the
CLI. The CLI remains canonical for scripting and CI; the GUI is the
operator-facing surface.

## One-line positioning

**The visible face of R1's agentic runtime for a single operator or small
team — transparent SOW + verification descent + cryptographic ledger, baked
in.**

## R1's differentiators, surfaced as first-class UI

| Panel | What it shows | Backed by |
|---|---|---|
| **SOW tree** | Live task decomposition tree with per-node status, cost, skill selection | `plan/`, `intent/`, `workflow/`, `scheduler/` |
| **Verification descent ladder** | T1..T8 tier grid per acceptance criterion with evidence drill-down | `verify/`, `convergence/`, `taskstate/`, `failure/` |
| **Ledger browser** | Content-addressed append-only event log with verify-chain and crypto-shred | `ledger/`, `ledger/nodes/`, `ledger/loops/` |
| **Memory bus viewer** | 5-scope key/value inspector (Session/Worker/AllSessions/Global/Always) | `memory/`, `wisdom/` |
| **Skill catalog + marketplace** | Faceted browser over installed skills and installable packs | `skill/`, `skillmfr/`, `skillselect/` |
| **MCP servers panel** | Manage MCP connections, test tool invocations | `mcp/` |
| **Observability dashboard** | Cost, latency, token use, per-provider and per-skill | `costtrack/`, `metrics/`, `telemetry/` |

## Target users

- **Primary:** individual operators running long-horizon agent tasks
  (researchers, consultants, solo founders, indie builders).
- **Secondary:** small teams (2-10) where one operator drives R1 and
  teammates view read-only session replays.
- **Tertiary:** enterprise evaluators auditing R1 before CloudSwarm rollout.

## Stack

- **Tauri v2** (Rust sidecar + system WebView + TypeScript frontend).
  Rejected Electron: 10-15 MB binary vs 120-180 MB; 50-80 MB RAM vs
  200-400 MB.
- **React + TypeScript + Vite + Tailwind + shadcn/ui** inside the WebView.
- **Zustand + TanStack Query** for state.
- **Monaco** for rich text (plan editing, skill manifest preview, JSON
  schema input).

Full stack rationale: `docs/architecture.md` §2.

## Architecture (one diagram)

```
┌──────────────────────────────────────────────────┐
│  R1 Desktop (Tauri app)                          │
│  ┌────────────────────────────────────────────┐  │
│  │ WebView (React + TS)                       │  │
│  │   ↕  Tauri event system (typed)            │  │
│  │ Rust sidecar                               │  │
│  │   ↕  stdin/stdout JSON RPC + event stream  │  │
│  │ r1 subprocess(es) — one per active session │  │
│  └────────────────────────────────────────────┘  │
└──────────────────────────────────────────────────┘
```

The desktop app **does not embed** an R1 runtime in-process. It launches the
`r1` binary as a child process and communicates via the existing
stdin/stdout JSON event protocol — the same interface CloudSwarm's Temporal
workers use. Zero code duplication between CLI and GUI.

- **Cortex augmentation** (`specs/desktop-cortex-augmentation.md`):
  the desktop now treats `r1 serve` as the primary transport via
  `tauri-plugin-websocket`, with the bundled per-OS sidecar binary
  as fallback. New cortex-aware UI primitives (LaneSidebar, lane
  pop-out windows, daemon discovery wizard, native menu, auto-start)
  layer on top of the original 12 R1D phases without rewriting any
  R1D-* file. Components consumed via the workspace package
  `@r1/web-components` so the upcoming web surface (spec 6) shares
  the same lane render path.

Full architecture: `docs/architecture.md`.

## Roadmap

See `PLAN.md` for the full 12-phase roadmap (`R1D-1` through `R1D-12`) with
deliverables, acceptance criteria, and effort estimates.

**Critical path:**
`R1D-1 → R1D-2 → (R1D-3 ‖ R1D-4) → R1D-5 + R1D-6 → R1D-7 + R1D-8 → R1D-9 → R1D-10 → R1D-11 → R1D-12`

Total: ~13-15 weeks for one senior engineer; ~6-8 weeks with two.

## Current state of this scaffold

- `Cargo.toml` / `src-tauri/tauri.conf.json` / `package.json` placeholders
  present.
- `cargo tauri dev` does not yet run; `npm run dev` fails cleanly with a
  "scaffold only" message.
- No Rust code yet in `src-tauri/src/`.
- No frontend code yet in `src/`.
- No icons yet in `src-tauri/icons/`.
- Bundled R1 binary path not yet wired (Tauri sidecar config empty).

**Next action:** pick up at `PLAN.md` §R1D-1.1 (`cargo tauri init` in this
directory). The placeholders already reserve the expected filenames so
`cargo tauri init` can be directed to merge into them.

## Contributing

Desktop work is dispatched to the R1 team. Until R1D-1 lands, this directory
stays read-only scaffold. See `plans/work-orders/work-r1-desktop-app.md` in
the plans repo for the full scope.

## License

MIT (inherits from the R1 repo root).
