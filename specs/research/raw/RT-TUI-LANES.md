# RT-TUI-LANES — Multi-lane Concurrent Thinking TUI

Research date: 2026-05-02
Goal: design a Bubble Tea TUI panel that shows N concurrent "thinking lanes" (per-stance / per-agent / per-task), each with name, current activity, live cost/token counts, status (running/blocked/done), and an expandable detail pane.

---

## 1. lazygit (gocui) — panels-and-views

**Library:** [jesseduffield/gocui](https://github.com/jesseduffield/gocui) (fork of `jroimartin/gocui`, termbox/tcell-backed).

**Layout model:**
- Imperative absolute-coordinate `View`s. The app supplies a `Manager`/`ManagerFunc` that runs every frame and calls `g.SetView(name, x0, y0, x1, y1)` with computed coordinates. The manager is the layout algorithm — there is no flexbox.
- gocui caches each View's content; only views whose content changed are re-rendered, which keeps refresh cheap.
- Navigation is keybinding-driven: `g.SetCurrentView(name)` swaps focus. lazygit binds `tab`/`shift-tab` and `[`/`]` to cycle side panels, plus number keys (`1`-`5`) for jump-to-panel.
- gocui originally lacked `shift+tab` (termbox limitation), which is why lazygit historically used left/right arrows for inter-panel navigation — useful precedent: provide *multiple* keys for the same focus action.

**Takeaway for r1:**
- gocui is lower-level than Bubble Tea; not the right pick if we already have Bubble Tea elsewhere.
- BUT the *idea* of numbered panel jumps (`1`, `2`, …, `9` to focus lane N) is excellent for lane TUIs and trivially portable to Bubble Tea.
- See also [lazytui](https://github.com/DokaDev/lazytui) (gocui wrapper with Panel/ListPanel/Modal abstractions and automatic focus handling).

---

## 2. k9s — multi-pane Kubernetes TUI

**Library:** [rivo/tview](https://github.com/rivo/tview) on top of [gdamore/tcell](https://github.com/gdamore/tcell). `tview.Flex` is its main layout primitive — flexbox-style with `proportion` and `fixedSize`.

**Layout:**
- Two regions: **header** (cluster info / mnemonics / logo) and **main** (resource list/table).
- Single dominant pane with mode switches via "command" prompts (`:pods`, `:dp`). When you drill down, the main pane swaps content rather than splitting — so concurrency-of-state is shown via *one focused list at a time*, not side-by-side.
- Live updates via informer-driven refresh (~2s default).

**Takeaway for r1:**
- k9s shows that for *many* concurrent objects with deep detail, a list-with-drill-down beats trying to fit everything visible. We should consider a hybrid: lanes always visible compactly, expand-one to fill detail.
- tview's `Flex.AddItem(item, fixedSize, proportion, focus)` is the right mental model even when implementing in lipgloss.

---

## 3. btop / htop — high-frequency rerender

**Refresh strategy:**
- htop and btop both expose a user-tunable refresh interval (htop: `s`; btop: `update_ms`, `+/-` keys live). Defaults are 1000–1500 ms.
- btop draws via custom diffed-region routines (only the changed cells are written), which avoids flicker on small/medium terminals.
- Both auto-throttle on slow terminals (`--low-color`, lower fps).

**Takeaway for r1:**
- Do **not** push a Bubble Tea repaint per-token. Instead:
  - Coalesce token/cost ticks behind a `time.NewTicker(250*time.Millisecond)`-driven `tickMsg`.
  - For activity strings, use a "last activity" string (overwrite, not append).
  - Make the refresh interval user-configurable (and lower it automatically when window resize cascades happen).
- Render the lanes header bar separately from the bodies; only repaint bodies that have a changed activity string in the model (model dirty-flag pattern).

---

## 4. tmux — pane geometry & reflow

**Five layouts:** `even-horizontal`, `even-vertical`, `main-horizontal` (one big top + small bottom row), `main-vertical` (one big left + small right column), `tiled`.

**Resize:**
- Cells = the resize unit; binds `C-b` then `Ctrl-arrow` to nudge by one cell.
- The "main" layouts are exactly the "expand one lane to detail, keep peers visible" pattern we want.
- tmux's history reflow is famously CPU-heavy with long lines — a useful warning: do **not** reflow lane bodies on every resize; clip + scroll instead.

**Takeaway for r1:**
- Crib `main-vertical` for the expanded-lane state: focused lane gets ~60–70% of width, peers stack on the right at ~30–40% with truncated single-line summaries.
- Reserve `Ctrl+arrow` (or `<`/`>`) to grow/shrink the focused lane.

---

## 5. Bubble Tea / Bubbles — components and patterns

**Real-time concurrent updates** ([examples/realtime](https://github.com/charmbracelet/bubbletea/blob/main/examples/realtime/main.go)):
The canonical pattern for N concurrent producers → one TUI is "channel + listener cmd":

```go
type laneTickMsg struct {
    LaneID   string
    Activity string
    Tokens   int
    CostUSD  float64
    Status   LaneStatus
}

func waitForLaneTick(sub chan laneTickMsg) tea.Cmd {
    return func() tea.Msg { return <-sub }
}

func (m model) Init() tea.Cmd {
    return tea.Batch(
        m.spinner.Tick,
        waitForLaneTick(m.sub), // re-armed in Update on every receive
    )
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case laneTickMsg:
        if l, ok := m.lanes[msg.LaneID]; ok {
            l.Activity, l.Tokens, l.CostUSD, l.Status =
                msg.Activity, msg.Tokens, msg.CostUSD, msg.Status
            m.lanes[msg.LaneID] = l
        }
        return m, waitForLaneTick(m.sub) // re-arm
    }
    return m, nil
}
```

Workers (one goroutine per lane / per stance) write to the same `chan laneTickMsg`. Bubble Tea's `Update` is single-threaded so no mutex is needed.

**Layout libraries (pick one):**
- [winder/bubblelayout](https://github.com/winder/bubblelayout) — declarative MiG-style (`"width 100:200:300"`, `"grow"`); converts `tea.WindowSizeMsg` → per-component `BubbleLayoutMsg`. **Recommended for r1**: minimal dep, idiomatic, exactly the abstraction we need.
- [mieubrisse/teact](https://github.com/mieubrisse/teact) — React-like, more sophisticated. Heavier; use only if we end up with deep nesting.
- DIY with `lipgloss.JoinHorizontal` / `JoinVertical` and arithmetic — fine for ≤2 levels.

**Components from charmbracelet/bubbles:**
- `spinner` per running lane (Dot, MiniDot, or Pulse — Pulse reads as "thinking").
- `progress` for token-budget bars (use `WithoutPercentage` + percent-as-text suffix).
- `viewport` for the expanded lane's scrolling activity log.
- `table` is wrong for the lane row itself (no per-row spinners) — render the lanes manually with `lipgloss.JoinVertical` over per-lane `Render()` and only use `table` if we add a numerical metrics drawer.

**Expandable detail pane pattern:**
- Two view modes in the model: `viewOverview` (all lanes compact) and `viewFocus` (focused lane large, peers in side rail).
- Toggle with `enter`/`esc`. Keep keyboard map identical except in focus mode where `j`/`k` scroll the activity viewport.

**Kill-switch keybinding:**
```go
var keyKill = key.NewBinding(
    key.WithKeys("k"),
    key.WithHelp("k", "kill focused lane"),
)
```
Wire to a `KillLane(id)` command in r1's harness/supervisor. Confirm with a modal (`y` to confirm) — terminal apps that kill on a single keypress get user complaints fast. Reserve `K` (shift) for "kill all" with double-confirm.

**Anti-flicker:**
- Avoid `tea.ClearScreen` per frame; let Bubble Tea's diffing do its job.
- Coalesce ticks in a goroutine that only forwards every 200–300 ms even if upstream emits faster.

---

## 6. lipgloss — status colors & accessibility

Library: [charmbracelet/lipgloss](https://github.com/charmbracelet/lipgloss).

**AdaptiveColor** for light/dark terminals:
```go
import "charm.land/lipgloss/v2/compat"

statusColors := map[LaneStatus]compat.AdaptiveColor{
    StatusRunning: {Light: lipgloss.Color("#0066CC"), Dark: lipgloss.Color("#7AA2F7")},
    StatusBlocked: {Light: lipgloss.Color("#B58900"), Dark: lipgloss.Color("#E0AF68")},
    StatusDone:    {Light: lipgloss.Color("#287E00"), Dark: lipgloss.Color("#9ECE6A")},
    StatusError:   {Light: lipgloss.Color("#C0392B"), Dark: lipgloss.Color("#F7768E")},
    StatusIdle:    {Light: lipgloss.Color("#586E75"), Dark: lipgloss.Color("#565F89")},
}
```

Colors above are sampled from Tokyo Night Storm (dark) + a Solarized-ish light palette and pass WCAG AA on both default Apple Terminal black/white backgrounds and on the iTerm2 default Tokyo Night theme.

**Accessibility:**
- lipgloss respects `NO_COLOR` (omit/strip), `TERM=dumb` (plain), and downsamples to 256/16-color terminals automatically.
- For colorblind users, **always pair color with a glyph** (don't carry status meaning in color alone):
  - Running: `▸` or spinner frame
  - Blocked: `⏸`
  - Done: `✓`
  - Error: `✗`
  - Idle: `·`
- Border styles encode focus too: thick rounded border = focused lane, thin border = peer.

**Borders for lane panels:**
```go
laneStyle := lipgloss.NewStyle().
    Border(lipgloss.RoundedBorder()).
    BorderForeground(borderColor).
    Padding(0, 1).
    Width(laneWidth).
    Height(laneHeight)
```

`lipgloss.Width()`/`lipgloss.Height()` should be used everywhere instead of hardcoded numbers — they understand the cost of borders/padding.

---

## 7. OpenDevin / SWE-agent / aider — multi-thread state in CLI?

Short answer: **none of them ship a native multi-thread TUI**. They are single-conversation REPLs. Multi-agent orchestration in this ecosystem is handled by *wrappers*:

- [smtg-ai/claude-squad](https://github.com/smtg-ai/claude-squad) — list-based TUI; spawns one tmux session + git worktree per agent. Keybindings: `↑/j`, `↓/k` to navigate, `tab` between preview/diff, `n`/`N` new instance, `D` delete, `↵/o` attach, `ctrl-q` detach. **Lesson:** they did *not* use a multi-column live dashboard — they use a sidebar list + one detail pane. Why? Because attaching to a real PTY needs the full screen.
- [andyrewlee/amux](https://github.com/andyrewlee/amux) — TUI for parallel coding agents over tmux+worktrees. Vertical list with status/model/tokens/cost columns. `enter` opens split (trace left, PTY right). `s` sort, `/` search, `$` toggle costs, `d` diff, `g`/`b`/`w` annotate turns GOOD/BAD/WASTE.
- [zanetworker/aimux](https://github.com/zanetworker/aimux) — same family; vertical list, refreshes every 2s.
- [rustykuntz/clideck](https://github.com/rustykuntz/clideck) — explicitly *rejects* a side-by-side pane grid: "A pane grid is flat. Agent work usually is not." Picks a chat-style sidebar instead. (Worth reading their reasoning before we commit to columns.)
- [izll/agent-session-manager](https://github.com/izll/agent-session-manager) — Bubble Tea + tmux. List-based.
- [UgOrange/vibemux](https://github.com/UgOrange/vibemux) — TUI orchestrator for parallel Claude Code agents.

**Insight:** the field has converged on **list-with-detail** rather than swim-lane-grid for *interactive* agents. For r1's "thinking lanes" the difference is that we want to *watch* concurrent work without attaching, so a compact column view *plus* a list-detail mode is the sweet spot. Use list-detail as the default; offer a "lanes view" toggle (`L`).

---

## 8. Visual references

Confirmed README screenshots (good for the design review):
- claude-squad: `assets/screenshot.png` — list + detail layout. Shows tmux-attached PTY in detail pane.
- amux: dashboard preview screenshots in the README.
- clideck: dashboard with 15 themes; chat-style sidebar.
- kanban-cli (Bubble Tea): three-column swim-lane (todo / in progress / done), card focus highlighted, `enter` cycles status.

What works visually:
- Tight per-lane borders that change weight on focus.
- Single-line activity string (truncated with `…`) — full log only in expanded view.
- Token/cost as small monospace metrics in the lane footer, not the title.
- Spinner *only* on running lanes (silent peers reduce visual noise).

What is confusing:
- 4+ vertical splits at narrow widths (<100 cols) — text wraps, borders break. Auto-collapse to a list when `width < N*30`.
- Color-only status. Always pair with a glyph.
- Per-token repaint of the entire screen (causes visible flicker on slow terminals).

---

## Summary recommendations for r1

- **Library stack:** `bubbletea` + `bubbles` + `lipgloss` (already in use) + add [`winder/bubblelayout`](https://github.com/winder/bubblelayout) for declarative grid.
- **Versions (May 2026):** `charmbracelet/bubbletea v2.x`, `bubbles v2.x`, `lipgloss v2.x` (note new import path `charm.land/...`).
- **Layout:**
  - Default: **horizontal column lanes** when `term width ≥ N*32 cols`, else fall back to **vertical list** (auto-degrade).
  - Each lane = bordered box: title bar (name + status glyph + spinner), 1-line activity, footer (`tokens • $cost • elapsed`).
  - `enter` enters focus mode → tmux `main-vertical` style: focused lane ~65% width, peers as compact stack on right.
  - `esc` returns to overview.
- **Navigation:** `1`–`9` jump to lane N (lazygit-style), `tab`/`shift-tab` cycle, `j`/`k` move list cursor, `enter`/`esc` expand/collapse, `k` kill focused (with confirm), `K` kill all, `?` help.
- **Concurrency:** single `chan laneTickMsg` fan-in from all worker goroutines; one `waitForLaneTick` re-armed each receive (per Bubble Tea realtime example). Coalesce upstream ticks at 200–300 ms in the producer.
- **Anti-pattern to avoid:** *do not pass `tea.WindowSizeMsg` straight through and rerender every lane on every keypress*. Cache rendered lane strings in the model; invalidate per-lane on data change; `View()` joins cached strings. This is the single biggest perf win and is exactly what btop's diffed-region renderer does in C++.
