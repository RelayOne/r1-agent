package plan

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// PreflightWorkspaceDevDeps is H-65: the pre-flight devdep resolver.
//
// Background: across 11 R05/R06 SOW runs analyzed on 2026-04-19, 55%
// of classified failures were "missing_devdep" — planner emitted AC
// commands that invoke binaries (tsc, vitest, next, eslint, …) which
// aren't declared in any workspace package's devDependencies. The
// command then fails with exit 127 ("X: not found") or TS5083
// ("Cannot read file tsconfig.json" — typescript walks up the tree
// looking for its config because no project has it installed).
//
// Prior scrubbers only rewrote malformed syntax; none of them
// guaranteed the commands would actually have their binaries
// present. The harness's PATH gatherer (H-60) walks sub-package
// node_modules/.bin dirs but that only helps if something is
// installed — if typescript was never added to anyone's
// devDependencies, no amount of PATH gathering produces a working
// tsc.
//
// H-65 closes the loop: before session dispatch, scan every AC
// command for known dev-tool binaries, check them against all
// package.json files in the workspace, and for any missing ones
// add them to the ROOT package.json's devDependencies and run
// pnpm install once. This converts a cascade of per-session repair
// loops (each trying and failing to fix the same missing dep) into
// a single up-front deterministic install.
//
// Returns a diagnostic slice of one-liners describing what it
// added (or why it didn't run). Non-fatal on any intermediate
// error — best-effort pre-flight.
func PreflightWorkspaceDevDeps(repoRoot string, sow *SOW) []string {
	if sow == nil || repoRoot == "" {
		return nil
	}
	rootPkg := filepath.Join(repoRoot, "package.json")
	if _, err := os.Stat(rootPkg); err != nil {
		return nil
	}

	needed := collectNeededBinaries(sow)
	if len(needed) == 0 {
		return nil
	}

	installed, err := discoverInstalledPackages(repoRoot)
	if err != nil {
		return []string{fmt.Sprintf("devdep preflight: failed to scan workspace package.json files: %v", err)}
	}

	missing := make([]string, 0, len(needed))
	for _, bin := range needed {
		pkg, ok := devToolBinaryToNpmPackage[bin]
		if !ok {
			continue
		}
		if installed[pkg] {
			continue
		}
		missing = append(missing, pkg)
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)

	var diag []string
	diag = append(diag, fmt.Sprintf("devdep preflight: ACs reference %d binary/binaries whose npm package is not declared anywhere in the workspace: %s", len(missing), strings.Join(missing, ", ")))

	added, err := addMissingToRootDevDeps(rootPkg, missing)
	if err != nil {
		diag = append(diag, fmt.Sprintf("devdep preflight: failed to update %s: %v", rootPkg, err))
		return diag
	}
	if len(added) > 0 {
		diag = append(diag, fmt.Sprintf("devdep preflight: added to root devDependencies: %s", strings.Join(added, ", ")))
	}

	if out, err := runInstall(repoRoot); err != nil {
		diag = append(diag, fmt.Sprintf("devdep preflight: install failed (%v); ACs may still hit missing-binary errors. Output tail: %s", err, tailLines(out, 10)))
	} else {
		diag = append(diag, "devdep preflight: ran workspace install; referenced binaries are now resolvable")
	}
	return diag
}

// Known binary → npm package map for the dev tools the planner
// commonly references. Keep conservative — only add entries for
// tools we're confident about (1:1 mapping, stable names). When a
// binary's name differs from the package (e.g. "tsc" lives in the
// "typescript" package), the map encodes that. When the names
// match (e.g. "vitest"), the package is the same string.
var devToolBinaryToNpmPackage = map[string]string{
	"tsc":        "typescript",
	"ts-node":    "ts-node",
	"tsx":        "tsx",
	"vitest":     "vitest",
	"jest":       "jest",
	"mocha":      "mocha",
	"next":       "next",
	"eslint":     "eslint",
	"prettier":   "prettier",
	"rollup":     "rollup",
	"esbuild":    "esbuild",
	"vite":       "vite",
	"webpack":    "webpack",
	"turbo":      "turbo",
	"nest":       "@nestjs/cli",
	"nodemon":    "nodemon",
	"concurrently": "concurrently",
	"rimraf":     "rimraf",
	"husky":      "husky",
	"swc":        "@swc/core",
}

