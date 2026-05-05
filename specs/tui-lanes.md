<!-- STATUS: done -->
<!-- CREATED: 2026-05-02 -->
<!-- BUILD_COMPLETED: 2026-05-03 -->
<!-- DEPENDS_ON: lanes-protocol -->
<!-- BUILD_ORDER: 4 -->

# TUI Lanes Panel — Implementation Spec

## Overview

A new Bubble Tea v2 panel inside the existing `internal/tui/` that renders cortex
"lanes" (per-Lobe / per-stance / per-task concurrent thinking threads) as
first-class UI primitives. Layout adapts to terminal width: side-by-side columns
when wide, vertical stack when narrow. Each lane is rendered to a cached string;
only lanes whose state changed are re-rendered between frames. Keybindings cover
jump-to-lane, cycle, focus-mode (65/35 main+peers), kill, and help. The panel
composes with the existing `internal/tui/interactive.go` model (`d`/`f`/`enter`/
`esc` modes) and runs from the same `internal/tui/runner.go` runtime entrypoint.
It subscribes to the lanes-protocol (spec 3) over local IPC when running embedded
(`r1 chat-interactive`) or over WebSocket when connecting to a remote `r1d`.

The panel is the canonical realtime example for r1: a single fan-in
`chan laneTickMsg` plus a `waitForLaneTick` `tea.Cmd` re-armed on every receive.
A producer goroutine coalesces upstream events at 200–300 ms windows so we never
push more than ~5 Hz into Bubble Tea even when upstream is firing at 200 Hz.

## Stack & Versions

Exact module paths (May 2026, lipgloss v2 moved to `charm.land/...`):

| Concern | Module | Version pin |
|---------|--------|-------------|
| Runtime | `github.com/charmbracelet/bubbletea/v2` | `v2.0.0-beta.4` |
| Components | `github.com/charmbracelet/bubbles/v2` | `v2.0.0-beta.3` |
| Styling | `charm.land/lipgloss/v2` | `v2.0.0-beta.5` |
| AdaptiveColor compat | `charm.land/lipgloss/v2/compat` | (transitive) |
| Layout grid | `github.com/winder/bubblelayout` | `v1.4.0` |
| Test harness | `github.com/charmbracelet/x/exp/teatest/v2` | latest pinned-SHA at PR time |
| Key bindings | `github.com/charmbracelet/bubbles/v2/key` | (transitive) |

Bubbles v2 sub-packages used: `spinner`, `progress`, `viewport`, `key`, `help`.

The existing `internal/tui/renderer/` already pins `bubbletea` v1 + `lipgloss` v1.
This spec's new `internal/tui/lanes/` package introduces v2. **Do not migrate
`renderer/` or `interactive.go` to v2 in this spec** — coexistence is fine
because v2 module paths differ. A follow-on spec can migrate the legacy code.

## Existing Patterns to Follow

- **Channel-fan-in + re-armed `tea.Cmd`** for streaming updates. Match the shape
  of `tickCmd()` in `internal/tui/interactive.go:105-107`, but for a typed
  `laneTickMsg` channel rather than a `time.Tick`.
- **`m.mu sync.Mutex`** wraps shared state inside the model, even though Bubble
  Tea's Update loop is single-threaded — the `Send*()` helpers in `interactive.go`
  (lines 427–449) write from outside the loop. Same pattern here for the
  embedded-mode IPC subscriber.
- **Public `Send*()` package functions** (`interactive.go:427-448`) wrap
  `p.Send(msg)`. Mirror this — `lanes.SendLaneStart`, `lanes.SendLaneTick`,
  `lanes.SendLaneEnd` etc.
- **`tea.WindowSizeMsg`** stored on the model (`interactive.go:163-165`); width
  is the trigger for layout-mode reselection.
- **Mode switching by single keystroke** (`d`/`f`/`enter`/`esc` in
  `interactive.go:113-141`). Reuse the same vocabulary; new keys (`1`–`9`, `K`,
  `?`) extend rather than replace.
- **lipgloss styles defined as package vars** (`interactive.go:241-249`). Keep
  v2 styles in their own file `lanes_styles.go`.
- **NO_COLOR / `TERM=dumb`** auto-handling via lipgloss — do not branch on env
  manually.
