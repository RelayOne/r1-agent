// Package plan — workspace_hygiene.go
//
// Deterministic, executor-identity aware workspace hygiene scanner and
// auto-fixer. Runs BEFORE the session's build gate fires so that LLM-
// written package scripts (and their implicit assumptions about what
// binaries are on PATH) can't silently fail with "tsc: not found" or
// "cannot find module scripts/generate-types.js".
//
// The module detects every package/build ecosystem present in the
// repository and runs the ecosystem-native scans. A single repo may
// declare multiple ecosystems (for example a Go backend with a pnpm
// monorepo frontend plus a Rust sidecar); each is scanned and fixed in
// parallel-safe, idempotent fashion.
//
// Node ecosystems (pnpm / npm / yarn) get the heaviest treatment
// because that is where the concrete failure mode lives today: LLM-
// written `scripts` blocks reference binaries like tsc, next, expo,
// vitest, tsup, turbo — without declaring the corresponding package
// in devDependencies. When the sub-workspace's build runs pnpm picks
// them up from the monorepo root if they were declared there, but
// when they were not declared anywhere the build explodes. The
// scanner parses every package.json, extracts the leading binary of
// every script value, and injects the missing devDep straight into
// the package.json so the subsequent `pnpm install` resolves it.
//
// Go, Cargo, and Python scanners are narrower — they flag missing
// install state and run `go mod tidy`, `cargo fetch`, or
// `poetry install` as appropriate. They do NOT attempt to build;
// that is the job of the foundation sanity gate that runs after.
package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/fileutil"
)

// ExecutorKind identifies a package/build ecosystem present in a
// repository. A single repository may contain multiple ecosystems.
type ExecutorKind string

// Supported executor kinds. The string values double as the
// human-readable labels used in log output.
const (
	ExecPnpm   ExecutorKind = "pnpm"
	ExecNpm    ExecutorKind = "npm"
	ExecYarn   ExecutorKind = "yarn"
	ExecCargo  ExecutorKind = "cargo"
	ExecGoMod  ExecutorKind = "go"
	ExecPip    ExecutorKind = "pip"
	ExecPoetry ExecutorKind = "poetry"
	ExecUv     ExecutorKind = "uv"
)

// HygieneFinding describes a single hygiene issue detected by the
// scanner. Findings are produced before AutoFix runs and again after
// AutoFix runs — the post-run set becomes HygieneReport.Remaining.
type HygieneFinding struct {
	// Executor identifies which ecosystem emitted the finding.
	Executor ExecutorKind

	// Package is the path (relative to repo root) of the manifest
	// that triggered the finding — package.json, Cargo.toml, go.mod,
	// pyproject.toml, or requirements.txt.
	Package string

	// Kind is a short category tag. One of:
	//   "missing-devdep"          — binary used by a script is not
	//                                declared in any dependency map.
	//   "missing-script-target"   — a script references a file on
	//                                disk that does not exist.
	//   "missing-install"         — the ecosystem has no on-disk
	//                                install state (e.g. go.sum
	//                                missing, cargo target/ missing).
	//   "missing-lockfile"        — lockfile for the ecosystem is
	//                                absent when policy requires one.
	//   "unresolved-import"       — reserved for future use.
	Kind string

	// Detail is a human-readable description ending in an
	// actionable sentence.
	Detail string

	// AutoFixable is true when the deterministic AutoFix path can
	// resolve this finding without human / agent intervention.
	AutoFixable bool

	// Suggested is context-specific: the npm package spec to inject
	// (e.g. "typescript@^5"), a path to create, or a command to run.
	Suggested string
}

// HygieneReport is the consolidated output of ScanAndAutoFix.
type HygieneReport struct {
	// Executors is the set of ecosystems detected in the repository.
	Executors []ExecutorKind

	// PreFix is the full list of findings before any auto-fixes ran.
	PreFix []HygieneFinding

	// AutoFixed is the subset of PreFix that AutoFix resolved.
	AutoFixed []HygieneFinding

	// Remaining is what is still outstanding after AutoFix ran — the
	// set the caller can hand to an LLM agent for follow-up repair.
	Remaining []HygieneFinding

	// Summary is a one-line string suitable for direct stdout output.
	Summary string
}

