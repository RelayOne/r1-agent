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
// To wire robotgo in:
//
//	go get github.com/go-vgo/robotgo
//	go build -tags desktop_robotgo ./...
//
// Mapping table (skill operation → robotgo call):
//
//	Screenshot()            → robotgo.CaptureScreen()
//	ScreenshotRegion(...)   → robotgo.CaptureScreen(x,y,w,h)
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

// NOTE: robotgo import + bridge methods are intentionally elided from
// the skeleton until the host build environment has the CGO toolchain.
// When wiring this on a real machine, paste the imports + method
// receivers below; the interface is fixed so a wired implementation
// drops in without changes elsewhere.
//
// Skeleton lives here so `git grep robotgo` finds the integration
// point and so a future operator knows exactly which file to edit.

// defaultBackend selects the real backend when this file is compiled.
// To activate, replace the body with `return newRobotgoBackend()` after
// adding the robotgo import.
func defaultBackend() Backend { return &stubBackend{} }

// BackendName identifies which backend was selected at build time.
const BackendName = "robotgo-skeleton"