- **Renderer cache pattern** — `internal/tui/renderer/renderer.go` already does
  per-section diffed rendering (event scroll ring buffer). Same idea applied
  per-lane.

## New Files

| File | Responsibility |
|------|----------------|
| `internal/tui/lanes/lanes.go` | `Model`, `Init/Update/View`, `tea.Msg` types, channel fan-in, IPC subscriber goroutine, render-cache map |
| `internal/tui/lanes/lanes_view.go` | Pure rendering: `viewOverview`, `viewFocus`, `viewStatusBar`, `renderLane`. No state mutation. |
| `internal/tui/lanes/lanes_keys.go` | `keyMap` struct, `key.NewBinding` definitions, `help.KeyMap` impl, kill-confirm modal helpers |
| `internal/tui/lanes/lanes_layout.go` | Adaptive layout decision (`bubblelayout` `Wrap`/`Cell`/`Grow` declarations + `decideMode(width, n) layoutMode`) |
| `internal/tui/lanes/lanes_cache.go` | `renderCache` map[string]string + dirty-bit set, `Invalidate`/`Get`/`Put` |
| `internal/tui/lanes/lanes_styles.go` | lipgloss v2 `Style` + `compat.AdaptiveColor` palette, glyph table |
| `internal/tui/lanes/lanes_test.go` | `teatest` snapshots + key-event tests (one test per layout mode + focus state) |
| `internal/tui/lanes/testdata/*.golden` | Golden ANSI output for each snapshot |

The package must compile with `go build ./internal/tui/lanes/` and pass
`go vet ./internal/tui/lanes/`. It must not import `internal/tui/renderer/`
(different Bubble Tea major version).

## Component Model

### `tea.Msg` types (concrete)

```go
// laneTickMsg is the single fan-in message. Producer batches upstream events
// and sends one of these every 200–300 ms per active lane that changed.
type laneTickMsg struct {
    LaneID    string
    Activity  string        // single-line; truncated by renderer
    Tokens    int
    CostUSD   float64
    Status    LaneStatus
    Model     string        // e.g. "haiku-4.5"
    Elapsed   time.Duration
    Err       string        // empty unless StatusError
}

type laneStartMsg struct {
    LaneID    string
    Title     string        // human label, e.g. "memory-recall"
    Role      string        // stance/lobe role
    StartedAt time.Time
}

type laneEndMsg struct {
    LaneID  string
    Final   LaneStatus      // Done | Errored | Cancelled
    CostUSD float64
    Tokens  int
}

type laneListMsg struct {
    Lanes []LaneSnapshot    // initial replay on subscribe / reconnect
}

// killAckMsg confirms r1d accepted a kill; UI can clear confirm modal.
type killAckMsg struct {
    LaneID string
    Err    string
}

type budgetMsg struct {
    SpentUSD float64
    LimitUSD float64
}
```

### Status enum

```go
type LaneStatus int8

const (
    StatusPending LaneStatus = iota
    StatusRunning
    StatusBlocked
    StatusDone
    StatusErrored
    StatusCancelled
)
```

### Model struct

```go
type Model struct {
    // Identity / config
    sessionID string
    transport Transport       // interface: LocalIPC | WebSocket
    sub       chan laneTickMsg
    cancel    context.CancelFunc

    // Lane state (ordered by createdAt then LaneID)
    lanes      []*Lane
    laneIndex  map[string]int

    // Layout
    width, height int
    mode          layoutMode    // overview | focus
    focusID       string
    cursor        int
    cols          int           // computed grid columns

    // Render cache
    cache *renderCache          // map[laneID]string + dirty set

    // bubblelayout
    layout bubblelayout.BubbleLayout

    // Components
    spinner spinner.Model
    budget  progress.Model
    vp      viewport.Model      // for focused-lane scrolling activity
    help    help.Model
    keys    keyMap

    // Modal state
    confirmKill string            // non-empty laneID = awaiting "y" or any other = cancel
    confirmAll  bool              // K pressed; awaiting double "y" "y"

    // Aggregate
    totalCost   float64
    totalTurns  int
    totalLanes  int
    currentModel string
    budgetLimit  float64
}

type Lane struct {
    ID         string
    Title      string
    Role       string
    Status     LaneStatus
    Activity   string
    Tokens     int
    CostUSD    float64
    Model      string
    Elapsed    time.Duration
    Err        string
    StartedAt  time.Time
    EndedAt    time.Time
    Dirty      bool          // set on any field write; cache uses to decide re-render
}
```