// DetectExecutors scans repoRoot and returns every ecosystem marker
// file it finds. The return order is deterministic (alphabetical by
// executor kind string) so log output is stable across runs.
//
// Marker rules:
//
//	pnpm    : pnpm-workspace.yaml OR pnpm-lock.yaml
//	npm     : package-lock.json (and no pnpm marker)
//	yarn    : yarn.lock
//	cargo   : Cargo.toml (anywhere — walks 2 levels deep)
//	go      : go.mod
//	pip     : requirements.txt
//	poetry  : pyproject.toml containing "[tool.poetry]"
//	uv      : uv.lock OR pyproject.toml containing "[tool.uv]"
//
// Node defaults to pnpm when package.json exists but no lockfile is
// present — pnpm is the workspace default for stoke-managed repos.
func DetectExecutors(repoRoot string) []ExecutorKind {
	seen := map[ExecutorKind]bool{}
	has := func(rel string) bool {
		_, err := os.Stat(filepath.Join(repoRoot, rel))
		return err == nil
	}

	// Node ecosystem detection.
	pnpmMarker := has("pnpm-workspace.yaml") || has("pnpm-lock.yaml")
	npmMarker := has("package-lock.json")
	yarnMarker := has("yarn.lock")
	pkgJSON := has("package.json")
	switch {
	case pnpmMarker:
		seen[ExecPnpm] = true
	case yarnMarker:
		seen[ExecYarn] = true
	case npmMarker:
		seen[ExecNpm] = true
	case pkgJSON:
		// Default Node runner when nothing is committed yet.
		seen[ExecPnpm] = true
	}

	// Go.
	if has("go.mod") {
		seen[ExecGoMod] = true
	}

	// Cargo — can live in root or sub-crate. Walk 2 levels deep.
	if hasCargoNear(repoRoot, 2) {
		seen[ExecCargo] = true
	}

	// Python family.
	if has("requirements.txt") {
		seen[ExecPip] = true
	}
	if has("pyproject.toml") {
		data, _ := os.ReadFile(filepath.Join(repoRoot, "pyproject.toml"))
		s := string(data)
		if strings.Contains(s, "[tool.poetry]") {
			seen[ExecPoetry] = true
		}
		if strings.Contains(s, "[tool.uv]") {
			seen[ExecUv] = true
		}
	}
	if has("uv.lock") {
		seen[ExecUv] = true
	}

	out := make([]ExecutorKind, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return string(out[i]) < string(out[j]) })
	return out
}

