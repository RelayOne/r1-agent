//go:build !desktop_robotgo

// Package desktop — stub backend.
//
// Selected by default: when the operator does NOT pass the
// `desktop_robotgo` build tag, this stub is the active backend so
// `go build ./...` and `go build -tags ci_no_gui ./...` both work
// without dragging in robotgo's CGO graph.
//
// robotgo requires CGO + platform GUI libraries (libxtst-dev on
// Linux, AppKit/Carbon on macOS, win32 on Windows). To activate the
// real backend, build with `-tags desktop_robotgo` on a host that
// has the toolchain.
//
// Every Backend method returns ErrUnsupported. Tests that need a
// scriptable backend should NewWithBackend a hand-rolled fake.

package desktop

import "image"

// stubBackend is the default backend in headless / non-CGO builds.
type stubBackend struct{}

func defaultBackend() Backend { return &stubBackend{} }

// BackendName identifies which backend was selected at build time.
// Useful for tests that want to skip robotgo-only assertions.
const BackendName = "stub"

func (s *stubBackend) Screenshot() (image.Image, error) {
	return nil, ErrUnsupported
}

func (s *stubBackend) ScreenshotRegion(x, y, w, h int) (image.Image, error) {
	return nil, ErrUnsupported
}

func (s *stubBackend) Click(x, y int, button MouseButton) error {
	return ErrUnsupported
}

func (s *stubBackend) DoubleClick(x, y int) error {
	return ErrUnsupported
}

func (s *stubBackend) MoveCursor(x, y int) error {
	return ErrUnsupported
}

func (s *stubBackend) TypeText(text string) error {
	return ErrUnsupported
}

func (s *stubBackend) KeyPress(key string) error {
	return ErrUnsupported
}

func (s *stubBackend) GetActiveWindowTitle() (string, error) {
	return "", ErrUnsupported
}

func (s *stubBackend) GetScreenSize() (int, int, error) {
	return 0, 0, ErrUnsupported
}

func (s *stubBackend) ListWindows() ([]WindowInfo, error) {
	return nil, ErrUnsupported
}

func (s *stubBackend) PickColor(x, y int) (Color, error) {
	return Color{}, ErrUnsupported
}
