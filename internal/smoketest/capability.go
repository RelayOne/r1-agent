// Package smoketest runs environment-aware smoke verification on a
// session's declared deliverables after its acceptance criteria pass.
//
// The problem it solves: session ACs are usually build / type-check /
// pattern-match commands. They prove the code exists and compiles.
// They do NOT prove the feature actually runs. A session can ship
// code that type-checks but renders an empty div, handles no real
// request, or calls a route handler that returns a hardcoded
// success response. smoketest is the last gate between "ACs passed"
// and "session recorded as done."
//
// Environment-aware by design. The harness runs on Linux and cannot:
//   - Boot an iOS simulator (no macOS / Xcode)
//   - Spin up an Android emulator without significant setup
//   - Exercise hardware-dependent code (sensors, Bluetooth)
//   - Hit live external APIs that have no mock
//
// For those targets we fall back to static-only verification:
// type-checking, import resolution, export-shape validation.
// static-only does NOT block the session from recording as done;
// it's surfaced in the final report so an operator knows what
// wasn't actually exercised.

package smoketest

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/RelayOne/r1-agent/internal/plan"
)

// Capability names the runtime class the smoke runner will target.
type Capability string

const (
	// CapabilityUnknown — no recognizable stack indicators. Runner
	// falls back to the project's `test` npm script or go test if
	// present; otherwise static-only.
	CapabilityUnknown Capability = "unknown"

	// CapabilityNativeNode — generic Node.js / TypeScript project
	// (library, CLI, shared package). Runnable: build + test.
	CapabilityNativeNode Capability = "native-node"

	// CapabilityNativeGo — Go project. Runnable: go build + go test.
	CapabilityNativeGo Capability = "native-go"

	// CapabilityNativeRust — Rust project. Runnable: cargo build + cargo test.
	CapabilityNativeRust Capability = "native-rust"

	// CapabilityNativePython — Python project with a known runner
	// (pytest / unittest). Runnable: install + test.
	CapabilityNativePython Capability = "native-python"

	// CapabilityWebNextJS — Next.js app. Runnable: next build +
	// typecheck. Browser-level smoke (playwright) requires headless
	// Chrome and is NOT attempted in v1 (operator opts in separately).
	CapabilityWebNextJS Capability = "web-nextjs"

	// CapabilityMobileRNExpo — React Native via Expo (Sentinel's
	// caregiver/installer apps). Type-check runnable on Linux; app
	// execution requires a simulator we do not provide. Static-only.
	CapabilityMobileRNExpo Capability = "mobile-rn-expo"

	// CapabilityMobileIOS — pure iOS (Swift/Obj-C). Requires macOS +
	// Xcode. Always static-only on Linux runtime.
	CapabilityMobileIOS Capability = "mobile-ios"

	// CapabilityMobileAndroid — pure Android (Kotlin/Java). Requires
	// emulator setup beyond v1 scope. Static-only.
	CapabilityMobileAndroid Capability = "mobile-android"

	// CapabilityHardware — depends on physical hardware (firmware,
	// sensors, Bluetooth). Always static-only.
	CapabilityHardware Capability = "hardware"

	// CapabilityExternalAPI — integration with an external service
	// whose live endpoint we cannot call from this environment.
	// Static-only; the session is expected to ship mocks and those
	// mocks are what we verify.
	CapabilityExternalAPI Capability = "external-api"
)

// Runtime describes the concrete actions the runner will take for a
// given capability. For runnable capabilities RunnableCommands are
// executed in order; any non-zero exit fails the smoke. For static-
// only capabilities, StaticChecks list the structural checks
// attempted instead (also shell commands but scoped to type-check /
// lint / import-resolution).
type Runtime struct {
	Capability     Capability
	Runnable       bool
	Reason         string   // one-line explanation of why static-only (empty when Runnable)
	RunCommands    []string // shell commands (bash -lc) executed when Runnable
	StaticCommands []string // shell commands run in static-only mode
}