// hasCargoNear returns true when any Cargo.toml exists at or below
// repoRoot within the given depth. Stops at node_modules and target
// directories so it doesn't spend time walking vendored trees.
func hasCargoNear(repoRoot string, maxDepth int) bool {
	found := false
	_ = filepath.WalkDir(repoRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if p != repoRoot && (name == "node_modules" || name == "target" || name == ".git" || strings.HasPrefix(name, ".")) {
				return fs.SkipDir
			}
			rel, _ := filepath.Rel(repoRoot, p)
			if strings.Count(rel, string(filepath.Separator)) >= maxDepth {
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() == "Cargo.toml" {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	return found
}

// ScanOnly runs the deterministic hygiene sweep for every detected
// ecosystem WITHOUT applying any auto-fixes. Safe to call on any
// repo; no files are written, no commands are run. Use this for the
// read-only audit path (see cmd/r1/inspect.go).
//
// The returned HygieneReport has PreFix populated with every
// finding the per-ecosystem scanners produced, AutoFixed set to nil,
// and Remaining set to a copy of PreFix — representing the full set
// of outstanding issues the caller could still act on. Summary is a
// one-line string suitable for direct stdout output.
//
// ScanOnly never returns a non-nil error in its current form; the
// signature preserves room for future scanners that could fail I/O.
func ScanOnly(ctx context.Context, repoRoot string) (*HygieneReport, error) {
	if repoRoot == "" {
		return &HygieneReport{Summary: "no repo root"}, nil
	}
	_ = ctx // reserved for future scanners that may honour cancellation
	execs := DetectExecutors(repoRoot)
	report := &HygieneReport{Executors: execs}
	if len(execs) == 0 {
		report.Summary = "no ecosystems detected"
		return report, nil
	}
	pre := scanAll(repoRoot, execs)
	report.PreFix = pre
	if len(pre) > 0 {
		remaining := make([]HygieneFinding, len(pre))
		copy(remaining, pre)
		report.Remaining = remaining
	}
	autoFixable := 0
	for _, f := range pre {
		if f.AutoFixable {
			autoFixable++
		}
	}
	report.Summary = fmt.Sprintf("%d finding(s), %d auto-fixable", len(pre), autoFixable)
	return report, nil
}

// ScanAndAutoFix runs the deterministic hygiene sweep for every
// detected ecosystem and returns a populated HygieneReport. It
// performs safe auto-fixes in place (injecting missing devDeps,
// running `pnpm install`, `go mod tidy`, `cargo fetch`, etc.) and
// re-scans the repository to populate Remaining.
//
// Errors encountered by individual fixes are absorbed into the
// Remaining list rather than bubbled up — the caller gets one
// consolidated report instead of a fatal failure. Safe to call
// multiple times; idempotent after the first successful fix. The
// overall operation is bounded by a 5-minute deadline (in addition
// to any deadline already attached to ctx).
func ScanAndAutoFix(ctx context.Context, repoRoot string) (*HygieneReport, error) {
	if repoRoot == "" {
		return &HygieneReport{Summary: "no repo root"}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	execs := DetectExecutors(repoRoot)
	report := &HygieneReport{Executors: execs}
	if len(execs) == 0 {
		report.Summary = "no ecosystems detected"
		return report, nil
	}

	fmt.Printf("  🧽 hygiene: detected %s\n", joinExecs(execs))

	// Phase 1: scan.
	report.PreFix = scanAll(repoRoot, execs)

	// Phase 2: auto-fix what we can.
	fixed := autoFixAll(ctx, repoRoot, execs, report.PreFix)
	report.AutoFixed = fixed

	// Phase 3: rescan to populate Remaining. We keep any pre-fix
	// findings that were NOT auto-fixable in the first place
	// (missing-script-target, etc.) and then re-run the scanners to
	// capture anything the install surfaced that still remains.
	rescanned := scanAll(repoRoot, execs)
	report.Remaining = mergeRemaining(rescanned, report.PreFix, fixed)

	// Build summary.
	report.Summary = fmt.Sprintf("%d pre-fix, %d auto-fixed, %d remaining",
		len(report.PreFix), len(report.AutoFixed), len(report.Remaining))

	if len(report.Remaining) > 0 {
		fmt.Printf("  🧽 hygiene: %d residual finding(s) after auto-fix\n", len(report.Remaining))
	} else if len(report.AutoFixed) > 0 {
		fmt.Printf("  🧽 hygiene: all findings resolved\n")
	}
	return report, nil
}

// joinExecs renders an ExecutorKind slice for log output.
func joinExecs(execs []ExecutorKind) string {
	parts := make([]string, len(execs))
	for i, e := range execs {
		parts[i] = string(e)
	}
	return strings.Join(parts, ", ")
}

// scanAll dispatches to per-ecosystem scanners and returns the
// concatenated findings list.
func scanAll(repoRoot string, execs []ExecutorKind) []HygieneFinding {
	var findings []HygieneFinding
	nodeSeen := false
	for _, e := range execs {
		switch e {
		case ExecPnpm, ExecNpm, ExecYarn:
			if nodeSeen {
				continue
			}
			nodeSeen = true
			findings = append(findings, scanNode(repoRoot, e)...)
		case ExecGoMod:
			findings = append(findings, scanGo(repoRoot)...)
		case ExecCargo:
			findings = append(findings, scanCargo(repoRoot)...)
		case ExecPip:
			findings = append(findings, scanPip(repoRoot)...)
		case ExecPoetry:
			findings = append(findings, scanPoetry(repoRoot)...)
		case ExecUv:
			findings = append(findings, scanUv(repoRoot)...)
		case ExecTS, ExecPyright:
			// Type-checkers have no package manifest to hygiene-scan.
		}
	}
	return findings
}

// mergeRemaining builds the post-fix remaining list. Any
// non-auto-fixable finding from the original scan is kept verbatim.
// Any finding that STILL appears after the rescan is also kept. Any
// finding that was auto-fixed and is not present in the rescan is
// dropped.
func mergeRemaining(rescan, pre, fixed []HygieneFinding) []HygieneFinding {
	fixedKey := map[string]bool{}
	for _, f := range fixed {
		fixedKey[findingKey(f)] = true
	}
	remaining := make([]HygieneFinding, 0, len(rescan)+len(pre))

	// Keep everything from the rescan. This captures "install surfaced
	// a new issue" as well as "auto-fix didn't actually remove it".
	remaining = append(remaining, rescan...)

	// And anything from pre that wasn't auto-fixable and isn't
	// already in the rescan list.
	seen := map[string]bool{}
	for _, f := range remaining {
		seen[findingKey(f)] = true
	}
	for _, f := range pre {
		if f.AutoFixable {
			continue
		}
		if fixedKey[findingKey(f)] {
			continue
		}
		k := findingKey(f)
		if seen[k] {
			continue
		}
		seen[k] = true
		remaining = append(remaining, f)
	}
	return remaining
}

// findingKey returns a stable identity key for a finding.
func findingKey(f HygieneFinding) string {
	return string(f.Executor) + "|" + f.Package + "|" + f.Kind + "|" + f.Suggested
}

// ---------------------------------------------------------------------
// Node scanner.
// ---------------------------------------------------------------------

// knownNodeBinaries maps CLI binaries commonly referenced from
// package.json scripts to the npm package that provides them. The
// version constraints are deliberately generous major-range pins so
// the package manager can pick a working resolution; they mirror the
// defaults the stoke-managed repos rely on today.
var knownNodeBinaries = map[string]string{
	"tsc":         "typescript@^5",
	"next":        "next@^14",
	"expo":        "expo@~51",
	"expo-cli":    "expo-cli",
	"vitest":      "vitest@^1",
	"vite":        "vite@^5",
	"eslint":      "eslint@^8",
	"prettier":    "prettier@^3",
	"tsup":        "tsup@^8",
	"turbo":       "turbo@^2",
	"jest":        "jest@^29",
	"rimraf":      "rimraf@^5",
	"tsc-alias":   "tsc-alias@^1",
	"tsx":         "tsx@^4",
	"changeset":   "@changesets/cli",
	"changesets":  "@changesets/cli",
	"nx":          "nx",
	"playwright":  "playwright@^1",
	"webpack":     "webpack@^5",
	"rollup":      "rollup@^4",
	"babel":       "@babel/cli",
	"postcss":     "postcss-cli",
	"tailwindcss": "tailwindcss@^3",
}

// readRootDeclared returns the set of package names declared as deps
// or devDeps (including peer/optional) in the root package.json.
// Used by scanNode to avoid false-flagging child-workspace scripts
// that call a tool declared at the monorepo root — pnpm hoists root
// devDeps into node_modules/.bin, so the binary already resolves.
func readRootDeclared(repoRoot string) map[string]bool {
	out := map[string]bool{}
	data, err := os.ReadFile(filepath.Join(repoRoot, "package.json"))
	if err != nil {
		return out
	}
	var pkg map[string]interface{}
	if json.Unmarshal(data, &pkg) != nil {
		return out
	}
	for _, key := range []string{"dependencies", "devDependencies", "peerDependencies", "optionalDependencies"} {
		for name := range asStringMap(pkg[key]) {
			out[name] = true
		}
	}
	return out
}

// nodeShellSkip is the set of leading tokens we ignore when extracting
// the binary a script actually invokes.
var nodeShellSkip = map[string]bool{
	"cd": true, "set": true, "export": true, "exec": true, "&&": true, "||": true, ";": true,
	"if": true, "then": true, "else": true, "fi": true, "do": true, "done": true, "for": true,
	"while": true, "time": true, "env": true,
}

// nodePathRe matches `node <file>` or `./path.sh` helper references in
// script values so we can flag missing script targets.
var nodePathRe = regexp.MustCompile(`(?:^|\s)(?:node\s+([^\s;&|]+)|(\.\/[^\s;&|]+\.(?:sh|js|mjs|cjs|ts|tsx|py)))`)

// scanNode walks every package.json under repoRoot (depth-capped,
// skipping node_modules) and returns findings for missing devDeps and
// missing script targets.
func scanNode(repoRoot string, kind ExecutorKind) []HygieneFinding {
	var findings []HygieneFinding
	// Pre-compute the root workspace's declared deps so child packages
	// whose scripts call a tool declared at the root (common pnpm
	// monorepo pattern) are NOT falsely flagged as missing-devdep.
	// Without this, every child `tsc --noEmit` script gets rewritten
	// even though the binary already resolves via the root install.
	rootDeclared := readRootDeclared(repoRoot)
	pkgFiles := findPackageJSONs(repoRoot)
	for _, pkgPath := range pkgFiles {
		rel, _ := filepath.Rel(repoRoot, pkgPath)
		data, err := os.ReadFile(pkgPath)
		if err != nil {
			continue
		}
		var pkg map[string]interface{}
		if err := json.Unmarshal(data, &pkg); err != nil {
			continue
		}
		scripts := asStringMap(pkg["scripts"])
		if len(scripts) == 0 {
			continue
		}
		deps := asStringMap(pkg["dependencies"])
		devDeps := asStringMap(pkg["devDependencies"])
		peer := asStringMap(pkg["peerDependencies"])
		optional := asStringMap(pkg["optionalDependencies"])

		isDeclared := func(pkgName string) bool {
			if deps[pkgName] != "" || devDeps[pkgName] != "" || peer[pkgName] != "" || optional[pkgName] != "" {
				return true
			}
			// Root-level declaration also counts — pnpm hoists root
			// devDependencies into node_modules/.bin so child scripts
			// that call `tsc`, `next`, etc. resolve fine without
			// declaring them locally. Avoid rewriting healthy
			// manifests in that case.
			return rootDeclared[pkgName]
		}

		seenBin := map[string]bool{}
		for _, scriptValue := range scripts {
			bin := extractLeadingBin(scriptValue)
			if bin == "" || seenBin[bin] {
				// still check helper paths below
			} else {
				seenBin[bin] = true
				if spec, ok := knownNodeBinaries[bin]; ok {
					pkgName := specPackage(spec)
					if !isDeclared(pkgName) {
						findings = append(findings, HygieneFinding{
							Executor:    kind,
							Package:     rel,
							Kind:        "missing-devdep",
							Detail:      fmt.Sprintf("script references %q but %s is not in dependencies/devDependencies of %s — inject into devDependencies.", bin, pkgName, rel),
							AutoFixable: true,
							Suggested:   spec,
						})
					}
				}
			}

			// Helper-path references (node scripts/foo.js, ./bin.sh).
			for _, m := range nodePathRe.FindAllStringSubmatch(scriptValue, -1) {
				path := m[1]
				if path == "" {
					path = m[2]
				}
				path = strings.TrimPrefix(path, "./")
				if path == "" {
					continue
				}
				abs := filepath.Join(filepath.Dir(pkgPath), path)
				if !fileutil.FileExists(abs) {
					findings = append(findings, HygieneFinding{
						Executor:    kind,
						Package:     rel,
						Kind:        "missing-script-target",
						Detail:      fmt.Sprintf("script references %q but that file does not exist on disk — create the helper or remove the script.", path),
						AutoFixable: false,
						Suggested:   path,
					})
				}
			}
		}
	}
	return findings
}

// findPackageJSONs returns every package.json under repoRoot that
// lives in a reasonable monorepo location. Depth is capped so we
// never walk into node_modules or deep vendored trees.
func findPackageJSONs(repoRoot string) []string {
	// Root package.json first.
	var out []string
	root := filepath.Join(repoRoot, "package.json")
	if fileutil.FileExists(root) {
		out = append(out, root)
	}
	// Monorepo workspace prefixes we care about.
	prefixes := []string{"apps", "packages", "tooling", "services", "libs"}
	for _, prefix := range prefixes {
		base := filepath.Join(repoRoot, prefix)
		if !fileutil.DirExists(base) {
			continue
		}
		_ = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				name := d.Name()
				if name == "node_modules" || name == ".next" || name == "dist" || name == "build" || strings.HasPrefix(name, ".") {
					return fs.SkipDir
				}
				// Cap at depth 3 below the prefix base.
				rel, _ := filepath.Rel(base, p)
				if strings.Count(rel, string(filepath.Separator)) >= 3 {
					return fs.SkipDir
				}
				return nil
			}
			if d.Name() == "package.json" {
				out = append(out, p)
			}
			return nil
		})
	}
	sort.Strings(out)
	return out
}

// asStringMap coerces an arbitrary JSON value into map[string]string,
// returning nil when the input isn't an object.
func asStringMap(v interface{}) map[string]string {
	m, ok := v.(map[string]interface{})
	if !ok {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, val := range m {
		if s, ok := val.(string); ok {
			out[k] = s
		}
	}
	return out
}

// extractLeadingBin pulls the first meaningful binary reference out of
// a shell-ish script value. Returns "" when the script doesn't
// obviously invoke one (e.g. "echo ok").
func extractLeadingBin(script string) string {
	tokens := strings.Fields(script)
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		// Skip env-prefix assignments like FOO=bar.
		if strings.Contains(tok, "=") && !strings.ContainsAny(tok, "/.\\") {
			// bare FOO=bar form
			if eqIdx := strings.Index(tok, "="); eqIdx > 0 && !strings.ContainsAny(tok[:eqIdx], " \t") {
				continue
			}
		}
		if nodeShellSkip[tok] {
			continue
		}
		if tok == "npx" {
			if i+1 < len(tokens) {
				return strings.TrimPrefix(tokens[i+1], "-")
			}
			continue
		}
		if tok == "pnpm" || tok == "yarn" || tok == "npm" {
			// Peel `pnpm exec <bin>`, `npm exec <bin>`, `yarn exec <bin>`,
			// and `pnpm dlx <bin>` to find the real binary being
			// invoked — without this, every `pnpm exec tsc --noEmit`
			// escaped the hygiene check even though the underlying
			// bin still needs a devDep. For the `pnpm run <script>`
			// form (nested script) we continue to return "" so the
			// nested script gets its own pass.
			if i+1 < len(tokens) {
				next := tokens[i+1]
				if next == "exec" || next == "dlx" {
					if i+2 < len(tokens) {
						cand := tokens[i+2]
						if strings.ContainsAny(cand, "/\\") {
							return ""
						}
						// Strip a leading flag token like "--"
						// (pnpm exec -- tsc --noEmit).
						if strings.HasPrefix(cand, "-") {
							if i+3 < len(tokens) {
								cand = tokens[i+3]
								if strings.ContainsAny(cand, "/\\") || strings.HasPrefix(cand, "-") {
									return ""
								}
								return cand
							}
							return ""
						}
						return cand
					}
				}
			}
			return ""
		}
		if tok == "node" {
			// node path.js — leading bin is not a named package.
			return ""
		}
		// Strip leading ./ or path — only bare binary names map.
		if strings.ContainsAny(tok, "/\\") {
			return ""
		}
		return tok
	}
	return ""
}

// specPackage strips the @version suffix from an npm spec. Handles
// both plain and scoped package names.
func specPackage(spec string) string {
	if strings.HasPrefix(spec, "@") {
		// scoped: @scope/name@range
		at := strings.LastIndex(spec, "@")
		if at > 0 {
			return spec[:at]
		}
		return spec
	}
	if at := strings.Index(spec, "@"); at > 0 {
		return spec[:at]
	}
	return spec
}

// specVersion returns the version range portion of an npm spec
// (without the leading @). Returns "*" when no version range is
// embedded.
func specVersion(spec string) string {
	if strings.HasPrefix(spec, "@") {
		at := strings.LastIndex(spec, "@")
		if at <= 0 {
			return "*"
		}
		return spec[at+1:]
	}
	if at := strings.Index(spec, "@"); at > 0 {
		return spec[at+1:]
	}
	return "*"
}

// ---------------------------------------------------------------------
// Go scanner.
// ---------------------------------------------------------------------

// scanGo returns findings for the Go module at repoRoot. We flag a
// missing go.sum ONLY when go.mod declares non-stdlib requirements —
// modules that use nothing outside the standard library legitimately
// have no go.sum after `go mod tidy`, and flagging them as missing-
// install would cause the scanner to keep surfacing a non-issue on
// every hygiene pass.
func scanGo(repoRoot string) []HygieneFinding {
	var findings []HygieneFinding
	modPath := filepath.Join(repoRoot, "go.mod")
	if !fileutil.FileExists(modPath) {
		return findings
	}
	if fileutil.FileExists(filepath.Join(repoRoot, "go.sum")) {
		return findings
	}
	// Parse go.mod for require directives. Absence of any require
	// means the module is stdlib-only — no go.sum is expected.
	data, err := os.ReadFile(modPath)
	if err != nil {
		return findings
	}
	if !goModHasRequires(string(data)) {
		return findings
	}
	findings = append(findings, HygieneFinding{
		Executor:    ExecGoMod,
		Package:     "go.mod",
		Kind:        "missing-install",
		Detail:      "go.sum is missing despite non-stdlib requires — run `go mod tidy` to materialize module checksums.",
		AutoFixable: true,
		Suggested:   "go mod tidy",
	})
	return findings
}

// goModHasRequires reports whether a go.mod text contains at least
// one non-stdlib require directive. Handles both single-line and
// block-form requires. Stdlib imports don't appear in go.mod.
func goModHasRequires(mod string) bool {
	lines := strings.Split(mod, "\n")
	inBlock := false
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		if strings.HasPrefix(line, "require (") {
			inBlock = true
			continue
		}
		if inBlock {
			if line == ")" {
				inBlock = false
				continue
			}
			// Any non-blank non-comment line inside the block is a require.
			return true
		}
		if strings.HasPrefix(line, "require ") {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------
// Cargo scanner.
// ---------------------------------------------------------------------

// scanCargo returns findings for the Cargo workspace at repoRoot.
// Historically we flagged a missing target/ directory as a signal
// that crates hadn't been fetched — but target/ is only created by
// build/check/test, not by `cargo fetch`, so that flag fired on
// every fresh clone AND kept firing after the auto-fix ran (because
// cargo fetch doesn't produce target/). Instead we now flag only
// when Cargo.toml exists but Cargo.lock is missing, which is the
// actual "nobody has run any cargo command here" signal.
func scanCargo(repoRoot string) []HygieneFinding {
	// A repo may have a root Cargo workspace OR a Rust sidecar under
	// crates/*, services/*, tools/*, etc. DetectExecutors already
	// signalled Cargo presence via the 2-level walk, so we mirror
	// that here: find every Cargo.toml and flag each crate whose
	// Cargo.lock is missing. Skip manifests nested under an ancestor
	// that already has a Cargo.lock (workspace member sharing root
	// lock).
	manifests := findCargoManifests(repoRoot)
	findings := make([]HygieneFinding, 0, len(manifests))
	for _, mpath := range manifests {
		dir := filepath.Dir(mpath)
		if fileutil.FileExists(filepath.Join(dir, "Cargo.lock")) {
			continue
		}
		// Check ancestor up to repoRoot for a Cargo.lock — workspace
		// members legitimately share the root lock.
		if ancestorHasCargoLock(dir, repoRoot) {
			continue
		}
		rel, _ := filepath.Rel(repoRoot, mpath)
		findings = append(findings, HygieneFinding{
			Executor:    ExecCargo,
			Package:     rel,
			Kind:        "missing-install",
			Detail:      fmt.Sprintf("Cargo.lock is missing for %s — run `cargo fetch` to resolve and lock dependencies.", rel),
			AutoFixable: true,
			Suggested:   "cargo fetch",
		})
	}
	return findings
}

// findCargoManifests walks up to 3 levels under repoRoot returning
// every Cargo.toml it finds. Skips target/, node_modules/, .git/.
func findCargoManifests(repoRoot string) []string {
	var out []string
	_ = filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			base := d.Name()
			if path != repoRoot && (base == "target" || base == "node_modules" || base == ".git" || strings.HasPrefix(base, ".")) {
				return filepath.SkipDir
			}
			rel, _ := filepath.Rel(repoRoot, path)
			if strings.Count(rel, string(filepath.Separator)) > 3 {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == "Cargo.toml" {
			out = append(out, path)
		}
		return nil
	})
	return out
}

// ancestorHasCargoLock walks up from dir toward repoRoot looking
// for a Cargo.lock. Used to avoid flagging Cargo workspace members
// that share the root lockfile.
func ancestorHasCargoLock(dir, repoRoot string) bool {
	cur := dir
	for {
		parent := filepath.Dir(cur)
		if parent == cur || !strings.HasPrefix(cur, repoRoot) {
			return false
		}
		if cur == repoRoot {
			return false
		}
		cur = parent
		if fileutil.FileExists(filepath.Join(cur, "Cargo.lock")) {
			return true
		}
		if cur == repoRoot {
			return false
		}
	}
}

// ---------------------------------------------------------------------
// Python scanners.
// ---------------------------------------------------------------------

// scanPip flags requirements.txt setups that have no venv — an
// ambiguous situation we don't try to resolve deterministically
// because choosing a venv strategy is a judgement call.
func scanPip(repoRoot string) []HygieneFinding {
	var findings []HygieneFinding
	if !fileutil.FileExists(filepath.Join(repoRoot, "requirements.txt")) {
		return findings
	}
	if !fileutil.DirExists(filepath.Join(repoRoot, ".venv")) &&
		!fileutil.DirExists(filepath.Join(repoRoot, "venv")) {
		findings = append(findings, HygieneFinding{
			Executor:    ExecPip,
			Package:     "requirements.txt",
			Kind:        "missing-install",
			Detail:      "requirements.txt present but no .venv/ found — choose a venv strategy (python -m venv, uv venv, etc.) and install.",
			AutoFixable: false,
			Suggested:   "python -m venv .venv && .venv/bin/pip install -r requirements.txt",
		})
	}
	return findings
}

// scanUv flags uv projects that are missing a uv.lock file. Fix is
// `uv sync` which both resolves and installs the locked dependencies.
func scanUv(repoRoot string) []HygieneFinding {
	var findings []HygieneFinding
	if !fileutil.FileExists(filepath.Join(repoRoot, "pyproject.toml")) {
		return findings
	}
	if !fileutil.FileExists(filepath.Join(repoRoot, "uv.lock")) {
		findings = append(findings, HygieneFinding{
			Executor:    ExecUv,
			Package:     "pyproject.toml",
			Kind:        "missing-lockfile",
			Detail:      "uv.lock is missing — run `uv sync` to resolve and install.",
			AutoFixable: true,
			Suggested:   "uv sync",
		})
	}
	return findings
}

// scanPoetry flags poetry configs missing a lockfile.
func scanPoetry(repoRoot string) []HygieneFinding {
	var findings []HygieneFinding
	if !fileutil.FileExists(filepath.Join(repoRoot, "pyproject.toml")) {
		return findings
	}
	if !fileutil.FileExists(filepath.Join(repoRoot, "poetry.lock")) {
		findings = append(findings, HygieneFinding{
			Executor:    ExecPoetry,
			Package:     "pyproject.toml",
			Kind:        "missing-lockfile",
			Detail:      "poetry.lock is missing — run `poetry install --no-interaction` to generate and install.",
			AutoFixable: true,
			Suggested:   "poetry install --no-interaction",
		})
	}
	return findings
}

// ---------------------------------------------------------------------
// Auto-fix implementations.
// ---------------------------------------------------------------------

// autoFixAll dispatches each finding to the appropriate fixer and
// returns the subset that was successfully addressed. A finding is
// considered "fixed" when the fixer returned nil error — the rescan
// step in ScanAndAutoFix is what confirms the fix actually took hold.
func autoFixAll(ctx context.Context, repoRoot string, execs []ExecutorKind, findings []HygieneFinding) []HygieneFinding {
	if len(findings) == 0 {
		return nil
	}

	// Group Node missing-devdep findings by package.json so we can
	// batch-write each manifest exactly once, then run install ONCE.
	// We track both the dep map and the original findings so we can
	// mark findings as fixed ONLY after their manifest's write +
	// install actually succeed — not speculatively at queue time.
	nodeDepsByPkg := map[string]map[string]string{}
	nodeFindingsByPkg := map[string][]HygieneFinding{}
	var fixed []HygieneFinding

	for _, f := range findings {
		if !f.AutoFixable {
			continue
		}
		switch f.Kind {
		case "missing-devdep":
			abs := filepath.Join(repoRoot, f.Package)
			if nodeDepsByPkg[abs] == nil {
				nodeDepsByPkg[abs] = map[string]string{}
			}
			nodeDepsByPkg[abs][specPackage(f.Suggested)] = specVersion(f.Suggested)
			nodeFindingsByPkg[abs] = append(nodeFindingsByPkg[abs], f)
		case "missing-install":
			switch f.Executor {
			case ExecGoMod:
				if err := runBashIn(ctx, repoRoot, "go mod tidy", 2*time.Minute); err == nil {
					fmt.Printf("  🧽 hygiene: go mod tidy — ok\n")
					fixed = append(fixed, f)
				} else {
					fmt.Printf("  🧽 hygiene: go mod tidy — failed: %v\n", err)
				}
			case ExecCargo:
				// Run cargo fetch in the crate's own directory so
				// sidecar crates (crates/*, services/foo, tools/*)
				// resolve against their own manifest rather than a
				// non-existent root Cargo.toml.
				crateDir := repoRoot
				if f.Package != "" && f.Package != "Cargo.toml" {
					crateDir = filepath.Join(repoRoot, filepath.Dir(f.Package))
				}
				if err := runBashIn(ctx, crateDir, "cargo fetch", 2*time.Minute); err == nil {
					fmt.Printf("  🧽 hygiene: cargo fetch (%s) — ok\n", f.Package)
					fixed = append(fixed, f)
				} else {
					fmt.Printf("  🧽 hygiene: cargo fetch (%s) — failed: %v\n", f.Package, err)
				}
			case ExecTS, ExecPyright, ExecPnpm, ExecNpm, ExecYarn, ExecPip, ExecPoetry, ExecUv:
				// No shared "install" auto-fix for these ecosystems —
				// node packages are handled via the missing-devdep
				// branch above; python pkg managers surface lockfile
				// hygiene below; type-checkers have no install step.
			}
		case "missing-lockfile":
			switch f.Executor {
			case ExecPoetry:
				if err := runBashIn(ctx, repoRoot, "poetry install --no-interaction", 2*time.Minute); err == nil {
					fmt.Printf("  🧽 hygiene: poetry install — ok\n")
					fixed = append(fixed, f)
				} else {
					fmt.Printf("  🧽 hygiene: poetry install — failed: %v\n", err)
				}
			case ExecUv:
				if err := runBashIn(ctx, repoRoot, "uv sync", 2*time.Minute); err == nil {
					fmt.Printf("  🧽 hygiene: uv sync — ok\n")
					fixed = append(fixed, f)
				} else {
					fmt.Printf("  🧽 hygiene: uv sync — failed: %v\n", err)
				}
			case ExecTS, ExecPyright, ExecPnpm, ExecNpm, ExecYarn, ExecCargo, ExecGoMod, ExecPip:
				// Only Poetry and uv surface auto-fixable lockfile hygiene.
			}
		}
	}

	// Apply Node devDep injections in a single pass per manifest, then
	// run a single install at the repo root. Only mark findings as
	// fixed when the manifest write for their package.json actually
	// succeeded — a failed write (malformed JSON, permission error)
	// must fall through to the rescan-as-remaining path, otherwise
	// the caller would report the finding as auto-fixed when the
	// dependency was never added.
	if len(nodeDepsByPkg) > 0 {
		for pkgPath, deps := range nodeDepsByPkg {
			if err := injectDevDeps(pkgPath, deps); err != nil {
				fmt.Printf("  🧽 hygiene: inject devDeps %s — failed: %v\n", pkgPath, err)
				continue
			}
			rel, _ := filepath.Rel(repoRoot, pkgPath)
			fmt.Printf("  🧽 hygiene: injected %d devDep(s) into %s\n", len(deps), rel)
			fixed = append(fixed, nodeFindingsByPkg[pkgPath]...)
		}
		runNodeInstall(ctx, repoRoot, execs)
	}

	return fixed
}

// injectDevDeps merges the supplied pkgName->versionRange map into the
// devDependencies section of the package.json at path, atomically.
// Preserves existing formatting as best we can (2-space indent).
func injectDevDeps(pkgJSONPath string, deps map[string]string) error {
	data, err := os.ReadFile(pkgJSONPath)
	if err != nil {
		return err
	}
	var pkg map[string]interface{}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return fmt.Errorf("parse %s: %w", pkgJSONPath, err)
	}
	dev, _ := pkg["devDependencies"].(map[string]interface{})
	if dev == nil {
		dev = map[string]interface{}{}
	}
	for name, ver := range deps {
		if _, already := dev[name]; already {
			continue
		}
		// Defer to `dependencies` if already declared there — don't
		// duplicate.
		if d, ok := pkg["dependencies"].(map[string]interface{}); ok {
			if _, has := d[name]; has {
				continue
			}
		}
		dev[name] = ver
	}
	pkg["devDependencies"] = dev

	buf, err := json.MarshalIndent(pkg, "", "  ")
	if err != nil {
		return err
	}
	buf = append(buf, '\n')
	return fileutil.WriteFileAtomic(pkgJSONPath, buf, fileutil.FilePerms)
}

// runNodeInstall invokes the preferred Node package manager. pnpm is
// tried first (the stoke-managed default); npm is the fallback.
func runNodeInstall(ctx context.Context, repoRoot string, execs []ExecutorKind) {
	// Pick the primary Node executor from the set.
	var primary ExecutorKind
	for _, e := range execs {
		if e == ExecPnpm || e == ExecYarn || e == ExecNpm {
			primary = e
			break
		}
	}
	var cmdLine string
	switch primary {
	case ExecYarn:
		cmdLine = "yarn install --silent"
	case ExecNpm:
		cmdLine = "npm install --silent"
	case ExecTS, ExecPyright, ExecPnpm, ExecCargo, ExecGoMod, ExecPip, ExecPoetry, ExecUv:
		cmdLine = "pnpm install --silent"
	default:
		cmdLine = "pnpm install --silent"
	}
	if err := runBashIn(ctx, repoRoot, cmdLine, 3*time.Minute); err != nil {
		fmt.Printf("  🧽 hygiene: %s — failed, trying fallback: %v\n", cmdLine, err)
		// Fallback: if pnpm failed, try npm.
		if primary != ExecNpm {
			if err2 := runBashIn(ctx, repoRoot, "npm install --silent", 3*time.Minute); err2 != nil {
				fmt.Printf("  🧽 hygiene: npm install fallback — failed: %v\n", err2)
				return
			}
			fmt.Printf("  🧽 hygiene: npm install fallback — ok\n")
			return
		}
		return
	}
	fmt.Printf("  🧽 hygiene: %s — ok\n", cmdLine)
}

// runBashIn shells out to `bash -lc <cmd>` rooted at cwd with the
// given timeout. Output is discarded; we care only about exit status.
func runBashIn(ctx context.Context, cwd, cmd string, timeout time.Duration) error {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	c := exec.CommandContext(cctx, "bash", "-lc", cmd) // #nosec G204 -- language toolchain binary invoked with Stoke-generated args.
	c.Dir = cwd
	return c.Run()
}
