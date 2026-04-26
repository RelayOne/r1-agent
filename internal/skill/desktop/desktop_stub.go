//go:build !desktop_robotgo

// Package desktop — stub-backend selector.
//
// Selected by default: when the operator does NOT pass the
// `desktop_robotgo` build tag, this file's defaultBackend wires the
// stub as the active backend so `go build ./...` and
// `go build -tags ci_no_gui ./...` both work without dragging in
// robotgo's CGO graph.
//
// robotgo requires CGO + platform GUI libraries (libxtst-dev on
// Linux, AppKit/Carbon on macOS, win32 on Windows). To activate the
// real backend, build with `-tags desktop_robotgo` on a host that
// has the toolchain.
//
// The stubBackend type itself lives in stub_backend.go (no build tag)
// so tests can construct it directly under any build configuration.

package desktop

func defaultBackend() Backend { return &stubBackend{} }

// BackendName identifies which backend was selected at build time.
const BackendName = "stub"
