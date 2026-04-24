// Package plan — integrity_desktop.go
//
// Cross-ecosystem integrity checks for Electron and Tauri desktop
// applications. Electron apps are standard Node.js packages and are
// fully covered by the TypeScript ecosystem (the `electron` package
// is a regular npm dependency). The tauri-specific contract this
// file enforces:
//
//   When the frontend calls `invoke('command_name', ...)` via
//   @tauri-apps/api, the Rust backend (src-tauri/src/*.rs) must have
//   a matching `#[tauri::command]` function with the same name
//   registered in the tauri::Builder::invoke_handler call.
//
// This is a cross-ecosystem check: neither the TypeScript ecosystem
// nor the Rust ecosystem alone can validate it, because the contract
// spans the two. The desktop ecosystem runs last (registered here
// after the primary single-language ecosystems) and only fires when
// it detects a Tauri project structure.
package plan

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/ericmacdougall/stoke/internal/logging"
)

func init() {
	RegisterEcosystem(&tauriEcosystem{})
}

type tauriEcosystem struct{}

func (tauriEcosystem) Name() string { return "tauri" }

// Owns claims nothing on its own — Tauri files are already claimed
// by rust (src-tauri/*.rs) or typescript (frontend). The tauri
// ecosystem relies on the gate calling its probes even when it
// owns zero files of its own, so we report Owns=false unconditionally.
// However the gate currently dispatches based on Owns, so we instead
// claim tauri.conf.json as our handle — a Tauri project always has
// this file, and owning it opts the whole project into the cross-
// check without colliding with other ecosystems.
func (tauriEcosystem) Owns(path string) bool {
	return filepath.Base(path) == "tauri.conf.json"
}

// AlwaysRun: the invoke/command contract probe is cross-ecosystem
// (frontend TS calls must match Rust backend handlers), so we need
// to run even when a session only touched one side.
func (tauriEcosystem) AlwaysRun() bool { return true }

