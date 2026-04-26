// Package desktop provides cross-platform desktop GUI automation.
//
// T-R1P-009: Desktop/GUI automation skill — gives R1 the ability to
// drive an OS-level GUI: take screenshots, move/click the mouse, type
// keystrokes, enumerate windows, and inspect the active window.
//
// Two backends are wired via build tags:
//
//   - default ("!ci_no_gui"): real implementation backed by
//     github.com/go-vgo/robotgo, which depends on platform CGO
//     packages (libxtst-dev / cocoa / win32). Built only when the host
//     toolchain has those headers.
//
//   - "ci_no_gui": pure-Go stub that returns deterministic empty
//     responses. Used in headless CI (no DISPLAY / WAYLAND_DISPLAY) to
//     keep `go test ./...` building and to let agent code that calls
//     the desktop skill still execute its own logic without a real
//     GUI underneath.
//
// Operator-facing API (Skill):
//
//	d := desktop.New()
//	img, _ := d.Screenshot()
//	region, _ := d.ScreenshotRegion(0, 0, 100, 100)
//	d.Click(120, 200, desktop.ButtonLeft)
//	d.DoubleClick(120, 200)
//	d.MoveCursor(50, 50)
//	d.TypeText("hello")
//	d.KeyPress("enter")
//	title, _ := d.GetWindowTitle()
//	w, h, _ := d.GetScreenSize()
//	windows, _ := d.ListWindows()
//	rgb, _ := d.PickColor(120, 200)
//
// All methods return ErrUnsupported when the active backend cannot
// service the call (e.g., stub backend in CI). Tests should branch on
// errors.Is(err, desktop.ErrUnsupported) rather than panicking.
package desktop

import (
	"errors"
	"image"
	"sync"
)

// ErrUnsupported is returned when the active desktop backend cannot
// service the operation. Callers should treat it as a soft failure.
var ErrUnsupported = errors.New("desktop: backend does not support this operation")

// MouseButton identifies a mouse button for click operations.
type MouseButton string

const (
	// ButtonLeft is the primary mouse button.
	ButtonLeft MouseButton = "left"
	// ButtonRight is the secondary mouse button.
	ButtonRight MouseButton = "right"
	// ButtonMiddle is the tertiary (wheel) mouse button.
	ButtonMiddle MouseButton = "middle"
)

// Color is an RGB triple returned by PickColor.
type Color struct {
	R uint8 `json:"r"`
	G uint8 `json:"g"`
	B uint8 `json:"b"`
}

// WindowInfo describes a single OS-level window.
type WindowInfo struct {
	// PID is the owning process id, when known.
	PID int `json:"pid"`
	// Title is the window title (may be empty for chromeless windows).
	Title string `json:"title"`
	// Bounds is the window bounding rectangle in screen coordinates.
	Bounds image.Rectangle `json:"bounds"`
	// Active reports whether this is the foreground window.
	Active bool `json:"active"`
}

// Backend is the OS-level abstraction. Implementations live in
// desktop_robotgo.go (real) and desktop_stub.go (CI fallback). Tests
// can also pass an in-memory Backend via NewWithBackend.
type Backend interface {
	Screenshot() (image.Image, error)
	ScreenshotRegion(x, y, w, h int) (image.Image, error)
	Click(x, y int, button MouseButton) error
	DoubleClick(x, y int) error
	MoveCursor(x, y int) error
	TypeText(text string) error
	KeyPress(key string) error
	GetActiveWindowTitle() (string, error)
	GetScreenSize() (width, height int, err error)
	ListWindows() ([]WindowInfo, error)
	PickColor(x, y int) (Color, error)
}

// Desktop is the high-level skill surface. It thinly wraps a Backend
// and serializes calls via a mutex so concurrent agent threads cannot
// race the cursor against itself.
type Desktop struct {
	mu sync.Mutex
	b  Backend
}

// New returns a Desktop bound to the build-tag-selected backend. In a
// non-CI build this is robotgo; in a "ci_no_gui" build this is the
// deterministic stub.
func New() *Desktop {
	return &Desktop{b: defaultBackend()}
}

// NewWithBackend returns a Desktop bound to the supplied Backend.
// Useful for unit tests.
func NewWithBackend(b Backend) *Desktop {
	return &Desktop{b: b}
}

// Backend returns the underlying Backend. Mostly for tests + diagnostics.
func (d *Desktop) Backend() Backend { return d.b }

// --- public ops (mirror the Backend surface, with serialization) ---

// Screenshot captures the entire primary screen.
func (d *Desktop) Screenshot() (image.Image, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.b.Screenshot()
}

// ScreenshotRegion captures a sub-rectangle of the primary screen.
func (d *Desktop) ScreenshotRegion(x, y, w, h int) (image.Image, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if w <= 0 || h <= 0 {
		return nil, errors.New("desktop: ScreenshotRegion: width and height must be > 0")
	}
	return d.b.ScreenshotRegion(x, y, w, h)
}

// Click presses and releases a mouse button at (x, y).
func (d *Desktop) Click(x, y int, button MouseButton) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if button == "" {
		button = ButtonLeft
	}
	return d.b.Click(x, y, button)
}

// DoubleClick presses the primary mouse button twice in quick
// succession at (x, y).
func (d *Desktop) DoubleClick(x, y int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.b.DoubleClick(x, y)
}

// MoveCursor moves the cursor to (x, y) without clicking.
func (d *Desktop) MoveCursor(x, y int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.b.MoveCursor(x, y)
}

// TypeText types text as if from the keyboard.
func (d *Desktop) TypeText(text string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if text == "" {
		return nil
	}
	return d.b.TypeText(text)
}

// KeyPress presses and releases a single named key (e.g., "enter",
// "esc", "f5", "a").
func (d *Desktop) KeyPress(key string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if key == "" {
		return errors.New("desktop: KeyPress: key required")
	}
	return d.b.KeyPress(key)
}

// GetWindowTitle returns the title of the foreground window.
func (d *Desktop) GetWindowTitle() (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.b.GetActiveWindowTitle()
}

// GetScreenSize returns the primary screen's dimensions in pixels.
func (d *Desktop) GetScreenSize() (int, int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.b.GetScreenSize()
}

// ListWindows enumerates all visible windows on screen.
func (d *Desktop) ListWindows() ([]WindowInfo, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.b.ListWindows()
}

// PickColor reads the RGB color at the given screen coordinate.
func (d *Desktop) PickColor(x, y int) (Color, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.b.PickColor(x, y)
}