### Transport interface

```go
// Transport feeds laneTickMsg into the model's sub channel. Two impls:
//   - localIPCTransport: dial unix socket / Windows named pipe at ~/.r1/r1d.sock
//     (matches D-D3 from decisions/index.md)
//   - wsTransport: dial ws://127.0.0.1:<port> using Sec-WebSocket-Protocol bearer
type Transport interface {
    // Subscribe streams lane events into out until ctx is cancelled.
    // Must replay the full current lane list on first connect (laneListMsg).
    // On reconnect, must replay from Last-Event-ID (lanes-protocol §reconnect).
    Subscribe(ctx context.Context, sessionID string, out chan<- laneTickMsg) error

    // Kill issues r1.lanes.kill RPC; returns nil on accept.
    Kill(ctx context.Context, laneID string) error
    KillAll(ctx context.Context) error
}
```

### waitForLaneTick — the canonical realtime cmd

```go
func (m *Model) waitForLaneTick() tea.Cmd {
    return func() tea.Msg { return <-m.sub }
}

func (m *Model) Init() tea.Cmd {
    go m.runProducer()                       // coalescer: upstream → m.sub @ 200–300 ms
    return tea.Batch(
        m.spinner.Tick,
        m.waitForLaneTick(),                 // re-armed in Update on every receive
    )
}
```

In `Update`: every time a `laneTickMsg`, `laneStartMsg`, `laneEndMsg`, or
`laneListMsg` lands, after applying the change, the returned cmd batches
`m.waitForLaneTick()` again. `m.sub` is **never closed by Update**; only by
`runProducer` on context cancel, in which case the final receive returns the
zero value and the model treats `LaneID == ""` as a no-op.

## Layout Algorithm

Adaptive column-vs-stack decision (lanes-protocol-aware, n = number of lanes):

```
LANE_MIN_WIDTH = 32   // border + 2-col padding + ~28 cols of content
COLS_MAX       = 4    // never exceed 4 columns; 5+ degrades readability

decideMode(width, height, n, mode) -> (cols, mode):
  if n == 0:
    return 1, modeEmpty

  if mode == modeFocus and len(focused-stack) > 0:
    // tmux main-vertical: focused 65%, peers stacked at 35% / N
    main_w = floor(0.65 * width)
    peer_w = width - main_w - 1   // gap
    return 1, modeFocus           // cols irrelevant; renderer hardcodes 2-region

  cols_can_fit = floor(width / LANE_MIN_WIDTH)
  cols_can_fit = clamp(cols_can_fit, 1, min(COLS_MAX, n))

  if cols_can_fit < 2:
    return 1, modeStack           // narrow terminal: vertical list
  return cols_can_fit, modeColumns
```

`bubblelayout` declarations for `modeColumns` (n=4 lanes example):

```
layout := bubblelayout.New()
laneIDs := layout.Wrap(
    layout.Add("0:0,1:0 grow"),  // row 0 col 0
    layout.Add("0:1,1:1 grow"),  // row 0 col 1
    layout.Add("0:2,1:2 grow"),  // row 0 col 2
    layout.Add("0:3,1:3 grow"),  // row 0 col 3
)
statusID := layout.Add("0:0..3,2 height 1")
```

For `modeStack` use a single column; `bubblelayout.Wrap` produces N rows of
`grow` cells. `tea.WindowSizeMsg` is forwarded via `layout.Resize(w, h)` and the
returned per-cell `BubbleLayoutMsg`s are stored on `Model` so each lane knows its
exact box.

`modeFocus` does **not** use `bubblelayout`; it's a hand-rolled
`lipgloss.JoinHorizontal(Top, focusedBox, peerStack)` because the 65/35 split is
fixed and changes only on resize.

## Render-Cache Contract

`renderCache` lives in `lanes_cache.go`:

