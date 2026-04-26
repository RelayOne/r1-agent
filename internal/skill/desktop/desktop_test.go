// desktop_test.go — unit tests for the Desktop skill (T-R1P-009).
//
// These tests run against a hand-rolled fake Backend so they exercise
// the public surface without depending on a real GUI or the robotgo
// CGO graph. Real-desktop tests live in desktop_real_test.go and are
// skipped when DISPLAY / WAYLAND_DISPLAY is empty.

package desktop

import (
	"errors"
	"image"
	"image/color"
	"strings"
	"sync"
	"testing"
)

// fakeBackend is an in-memory Backend implementation for tests. Every
// call appends to ops so tests can assert ordering. ScreenshotResult
// and ScreenshotErr let tests script return values for the image-
// capture entry points.
type fakeBackend struct {
	mu     sync.Mutex
	ops    []string
	screen image.Image
	width  int
	height int
	title  string
	color  Color
	err    error
}

func (f *fakeBackend) record(op string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ops = append(f.ops, op)
}

func (f *fakeBackend) snapshotOps() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.ops))
	copy(out, f.ops)
	return out
}

func (f *fakeBackend) Screenshot() (image.Image, error) {
	f.record("Screenshot")
	if f.err != nil {
		return nil, f.err
	}
	return f.screen, nil
}

func (f *fakeBackend) ScreenshotRegion(x, y, w, h int) (image.Image, error) {
	f.record("ScreenshotRegion")
	if f.err != nil {
		return nil, f.err
	}
	return f.screen, nil
}

func (f *fakeBackend) Click(x, y int, button MouseButton) error {
	f.record("Click:" + string(button))
	return f.err
}

func (f *fakeBackend) DoubleClick(x, y int) error {
	f.record("DoubleClick")
	return f.err
}

func (f *fakeBackend) MoveCursor(x, y int) error {
	f.record("MoveCursor")
	return f.err
}

func (f *fakeBackend) TypeText(text string) error {
	f.record("TypeText:" + text)
	return f.err
}

func (f *fakeBackend) KeyPress(key string) error {
	f.record("KeyPress:" + key)
	return f.err
}

func (f *fakeBackend) GetActiveWindowTitle() (string, error) {
	f.record("GetActiveWindowTitle")
	return f.title, f.err
}

func (f *fakeBackend) GetScreenSize() (int, int, error) {
	f.record("GetScreenSize")
	return f.width, f.height, f.err
}

func (f *fakeBackend) ListWindows() ([]WindowInfo, error) {
	f.record("ListWindows")
	if f.err != nil {
		return nil, f.err
	}
	return []WindowInfo{
		{PID: 1, Title: "alpha", Active: true, Bounds: image.Rect(0, 0, 100, 100)},
		{PID: 2, Title: "beta", Active: false, Bounds: image.Rect(100, 0, 200, 100)},
	}, nil
}

func (f *fakeBackend) PickColor(x, y int) (Color, error) {
	f.record("PickColor")
	return f.color, f.err
}

// TestDesktopScreenshotRoutes asserts that Screenshot dispatches to
// the backend and propagates the returned image.
func TestDesktopScreenshotRoutes(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(0, 0, color.RGBA{R: 255, G: 0, B: 0, A: 255})
	fb := &fakeBackend{screen: img, width: 4, height: 4}
	d := NewWithBackend(fb)

	out, err := d.Screenshot()
	if err != nil {
		t.Fatalf("Screenshot: %v", err)
	}
	if out == nil {
		t.Fatal("Screenshot returned nil image")
	}
	r, g, b, _ := out.At(0, 0).RGBA()
	if r>>8 != 255 || g>>8 != 0 || b>>8 != 0 {
		t.Errorf("pixel(0,0) = (%d,%d,%d), want red", r>>8, g>>8, b>>8)
	}
	if got := fb.snapshotOps(); len(got) != 1 || got[0] != "Screenshot" {
		t.Errorf("ops = %v, want [Screenshot]", got)
	}
}

// TestDesktopScreenshotRegionGuards rejects non-positive dimensions
// before reaching the backend.
func TestDesktopScreenshotRegionGuards(t *testing.T) {
	fb := &fakeBackend{}
	d := NewWithBackend(fb)

	if _, err := d.ScreenshotRegion(0, 0, 0, 100); err == nil {
		t.Error("ScreenshotRegion(w=0) should error")
	}
	if _, err := d.ScreenshotRegion(0, 0, 100, -1); err == nil {
		t.Error("ScreenshotRegion(h<0) should error")
	}
	if got := fb.snapshotOps(); len(got) != 0 {
		t.Errorf("backend should not have been called, got ops = %v", got)
	}

	if _, err := d.ScreenshotRegion(0, 0, 10, 10); err != nil {
		t.Errorf("valid ScreenshotRegion: %v", err)
	}
	if got := fb.snapshotOps(); len(got) != 1 || got[0] != "ScreenshotRegion" {
		t.Errorf("ops = %v, want [ScreenshotRegion]", got)
	}
}

// TestDesktopClickDefaultsToLeft confirms that Click("") routes to the
// left mouse button instead of erroring.
func TestDesktopClickDefaultsToLeft(t *testing.T) {
	fb := &fakeBackend{}
	d := NewWithBackend(fb)

	if err := d.Click(10, 10, ""); err != nil {
		t.Fatalf("Click empty button: %v", err)
	}
	if err := d.Click(20, 20, ButtonRight); err != nil {
		t.Fatalf("Click right: %v", err)
	}
	got := fb.snapshotOps()
	if len(got) != 2 || got[0] != "Click:left" || got[1] != "Click:right" {
		t.Errorf("ops = %v", got)
	}
}

