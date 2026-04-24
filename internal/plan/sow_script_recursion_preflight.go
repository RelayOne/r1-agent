package plan

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PreflightScriptRecursion is H-79: pre-execution scan that finds
// package.json scripts whose name collides with an executable binary
// AND whose value invokes that same binary. Running such a script
// via `pnpm <name>` or `pnpm --filter X <name>` triggers an infinite
// recursion — pnpm resolves the script before falling through to
// node_modules/.bin, so the script calls itself.
//
// R06-sow-serial today lost AC10/AC11/AC12 to this exact pattern:
// worker wrote `"vitest": "vitest run"` in apps/web/package.json
// thinking they were aliasing the binary. Instead, `pnpm --filter
// web vitest` ran the script recursively. The reasoning pass caught
// it per-AC (H-71 now also rewrites the AC form), but workers keep
// writing the colliding script.
//
// H-79 fixes it at the SOURCE: delete the colliding entry from the
// scripts block entirely. Any legitimate alias can live under a
// different name (e.g. `"test:unit": "vitest run"`).
//
// Returns a diagnostic slice. Non-fatal on every intermediate error.
// Verbose: every edit is logged with the before/after snippet so
// operators can audit what the preflight changed.
func PreflightScriptRecursion(repoRoot string) []string {
	if repoRoot == "" {
		return nil
	}
	var diag []string

	skipDirs := map[string]bool{
		"node_modules": true, ".git": true, ".turbo": true, ".next": true,
		"dist": true, "build": true, "target": true, ".venv": true,
		"venv": true, "__pycache__": true,
	}

	// H-79: the canonical set of package.json scripts that are
	// commonly-aliased to their binary of the same name. These are
	// the recursion-risk pairs we scan for.
	knownRecursionRisks := map[string]bool{
		"vitest":       true,
		"jest":         true,
		"mocha":        true,
		"eslint":       true,
		"prettier":     true,
		"tsc":          true,
		"next":         true,
		"webpack":      true,
		"rollup":       true,
		"esbuild":      true,
		"vite":         true,
		"turbo":        true,
		"nx":           true,
		"husky":        true,
		"concurrently": true,
	}

	_ = filepath.Walk(repoRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() && skipDirs[info.Name()] {
			return filepath.SkipDir
		}
		if info.IsDir() || info.Name() != "package.json" {
			return nil
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		var pkg map[string]any
		if jErr := json.Unmarshal(b, &pkg); jErr != nil {
			return nil
		}
		scripts, ok := pkg["scripts"].(map[string]any)
		if !ok || len(scripts) == 0 {
			return nil
		}
		var removed []string
		for name, valAny := range scripts {
			if !knownRecursionRisks[name] {
				continue
			}
			val, _ := valAny.(string)
			val = strings.TrimSpace(val)
			// Self-recursive patterns:
			//   "vitest": "vitest run"
			//   "eslint": "eslint ."
			//   "tsc":    "tsc --noEmit"
			// all resolve the script named <name> before falling
			// through to node_modules/.bin/<name> — running the
			// script recursively.
			if val == name || strings.HasPrefix(val, name+" ") || strings.HasPrefix(val, name+"\t") {
				removed = append(removed, fmt.Sprintf("%s = %q", name, val))
				delete(scripts, name)
			}
		}
		if len(removed) == 0 {
			return nil
		}
		pkg["scripts"] = scripts
		updated, mErr := json.MarshalIndent(pkg, "", "  ")
		if mErr != nil {
			diag = append(diag, fmt.Sprintf("H-79 script-recursion preflight: failed to re-marshal %s: %v", path, mErr))
			return nil
		}
		updated = append(updated, '\n')
		if wErr := os.WriteFile(path, updated, 0644); wErr != nil {
			diag = append(diag, fmt.Sprintf("H-79 script-recursion preflight: failed to write %s: %v", path, wErr))
			return nil
		}
		rel, _ := filepath.Rel(repoRoot, path)
		diag = append(diag, fmt.Sprintf("H-79: removed %d self-recursive script(s) from %s: %s", len(removed), rel, strings.Join(removed, "; ")))
		return nil
	})

	sort.Strings(diag)
	return diag
}