```go
type renderCache struct {
    s       map[string]string   // laneID -> rendered string
    dirty   map[string]struct{} // laneIDs needing re-render
    width   map[string]int      // last cell width used; resize invalidates
}

func (c *renderCache) Invalidate(laneID string)
func (c *renderCache) Get(laneID string, width int) (string, bool)
func (c *renderCache) Put(laneID string, width int, s string)
```

A lane's cached string is invalidated when **any** of the following occur:

1. Its `Lane.Dirty` flag is true (set on any field write in Update).
2. `tea.WindowSizeMsg` changes the cell width assigned to that lane (compare
   `width[laneID]` against new BubbleLayoutMsg).
3. The lane's status transitions Pending↔Running↔Blocked↔Done↔Errored↔Cancelled
   (border weight or glyph changes).
4. Focus moves on or off the lane (border style differs in focused vs peer).
5. Spinner frame ticks **and** `Status == StatusRunning` (only one tick per
   coalesce window — matches the 200–300 ms producer cadence).
6. Cache is dropped entirely on `tea.WindowSizeMsg` if `width` or `cols` changed.

`View()` iterates lanes in stable order (createdAt asc, laneID tiebreak), calls
`cache.Get`, falls through to `renderLane()` and `cache.Put` on miss. Status bar
is **not** cached — it always re-renders (cheap, single line).

Anti-pattern explicitly rejected: rebuilding all lane strings on every
`laneTickMsg`. The `Dirty` flag is the sole cache-invalidation trigger for
data-driven changes. Per-token repaint is forbidden (D-S2).

## Keybinding Map

| Key | Mode | Action |
|-----|------|--------|
| `1`–`9` | overview, focus | Jump cursor to lane N (creation order); enter focus mode if already on N |
| `tab` | overview | Cycle cursor forward; wraps |
| `shift+tab` | overview | Cycle cursor backward; wraps |
| `j` / `↓` | overview | Cursor down |
| `k` / `↑` | overview | Cursor up |
| `j` / `↓` | focus | Scroll activity viewport down |
| `k` / `↑` | focus | Scroll activity viewport up |
| `enter` | overview | Enter focus mode on cursor lane |
| `esc` | focus | Return to overview |
| `k` | focus or overview | Arm kill-confirm for cursor/focused lane |
| `y` | kill-confirm | Confirm kill (calls `transport.Kill`) |
| any other | kill-confirm | Cancel kill-confirm |
| `K` | overview | Arm kill-all-confirm |
| `y` then `y` | kill-all-confirm | Double-confirm; calls `transport.KillAll` |
| `?` | any | Toggle help overlay (`bubbles/v2/help`) |
| `q` / `ctrl+c` | any | Quit panel (does not kill lanes; just disconnects) |
| `r` | any | Force re-render (drops cache; debug aid) |
| `L` | any | Toggle lanes panel visibility (when composed in interactive.go) |

The `k` collision (kill vs cursor-up) is resolved by mode:

- In **overview** mode the cursor uses `k`/`↑`. Kill is `K` (shift) or `x` as
  alias for accessibility — bind both.
- In **focus** mode `j`/`k` move the viewport; `k` does not kill. Kill is `x`
  in focus mode.
- We deliberately bind `x` as a kill alias in **both** modes (claude-squad
  precedent: multiple keys for the same focus action).

`bubbles/v2/key` declarations live in `lanes_keys.go`. Help text is generated
via `help.KeyMap.ShortHelp()` / `FullHelp()`.

## Status Bar Layout

Single line, always rendered, lives at the bottom row of the panel:

```
 ⚡ r1 lanes  [4 active 1 done 0 err]  $0.0837 / $1.00 [████░░░░░░]  42 turns  haiku-4.5  ?=help
```

Layout rules:

- Left segment: `⚡ r1 lanes` (title) + `[N active M done X err]` counts (status
  vocabulary glyphs).
- Center segment: `$spent / $limit` + 10-char `progress.Model` bar; bar color
  shifts at 70% (yellow) and 90% (red) using `compat.AdaptiveColor`.
- Right segment: turns count, current model name (most recent
  `laneTickMsg.Model`), and help hint.
- Truncation: when `width < 80`, drop in this order: model name, turns,
  help hint. Below `width = 50`, collapse to `[N a M d X e] $cost`.

The status bar is rendered fresh every frame (no cache). It depends on
aggregated counters (`totalCost`, `totalLanes`, etc.) updated in Update.

