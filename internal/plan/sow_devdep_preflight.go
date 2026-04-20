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

	var missing []string
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
	var added []string
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
	if err := os.WriteFile(rootPkgPath, updated, 0644); err != nil {
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
	cmd := exec.Command(cmdName, args...)
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