// leadingBinaryRE extracts the first word of a command. Handles
// "cd X && bin …" prefix, env-var prefix (FOO=bar bin …), and
// package runners we strip elsewhere (npx, pnpm exec). Also
// handles "pnpm --filter X exec <bin>" → captures <bin>.
var (
	leadingBinaryRE     = regexp.MustCompile(`(?:^|&&\s*|;\s*|\|\|\s*)(?:[A-Z_][A-Z0-9_]*=\S+\s+)*([a-zA-Z][a-zA-Z0-9_.-]*)`)
	pnpmFilterExecRE    = regexp.MustCompile(`\bpnpm\s+(?:--filter\s+\S+\s+)+exec\s+([a-zA-Z][a-zA-Z0-9_.-]*)`)
	pnpmFilterSimpleRE  = regexp.MustCompile(`\bpnpm\s+--filter\s+\S+\s+([a-zA-Z][a-zA-Z0-9_.-]*)`)
	pnpmRunRE           = regexp.MustCompile(`\bpnpm\s+(?:run\s+)?([a-zA-Z][a-zA-Z0-9_.-]*)`)
)

func collectNeededBinaries(sow *SOW) []string {
	seen := map[string]bool{}
	addToken := func(tok string) {
		if _, ok := devToolBinaryToNpmPackage[tok]; ok {
			seen[tok] = true
		}
	}
	for _, sess := range sow.Sessions {
		for _, ac := range sess.AcceptanceCriteria {
			cmd := strings.TrimSpace(ac.Command)
			if cmd == "" {
				continue
			}
			for _, m := range pnpmFilterExecRE.FindAllStringSubmatch(cmd, -1) {
				addToken(m[1])
			}
			for _, m := range pnpmFilterSimpleRE.FindAllStringSubmatch(cmd, -1) {
				addToken(m[1])
			}
			for _, m := range leadingBinaryRE.FindAllStringSubmatch(cmd, -1) {
				addToken(m[1])
			}
			// pnpm run <script> doesn't directly need the binary (it
			// runs a package.json script) but we still check for
			// typescript / vitest if the script name matches. Skip
			// the generic pnpm-run case — too noisy.
			_ = pnpmRunRE
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// discoverInstalledPackages returns a set of every package name
// declared under "dependencies", "devDependencies", or
// "peerDependencies" of any package.json found under repoRoot.
// This is the authoritative "is this package available" test:
// if the workspace's graph declares it, pnpm install will place
// its binary in some node_modules/.bin accessible via H-60's
// PATH gatherer.
func discoverInstalledPackages(repoRoot string) (map[string]bool, error) {
	installed := map[string]bool{}
	skipDirs := map[string]bool{
		"node_modules": true, ".git": true, ".turbo": true, ".next": true,
		"dist": true, "build": true, "target": true, ".venv": true, "venv": true,
	}
	err := filepath.Walk(repoRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() && skipDirs[info.Name()] {
			return filepath.SkipDir
		}
		if info.IsDir() || info.Name() != "package.json" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var pkg map[string]any
		if err := json.Unmarshal(b, &pkg); err != nil {
			return nil
		}
		for _, field := range []string{"dependencies", "devDependencies", "peerDependencies"} {
			m, ok := pkg[field].(map[string]any)
			if !ok {
				continue
			}
			for k := range m {
				installed[k] = true
			}
		}
		return nil
	})
	return installed, err
}

// addMissingToRootDevDeps mutates the root package.json by adding
// every missing package to devDependencies with a permissive "*"
// version spec. "*" is deliberate: pnpm install will resolve it
// to the latest published version at install time, which matches
// what a human would do when scaffolding a fresh project. The
// function is a no-op when the package is already present
// anywhere in the file. Returns the list of newly-added names.
func addMissingToRootDevDeps(rootPkgPath string, missing []string) ([]string, error) {
	b, err := os.ReadFile(rootPkgPath)
	if err != nil {
		return nil, err
	}
	var pkg map[string]any
	if err := json.Unmarshal(b, &pkg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", rootPkgPath, err)
	}
	existing := map[string]bool{}
	for _, field := range []string{"dependencies", "devDependencies", "peerDependencies"} {
		m, _ := pkg[field].(map[string]any)
		for k := range m {
			existing[k] = true
		}
	}
	dev, _ := pkg["devDependencies"].(map[string]any)
	if dev == nil {
		dev = map[string]any{}
	}
	added := make([]string, 0, len(missing))
	for _, name := range missing {
		if existing[name] {
			continue
		}
		dev[name] = "*"
		added = append(added, name)
	}
	if len(added) == 0 {
		return nil, nil
	}
	pkg["devDependencies"] = dev
	updated, err := json.MarshalIndent(pkg, "", "  ")
	if err != nil {
		return nil, err
	}
	updated = append(updated, '\n')
	if err := os.WriteFile(rootPkgPath, updated, 0644); err != nil { // #nosec G306 -- plan/SOW artefact consumed by Stoke tooling; 0644 is appropriate.
		return nil, err
	}
	return added, nil
}

// runInstall picks the right install command based on which lock
// file lives next to the package.json and runs it with a short
// timeout. Best-effort — on error we return the combined output
// so the diagnostic line can include the tail.
func runInstall(repoRoot string) ([]byte, error) {
	var cmdName string
	var args []string
	switch {
	case fileExists(filepath.Join(repoRoot, "pnpm-lock.yaml")), fileExists(filepath.Join(repoRoot, "pnpm-workspace.yaml")):
		cmdName, args = "pnpm", []string{"install", "--no-frozen-lockfile"}
	case fileExists(filepath.Join(repoRoot, "yarn.lock")):
		cmdName, args = "yarn", []string{"install"}
	default:
		cmdName, args = "npm", []string{"install", "--no-audit", "--no-fund"}
	}
	cmd := exec.Command(cmdName, args...) // #nosec G204 -- language toolchain binary invoked with Stoke-generated args.
	cmd.Dir = repoRoot
	return cmd.CombinedOutput()
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func tailLines(b []byte, n int) string {
	s := string(b)
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

// PreflightFixModuleType is H-66: the module-type scrubber.
//
// Problem: cross-package contract failures where a package's source
// uses ESM syntax but the package.json does not declare
// "type":"module" (or declares "commonjs"), and consumers hit
// ERR_REQUIRE_ESM at runtime.
//
// Safety — this is the hard part. The previous iteration (dc05ee6,
// reverted in 5ff5ca2) keyed purely on "source uses ESM syntax" and
// silently broke packages that author ESM in source but ship CJS
// via tsc/bundler emit. This version adds three negative signals
// that cause the scrubber to LEAVE A PACKAGE ALONE even if its
// source looks like ESM:
//
//  1. Declared CJS emit: main/exports/bin points to a .cjs file
//     anywhere in its value → the package ships CommonJS. Don't
//     rewrite.
//  2. tsconfig says CommonJS: sibling tsconfig.json has
//     compilerOptions.module == "commonjs" (case-insensitive) →
//     the package emits CJS regardless of source syntax. Don't
//     rewrite.
//  3. Monorepo config file false positive: the only ESM-looking
//     files in the scan are *.config.{ts,js,mjs,cts,mts} (vite,
//     eslint, jest etc. configs at package root) → not a signal
//     about the package's runtime shape. Don't rewrite.
//
// Traversal: walk a package's src/ tree (or package root if there
// is no src/), stopping at any directory containing a nested
// package.json so nested workspaces aren't mistaken for the parent
// package's sources.
//
// JSON rewrite uses stdlib encoding/json. Original key order is
// NOT preserved — output is alphabetical. This is an accepted
// trade-off: preserving order with stdlib requires a token-stream
// re-walk we don't want the maintenance cost of.
func PreflightFixModuleType(repoRoot string) []string {
	if repoRoot == "" {
		return nil
	}
	if _, err := os.Stat(repoRoot); err != nil {
		return nil
	}
	skipDirs := map[string]bool{
		"node_modules": true, ".git": true, ".turbo": true, ".next": true,
		"dist": true, "build": true, "target": true, ".venv": true, "venv": true,
		"out": true, ".cache": true, ".pnpm-store": true,
	}
	var pkgPaths []string
	_ = filepath.Walk(repoRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() && skipDirs[info.Name()] {
			return filepath.SkipDir
		}
		if !info.IsDir() && info.Name() == "package.json" {
			pkgPaths = append(pkgPaths, path)
		}
		return nil
	})
	sort.Strings(pkgPaths)

	var diag []string
	for _, pkgPath := range pkgPaths {
		rel, _ := filepath.Rel(repoRoot, pkgPath)
		if rel == "" {
			rel = pkgPath
		}
		line, err := fixOneModuleType(pkgPath, skipDirs)
		if err != nil {
			// Non-fatal (invalid package.json, permission error, …).
			// Emit a single diagnostic then move on.
			diag = append(diag, fmt.Sprintf("%s: skipped (%v)", rel, err))
			continue
		}
		if line != "" {
			diag = append(diag, fmt.Sprintf("%s: %s", rel, line))
		}
	}
	return diag
}

// fixOneModuleType evaluates a single package.json. Returns:
//   - ("", nil) if the package was left alone (no change warranted)
//   - (description, nil) if the file was rewritten (description is
//     the non-path part of the diagnostic line)
//   - ("", err) if the file was unreadable/unparseable
func fixOneModuleType(pkgPath string, skipDirs map[string]bool) (string, error) {
	raw, err := os.ReadFile(pkgPath)
	if err != nil {
		return "", err
	}
	var pkg map[string]any
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return "", fmt.Errorf("invalid JSON: %w", err)
	}

	// Already a module package — nothing to do.
	if t, ok := pkg["type"].(string); ok && t == "module" {
		return "", nil
	}

	pkgDir := filepath.Dir(pkgPath)

	// Negative signal 1: declared CJS emit via main/exports/bin.
	if declaredCJSEmit(pkg) {
		return "", nil
	}
	// Negative signal 2: tsconfig says CommonJS.
	if tsconfigSaysCommonJS(pkgDir) {
		return "", nil
	}

	// Pick the scan root. Prefer src/ if it exists; otherwise scan
	// package root (applying skipDirs + nested-package-boundary
	// rules so sibling nested workspaces don't leak in).
	scanRoot := filepath.Join(pkgDir, "src")
	if st, err := os.Stat(scanRoot); err != nil || !st.IsDir() {
		scanRoot = pkgDir
	}

	matches := scanForESMSyntax(scanRoot, pkgDir, skipDirs)
	// Negative signal 3: all matches are config files (vite.config.ts,
	// eslint.config.js, jest.config.mjs, …) — don't rewrite based
	// on build-tooling config syntax.
	nonConfig := 0
	for _, m := range matches {
		if !isConfigFile(m) {
			nonConfig++
		}
	}
	if nonConfig == 0 {
		return "", nil
	}

	// Rewrite. json.MarshalIndent produces alphabetically-ordered
	// keys; we accept that trade-off vs. preserving original order.
	prevType, _ := pkg["type"].(string)
	pkg["type"] = "module"
	updated, err := json.MarshalIndent(pkg, "", "  ")
	if err != nil {
		return "", err
	}
	// Preserve trailing newline convention if the original had one.
	if len(raw) > 0 && raw[len(raw)-1] == '\n' {
		updated = append(updated, '\n')
	}
	if err := os.WriteFile(pkgPath, updated, 0644); err != nil { // #nosec G306 -- plan/SOW artefact consumed by Stoke tooling; 0644 is appropriate.
		return "", err
	}
	if prevType == "" {
		return fmt.Sprintf(`set "type":"module" (found ES module syntax in %d source file(s))`, nonConfig), nil
	}
	return fmt.Sprintf(`upgraded "type":%q → "module" (found ES module syntax in %d source file(s))`, prevType, nonConfig), nil
}

// declaredCJSEmit returns true when the package declares CJS emit
// via main/bin/exports pointing to a .cjs file. We walk exports as
// an arbitrarily-nested map/string because exports maps commonly
// look like {".": {"require": "./dist/x.cjs", "import": …}}.
func declaredCJSEmit(pkg map[string]any) bool {
	if main, ok := pkg["main"].(string); ok && strings.HasSuffix(main, ".cjs") {
		return true
	}
	// bin may be a string or an object.
	switch b := pkg["bin"].(type) {
	case string:
		if strings.HasSuffix(b, ".cjs") {
			return true
		}
	case map[string]any:
		for _, v := range b {
			if s, ok := v.(string); ok && strings.HasSuffix(s, ".cjs") {
				return true
			}
		}
	}
	if exp, ok := pkg["exports"]; ok && exportsHasCJS(exp) {
		return true
	}
	return false
}

func exportsHasCJS(v any) bool {
	switch t := v.(type) {
	case string:
		return strings.HasSuffix(t, ".cjs")
	case map[string]any:
		for _, child := range t {
			if exportsHasCJS(child) {
				return true
			}
		}
	case []any:
		for _, child := range t {
			if exportsHasCJS(child) {
				return true
			}
		}
	}
	return false
}

// tsconfigSaysCommonJS returns true if a sibling tsconfig.json (or
// tsconfig.build.json) has compilerOptions.module == "commonjs"
// (case-insensitive). We don't follow `extends` — best-effort.
func tsconfigSaysCommonJS(pkgDir string) bool {
	for _, name := range []string{"tsconfig.json", "tsconfig.build.json"} {
		p := filepath.Join(pkgDir, name)
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		// tsconfig allows comments/trailing commas in practice. Try
		// strict JSON first; if that fails, fall back to a regex
		// on the raw text.
		var cfg map[string]any
		if err := json.Unmarshal(b, &cfg); err == nil {
			co, _ := cfg["compilerOptions"].(map[string]any)
			if m, ok := co["module"].(string); ok {
				if strings.EqualFold(m, "commonjs") {
					return true
				}
			}
			continue
		}
		if tsconfigModuleRE.Match(b) {
			return true
		}
	}
	return false
}

var tsconfigModuleRE = regexp.MustCompile(`(?i)"module"\s*:\s*"commonjs"`)

// scanForESMSyntax walks scanRoot and returns the relative paths of
// every ambiguous-extension source file (.ts/.tsx/.js/.jsx) that
// starts a line with ESM-level import or export syntax. Stops
// descending into nested package.json directories so nested
// workspaces aren't misread.
//
// Extensions whose module format is already unambiguous from the
// filename itself — .mjs/.mts (always ESM) and .cjs/.cts (always
// CJS) — are deliberately NOT scanned. Their presence tells us
// nothing about what "type" should be in package.json, because
// Node and TypeScript honor those extensions regardless of the
// package-level type field. Including them as positive matches
// would cause packages that only ship e.g. index.mjs or index.mts
// to be incorrectly flipped to "type":"module", and a package
// shipping only index.cts (explicitly CJS) could also be flipped
// if any other heuristic then matched.
//
// Only files directly owned by pkgDir's package are considered:
// we refuse to scan into any subdirectory that declares its own
// package.json (unless the scanRoot itself is that directory).
func scanForESMSyntax(scanRoot, pkgDir string, skipDirs map[string]bool) []string {
	if scanRoot == "" {
		return nil
	}
	var matches []string
	_ = filepath.Walk(scanRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if skipDirs[info.Name()] {
				return filepath.SkipDir
			}
			// Stop at nested package.json boundaries. Exception:
			// the scanRoot itself (which is either src/ or pkgDir)
			// — its own package.json is the one we're evaluating.
			if path != scanRoot && path != pkgDir {
				if _, err := os.Stat(filepath.Join(path, "package.json")); err == nil {
					return filepath.SkipDir
				}
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(info.Name()))
		switch ext {
		case ".ts", ".tsx", ".js", ".jsx":
			// Ambiguous extensions whose module format is
			// determined by package.json "type". Scan these.
		default:
			// .mjs/.mts are always ESM, .cjs/.cts are always CJS;
			// their presence is not evidence the "type" field
			// needs changing. Everything else is non-source.
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if esmSyntaxRE.Match(b) {
			rel, _ := filepath.Rel(pkgDir, path)
			if rel == "" {
				rel = path
			}
			matches = append(matches, rel)
		}
		return nil
	})
	return matches
}

// esmSyntaxRE matches top-of-line (after optional whitespace) ESM
// import/export forms:
//   import X from 'Y'
//   import 'Y'
//   import {…} from 'Y'
//   export X / export { … } / export default / export * from …
// Type-only imports/exports (import type, export type) are
// intentionally matched — they compile away in CJS emit, but the
// tsconfig/emit-target guard upstream is what's supposed to catch
// the CJS-emit case.
var esmSyntaxRE = regexp.MustCompile(`(?m)^\s*(import\s|import\{|export\s|export\{|export\*)`)

// isConfigFile reports whether rel is a known build-tooling config
// file whose ESM syntax shouldn't trigger a rewrite of the owning
// package.json. Checks the filename only; directory doesn't matter
// because the nested-package guard already scoped us to one package.
func isConfigFile(rel string) bool {
	base := filepath.Base(rel)
	// *.config.{ts,tsx,js,jsx,mjs,cjs,cts,mts}
	if strings.Contains(base, ".config.") {
		return true
	}
	// A handful of conventional un-suffixed configs.
	switch base {
	case "vite.config.ts", "vite.config.js", "vite.config.mjs",
		"vitest.config.ts", "vitest.config.js", "vitest.config.mjs",
		"eslint.config.js", "eslint.config.mjs", "eslint.config.cjs",
		"rollup.config.js", "rollup.config.mjs", "rollup.config.ts",
		"webpack.config.js", "webpack.config.mjs",
		"jest.config.js", "jest.config.mjs", "jest.config.ts",
		"next.config.js", "next.config.mjs", "next.config.ts",
		"svelte.config.js", "svelte.config.mjs",
		"astro.config.js", "astro.config.mjs", "astro.config.ts",
		"tailwind.config.js", "tailwind.config.mjs", "tailwind.config.ts",
		"postcss.config.js", "postcss.config.mjs":
		return true
	}
	return false
}