// The interesting contract is cross-cutting: the probe doesn't care
// about the specific files the session wrote; it runs if the project
// has a Tauri structure.
func (tauriEcosystem) UnresolvedImports(projectRoot string, files []string) ([]ManifestMiss, error) {
	// Only activate when the project has a tauri.conf.json AND a
	// src-tauri directory AND at least one .ts/.tsx/.js/.jsx file
	// in the session's write set (so the check has something to
	// target). Otherwise return empty — other ecosystems handle
	// per-file integrity.
	if !tauriProjectDetected(projectRoot) {
		return nil, nil
	}
	// Scan the frontend source tree for `invoke('X')` calls and
	// collect the command names. (This is a workspace-wide scan
	// because the frontend's write set may reference commands added
	// in a previous session's backend writes.)
	frontendInvokes := scanTauriInvokes(projectRoot)
	backendCommands := scanTauriCommands(projectRoot)
	registeredCommands := scanTauriInvokeHandlerList(projectRoot)

	var out []ManifestMiss
	for name, srcFile := range frontendInvokes {
		if _, ok := backendCommands[name]; !ok {
			rel, _ := filepath.Rel(projectRoot, srcFile)
			out = append(out, ManifestMiss{
				SourceFile: rel,
				ImportPath: name,
				Manifest:   "src-tauri/src/",
				AddCommand: fmt.Sprintf("add `#[tauri::command] fn %s(...)` to src-tauri/src/ and register it in tauri::Builder::invoke_handler", name),
			})
			continue
		}
		if _, ok := registeredCommands[name]; !ok {
			rel, _ := filepath.Rel(projectRoot, srcFile)
			out = append(out, ManifestMiss{
				SourceFile: rel,
				ImportPath: name,
				Manifest:   "src-tauri/src/main.rs (invoke_handler)",
				AddCommand: fmt.Sprintf("register `%s` in tauri::Builder::default().invoke_handler(tauri::generate_handler![...]) in src-tauri/src/main.rs", name),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SourceFile != out[j].SourceFile {
			return out[i].SourceFile < out[j].SourceFile
		}
		return out[i].ImportPath < out[j].ImportPath
	})
	return out, nil
}

func (tauriEcosystem) MissingPublicSurface(projectRoot string, files []string) ([]PublicSurfaceMiss, error) {
	return nil, nil
}

// Tauri has no dedicated compile step beyond what rust + typescript
// already cover. Cross-ecosystem compile regression is surfaced by
// the per-ecosystem probes.
func (tauriEcosystem) CompileErrors(ctx context.Context, projectRoot string, files []string) ([]CompileErr, error) {
	return nil, nil
}

// ---------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------

func tauriProjectDetected(projectRoot string) bool {
	if info, err := os.Stat(filepath.Join(projectRoot, "src-tauri")); err != nil || !info.IsDir() {
		return false
	}
	// Either src-tauri/tauri.conf.json or tauri.conf.json at the root.
	for _, p := range []string{
		filepath.Join(projectRoot, "src-tauri", "tauri.conf.json"),
		filepath.Join(projectRoot, "tauri.conf.json"),
	} {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

var tauriInvokeRE = regexp.MustCompile(`\binvoke\s*\(\s*['"\x60]([A-Za-z_][A-Za-z0-9_]*)['"\x60]`)

// scanTauriInvokes walks the frontend source (anything outside
// src-tauri and node_modules) and returns a map of command name →
// first file where it appears. This lets the gate surface a concrete
// file:line if the backend handler is missing.
func scanTauriInvokes(projectRoot string) map[string]string {
	out := map[string]string{}
	_ = filepath.WalkDir(projectRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			// Best-effort invoke scan: log and skip unreadable
			// subtrees so one permission-denied path can't hide the
			// rest of the frontend source.
			logging.Global().Warn("plan.integrity_desktop: invoke walk error", "path", path, "err", walkErr)
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == "node_modules" || name == "src-tauri" || name == ".git" ||
				name == "dist" || name == "build" || name == "target" {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if ext != ".ts" && ext != ".tsx" && ext != ".js" && ext != ".jsx" &&
			ext != ".mjs" && ext != ".cjs" && ext != ".vue" && ext != ".svelte" {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			// Unreadable frontend file: log and continue so one bad
			// file can't hide invocations declared elsewhere.
			logging.Global().Warn("plan.integrity_desktop: unreadable frontend file", "path", path, "err", readErr)
		} else {
			for _, m := range tauriInvokeRE.FindAllStringSubmatch(string(body), -1) {
				if _, dup := out[m[1]]; !dup {
					out[m[1]] = path
				}
			}
		}
		return nil
	})
	return out
}

var tauriCommandRE = regexp.MustCompile(`(?s)#\[tauri::command(?:[^\]]*)?\][^f]*?fn\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`)

func scanTauriCommands(projectRoot string) map[string]string {
	out := map[string]string{}
	srcTauri := filepath.Join(projectRoot, "src-tauri")
	_ = filepath.WalkDir(srcTauri, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			// Best-effort command scan: log and skip unreadable
			// subtrees.
			logging.Global().Warn("plan.integrity_desktop: command walk error", "path", path, "err", walkErr)
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if d.Name() == "target" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".rs") {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			// Unreadable .rs file: log and continue.
			logging.Global().Warn("plan.integrity_desktop: unreadable .rs file", "path", path, "err", readErr)
		} else {
			for _, m := range tauriCommandRE.FindAllStringSubmatch(string(body), -1) {
				out[m[1]] = path
			}
		}
		return nil
	})
	return out
}

// tauriHandlerRE matches the multi-name form:
//   tauri::generate_handler![a, b, c, path::to::fn]
var tauriHandlerRE = regexp.MustCompile(`tauri::generate_handler!\s*\[([^\]]*)\]`)

func scanTauriInvokeHandlerList(projectRoot string) map[string]struct{} {
	out := map[string]struct{}{}
	srcTauri := filepath.Join(projectRoot, "src-tauri")
	_ = filepath.WalkDir(srcTauri, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			// Best-effort handler-list scan: log and skip unreadable
			// subtrees.
			logging.Global().Warn("plan.integrity_desktop: handler walk error", "path", path, "err", walkErr)
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if d.Name() == "target" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".rs") {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			// Unreadable .rs file: log and continue.
			logging.Global().Warn("plan.integrity_desktop: unreadable handler file", "path", path, "err", readErr)
		} else {
			for _, m := range tauriHandlerRE.FindAllStringSubmatch(string(body), -1) {
				for _, raw := range strings.Split(m[1], ",") {
					raw = strings.TrimSpace(raw)
					if raw == "" {
						continue
					}
					// Last path segment only ("a::b::c" → "c").
					if i := strings.LastIndex(raw, "::"); i >= 0 {
						raw = raw[i+2:]
					}
					out[raw] = struct{}{}
				}
			}
		}
		return nil
	})
	return out
}
