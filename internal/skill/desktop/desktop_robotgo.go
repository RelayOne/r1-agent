//go:build desktop_robotgo

// Package desktop — real robotgo-backed implementation.
//
// Build with `-tags desktop_robotgo` ON A HOST WITH:
//   - CGO_ENABLED=1
//   - libxtst-dev / libxkbcommon-dev / libpng-dev (Linux)
//   - Xcode command-line tools + AppKit (macOS)
//   - MinGW + win32 headers (Windows)
//   - DISPLAY or WAYLAND_DISPLAY set at runtime (Linux)
//
// The opt-in tag avoids importing the heavy robotgo CGO graph in CI
// builds that don't need it. When the tag is absent, the stub backend
// in desktop_stub.go is selected instead.
//
// Mapping table (skill operation → robotgo call):
//
//	Screenshot()            → robotgo.CaptureImg()
//	ScreenshotRegion(...)   → robotgo.CaptureImg(x,y,w,h)
//	Click(x,y,button)       → robotgo.Move(x,y); robotgo.Click(string(button))
//	DoubleClick(x,y)        → robotgo.Move(x,y); robotgo.Click("left", true)
//	MoveCursor(x,y)         → robotgo.Move(x,y)
//	TypeText(s)             → robotgo.TypeStr(s)
//	KeyPress(k)             → robotgo.KeyTap(k)
//	GetActiveWindowTitle()  → robotgo.GetTitle()
//	GetScreenSize()         → robotgo.GetScreenSize()
//	ListWindows()           → robotgo.FindIds("") + GetTitle/GetBounds per pid
//	PickColor(x,y)          → robotgo.GetPixelColor(x,y) → parse RRGGBB hex

package desktop

import (
	"fmt"
	"image"
	"strconv"

	"github.com/go-vgo/robotgo"
)

// robotgoBackend implements Backend by delegating to robotgo.
type robotgoBackend struct{}

// defaultBackend selects the real backend when this file is compiled.
func defaultBackend() Backend { return &robotgoBackend{} }

// BackendName identifies which backend was selected at build time.
const BackendName = "robotgo"

func (r *robotgoBackend) Screenshot() (image.Image, error) {
	img, err := robotgo.CaptureImg()
	if err != nil {
		return nil, fmt.Errorf("robotgo.CaptureImg: %w", err)
	}
	return img, nil
}

func (r *robotgoBackend) ScreenshotRegion(x, y, w, h int) (image.Image, error) {
	img, err := robotgo.CaptureImg(x, y, w, h)
	if err != nil {
		return nil, fmt.Errorf("robotgo.CaptureImg(%d,%d,%d,%d): %w", x, y, w, h, err)
	}
	return img, nil
}

func (r *robotgoBackend) Click(x, y int, button MouseButton) error {
	robotgo.Move(x, y)
	if err := robotgo.Click(string(button)); err != nil {
		return fmt.Errorf("robotgo.Click(%s): %w", button, err)
	}
	return nil
}

func (r *robotgoBackend) DoubleClick(x, y int) error {
	robotgo.Move(x, y)
	if err := robotgo.Click("left", true); err != nil {
		return fmt.Errorf("robotgo.Click(double): %w", err)
	}
	return nil
}

func (r *robotgoBackend) MoveCursor(x, y int) error {
	robotgo.Move(x, y)
	return nil
}

func (r *robotgoBackend) TypeText(text string) error {
	robotgo.TypeStr(text)
	return nil
}

func (r *robotgoBackend) KeyPress(key string) error {
	if err := robotgo.KeyTap(key); err != nil {
		return fmt.Errorf("robotgo.KeyTap(%q): %w", key, err)
	}
	return nil
}

func (r *robotgoBackend) GetActiveWindowTitle() (string, error) {
	return robotgo.GetTitle(), nil
}

func (r *robotgoBackend) GetScreenSize() (int, int, error) {
	w, h := robotgo.GetScreenSize()
	return w, h, nil
}

// ListWindows enumerates visible windows by walking the PID list and
// querying robotgo for per-PID title + bounds.
//
// robotgo's window-listing surface is intentionally minimal: it
// returns PIDs, not WindowIDs, and one title per PID (the active
// window for that process). Multi-window-per-process apps will only
// surface their foreground window — acceptable for the agent's
// "what's open" use case.
func (r *robotgoBackend) ListWindows() ([]WindowInfo, error) {
	pids, err := robotgo.FindIds("")
	if err != nil {
		return nil, fmt.Errorf("robotgo.FindIds: %w", err)
	}
	activePID := robotgo.GetPid()
	out := make([]WindowInfo, 0, len(pids))
	for _, pid := range pids {
		title := robotgo.GetTitle(pid)
		x, y, w, h := robotgo.GetBounds(pid)
		out = append(out, WindowInfo{
			PID:    pid,
			Title:  title,
			Bounds: image.Rect(x, y, x+w, y+h),
			Active: pid == activePID,
		})
	}
	return out, nil
}

func (r *robotgoBackend) PickColor(x, y int) (Color, error) {
	hex := robotgo.GetPixelColor(x, y) // returns "RRGGBB" lowercase
	if len(hex) != 6 {
		return Color{}, fmt.Errorf("robotgo.GetPixelColor: unexpected hex %q", hex)
	}
	rByte, err := strconv.ParseUint(hex[0:2], 16, 8)
	if err != nil {
		return Color{}, fmt.Errorf("parse R: %w", err)
	}
	gByte, err := strconv.ParseUint(hex[2:4], 16, 8)
	if err != nil {
		return Color{}, fmt.Errorf("parse G: %w", err)
	}
	bByte, err := strconv.ParseUint(hex[4:6], 16, 8)
	if err != nil {
		return Color{}, fmt.Errorf("parse B: %w", err)
	}
	return Color{R: uint8(rByte), G: uint8(gByte), B: uint8(bByte)}, nil
}