## Subscription Wiring

Embedded mode (`r1 chat-interactive --lanes`):

1. `runner.go` constructs `lanes.Model` with `transport = localIPCTransport{}`.
2. `localIPCTransport.Subscribe` dials `~/.r1/r1d.sock` (D-D3, D-D5 from
   `docs/decisions/index.md`). If the socket is missing, the transport spins up
   an in-process bus shim that reads from the cortex Workspace directly (so
   `r1 chat-interactive` works without `r1d`).
3. JSON-RPC 2.0 envelope; method `lanes.subscribe` returns NDJSON stream of
   lane events. Decoder maps to `laneTickMsg` etc. and pushes to `m.sub`.

Remote mode (TUI connecting to running daemon):

1. `transport = wsTransport{addr: "ws://127.0.0.1:<port>"}`.
2. Bearer token via `Sec-WebSocket-Protocol: ["r1.bearer", token]` (D-S6).
3. Reconnect with `Last-Event-ID` header on disconnect (RT-R1D-DAEMON).

The producer goroutine (`runProducer`) wraps `Transport.Subscribe` with a
200–300 ms coalesce window: it holds a `map[laneID]laneTickMsg`, overwriting on
each upstream event; on the timer tick, it flushes all queued lanes into
`m.sub` and resets the map. Status changes (Pending→Running→Done) bypass the
coalescer (sent immediately) so transitions feel snappy.

## Boundaries — What NOT To Do

- Do **not** modify `internal/tui/renderer/`, `interactive.go`, or `runner.go`
  beyond a thin `Mount()` hook that lets them embed the lanes panel. Any change
  to renderer events or interactive modes must go in a separate spec.
- Do **not** import `bubbletea` v1 from the new package; do not import
  `bubbletea/v2` from existing code in this PR.
- Do **not** ship a `tea.ClearScreen` anywhere. Bubble Tea v2 diffing handles it.
- Do **not** recompute lane strings from raw events in `View()`. Cache or bust.
- Do **not** branch on `os.Getenv("NO_COLOR")` directly — lipgloss handles it.
- Do **not** reflow lane bodies on every resize; clip + scroll instead
  (RT-TUI-LANES tmux warning).
- Do **not** hold `m.mu` across a `tea.Cmd` invocation; copy values out first.
- Do **not** use `bubbles/table` for the lane grid; manual `lipgloss.JoinX`.
- Do **not** persist lane state to disk from this package; that's r1d's job.
- Do **not** introduce a second goroutine per lane — one producer goroutine
  total fans in from upstream.

## Testing

Stack: `teatest` v2 (`charmbracelet/x/exp/teatest/v2`) for golden snapshots and
key-event injection.

### Unit tests (`lanes_test.go`)

- [ ] `TestDecideMode_NarrowWidth` → `width=60, n=4` → `cols=1, mode=stack`.
- [ ] `TestDecideMode_WideWidth` → `width=200, n=4` → `cols=4, mode=columns`.
- [ ] `TestDecideMode_FocusOverride` → focus mode forces `mode=focus` regardless of width.
- [ ] `TestRenderCache_DirtyInvalidates` → set `Dirty=true`, `Get` returns miss.
- [ ] `TestRenderCache_WidthChangeInvalidates` → changing cell width drops entry.
- [ ] `TestRenderCache_StatusChangeInvalidates` → Pending→Running drops entry.

### teatest snapshot tests

Each snapshot uses `teatest.NewTestModel`, sends a fixed sequence of
`laneStartMsg`/`laneTickMsg`/key events, calls `tm.Output()` after a short
settle, and compares against `testdata/<name>.golden` via
`teatest.RequireEqualOutput`.

