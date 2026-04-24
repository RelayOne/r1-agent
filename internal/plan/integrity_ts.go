// Package plan — integrity_ts.go
//
// TypeScript / JavaScript / Node ecosystem implementation of the
// Ecosystem interface. Handles .ts/.tsx/.js/.jsx/.mjs/.cjs files in
// packages that have a package.json on the path from the file to
// the project root.
package plan

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

func init() {
	RegisterEcosystem(&tsEcosystem{})
}

type tsEcosystem struct{}

func (tsEcosystem) Name() string { return "typescript" }

func (tsEcosystem) Owns(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs":
		return true
	}
	return false
}

// tsImportRE matches import/require specifiers. Captures the bare
// specifier so the scanner can validate it against package.json.
var tsImportRE = regexp.MustCompile(`(?m)(?:^|\s)(?:import\s+(?:[^'"]+?\s+from\s+)?|require\s*\(\s*)['"]([^'"]+)['"]`)

func (tsEcosystem) UnresolvedImports(projectRoot string, files []string) ([]ManifestMiss, error) {
	wsPackages, err := tsCollectWorkspaceNames(projectRoot)
	if err != nil {
		return nil, err
	}
	builtins := tsNodeBuiltins()
	depCache := map[string]map[string]struct{}{}
	pkgCache := map[string]string{}

	var out []ManifestMiss
	seen := map[string]struct{}{}
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		fileMissing := map[string]struct{}{}
		for _, m := range tsImportRE.FindAllStringSubmatch(string(body), -1) {
			spec := m[1]
			if spec == "" || strings.HasPrefix(spec, ".") || strings.HasPrefix(spec, "/") {
				continue
			}
			if strings.HasPrefix(spec, "node:") {
				continue
			}
			root := tsImportRoot(spec)
			if root == "" {
				continue
			}
			if _, isBuiltin := builtins[root]; isBuiltin {
				continue
			}
			if _, ok := wsPackages[root]; ok {
				continue
			}
			deps, nearest := tsResolveDeps(f, projectRoot, depCache, pkgCache)
			if _, ok := deps[root]; ok {
				continue
			}
			if _, dup := fileMissing[root]; dup {
				continue
			}
			fileMissing[root] = struct{}{}
			key := f + "::" + root
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			relFile, _ := filepath.Rel(projectRoot, f)
			relMani, _ := filepath.Rel(projectRoot, nearest)
			// Prefer pnpm → npm → yarn based on lockfile presence at
			// projectRoot. Affects the AddCommand text only.
			pkgMgr := detectNodePkgMgr(projectRoot)
			pkgName := readPackageJsonName(nearest)
			add := fmt.Sprintf("%s add %s", pkgMgr, root)
			if pkgName != "" && pkgMgr == "pnpm" {
				add = fmt.Sprintf("pnpm --filter %s add %s", pkgName, root)
			}
			out = append(out, ManifestMiss{
				SourceFile: relFile,
				ImportPath: root,
				Manifest:   relMani,
				AddCommand: add,
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

func (tsEcosystem) MissingPublicSurface(projectRoot string, files []string) ([]PublicSurfaceMiss, error) {
	var out []PublicSurfaceMiss
	for _, f := range files {
		rel, err := filepath.Rel(projectRoot, f)
		if err != nil {
			continue
		}
		// Heuristic: must live under packages/*/src/ (or any */src/
		// that has a sibling package.json). Apps/<name>/app/ is a
		// Next.js route surface, not a library; skip.
		if !strings.Contains(rel, "/src/") {
			continue
		}
		base := filepath.Base(f)
		if base == "index.ts" || base == "index.tsx" || base == "index.js" {
			continue
		}
		if tsIsTestFile(base) {
			continue
		}
		body, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if !tsHasTopLevelExport(string(body)) {
			continue
		}
		barrel := tsNearestBarrel(f, projectRoot)
		if barrel == "" {
			continue
		}
		relFromBarrel, err := filepath.Rel(filepath.Dir(barrel), f)
		if err != nil {
			continue
		}
		relFromBarrel = strings.TrimSuffix(relFromBarrel, filepath.Ext(relFromBarrel))
		if !strings.HasPrefix(relFromBarrel, ".") {
			relFromBarrel = "./" + relFromBarrel
		}
		barrelBody, _ := os.ReadFile(barrel)
		if strings.Contains(string(barrelBody), relFromBarrel) {
			continue
		}
		srcRel, _ := filepath.Rel(projectRoot, f)
		barrelRel, _ := filepath.Rel(projectRoot, barrel)
		out = append(out, PublicSurfaceMiss{
			SourceFile: srcRel,
			TargetFile: barrelRel,
			FixLine:    fmt.Sprintf("export * from '%s';", relFromBarrel),
		})
	}
	return out, nil
}

// tscErrRE matches tsc --noEmit error lines:
//
//	path/to/file.ts(12,3): error TS1234: message text
var tscErrRE = regexp.MustCompile(`^(.+?)\((\d+),(\d+)\):\s+error\s+(TS\d+):\s+(.*)$`)

func (tsEcosystem) CompileErrors(ctx context.Context, projectRoot string, files []string) ([]CompileErr, error) {
	pkgDirs := map[string]struct{}{}
	for _, f := range files {
		root := tsPackageRootFor(f, projectRoot)
		if root == "" {
			continue
		}
		pkgDirs[root] = struct{}{}
	}
	if len(pkgDirs) == 0 {
		return nil, nil
	}
	roots := make([]string, 0, len(pkgDirs))
	for r := range pkgDirs {
		roots = append(roots, r)
	}
	sort.Strings(roots)
	var all []CompileErr
	for _, root := range roots {
		errs, err := tsRunTscInDir(ctx, projectRoot, root)
		if err != nil {
			return all, err
		}
		all = append(all, errs...)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].File != all[j].File {
			return all[i].File < all[j].File
		}
		if all[i].Line != all[j].Line {
			return all[i].Line < all[j].Line
		}
		return all[i].Code < all[j].Code
	})
	return all, nil
}

func tsRunTscInDir(parentCtx context.Context, projectRoot, pkgDir string) ([]CompileErr, error) {
	ctx, cancel := context.WithTimeout(parentCtx, 120*time.Second)
	defer cancel()
	bin := "pnpm"
	args := []string{"exec", "tsc", "--noEmit", "--pretty", "false"}
	if _, err := exec.LookPath("pnpm"); err != nil {
		if _, err := exec.LookPath("npx"); err == nil {
			bin = "npx"
			args = []string{"--no-install", "tsc", "--noEmit", "--pretty", "false"}
		} else if _, err := exec.LookPath("tsc"); err == nil {
			bin = "tsc"
			args = []string{"--noEmit", "--pretty", "false"}
		} else {
			return nil, nil
		}
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = pkgDir
	out, _ := cmd.CombinedOutput()
	lines := strings.Split(string(out), "\n")
	errs := make([]CompileErr, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		m := tscErrRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		file := m[1]
		if !filepath.IsAbs(file) {
			file = filepath.Join(pkgDir, file)
		}
		rel, err := filepath.Rel(projectRoot, file)
		if err != nil {
			rel = file
		}
		var lno, col int
		fmt.Sscanf(m[2], "%d", &lno)
		fmt.Sscanf(m[3], "%d", &col)
		errs = append(errs, CompileErr{
			File: rel, Line: lno, Column: col, Code: m[4], Message: m[5],
		})
	}
	return errs, nil
}

// ---------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------

func tsImportRoot(spec string) string {
	if strings.HasPrefix(spec, "@") {
		parts := strings.SplitN(spec, "/", 3)
		if len(parts) >= 2 {
			return parts[0] + "/" + parts[1]
		}
		return spec
	}
	if i := strings.Index(spec, "/"); i > 0 {
		return spec[:i]
	}
	return spec
}

func tsResolveDeps(file, projectRoot string, cache map[string]map[string]struct{}, pkgCache map[string]string) (map[string]struct{}, string) {
	dir := filepath.Dir(file)
	union := map[string]struct{}{}
	nearest := ""
	for {
		pj := filepath.Join(dir, "package.json")
		if info, err := os.Stat(pj); err == nil && !info.IsDir() {
			if nearest == "" {
				nearest = pj
			}
			deps, ok := cache[pj]
			if !ok {
				deps = tsReadPackageJsonDeps(pj)
				cache[pj] = deps
				pkgCache[pj] = pj
			}
			for k := range deps {
				union[k] = struct{}{}
			}
		}
		if dir == projectRoot || dir == "/" || dir == "." {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return union, nearest
}

func tsReadPackageJsonDeps(path string) map[string]struct{} {
	out := map[string]struct{}{}
	body, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var pj struct {
		Dependencies         map[string]string `json:"dependencies"`
		DevDependencies      map[string]string `json:"devDependencies"`
		PeerDependencies     map[string]string `json:"peerDependencies"`
		OptionalDependencies map[string]string `json:"optionalDependencies"`
	}
	if err := jsonUnmarshalLenient(body, &pj); err != nil {
		return out
	}
	for k := range pj.Dependencies {
		out[k] = struct{}{}
	}
	for k := range pj.DevDependencies {
		out[k] = struct{}{}
	}
	for k := range pj.PeerDependencies {
		out[k] = struct{}{}
	}
	for k := range pj.OptionalDependencies {
		out[k] = struct{}{}
	}
	return out
}

func readPackageJsonName(path string) string {
	body, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var pj struct {
		Name string `json:"name"`
	}
	if err := jsonUnmarshalLenient(body, &pj); err != nil {
		return ""
	}
	return pj.Name
}

func tsCollectWorkspaceNames(projectRoot string) (map[string]struct{}, error) {
	out := map[string]struct{}{}
	err := filepath.WalkDir(projectRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == "node_modules" || name == ".git" || name == "dist" || name == "build" || name == ".next" || name == ".turbo" {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != "package.json" {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var pj struct {
			Name string `json:"name"`
		}
		if err := jsonUnmarshalLenient(body, &pj); err != nil {
			return nil
		}
		if pj.Name != "" {
			out[pj.Name] = struct{}{}
		}
		return nil
	})
	return out, err
}

func tsNodeBuiltins() map[string]struct{} {
	names := []string{
		"assert", "async_hooks", "buffer", "child_process", "cluster",
		"console", "constants", "crypto", "dgram", "diagnostics_channel",
		"dns", "domain", "events", "fs", "http", "http2", "https",
		"inspector", "module", "net", "os", "path", "perf_hooks",
		"process", "punycode", "querystring", "readline", "repl",
		"stream", "string_decoder", "sys", "timers", "tls", "trace_events",
		"tty", "url", "util", "v8", "vm", "wasi", "worker_threads", "zlib",
	}
	out := make(map[string]struct{}, len(names))
	for _, n := range names {
		out[n] = struct{}{}
	}
	return out
}

func tsHasTopLevelExport(src string) bool {
	for _, line := range strings.Split(src, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "export ") || strings.HasPrefix(trim, "export{") ||
			strings.HasPrefix(trim, "export*") || strings.HasPrefix(trim, "export default") {
			return true
		}
	}
	return false
}

func tsIsTestFile(base string) bool {
	return strings.HasSuffix(base, ".test.ts") || strings.HasSuffix(base, ".test.tsx") ||
		strings.HasSuffix(base, ".spec.ts") || strings.HasSuffix(base, ".spec.tsx") ||
		strings.HasSuffix(base, ".stories.tsx") || strings.HasSuffix(base, ".stories.ts")
}

func tsNearestBarrel(file, projectRoot string) string {
	dir := filepath.Dir(file)
	for {
		for _, name := range []string{"index.ts", "index.tsx"} {
			p := filepath.Join(dir, name)
			if info, err := os.Stat(p); err == nil && !info.IsDir() {
				return p
			}
		}
		if filepath.Base(dir) == "src" {
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir || parent == projectRoot || parent == "/" || parent == "." {
			return ""
		}
		dir = parent
	}
}

func tsPackageRootFor(file, projectRoot string) string {
	dir := filepath.Dir(file)
	for {
		if info, err := os.Stat(filepath.Join(dir, "package.json")); err == nil && !info.IsDir() {
			return dir
		}
		if dir == projectRoot || dir == "/" || dir == "." {
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func detectNodePkgMgr(projectRoot string) string {
	if _, err := os.Stat(filepath.Join(projectRoot, "pnpm-lock.yaml")); err == nil {
		return "pnpm"
	}
	if _, err := os.Stat(filepath.Join(projectRoot, "yarn.lock")); err == nil {
		return "yarn"
	}
	return "npm"
}
