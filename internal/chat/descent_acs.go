package chat

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// AcceptanceCriterion is the chat-package-local shape used by the
// descent gate's AC factory.
//
// Policy D (spec: specs/chat-descent-control.md) notes that when
// plan.AcceptanceCriterion is the canonical shape in scope, we adapt;
// the canonical plan.AcceptanceCriterion carries additional fields
// (Description, FileExists, ContentMatch, VerifyFunc) that chat
// descent does not need. Keeping a narrow local type isolates CDC-2
// from churn in the broader plan package — the CDC-3 commit that
// actually wires these into the descent engine will perform the
// adapter conversion at the call site.
type AcceptanceCriterion struct {
	ID      string
	Command string
}

// BuildACsForTouched inspects Repo + the filtered list of dirtied
// paths and returns synthetic ACs per spec §1.3. The returned slice
// is ordered (build/vet first, typecheck next, test last) so a
// failing earlier AC short-circuits later AC execution in the
// descent engine. Returns nil when no matching language/toolchain is
// detected.
func BuildACsForTouched(repo string, changed []string) []AcceptanceCriterion {
	lang := detectLanguage(repo, changed)
	switch lang {
	case "go":
		return []AcceptanceCriterion{
			{ID: "chat.build", Command: "go build ./..."},
			{ID: "chat.vet", Command: "go vet ./..."},
			{ID: "chat.test", Command: "go test ./... -count=1 -timeout=2m -run=."},
		}
	case "ts":
		return buildNodeACs(repo, true)
	case "js":
		return buildNodeACs(repo, false)
	case "python":
		if hasPytestConfig(repo) {
			return []AcceptanceCriterion{
				{ID: "chat.test", Command: "pytest -q"},
			}
		}
		return nil
	case "rust":
		return []AcceptanceCriterion{
			{ID: "chat.build", Command: "cargo check"},
			{ID: "chat.test", Command: "cargo test"},
		}
	case "config-manifest":
		// Dep-manifest-only turn. Per spec §1.3 last bullet, trigger
		// install first. Downstream build ACs rerun on the next
		// source edit — we deliberately only emit install here so
		// the chat round trip stays short.
		return []AcceptanceCriterion{
			{ID: "chat.install", Command: "pnpm install --frozen-lockfile"},
		}
	}
	return nil
}

// buildNodeACs assembles the TS/JS AC list. isTS forces the typecheck
// AC when tsconfig.json is present; pure-JS repos skip it.
func buildNodeACs(repo string, isTS bool) []AcceptanceCriterion {
	acs := make([]AcceptanceCriterion, 0, 3)
	if pnpmAvailable() {
		acs = append(acs, AcceptanceCriterion{ID: "chat.build", Command: "pnpm build"})
		if isTS && hasTSConfig(repo) {
			acs = append(acs, AcceptanceCriterion{ID: "chat.typecheck", Command: "pnpm tsc --noEmit"})
		}
		if hasTestScript(repo) {
			acs = append(acs, AcceptanceCriterion{ID: "chat.test", Command: "pnpm test -- --run"})
		}
	} else {
		acs = append(acs, AcceptanceCriterion{ID: "chat.build", Command: "npm run build"})
	}
	return acs
}

// detectLanguage inspects the changed-file list and returns a coarse
// language bucket used to pick the AC set. Precedence follows spec
// §1.3: Go wins over everything (Go-only touch dominates Go
// monorepos), TS wins over JS when any .ts/.tsx is present, then
// Python, then Rust. If only dependency manifests moved and no source
// extension is in the list, return "config-manifest".
func detectLanguage(repo string, changed []string) string {
	var anyGo, anyTS, anyJS, anyPy, anyRS, anyManifest bool
	for _, p := range changed {
		ext := strings.ToLower(filepath.Ext(p))
		switch ext {
		case ".go":
			anyGo = true
		case ".ts", ".tsx":
			anyTS = true
		case ".js", ".jsx":
			anyJS = true
		case ".py":
			anyPy = true
		case ".rs":
			anyRS = true
		}
		if _, ok := configBasenames[filepath.Base(p)]; ok {
			anyManifest = true
		}
	}
	switch {
	case anyGo:
		return "go"
	case anyTS:
		return "ts"
	case anyJS:
		// If the repo has a tsconfig, prefer the TS path so the
		// typecheck AC runs — mixed JS/TS repos benefit from
		// catching type errors introduced via .js edits too.
		if hasTSConfig(repo) {
			return "ts"
		}
		return "js"
	case anyPy:
		return "python"
	case anyRS:
		return "rust"
	case anyManifest:
		return "config-manifest"
	}
	return ""
}

// pnpmAvailable reports whether pnpm is on PATH.
func pnpmAvailable() bool {
	_, err := exec.LookPath("pnpm")
	return err == nil
}

// hasTSConfig reports whether tsconfig.json exists at the repo root.
func hasTSConfig(repo string) bool {
	_, err := os.Stat(filepath.Join(repo, "tsconfig.json"))
	return err == nil
}

// hasTestScript reports whether the repo's package.json declares a
// "test" script. A missing or malformed package.json returns false.
// The detector is intentionally coarse — it looks for a "test":
// substring rather than parsing JSON — because the AC is cheap
// enough to skip silently when the heuristic misses, and a full JSON
// parse would add brittle dependencies for a best-effort detector.
func hasTestScript(repo string) bool {
	raw, err := os.ReadFile(filepath.Join(repo, "package.json"))
	if err != nil {
		return false
	}
	// Cheap text scan: look for a "scripts" block and a "test" key.
	// Handles quoted "test": and "test" : (with optional whitespace).
	// If both appear, treat as truthy.
	text := string(raw)
	if !strings.Contains(text, "\"scripts\"") {
		return false
	}
	return strings.Contains(text, "\"test\"")
}

// hasPytestConfig reports whether the repo contains pytest
// configuration at the root. Any of pytest.ini, pyproject.toml with a
// [tool.pytest.ini_options] section, or setup.cfg with [tool:pytest]
// counts; absent config means pytest is unsafe to invoke blindly.
func hasPytestConfig(repo string) bool {
	if _, err := os.Stat(filepath.Join(repo, "pytest.ini")); err == nil {
		return true
	}
	if data, err := os.ReadFile(filepath.Join(repo, "pyproject.toml")); err == nil {
		if strings.Contains(string(data), "[tool.pytest.ini_options]") {
			return true
		}
	}
	if data, err := os.ReadFile(filepath.Join(repo, "setup.cfg")); err == nil {
		if strings.Contains(string(data), "[tool:pytest]") {
			return true
		}
	}
	return false
}