- [ ] `TestSnapshot_Empty` — no lanes, `width=120`. Golden = empty status bar only.
- [ ] `TestSnapshot_StackMode` — 3 lanes, `width=60`. Golden = vertical stack.
- [ ] `TestSnapshot_ColumnsMode_2` — 2 lanes, `width=80`. Golden = 2-col grid.
- [ ] `TestSnapshot_ColumnsMode_4` — 4 lanes, `width=160`. Golden = 4-col grid.
- [ ] `TestSnapshot_FocusMode` — 4 lanes, `enter` on lane 2, `width=140`. Golden = 65/35.
- [ ] `TestSnapshot_KillConfirm` — `x` then snapshot before `y`. Golden shows modal.
- [ ] `TestSnapshot_KillAllConfirm` — `K` then snapshot. Golden shows double-confirm.
- [ ] `TestSnapshot_HelpOverlay` — `?` toggles. Golden shows help.
- [ ] `TestSnapshot_NoColor` — env `NO_COLOR=1`. Golden has zero ANSI escapes.
- [ ] `TestSnapshot_StatusGlyphs` — each of 6 statuses present at least once.

### Behavioral tests

- [ ] `TestKeybinding_JumpToLane` — `3` moves cursor to lane index 2.
- [ ] `TestKeybinding_TabCycle` — `tab` past last wraps to first.
- [ ] `TestKeybinding_KillFlow` — `x` then `y` calls `transport.Kill(laneID)`.
- [ ] `TestKeybinding_KillCancel` — `x` then any-other-key cancels modal.
- [ ] `TestProducer_Coalesce` — fire 100 upstream events in 50 ms; `m.sub`
      receives ≤1 per lane in the next 300 ms window.
- [ ] `TestProducer_StatusBypass` — status change bypasses coalescer.
- [ ] `TestReconnect_ReplaysLaneList` — disconnect then reconnect; first message
      is `laneListMsg` with last known state.

### Manual / integration

- [ ] Run `r1 chat-interactive --lanes` against a live cortex with 4 active
      Lobes; confirm no flicker on a 200 Hz token stream.
- [ ] Resize terminal from 200 → 60 cols; confirm graceful columns→stack flip
      with no panic.
- [ ] `NO_COLOR=1 TERM=dumb r1 chat-interactive --lanes` — confirm glyphs
      remain readable, ANSI absent.

## Acceptance Criteria

- WHEN `width >= cols * 32` and lane count `n >= 2` THE SYSTEM SHALL render
  lanes in a horizontal grid with up to 4 columns.
- WHEN `width < 64` THE SYSTEM SHALL render lanes as a single vertical stack.
- WHEN the user presses `enter` on a lane THE SYSTEM SHALL render that lane at
  ~65% width with peers stacked on the right at ~35%.
- WHEN a lane's data is unchanged between frames THE SYSTEM SHALL reuse the
  cached render string for that lane.
- WHEN a single upstream lane emits 200 events per second THE SYSTEM SHALL emit
  at most 5 `laneTickMsg` per second per lane to the model.
- WHEN the user presses `x` then `y` on a focused lane THE SYSTEM SHALL invoke
  `transport.Kill(laneID)` exactly once.
- WHEN `NO_COLOR=1` THE SYSTEM SHALL render zero ANSI color escape sequences,
  and status SHALL still be unambiguous via glyph.
- WHEN the daemon connection drops and reconnects THE SYSTEM SHALL replay the
  full lane list within one frame of reconnect.

## Risks & Mitigations

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Bubble Tea v2 / bubbles v2 / lipgloss v2 are still beta-tagged in May 2026; minor releases may break golden snapshots. | Medium | Snapshot churn, CI flakes. | Pin exact `v2.0.0-beta.N` versions in `go.mod`; gate snapshot tests behind a `SNAPSHOTS=1` env so CI failures are gated and reviewable; bump beta SHA in a dedicated commit. |
| `winder/bubblelayout` is a small third-party module; upstream stalls could leave us blocked. | Low | Layout regressions on resize. | Vendor-friendly (single file), <500 LOC; if it stalls we can fork into `internal/tui/lanes/layout/`. `modeFocus` already bypasses it. |
| Coalescing window (200–300 ms) hides true latency from operators debugging the cortex. | Medium | Misleading "feel" during debug. | Status transitions bypass coalescer; `r` (force re-render) drops cache; debug build flag `--lanes-flush=10ms` lowers the window. |
| `k` / `K` / `x` rebinding may collide with future global keymap changes in `interactive.go`. | Low | UX confusion. | Mode-scoped bindings, documented in §Keybinding map; `bubbles/v2/help` surface makes them discoverable. |
| Render-cache bug could ship a stale string when a status changes via a non-Update path. | Medium | Wrong visual state. | All lane mutation goes through `Update`; setters set `Dirty=true`; explicit cache invalidation rules (§Render-cache contract). Behavioral test asserts status-change invalidation. |
| Local IPC vs WS code paths drift, leaving one transport untested. | Medium | Silent regression on either CLI or remote. | Both transports share the same NDJSON decoder and `Transport` interface; both are exercised in `lanes_test.go` (mocked dialer). |
| `NO_COLOR` / `TERM=dumb` regress on minor lipgloss updates. | Low | Poor terminal compatibility. | Snapshot test `TestSnapshot_NoColor` asserts zero ANSI; paired-glyph rule means status remains legible without color (accessibility). |
| Producer goroutine leak on `tea.Quit` if `cancel()` not called. | Medium | Goroutine + socket leak in tests. | `Init` returns `cleanup func()`; `Mount` wires it through; test `TestProducer_StopOnCancel` asserts no goroutine remains after ctx cancel. |

