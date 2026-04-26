// Package desktop — stub backend type, always compiled.
//
// The stubBackend type lives here (no build tag) so tests can
// construct it directly via NewWithBackend(&stubBackend{}) regardless
// of which real backend is selected at build time. The build-tag-
// gated selection of stubBackend AS THE DEFAULT lives in
// desktop_stub.go (which compiles only when !desktop_robotgo).
//
// Every Backend method returns ErrUnsupported.

package desktop

import "image"

// stubBackend is a Backend that returns ErrUnsupported for every
// operation. Selected as the default when the desktop_robotgo build
// tag is absent, and exposed unconditionally so tests can assert its
// behavior under any build tag.
type stubBackend struct{}

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