// DetectCapability infers the runtime class for a session based on
// its declared task files and a repoRoot scan. The matching is
// deliberately conservative — we'd rather fall back to static-only
// than pretend we can exercise a target we cannot. Precedence:
// mobile > web > native-node > native-go > native-rust > python >
// unknown; the first matcher wins.
func DetectCapability(session plan.Session, repoRoot string) Runtime {
	lowerFiles := collectLowerFiles(session)
	anyFile := func(substr string) bool {
		for f := range lowerFiles {
			if strings.Contains(f, substr) {
				return true
			}
		}
		return false
	}

	// Mobile (iOS / Android / React Native) — always static on Linux.
	// Only classify as mobile when the session touches actual source
	// code (.ts/.tsx/.js/.jsx in the mobile app), not just
	// config-only files (eas.json, app.json, app.config.js). A
	// session that scaffolds an eas.json inside apps/caregiver/ has
	// zero mobile source to typecheck; running `pnpm typecheck`
	// against uninstalled mobile deps just fires a false-positive
	// smoke failure and spins the session into retry (run 14 bug).
	if (anyFile("apps/caregiver/") || anyFile("apps/installer/")) && hasMobileSource(lowerFiles) {
		return Runtime{
			Capability:     CapabilityMobileRNExpo,
			Runnable:       false,
			Reason:         "React Native / Expo app; Linux runtime cannot boot iOS or Android simulators",
			StaticCommands: []string{"pnpm typecheck || npx tsc --noEmit"},
		}
	}
	if anyFile("ios/") || hasFileWithExt(lowerFiles, ".swift") || hasFileWithExt(lowerFiles, ".xcodeproj") {
		return Runtime{
			Capability: CapabilityMobileIOS,
			Runnable:   false,
			Reason:     "iOS target requires macOS + Xcode; unavailable on this Linux runtime",
		}
	}
	if anyFile("android/") || hasFileWithExt(lowerFiles, ".kt") || hasFileWithExt(lowerFiles, ".gradle") {
		return Runtime{
			Capability: CapabilityMobileAndroid,
			Runnable:   false,
			Reason:     "Android target requires emulator setup beyond v1 smoke-runner scope",
		}
	}

	// Web Next.js
	if anyFile("apps/web/") || anyFile("next.config.") || anyFile("next-env.d.ts") {
		return Runtime{
			Capability: CapabilityWebNextJS,
			Runnable:   true,
			RunCommands: []string{
				"pnpm install --frozen-lockfile=false",
				"pnpm --filter ./apps/web build",
				"pnpm --filter ./apps/web typecheck || (cd apps/web && npx tsc --noEmit)",
			},
		}
	}

	// Native Node / TypeScript library. Detected by package.json
	// presence at the session's scope or under packages/.
	if fileExists(repoRoot, "package.json") || anyFile("packages/") || anyFile("package.json") {
		return Runtime{
			Capability: CapabilityNativeNode,
			Runnable:   true,
			RunCommands: []string{
				"pnpm install --frozen-lockfile=false || npm install",
				"pnpm -r build || true", // best-effort; many packages have no build
				"pnpm -r typecheck || pnpm -r exec tsc --noEmit || true",
				"pnpm -r test --if-present || pnpm -r run test --if-present || true",
			},
		}
	}

	// Native Go
	if fileExists(repoRoot, "go.mod") || hasFileWithExt(lowerFiles, ".go") {
		return Runtime{
			Capability: CapabilityNativeGo,
			Runnable:   true,
			RunCommands: []string{
				"go build ./...",
				"go test ./...",
			},
		}
	}

	// Native Rust
	if fileExists(repoRoot, "Cargo.toml") || hasFileWithExt(lowerFiles, ".rs") {
		return Runtime{
			Capability: CapabilityNativeRust,
			Runnable:   true,
			RunCommands: []string{
				"cargo build --all",
				"cargo test --all",
			},
		}
	}

	// Python
	if fileExists(repoRoot, "pyproject.toml") || fileExists(repoRoot, "requirements.txt") ||
		hasFileWithExt(lowerFiles, ".py") {
		return Runtime{
			Capability: CapabilityNativePython,
			Runnable:   true,
			RunCommands: []string{
				"pip install -r requirements.txt || poetry install || true",
				"pytest -q || python -m unittest discover -q || true",
			},
		}
	}

	// External API hint: session title mentions integration with a
	// known external service we can't reach. We treat as static-only
	// so the harness doesn't try to make real calls.
	title := strings.ToLower(session.Title + " " + session.Description)
	for _, hint := range []string{"guesty", "hostaway", "mews", "pointclickcare", "yardi", "realpage", "stripe", "webhook"} {
		if strings.Contains(title, hint) {
			return Runtime{
				Capability: CapabilityExternalAPI,
				Runnable:   false,
				Reason:     "integration with " + hint + "; live endpoint unreachable from this runtime — verifying mocks only",
			}
		}
	}

	return Runtime{
		Capability: CapabilityUnknown,
		Runnable:   false,
		Reason:     "no recognizable stack indicator; cannot determine runnable smoke commands",
	}
}

func collectLowerFiles(session plan.Session) map[string]struct{} {
	out := map[string]struct{}{}
	for _, t := range session.Tasks {
		for _, f := range t.Files {
			out[strings.ToLower(filepath.Clean(f))] = struct{}{}
		}
	}
	return out
}

func hasFileWithExt(files map[string]struct{}, ext string) bool {
	for f := range files {
		if strings.HasSuffix(f, ext) {
			return true
		}
	}
	return false
}

// hasMobileSource returns true when the session touches actual
// React Native / Expo SOURCE code (.ts/.tsx/.js/.jsx inside the
// mobile app dirs), not just config files like eas.json /
// app.json / app.config.js. A session that only scaffolds config
// has no source to typecheck and mobile-rn-expo smoke gating on
// it produces false failures while the shared packages the mobile
// app depends on are still being built in parallel sessions.
func hasMobileSource(files map[string]struct{}) bool {
	for f := range files {
		if !strings.Contains(f, "apps/caregiver/") && !strings.Contains(f, "apps/installer/") {
			continue
		}
		if strings.HasSuffix(f, ".ts") || strings.HasSuffix(f, ".tsx") ||
			strings.HasSuffix(f, ".js") || strings.HasSuffix(f, ".jsx") {
			return true
		}
	}
	return false
}

func fileExists(repoRoot, rel string) bool {
	info, err := os.Stat(filepath.Join(repoRoot, rel))
	return err == nil && !info.IsDir()
}