## Implementation Checklist

1. [ ] Add `internal/tui/lanes/` package directory; `package lanes`; doc.go
       describing the spec ref + boundaries.
2. [ ] Add module dependencies — `bubbletea/v2`, `bubbles/v2`, `lipgloss/v2`,
       `winder/bubblelayout` — to `go.mod`. Run `go mod tidy`.
3. [ ] Define `LaneStatus` enum + `String()` + glyph table in `lanes_styles.go`
       (StatusPending=`·`, StatusRunning=`▸`, StatusBlocked=`⏸`, StatusDone=`✓`,
       StatusErrored=`✗`, StatusCancelled=`⊘`). Pair each with
       `compat.AdaptiveColor` per RT-TUI-LANES Tokyo Night palette.
4. [ ] Define all `tea.Msg` types in `lanes.go`: `laneTickMsg`, `laneStartMsg`,
       `laneEndMsg`, `laneListMsg`, `killAckMsg`, `budgetMsg`, plus
       `windowChangedMsg` for layout recalc.
5. [ ] Define `Lane` struct with `Dirty bool` field; setters that flip `Dirty=true`.
6. [ ] Define `Model` struct with all fields enumerated in §Component model.
7. [ ] Implement `New(sessionID string, t Transport, opts ...Option) *Model` —
       initialize spinner (Pulse), progress bar, viewport, help, keys, cache.
8. [ ] Implement `Transport` interface and both concrete transports:
       `localIPCTransport` dials `~/.r1/r1d.sock` (unix) or
       `\\.\pipe\r1d` (Windows), reads NDJSON frames, decodes the
       lanes-protocol envelope (`{type, lane_id, seq, data}` per spec 3
       §envelope), and pushes typed messages to `out`. `wsTransport`
       dials `ws://127.0.0.1:<port>` with the
       `Sec-WebSocket-Protocol: ["r1.bearer", token]` handshake (D-S6),
       performs the same decode, and on disconnect reconnects with the
       `Last-Event-ID` header carrying the last received `seq`. Both
       transports implement `Kill(ctx, laneID)` and `KillAll(ctx)` by
       sending the `r1.lanes.kill` / `r1.lanes.killAll` JSON-RPC
       requests defined in lanes-protocol §RPC. Envelope schema details
       (field names, error codes, replay semantics) are owned by
       lanes-protocol; this spec consumes them as-shipped.
9. [ ] Implement `runProducer(ctx)`: 250 ms ticker; coalesce map keyed by
       `LaneID`; status changes bypass; flush map → `m.sub` on tick; close on
       ctx.Done().
10. [ ] Implement `waitForLaneTick` cmd; re-arm in every `Update` branch that
        consumes from `m.sub`.
11. [ ] Implement `Init()` → `tea.Batch(spinner.Tick, waitForLaneTick())`;
        spawn `runProducer` from a `tea.Cmd` (or before `tea.NewProgram` runs,
        depending on lifecycle).
12. [ ] Implement `Update` for every msg type; mutate state; set `Dirty`;
        invalidate cache; return re-armed cmds.
13. [ ] Implement `tea.WindowSizeMsg` handling: store `width`/`height`, call
        `decideMode`, if `cols` or `mode` changed reset `m.cache` entirely,
        forward to `bubblelayout.Resize`.
