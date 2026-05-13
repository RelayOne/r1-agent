package main

// throttle_wiring.go — daemon-side glue for the C3 per-tool throttle
// gate. Owns:
//
//   - newServeThrottler: constructs the Limiter at startup, loading
//     the operator's r1.policy.yaml (or auto-discovered default) and
//     falling back to the bundled defaults on parse failure.
//   - makeThrottleReloader: returns the closure handed to
//     jsonrpc.HubHandler.SetReloadConfigFunc so daemon.reload_config
//     reloads the throttle policy hot.
//   - serveThrottlerHandle: the lifetime owner; wraps the Limiter
//     plus its GC goroutine context so runServeLoop's defer can
//     unwind cleanly.
//
// Wiring lives here rather than in internal/throttle so the throttle
// package stays free of dependencies on cmd/r1 logging and config
// auto-discovery. See specs/per-tool-throttling.md T14 + T16.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/RelayOne/r1/internal/config"
	"github.com/RelayOne/r1/internal/throttle"
)

// serveThrottlerHandle bundles the live Limiter with the cancel func
// that stops its background GC goroutine. runServeLoop calls Stop on
// graceful shutdown so the GC loop exits cleanly.
type serveThrottlerHandle struct {
	Limiter throttle.Limiter
	cancel  context.CancelFunc
	source  string // path of the policy file actually applied (informational)
}

// serveThrottler is the package-level handle wired into the
// reload-config callback. Initialised by newServeThrottler at
// startup; the reload closure mutates its embedded Limiter via
// Reload(), so the handle itself does not need to be replaced.
var serveThrottler *serveThrottlerHandle

// Stop cancels the GC goroutine. Safe to call on a nil handle.
func (h *serveThrottlerHandle) Stop() {
	if h == nil || h.cancel == nil {
		return
	}
	h.cancel()
}

// newServeThrottler constructs the daemon's Limiter at startup. The
// configPath argument is the operator-supplied --config flag value;
// when empty, we auto-discover via the same search order
// config.AutoLoadPolicy uses.
//
// On any error (file missing, parse fail, validate fail) we fall
// back to the bundled defaults rather than crashing the daemon —
// throttling is a safety feature, but the daemon must still come
// up. The fallback path is logged so the operator can spot a
// silent misconfig.
func newServeThrottler(configPath string) *serveThrottlerHandle {
	cfg, src := loadThrottlingConfig(configPath)
	l := throttle.New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	go throttle.StartGC(ctx, l)
	return &serveThrottlerHandle{Limiter: l, cancel: cancel, source: src}
}

// loadThrottlingConfig reads the operator's policy file and extracts
// the throttling block. Always returns a viable config — defaults
// stand in when anything goes wrong, so the daemon never refuses to
// start because of a typo'd rate string.
func loadThrottlingConfig(configPath string) (throttle.Config, string) {
	repoRoot, _ := os.Getwd()
	policy, err := config.AutoLoadPolicy(repoRoot, configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warn: throttle: load policy %q failed: %v (using bundled defaults)\n",
			configPath, err)
		return throttle.DefaultPolicy(), "<bundled-defaults>"
	}
	if policy.Throttling.IsZero() {
		fmt.Fprintf(os.Stderr, "info: throttle: no throttling: block in policy; using bundled defaults\n")
		return throttle.DefaultPolicy(), "<bundled-defaults>"
	}
	src := configPath
	if src == "" {
		src = filepath.Join(repoRoot, "<auto-discovered>")
	}
	return policy.Throttling, src
}

// makeThrottleReloader returns the SetReloadConfigFunc closure that
// daemon.reload_config invokes. The closure:
//
//  1. Resolves the policy path (operator-supplied or auto-discovered).
//  2. Loads + validates the new throttling config.
//  3. Calls Limiter.Reload to swap the active policy without
//     dropping tokens.
//  4. Returns the absolute path applied so operators can confirm
//     which file took effect.
//
// Validation failure preserves the old policy and surfaces the
// error via the RPC envelope. The acceptance criterion is that the
// new rate is observable within 50ms — the atomic.Pointer swap
// inside Reload makes that effectively immediate (sub-µs).
func makeThrottleReloader(h *serveThrottlerHandle) func(string) (string, error) {
	return func(path string) (string, error) {
		if h == nil || h.Limiter == nil {
			return path, fmt.Errorf("throttle: limiter not initialised")
		}
		newCfg, src := loadThrottlingConfig(path)
		if err := h.Limiter.Reload(newCfg); err != nil {
			return path, fmt.Errorf("throttle: reload rejected: %w", err)
		}
		h.source = src
		if strings.TrimSpace(src) == "" {
			src = path
		}
		return src, nil
	}
}
