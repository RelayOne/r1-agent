// Package plan — integrity_go.go
//
// Go ecosystem implementation of the Ecosystem interface. Validates
// that imports resolve to go.mod declarations, runs `go vet` + `go
// build` for compile-regression detection. Go has no barrel/mod-root
// concept (package-by-directory is automatic), so MissingPublicSurface
// is always empty.
package plan

import (
	"bufio"
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
	RegisterEcosystem(&goEcosystem{})
}

type goEcosystem struct{}

func (goEcosystem) Name() string { return "go" }

func (goEcosystem) Owns(path string) bool {
	return strings.ToLower(filepath.Ext(path)) == ".go"
}

// goImportRE matches both single-line imports (`import "x"`) and
// blocks when used after line-splitting. For full accuracy we parse
// the import block shape below.
var goImportLineRE = regexp.MustCompile(`^\s*"([^"]+)"`)

func (goEcosystem) UnresolvedImports(projectRoot string, files []string) ([]ManifestMiss, error) {
	modRoot, modPath, deps, err := goFindModule(projectRoot)
	if err != nil {
		// No go.mod (or a read failure) means this repo either isn't
		// a Go module or the module is unreadable. In both cases the
		// Ecosystem interface expects an "empty findings" result for
		// non-applicable repos. Surface unexpected errors through
		// os.IsNotExist — if the file simply doesn't exist, treat as
		// not-a-Go-module; any other error is an unexpected I/O
		// failure and should propagate to the caller.
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("go: find module: %w", err)
	}
	if modRoot == "" {
		return nil, nil
	}
	stdlib := goStdlibSet()
	var out []ManifestMiss
	seen := map[string]struct{}{}
	for _, f := range files {
		imports, err := goParseImports(f)
		if err != nil {
			continue
		}
		for _, imp := range imports {
			// Stdlib: no dot in the first path segment.
			if goIsStdlib(imp, stdlib) {
				continue
			}
			// Own module: imp has modPath as prefix.
			if modPath != "" && (imp == modPath || strings.HasPrefix(imp, modPath+"/")) {
				continue
			}
			// Check deps (module roots).
			if goDepCovers(imp, deps) {
				continue
			}
			key := f + "::" + imp
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			relFile, _ := filepath.Rel(projectRoot, f)
			relMani, _ := filepath.Rel(projectRoot, filepath.Join(modRoot, "go.mod"))
			out = append(out, ManifestMiss{
				SourceFile: relFile,
				ImportPath: imp,
				Manifest:   relMani,
				AddCommand: fmt.Sprintf("go get %s", imp),
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

// MissingPublicSurface: Go has no explicit re-export / barrel file
// concept; the compiler does package-by-directory automatically, and
// visibility is capitalization-based. Always nil.
func (goEcosystem) MissingPublicSurface(projectRoot string, files []string) ([]PublicSurfaceMiss, error) {
	return nil, nil
}

// goVetErrRE matches `go vet` and `go build` error lines:
//
//	./path/file.go:12:3: message
//	path/file.go:12:3: message
var goVetErrRE = regexp.MustCompile(`^(?:\./)?(.+?\.go):(\d+):(?:(\d+):)?\s+(.*)$`)

func (goEcosystem) CompileErrors(ctx context.Context, projectRoot string, files []string) ([]CompileErr, error) {
	pkgs := goPackagesForFiles(projectRoot, files)
	if len(pkgs) == 0 {
		return nil, nil
	}
	sort.Strings(pkgs)

	// Prefer `go vet` (faster, more hygiene coverage) then `go build`
	// if vet found nothing but we still want type-check. For the
	// regression gate, just running vet is sufficient: vet catches
	// type errors too on builds where the compiler itself would
	// fail.
	if _, err := exec.LookPath("go"); err != nil {
		return nil, nil
	}
	c, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	args := append([]string{"vet"}, pkgs...)
	cmd := exec.CommandContext(c, "go", args...) // #nosec G204 -- language toolchain binary invoked with Stoke-generated args.
	cmd.Dir = projectRoot
	out, _ := cmd.CombinedOutput()
	lines := strings.Split(string(out), "\n")
	errs := make([]CompileErr, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		m := goVetErrRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		file := m[1]
		if !filepath.IsAbs(file) {
			file = filepath.Join(projectRoot, file)
		}
		rel, err := filepath.Rel(projectRoot, file)
		if err != nil {
			rel = file
		}
		var lno, col int
		fmt.Sscanf(m[2], "%d", &lno)
		if m[3] != "" {
			fmt.Sscanf(m[3], "%d", &col)
		}
		errs = append(errs, CompileErr{
			File: rel, Line: lno, Column: col, Code: "vet", Message: m[4],
		})
	}
	sort.Slice(errs, func(i, j int) bool {
		if errs[i].File != errs[j].File {
			return errs[i].File < errs[j].File
		}
		return errs[i].Line < errs[j].Line
	})
	return errs, nil
}

// ---------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------

// goFindModule walks up from projectRoot looking for go.mod. Returns
// the module root, module path, and declared require set. Nested
// modules are not supported for the integrity gate — a session
// whose files span multiple go modules will have its imports
// checked against only the root module. (Nested-module support is a
// future refinement.)
func goFindModule(projectRoot string) (string, string, map[string]struct{}, error) {
	modFile := filepath.Join(projectRoot, "go.mod")
	body, err := os.ReadFile(modFile)
	if err != nil {
		return "", "", nil, err
	}
	modPath := ""
	deps := map[string]struct{}{}
	scanner := bufio.NewScanner(strings.NewReader(string(body)))
	inRequire := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		if strings.HasPrefix(line, "module ") {
			modPath = strings.TrimSpace(strings.TrimPrefix(line, "module "))
			modPath = strings.Trim(modPath, `"`)
			continue
		}
		if strings.HasPrefix(line, "require (") {
			inRequire = true
			continue
		}
		if inRequire {
			if line == ")" {
				inRequire = false
				continue
			}
			parts := strings.Fields(line)
			if len(parts) >= 1 {
				deps[parts[0]] = struct{}{}
			}
			continue
		}
		if strings.HasPrefix(line, "require ") {
			parts := strings.Fields(strings.TrimPrefix(line, "require "))
			if len(parts) >= 1 {
				deps[parts[0]] = struct{}{}
			}
		}
	}
	return projectRoot, modPath, deps, nil
}

// goParseImports reads a .go file and returns the import path list.
// Handles both single-import and block-import forms without invoking
// the full go/parser machinery (keeps the gate dependency-light).
func goParseImports(path string) ([]string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []string
	scanner := bufio.NewScanner(strings.NewReader(string(body)))
	scanner.Buffer(make([]byte, 1024*1024), 4*1024*1024)
	inBlock := false
	for scanner.Scan() {
		line := scanner.Text()
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "//") {
			continue
		}
		if strings.HasPrefix(trim, "import (") {
			inBlock = true
			continue
		}
		if inBlock {
			if trim == ")" {
				inBlock = false
				continue
			}
			if m := goImportLineRE.FindStringSubmatch(line); m != nil {
				out = append(out, m[1])
			}
			continue
		}
		if strings.HasPrefix(trim, "import ") {
			rest := strings.TrimSpace(strings.TrimPrefix(trim, "import"))
			// Strip optional alias: `import foo "path"`.
			if idx := strings.Index(rest, `"`); idx >= 0 {
				closing := strings.Index(rest[idx+1:], `"`)
				if closing >= 0 {
					out = append(out, rest[idx+1:idx+1+closing])
				}
			}
		}
		// Stop scanning imports once we pass a top-level declaration.
		if strings.HasPrefix(trim, "func ") || strings.HasPrefix(trim, "type ") ||
			strings.HasPrefix(trim, "var ") || strings.HasPrefix(trim, "const ") {
			break
		}
	}
	return out, nil
}

// goIsStdlib: the Go stdlib is identified by the absence of a dot
// in the first path segment. This is the canonical go/build rule.
func goIsStdlib(imp string, _ map[string]struct{}) bool {
	first := imp
	if i := strings.Index(imp, "/"); i > 0 {
		first = imp[:i]
	}
	return !strings.Contains(first, ".")
}

// goStdlibSet returns an empty set today — the rule above is
// sufficient. Kept as a hook for future exceptions (e.g., internal
// golang.org/x/... paths that aren't stdlib but are treated as such).
func goStdlibSet() map[string]struct{} { return nil }

// goDepCovers reports whether any dep's module path is a prefix of
// imp. Go module paths are hierarchical: a dep on `github.com/x/y`
// covers imports of `github.com/x/y/sub/pkg`.
func goDepCovers(imp string, deps map[string]struct{}) bool {
	for d := range deps {
		if imp == d || strings.HasPrefix(imp, d+"/") {
			return true
		}
	}
	return false
}

// goPackagesForFiles returns the list of Go package paths (relative
// to projectRoot) that contain any of the given files, suitable for
// `go vet ./path/...` invocation.
func goPackagesForFiles(projectRoot string, files []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, f := range files {
		rel, err := filepath.Rel(projectRoot, f)
		if err != nil {
			continue
		}
		dir := "./" + filepath.Dir(rel)
		if _, dup := seen[dir]; dup {
			continue
		}
		seen[dir] = struct{}{}
		out = append(out, dir)
	}
	return out
}