14. [ ] Implement `View()` dispatch: `viewEmpty` / `viewOverview` / `viewFocus`
        + always-on `viewStatusBar`; help overlay rendered on top via
        `lipgloss.Place`.
15. [ ] Implement `viewOverview`: iterate lanes in stable order, render each via
        cache or `renderLane`, join with `bubblelayout` cells in `modeColumns`
        or vertical join in `modeStack`.
16. [ ] Implement `viewFocus`: hand-rolled 65/35 horizontal join; focused lane
        uses `viewport.Model` for activity log; peers use `renderLanePeer`
        (single-line summary).
17. [ ] Implement `renderLane(lane, width, focused) string`: bordered box with
        title row (glyph + spinner if Running + name + role), 1-line activity
        (truncate with `…`), footer (`tokens • $cost • elapsed • model`).
18. [ ] Implement `renderLanePeer(lane, width) string`: 1-line row, no border,
        `glyph name … cost`.
19. [ ] Implement `viewStatusBar(width)`: 3-segment layout with truncation
        ladder per §Status bar layout.
20. [ ] Implement `lanes_cache.go` with `renderCache`, `Invalidate`, `Get`,
        `Put`; clear-on-resize helper.
21. [ ] Implement `lanes_layout.go` `decideMode(width, n, currentMode)
        (cols int, mode layoutMode)` per §Layout algorithm pseudo-code.
22. [ ] Implement `lanes_keys.go` `keyMap` with `key.NewBinding` for every row
        in §Keybinding map; implement `help.KeyMap` interface.
23. [ ] Implement kill-confirm modal: state on `Model.confirmKill string` and
        `Model.confirmAll bool`; `View` overlays modal via `lipgloss.Place`
        when set.
24. [ ] Implement `Send*()` helpers exported for `runner.go` / `interactive.go`
        composition: `SendLaneStart`, `SendLaneTick`, `SendLaneEnd`,
        `SendLaneList`, `SendBudget`. Each wraps `p.Send(msg)`.
25. [ ] Implement `Mount(parent *tea.Program) (subModel tea.Model, cleanup func())`
        — composition hook so `interactive.go` can embed the panel under an `L`
        toggle without circular import.
26. [ ] Wire `runner.go` flag `--lanes` (off by default); when set, attach
        `lanes.Model` and route `lanes.Send*` from the existing event router.
27. [ ] Add `cmd/r1/main.go` `--lanes` passthrough on `r1 chat-interactive`
        only (other commands ignore).
28. [ ] Snapshot tests: `TestSnapshot_Empty`, `TestSnapshot_StackMode`,
        `TestSnapshot_ColumnsMode_2`, `TestSnapshot_ColumnsMode_4`,
        `TestSnapshot_FocusMode`, with golden files under `testdata/`.
29. [ ] Snapshot tests: `TestSnapshot_KillConfirm`, `TestSnapshot_KillAllConfirm`,
        `TestSnapshot_HelpOverlay`.
30. [ ] Snapshot tests: `TestSnapshot_NoColor` (set `t.Setenv("NO_COLOR", "1")`)
        and `TestSnapshot_StatusGlyphs` (one lane per status).
31. [ ] Behavioral tests: `TestKeybinding_JumpToLane`,
        `TestKeybinding_TabCycle`, `TestKeybinding_KillFlow`,
        `TestKeybinding_KillCancel`.
32. [ ] Behavioral tests: `TestProducer_Coalesce`, `TestProducer_StatusBypass`.
33. [ ] Unit tests for `lanes_cache.go`: dirty-invalidates, width-invalidates,
        status-invalidates.
34. [ ] Unit tests for `lanes_layout.go`: narrow / wide / focus-override / n=0.
35. [ ] Add `go vet ./internal/tui/lanes/` to existing CI gate; ensure
        `go test ./...` passes.
36. [ ] Document the package in a top-of-file comment that points to this spec
        and to lanes-protocol (spec 3) for envelope details.
37. [ ] Add accessibility note in package doc: every status colored cell pairs
        with a glyph; `NO_COLOR` and `TERM=dumb` are honored automatically.
38. [ ] Smoke-run `r1 chat-interactive --lanes` against a 4-Lobe cortex; verify
        no flicker, no panic on resize 200→60 cols, kill confirms work.
