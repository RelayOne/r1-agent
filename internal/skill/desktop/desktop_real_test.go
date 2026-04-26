//go:build desktop_robotgo

// desktop_real_test.go — real-desktop interaction tests for the
// robotgo backend (T-R1P-009).
//
// Build tag: this file is only compiled when `-tags desktop_robotgo`
// is passed to `go test`. Selecting the real backend at build time
// (rather than skipping at runtime) keeps the default + ci_no_gui
// builds free of any "skip when stub active" runtime check — the
// stub-only tests in desktop_test.go always run, the real-only tests
// in this file only build when the operator opts in.
//
// Runtime guard: the only spec-sanctioned skip remains — when neither
// DISPLAY nor WAYLAND_DISPLAY is set the host has no display server
// reachable and there's nothing to test against.

package desktop

import (
	"os"
	"testing"
)

// hasDisplayServer is the spec-sanctioned guard: the task statement
// authorizes `t.Skip()` for real-desktop tests when DISPLAY /
// WAYLAND_DISPLAY are absent. Encapsulated here so every test in
// this file uses the identical predicate.
func hasDisplayServer() bool {
	return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
}

// TestRealBackendIsRobotgo asserts the build tag selected the real
// backend, not the stub. No display server required.
func TestRealBackendIsRobotgo(t *testing.T) {
	if BackendName != "robotgo" {
		t.Fatalf("BackendName = %q, want %q under -tags desktop_robotgo", BackendName, "robotgo")
	}
	if d := New(); d.Backend() == nil {
		t.Fatal("New() returned Desktop with nil backend")
	}
}

// TestRealBackendScreenSize asks the X / Wayland server for the
// primary screen geometry. Expects positive width + height.
func TestRealBackendScreenSize(t *testing.T) {
	if !hasDisplayServer() {
		t.Skip("no display server (DISPLAY / WAYLAND_DISPLAY unset)")
	}
	d := New()
	w, h, err := d.GetScreenSize()
	if err != nil {
		t.Fatalf("GetScreenSize: %v", err)
	}
	if w <= 0 || h <= 0 {
		t.Errorf("screen = (%d, %d), want positive", w, h)
	}
	t.Logf("real screen geometry: %dx%d", w, h)
}

// TestRealBackendActiveWindowTitle reads the foreground window title.
// Title may legitimately be empty (no window focused), so we only
// require the call to return without erroring.
func TestRealBackendActiveWindowTitle(t *testing.T) {
	if !hasDisplayServer() {
		t.Skip("no display server (DISPLAY / WAYLAND_DISPLAY unset)")
	}
	d := New()
	title, err := d.GetWindowTitle()
	if err != nil {
		t.Fatalf("GetWindowTitle: %v", err)
	}
	t.Logf("active window title: %q", title)
}

// TestRealBackendListWindows enumerates visible windows. Window count
// can be zero on a fresh tty; we only assert the call returns without
// erroring and that any returned window has a non-zero PID.
func TestRealBackendListWindows(t *testing.T) {
	if !hasDisplayServer() {
		t.Skip("no display server (DISPLAY / WAYLAND_DISPLAY unset)")
	}
	d := New()
	wins, err := d.ListWindows()
	if err != nil {
		t.Fatalf("ListWindows: %v", err)
	}
	t.Logf("real window count: %d", len(wins))
	for i, w := range wins {
		if w.PID <= 0 {
			t.Errorf("wins[%d].PID = %d, want positive", i, w.PID)
		}
		// Sanity: bounds rect should not be NaN-shaped (Min ≤ Max).
		if w.Bounds.Min.X > w.Bounds.Max.X || w.Bounds.Min.Y > w.Bounds.Max.Y {
			t.Errorf("wins[%d].Bounds inverted: %+v", i, w.Bounds)
		}
	}
}

// TestRealBackendCursorPositioning is a non-destructive movement
// test: it asks robotgo to move the cursor to a small corner offset
// and asserts the call returns without error.
//
// Mouse-CLICK and key-press tests are deliberately omitted — they
// would interfere with whatever the operator is doing. PickColor /
// Screenshot are also omitted because they hit XGetImage, which
// trips Xlib BadMatch on nested Wayland compositors (XWayland) and
// can abort the test process.
func TestRealBackendCursorPositioning(t *testing.T) {
	if !hasDisplayServer() {
		t.Skip("no display server (DISPLAY / WAYLAND_DISPLAY unset)")
	}
	d := New()
	// Move to a corner that exists on every reasonable resolution.
	if err := d.MoveCursor(10, 10); err != nil {
		t.Fatalf("MoveCursor(10,10): %v", err)
	}
	// Confirm the Backend interface handle is the real backend (not
	// the stub, which would silently no-op the move).
	if BackendName != "robotgo" {
		t.Fatalf("BackendName = %q, want robotgo", BackendName)
	}
}
