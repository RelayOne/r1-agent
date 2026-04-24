// Package plan — integrity_csharp.go
//
// C# / .NET ecosystem. Validates `using Namespace;` against
// PackageReference entries in .csproj / packages.config / Directory.Packages.props.
// Compile regression uses `dotnet build` / `dotnet msbuild`.
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

	"github.com/ericmacdougall/stoke/internal/logging"
)

func init() {
	RegisterEcosystem(&csharpEcosystem{})
}

type csharpEcosystem struct{}

func (csharpEcosystem) Name() string { return "csharp" }

func (csharpEcosystem) Owns(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".cs" || ext == ".csx"
}

var csUsingRE = regexp.MustCompile(`(?m)^\s*using\s+(?:static\s+)?([A-Za-z_][A-Za-z0-9_\.]*)\s*;`)

func (csharpEcosystem) UnresolvedImports(projectRoot string, files []string) ([]ManifestMiss, error) {
	csprojs := csFindProjects(projectRoot)
	if len(csprojs) == 0 {
		return nil, nil
	}
	deps := map[string]struct{}{}
	for _, p := range csprojs {
		for k := range csReadPackageRefs(p) {
			deps[k] = struct{}{}
		}
	}
	localNS := csLocalNamespaces(projectRoot)
	stdRoots := csStdlibRoots()
	var out []ManifestMiss
	seen := map[string]struct{}{}
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, m := range csUsingRE.FindAllStringSubmatch(string(body), -1) {
			ns := m[1]
			root := ns
			if i := strings.Index(ns, "."); i > 0 {
				root = ns[:i]
			}
			if _, ok := stdRoots[root]; ok {
				continue
			}
			if csLocalCovers(ns, localNS) {
				continue
			}
			if csDepCovers(ns, deps) {
				continue
			}
			key := f + "::" + ns
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			relFile, _ := filepath.Rel(projectRoot, f)
			mani := ""
			if len(csprojs) > 0 {
				mani, _ = filepath.Rel(projectRoot, csprojs[0])
			}
			out = append(out, ManifestMiss{
				SourceFile: relFile,
				ImportPath: ns,
				Manifest:   mani,
				AddCommand: fmt.Sprintf("dotnet add package <package>  # covering namespace %s", ns),
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

func (csharpEcosystem) MissingPublicSurface(projectRoot string, files []string) ([]PublicSurfaceMiss, error) {
	return nil, nil
}

// dotnet build errors:
//   Foo.cs(12,3): error CS0103: The name 'foo' does not exist
var csErrRE = regexp.MustCompile(`^(.+?\.cs)\((\d+),(\d+)\):\s*error\s+(CS\d+):\s*(.*)$`)

func (csharpEcosystem) CompileErrors(ctx context.Context, projectRoot string, files []string) ([]CompileErr, error) {
	if _, err := exec.LookPath("dotnet"); err != nil {
		return nil, nil
	}
	c, cancel := context.WithTimeout(ctx, 240*time.Second)
	defer cancel()
	cmd := exec.CommandContext(c, "dotnet", "build", "--no-restore", "--nologo", "-v", "quiet")
	cmd.Dir = projectRoot
	out, _ := cmd.CombinedOutput()
	lines := strings.Split(string(out), "\n")
	errs := make([]CompileErr, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		m := csErrRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		file := m[1]
		if !filepath.IsAbs(file) {
			file = filepath.Join(projectRoot, file)
		}
		rel, _ := filepath.Rel(projectRoot, file)
		var lno, col int
		fmt.Sscanf(m[2], "%d", &lno)
		fmt.Sscanf(m[3], "%d", &col)
		errs = append(errs, CompileErr{File: rel, Line: lno, Column: col, Code: m[4], Message: m[5]})
	}
	sort.Slice(errs, func(i, j int) bool {
		if errs[i].File != errs[j].File {
			return errs[i].File < errs[j].File
		}
		return errs[i].Line < errs[j].Line
	})
	return errs, nil
}

func csFindProjects(projectRoot string) []string {
	var out []string
	_ = filepath.WalkDir(projectRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			// Best-effort project discovery: log and skip unreadable
			// subtrees so a single bad path can't hide valid projects.
			logging.Global().Warn("plan.integrity_csharp: project walk error", "path", path, "err", walkErr)
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == "bin" || name == "obj" || name == "node_modules" || name == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if ext == ".csproj" || ext == ".fsproj" || ext == ".vbproj" || d.Name() == "Directory.Packages.props" || d.Name() == "packages.config" {
			out = append(out, path)
		}
		return nil
	})
	return out
}

func csReadPackageRefs(path string) map[string]struct{} {
	out := map[string]struct{}{}
	body, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	// PackageReference Include="X.Y" (MSBuild) and packages.config <package id="X.Y"/>.
	re := regexp.MustCompile(`(?:PackageReference|package)\s+(?:Include|id)\s*=\s*"([^"]+)"`)
	for _, m := range re.FindAllStringSubmatch(string(body), -1) {
		out[m[1]] = struct{}{}
	}
	return out
}

func csLocalNamespaces(projectRoot string) map[string]struct{} {
	out := map[string]struct{}{}
	re := regexp.MustCompile(`(?m)^\s*namespace\s+([A-Za-z_][A-Za-z0-9_\.]*)`)
	_ = filepath.WalkDir(projectRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			// Best-effort namespace scan: log and skip unreadable
			// subtrees so a single bad path can't hide valid
			// namespaces elsewhere.
			logging.Global().Warn("plan.integrity_csharp: namespace walk error", "path", path, "err", walkErr)
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == "bin" || name == "obj" || name == "node_modules" || name == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.ToLower(filepath.Ext(d.Name())) != ".cs" {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			// Unreadable .cs file: log and continue so the rest of
			// the namespace scan still runs.
			logging.Global().Warn("plan.integrity_csharp: unreadable .cs file", "path", path, "err", readErr)
		} else {
			for _, m := range re.FindAllStringSubmatch(string(body), -1) {
				out[m[1]] = struct{}{}
			}
		}
		return nil
	})
	return out
}

func csLocalCovers(ns string, local map[string]struct{}) bool {
	for p := range local {
		if ns == p || strings.HasPrefix(ns, p+".") || strings.HasPrefix(p, ns+".") {
			return true
		}
	}
	return false
}

func csDepCovers(ns string, deps map[string]struct{}) bool {
	for p := range deps {
		if ns == p || strings.HasPrefix(ns, p+".") {
			return true
		}
	}
	return false
}

func csStdlibRoots() map[string]struct{} {
	names := []string{"System", "Microsoft", "Windows", "UnityEngine", "Unity", "UnityEditor", "Xamarin"}
	out := make(map[string]struct{}, len(names))
	for _, n := range names {
		out[n] = struct{}{}
	}
	return out
}