// TestDesktopTypeTextNoOp ensures empty input is a no-op rather than
// an error or backend round-trip.
func TestDesktopTypeTextNoOp(t *testing.T) {
	fb := &fakeBackend{}
	d := NewWithBackend(fb)

	if err := d.TypeText(""); err != nil {
		t.Errorf("TypeText empty: %v", err)
	}
	if got := fb.snapshotOps(); len(got) != 0 {
		t.Errorf("empty TypeText should not hit backend, got %v", got)
	}

	if err := d.TypeText("hello"); err != nil {
		t.Fatalf("TypeText: %v", err)
	}
	got := fb.snapshotOps()
	if len(got) != 1 || got[0] != "TypeText:hello" {
		t.Errorf("ops = %v", got)
	}
}

// TestDesktopKeyPressRequiresKey rejects an empty key argument.
func TestDesktopKeyPressRequiresKey(t *testing.T) {
	fb := &fakeBackend{}
	d := NewWithBackend(fb)

	if err := d.KeyPress(""); err == nil {
		t.Error("KeyPress(\"\") should error")
	}
	if err := d.KeyPress("enter"); err != nil {
		t.Errorf("KeyPress enter: %v", err)
	}
	got := fb.snapshotOps()
	if len(got) != 1 || got[0] != "KeyPress:enter" {
		t.Errorf("ops = %v", got)
	}
}

// TestDesktopWindowAndScreenInfo verifies title + screen size +
// window list propagation.
func TestDesktopWindowAndScreenInfo(t *testing.T) {
	fb := &fakeBackend{title: "Firefox", width: 1920, height: 1080}
	d := NewWithBackend(fb)

	title, err := d.GetWindowTitle()
	if err != nil {
		t.Fatalf("GetWindowTitle: %v", err)
	}
	if title != "Firefox" {
		t.Errorf("title = %q, want Firefox", title)
	}

	w, h, err := d.GetScreenSize()
	if err != nil {
		t.Fatalf("GetScreenSize: %v", err)
	}
	if w != 1920 || h != 1080 {
		t.Errorf("screen = (%d, %d)", w, h)
	}

	wins, err := d.ListWindows()
	if err != nil {
		t.Fatalf("ListWindows: %v", err)
	}
	if len(wins) != 2 {
		t.Fatalf("ListWindows returned %d, want 2", len(wins))
	}
	if !wins[0].Active || wins[1].Active {
		t.Errorf("active flags wrong: %+v", wins)
	}
	if wins[0].Title != "alpha" || wins[1].Title != "beta" {
		t.Errorf("titles = %q, %q", wins[0].Title, wins[1].Title)
	}
}

// TestDesktopColorPick exercises the PickColor path.
func TestDesktopColorPick(t *testing.T) {
	fb := &fakeBackend{color: Color{R: 12, G: 34, B: 56}}
	d := NewWithBackend(fb)

	c, err := d.PickColor(5, 5)
	if err != nil {
		t.Fatalf("PickColor: %v", err)
	}
	if c != (Color{R: 12, G: 34, B: 56}) {
		t.Errorf("color = %+v", c)
	}
}

// TestDesktopStubBackendUnsupported asserts that the stub backend
// returns ErrUnsupported for every operation. Constructs the stub
// directly via NewWithBackend so the test runs under every build tag
// (default, ci_no_gui, AND desktop_robotgo) and never needs to skip.
func TestDesktopStubBackendUnsupported(t *testing.T) {
	d := NewWithBackend(&stubBackend{})

	cases := []struct {
		name string
		fn   func() error
	}{
		{"Screenshot", func() error { _, e := d.Screenshot(); return e }},
		{"ScreenshotRegion", func() error { _, e := d.ScreenshotRegion(0, 0, 10, 10); return e }},
		{"MoveCursor", func() error { return d.MoveCursor(0, 0) }},
		{"Click", func() error { return d.Click(0, 0, ButtonLeft) }},
		{"DoubleClick", func() error { return d.DoubleClick(0, 0) }},
		{"TypeText", func() error { return d.TypeText("a") }},
		{"KeyPress", func() error { return d.KeyPress("a") }},
		{"GetWindowTitle", func() error { _, e := d.GetWindowTitle(); return e }},
		{"GetScreenSize", func() error { _, _, e := d.GetScreenSize(); return e }},
		{"ListWindows", func() error { _, e := d.ListWindows(); return e }},
		{"PickColor", func() error { _, e := d.PickColor(0, 0); return e }},
	}
	for _, c := range cases {
		err := c.fn()
		if err == nil {
			t.Errorf("%s: expected error from stub backend", c.name)
			continue
		}
		if !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: error = %v, want ErrUnsupported", c.name, err)
		}
	}
}

// (Real-backend tests live in desktop_real_test.go, which is build-
// tag-gated to `desktop_robotgo` so the test file simply doesn't
// compile when the stub backend is active. This avoids the need for
// any "stub backend, skip" runtime check in this file.)

// TestDesktopOpsConcurrencyOK confirms the public Desktop wrapper
// serializes concurrent backend calls (sanity, since callers may share
// a singleton). We just verify the recorded op count matches the
// number of goroutines.
func TestDesktopOpsConcurrencyOK(t *testing.T) {
	fb := &fakeBackend{}
	d := NewWithBackend(fb)

	const N = 50
	done := make(chan struct{}, N)
	for i := 0; i < N; i++ {
		go func() {
			_ = d.MoveCursor(0, 0)
			done <- struct{}{}
		}()
	}
	for i := 0; i < N; i++ {
		<-done
	}

	ops := fb.snapshotOps()
	if len(ops) != N {
		t.Errorf("ops = %d, want %d", len(ops), N)
	}
	for _, op := range ops {
		if !strings.HasPrefix(op, "MoveCursor") {
			t.Errorf("unexpected op %q", op)
		}
	}
}
