// Package main — vendor_check.go
//
// work-stoke TASK 15: boot-time self-check for the UI vendor directory.
//
// The 3D ledger visualiser in cmd/r1-server/ui/graph.html currently
// loads Three.js, three-spritetext, and 3d-force-graph from a public CDN.
// The spec acceptance criterion (AC #6) mandates offline operation, so
// operators are expected to vendor the ESM bundles into
// cmd/r1-server/ui/vendor/ (see ui/vendor/README.md for the list).
//
// Because the library blobs are deliberately NOT committed to the repo
// (see the README for the licence / size rationale), we cannot fail the
// build when they are absent — a fresh clone has to build cleanly. What
// we CAN do is log a clear WARNING at server start-up so the operator is
// told exactly which file is missing and where the vendoring docs live.
//
// The sentinel file is `three.module.js`; its presence implies the rest
// of the pinned set has been installed via the documented npm procedure.
// The check is best-effort and never returns an error — a missing vendor
// tree is a degraded-UI condition, not a server-startup failure.
package main

import (
	"io/fs"
	"log/slog"
)

// vendorSentinel is the path, relative to the embedded UI filesystem
// root, whose presence indicates the operator has populated the vendor
// tree. Keep it in sync with ui/vendor/README.md.
const vendorSentinel = "vendor/three.min.js"

// vendorDocsURL is the in-repo pointer surfaced in the WARNING line.
// Operators running r1-server without internet access can open it
// directly from their checkout.
const vendorDocsURL = "cmd/r1-server/ui/vendor/README.md"

// checkVendoredLibs probes the embedded UI filesystem for the vendored
// Three.js bundle and logs a single WARNING line if it is absent.
//
// The function is exported (lowercase here but called from main.go) so
// tests in vendor_check_test.go can drive it against an in-memory fs.FS
// independent of the real //go:embed tree.
func checkVendoredLibs(vfs fs.FS, logger *slog.Logger) (present bool) {
	if logger == nil {
		logger = slog.Default()
	}
	if vfs == nil {
		logger.Warn("vendor check skipped: nil filesystem")
		return false
	}
	f, err := vfs.Open(vendorSentinel)
	if err != nil {
		logger.Warn("3D UI vendor bundle missing — graph.html will fall back to CDN scripts",
			"missing_file", vendorSentinel,
			"docs", vendorDocsURL,
			"err", err.Error(),
		)
		return false
	}
	_ = f.Close()
	logger.Info("3D UI vendor bundle present", "sentinel", vendorSentinel)
	return true
}
