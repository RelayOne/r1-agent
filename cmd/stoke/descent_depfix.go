package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Regex patterns that capture the package name from common "missing
// module" error messages. Covers Node's ERR_MODULE_NOT_FOUND, the
// TypeScript TS2307 "Cannot find module" form, webpack/vite resolver
// errors, and the pnpm lockfile resolution failure shape.
var missingModulePatterns = []*regexp.Regexp{
	// Cannot find module 'zod'
	// Cannot find module "zod"
	regexp.MustCompile(`(?i)cannot\s+find\s+module\s+['"]([@\w\-./]+)['"]`),
	// Cannot resolve module 'foo'
	regexp.MustCompile(`(?i)cannot\s+resolve\s+module\s+['"]([@\w\-./]+)['"]`),
	// ERR_MODULE_NOT_FOUND ... 'react'
	regexp.MustCompile(`ERR_MODULE_NOT_FOUND[^'"]*['"]([@\w\-./]+)['"]`),
	// Module not found: Can't resolve 'vitest'
	// Uses non-greedy .*? instead of [^'"]* to survive the apostrophe
	// in "Can't" — otherwise the excluded-class match stops at the
	// first quote it hits, which is inside the word, not the package.
	regexp.MustCompile(`(?i)module\s+not\s+found.*?['"]([@\w\-./]+)['"]`),
	// TS2307: Cannot find module 'X' or its corresponding type declarations.
	regexp.MustCompile(`TS2307[^'"]*['"]([@\w\-./]+)['"]`),
}

// extractMissingNpmPackages parses stderr for patterns that name an
// unresolvable npm package, and returns the unique set of NPM-package
// names (not paths). Relative paths like './foo' or '../bar' are
// dropped — those aren't install-able. Results are sorted for
// deterministic log output.
func extractMissingNpmPackages(stderr string) []string {
	if stderr == "" {
		return nil
	}
	seen := map[string]struct{}{}
	for _, re := range missingModulePatterns {
		for _, m := range re.FindAllStringSubmatch(stderr, -1) {
			if len(m) < 2 {
				continue
			}
			pkg := m[1]
			// Skip relative / absolute paths — those aren't npm deps.
			if strings.HasPrefix(pkg, ".") || strings.HasPrefix(pkg, "/") {
				continue
			}
			// Extract the bare package name from a scoped subpath like
			// "@repo/types/schemas" → "@repo/types". The importing file
			// resolves via the package name, not the subpath.
			pkg = packageRootFromImport(pkg)
			// Skip node: builtins, empty strings, and single-letter
			// entries that are almost certainly regex false positives.
			if pkg == "" || strings.HasPrefix(pkg, "node:") || len(pkg) < 2 {
				continue
			}
			seen[pkg] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// packageRootFromImport collapses a deep import specifier into the
// installable package name. Cases:
//
//	"zod"                       -> "zod"
//	"zod/schemas"               -> "zod"
//	"@anthropic/sdk"            -> "@anthropic/sdk"
//	"@anthropic/sdk/messages"   -> "@anthropic/sdk"
//	"@anthropic"                -> ""  (scoped without name is invalid)
func packageRootFromImport(spec string) string {
	if spec == "" {
		return ""
	}
	parts := strings.Split(spec, "/")
	if strings.HasPrefix(spec, "@") {
		if len(parts) < 2 {
			return ""
		}
		return parts[0] + "/" + parts[1]
	}
	return parts[0]
}

// addRootDevDeps appends the given packages to the root
// package.json's devDependencies using "*" as the version spec.
// pnpm install will resolve "*" to the latest compatible version
// against the registry. This is deliberately loose — the dep is a
// recovery-path install, not a long-term constraint; normal
// workflows still declare specific versions. Returns an error if
// the root package.json can't be read/written.
//
// Duplicates (packages already present in deps/devDeps/peerDeps) are
// skipped to avoid overwriting an intentional version pin.
func addRootDevDeps(repoRoot string, packages []string) error {
	pkgPath := filepath.Join(repoRoot, "package.json")
	raw, err := os.ReadFile(pkgPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", pkgPath, err)
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return fmt.Errorf("parse %s: %w", pkgPath, err)
	}
	existing := map[string]struct{}{}
	for _, block := range []string{"dependencies", "devDependencies", "peerDependencies"} {
		if v, ok := obj[block].(map[string]any); ok {
			for k := range v {
				existing[k] = struct{}{}
			}
		}
	}
	devDeps, _ := obj["devDependencies"].(map[string]any)
	if devDeps == nil {
		devDeps = map[string]any{}
	}
	changed := false
	for _, p := range packages {
		if _, dupe := existing[p]; dupe {
			continue
		}
		devDeps[p] = "*"
		changed = true
	}
	if !changed {
		return nil
	}
	obj["devDependencies"] = devDeps
	out, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return err
	}
	// Append trailing newline to match typical editor convention.
	out = append(out, '\n')
	return os.WriteFile(pkgPath, out, 0o644) // #nosec G306 -- CLI output artefact; user-readable.
}
