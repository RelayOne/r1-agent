---
name: desktop-automation
description: Cross-platform desktop GUI automation — screenshots, mouse, keyboard, window enumeration.
triggers: [desktop, screenshot, mouse, keyboard, click, gui, automation]
keywords: [desktop, gui, screenshot, click, mouse, keyboard, robotgo, automation, screen, cursor]
allowed-tools: [bash, Read]
---

# Desktop Automation

> Cross-platform desktop GUI control for the agent: take screenshots, move/click the
> mouse, type keystrokes, enumerate windows, pick pixel colors.

## When to use

- The task requires interacting with a native GUI app that has no web/CLI surface.
- The user asks for "automate this app", "screenshot the screen", "click the button at X,Y".
- Web automation (`browser_*` tools) is not enough because the target is outside the browser.

## Operations (Go API)

The skill is implemented at `internal/skill/desktop` and exposes a `Desktop`
type with these methods:

- `Screenshot()` — capture the entire primary screen as `image.Image`.
- `ScreenshotRegion(x, y, w, h)` — capture a sub-rectangle.
- `Click(x, y, button)` — single click; button is `ButtonLeft` / `ButtonRight` / `ButtonMiddle`.
- `DoubleClick(x, y)` — primary-button double click.
- `MoveCursor(x, y)` — move cursor without clicking.
- `TypeText(text)` — type a string as if from the keyboard.
- `KeyPress(key)` — press a single named key (e.g., "enter", "esc", "f5").
- `GetWindowTitle()` — title of the active foreground window.
- `GetScreenSize()` — width and height of the primary screen.
- `ListWindows()` — every visible window with PID / title / bounds / active flag.
- `PickColor(x, y)` — RGB color at a pixel.

## Build tags

- Default build: stub backend — every op returns `ErrUnsupported`. Safe in CI / headless.
- `-tags desktop_robotgo`: real backend backed by `github.com/go-vgo/robotgo`.
  Requires CGO + platform GUI libraries (libxtst-dev on Linux, AppKit on macOS,
  win32 on Windows).

Tests use a hand-rolled `Backend` so they pass without a display server.

## Gotchas

- **Robotgo CGO**: fails to compile on hosts without the GUI dev headers.
  Build with `-tags ci_no_gui` or omit the `desktop_robotgo` tag to skip it.
- **No DISPLAY in CI**: real-desktop tests `t.Skip()` when neither `DISPLAY`
  nor `WAYLAND_DISPLAY` is set. Don't try to "fix" them by spinning up Xvfb
  in CI unless the user explicitly asks — adds minutes of toolchain setup.
- **Coordinate frame**: `(0, 0)` is top-left on every platform. Multi-monitor
  setups are NOT addressable in this MVP — capture / click happens on the
  primary screen only.
- **Click safety**: serialize via the Desktop wrapper's mutex. Two goroutines
  clicking at once will fight over the cursor and produce non-deterministic
  results.
- **Key naming**: robotgo uses lowercase ASCII names ("enter", not "Enter";
  "f5", not "F5"). The wrapper does not translate.
- **No undo**: every desktop action is irreversible. Always confirm with the
  user before scripting destructive sequences (file deletion via GUI, etc.).
