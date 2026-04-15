// Package plan — integrity_rust.go
//
// Rust ecosystem implementation of the Ecosystem interface. Validates
// `use foo::bar` statements against [dependencies] in Cargo.toml and
// runs `cargo check` for compile-regression detection. Public-surface
// check: a session that adds src/foo.rs without `mod foo;` in the
// nearest lib.rs / mod.rs surfaces that gap.
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
	RegisterEcosystem(&rustEcosystem{})
}

type rustEcosystem struct{}

func (rustEcosystem) Name() string { return "rust" }

func (rustEcosystem) Owns(path string) bool {
	return strings.ToLower(filepath.Ext(path)) == ".rs"
}

// rustUseRE matches `use foo::...;` and `extern crate foo;` top-level
// crate references.
var rustUseRE = regexp.MustCompile(`(?m)^\s*(?:pub\s+)?use\s+([A-Za-z_][A-Za-z0-9_]*)(?:\s*::|;)`)
var rustExternRE = regexp.MustCompile(`(?m)^\s*extern\s+crate\s+([A-Za-z_][A-Za-z0-9_]*)`)

func (rustEcosystem) UnresolvedImports(projectRoot string, files []string) ([]ManifestMiss, error) {
	manifests := rustCollectManifests(projectRoot)
	if len(manifests) == 0 {
		return nil, nil
	}
	stdPrefixes := rustStdCrates()
	var out []ManifestMiss
	seen := map[string]struct{}{}
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		mani := rustNearestManifest(f, manifests)
		if mani == "" {
			continue
		}
		deps := rustManifestDeps(mani)
		crateName := rustCrateName(mani)
		tops := map[string]struct{}{}
		for _, m := range rustUseRE.FindAllStringSubmatch(string(body), -1) {
			tops[m[1]] = struct{}{}
		}
		for _, m := range rustExternRE.FindAllStringSubmatch(string(body), -1) {
			tops[m[1]] = struct{}{}
		}
		for top := range tops {
			// Skip self, crate aliases, stdlib roots, and module
			// self-references.
			if top == "crate" || top == "self" || top == "super" || top == "Self" {
				continue
			}
			if _, ok := stdPrefixes[top]; ok {
				continue
			}
			// Rust dep names sometimes differ from crate import name
			// via hyphens vs underscores. `tokio-tungstenite` in
			// Cargo.toml imports as `tokio_tungstenite`. Normalize.
			normalized := strings.ReplaceAll(top, "_", "-")
			if _, ok := deps[top]; ok {
				continue
			}
			if _, ok := deps[normalized]; ok {
				continue
			}
			if top == crateName || normalized == crateName {
				continue
			}
			// Could be a local module — check for a sibling file or
			// mod declaration. Skip if so.
			if rustIsLocalModule(f, top) {
				continue
			}
			key := f + "::" + top
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			relFile, _ := filepath.Rel(projectRoot, f)
			relMani, _ := filepath.Rel(projectRoot, mani)
			out = append(out, ManifestMiss{
				SourceFile: relFile,
				ImportPath: top,
				Manifest:   relMani,
				AddCommand: fmt.Sprintf("cargo add %s --manifest-path %s", normalized, relMani),
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

// MissingPublicSurface for Rust: session added src/foo.rs but the
// nearest lib.rs / mod.rs / main.rs does not declare `mod foo;`.
// Without the declaration, the file is invisible to the compiler.
func (rustEcosystem) MissingPublicSurface(projectRoot string, files []string) ([]PublicSurfaceMiss, error) {
	var out []PublicSurfaceMiss
	for _, f := range files {
		base := filepath.Base(f)
		if base == "lib.rs" || base == "main.rs" || base == "mod.rs" || base == "build.rs" {
			continue
		}
		if !strings.HasSuffix(base, ".rs") {
			continue
		}
		// Find sibling mod.rs / lib.rs / main.rs in the same or
		// parent directory.
		modFile := rustNearestModFile(f)
		if modFile == "" {
			continue
		}
		modName := strings.TrimSuffix(base, ".rs")
		body, err := os.ReadFile(modFile)
		if err != nil {
			continue
		}
		// Look for `mod <name>;` or `pub mod <name>;`.
		pat := regexp.MustCompile(`(?m)^\s*(?:pub\s+)?mod\s+` + regexp.QuoteMeta(modName) + `\s*(?:;|\{)`)
		if pat.MatchString(string(body)) {
			continue
		}
		srcRel, _ := filepath.Rel(projectRoot, f)
		modRel, _ := filepath.Rel(projectRoot, modFile)
		out = append(out, PublicSurfaceMiss{
			SourceFile: srcRel,
			TargetFile: modRel,
			FixLine:    fmt.Sprintf("pub mod %s;", modName),
		})
	}
	return out, nil
}

// rustcErrRE matches `cargo check` / rustc error lines:
//
//	error[E0425]: cannot find value `foo` in this scope
//	 --> src/foo.rs:12:5
//
// We merge the code line + location line into one CompileErr.
var rustErrHeadRE = regexp.MustCompile(`^error(?:\[(E\d+)\])?:\s*(.*)$`)
var rustErrLocRE = regexp.MustCompile(`^\s*-->\s+(.+?):(\d+):(\d+)`)

func (rustEcosystem) CompileErrors(ctx context.Context, projectRoot string, files []string) ([]CompileErr, error) {
	manifests := rustCollectManifests(projectRoot)
	if len(manifests) == 0 {
		return nil, nil
	}
	relevant := map[string]struct{}{}
	for _, f := range files {
		m := rustNearestManifest(f, manifests)
		if m != "" {
			relevant[m] = struct{}{}
		}
	}
	if len(relevant) == 0 {
		return nil, nil
	}
	if _, err := exec.LookPath("cargo"); err != nil {
		return nil, nil
	}
	var all []CompileErr
	for mani := range relevant {
		c, cancel := context.WithTimeout(ctx, 180*time.Second)
		cmd := exec.CommandContext(c, "cargo", "check", "--message-format", "short", "--manifest-path", mani)
		cmd.Dir = projectRoot
		out, _ := cmd.CombinedOutput()
		cancel()
		// cargo check --message-format short produces lines like:
		//   src/foo.rs:12:3: error[E0425]: message
		shortRE := regexp.MustCompile(`^(.+?\.rs):(\d+):(\d+):\s+error(?:\[(E\d+)\])?:\s*(.*)$`)
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if m := shortRE.FindStringSubmatch(line); m != nil {
				var lno, col int
				fmt.Sscanf(m[2], "%d", &lno)
				fmt.Sscanf(m[3], "%d", &col)
				code := m[4]
				if code == "" {
					code = "rustc"
				}
				file := m[1]
				if !filepath.IsAbs(file) {
					file = filepath.Join(filepath.Dir(mani), file)
				}
				rel, err := filepath.Rel(projectRoot, file)
				if err != nil {
					rel = file
				}
				all = append(all, CompileErr{
					File: rel, Line: lno, Column: col, Code: code, Message: m[5],
				})
			}
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].File != all[j].File {
			return all[i].File < all[j].File
		}
		return all[i].Line < all[j].Line
	})
	return all, nil
}

// ---------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------

func rustCollectManifests(projectRoot string) []string {
	var out []string
	_ = filepath.WalkDir(projectRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == "target" || name == ".git" || name == "node_modules" {
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

func rustNearestManifest(file string, manifests []string) string {
	best := ""
	bestLen := -1
	for _, m := range manifests {
		dir := filepath.Dir(m)
		if strings.HasPrefix(file, dir+string(os.PathSeparator)) || file == dir {
			if len(dir) > bestLen {
				bestLen = len(dir)
				best = m
			}
		}
	}
	return best
}

func rustManifestDeps(path string) map[string]struct{} {
	out := map[string]struct{}{}
	body, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	scanner := bufio.NewScanner(strings.NewReader(string(body)))
	section := ""
	sectionRE := regexp.MustCompile(`^\[([^\]]+)\]`)
	depRE := regexp.MustCompile(`^([A-Za-z0-9_-]+)\s*=`)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if m := sectionRE.FindStringSubmatch(line); m != nil {
			section = m[1]
			continue
		}
		if section == "dependencies" || section == "dev-dependencies" ||
			section == "build-dependencies" || strings.HasSuffix(section, ".dependencies") {
			if m := depRE.FindStringSubmatch(line); m != nil {
				out[m[1]] = struct{}{}
				// Also register underscore form (common Rust
				// import-vs-crate naming drift).
				out[strings.ReplaceAll(m[1], "-", "_")] = struct{}{}
			}
		}
	}
	return out
}

func rustCrateName(manifest string) string {
	body, err := os.ReadFile(manifest)
	if err != nil {
		return ""
	}
	scanner := bufio.NewScanner(strings.NewReader(string(body)))
	inPkg := false
	nameRE := regexp.MustCompile(`^name\s*=\s*"([^"]+)"`)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "[package]" {
			inPkg = true
			continue
		}
		if strings.HasPrefix(line, "[") && line != "[package]" {
			inPkg = false
			continue
		}
		if inPkg {
			if m := nameRE.FindStringSubmatch(line); m != nil {
				return strings.ReplaceAll(m[1], "-", "_")
			}
		}
	}
	return ""
}

func rustStdCrates() map[string]struct{} {
	names := []string{"std", "core", "alloc", "proc_macro", "test"}
	out := make(map[string]struct{}, len(names))
	for _, n := range names {
		out[n] = struct{}{}
	}
	return out
}

func rustIsLocalModule(file, name string) bool {
	dir := filepath.Dir(file)
	candidates := []string{
		filepath.Join(dir, name+".rs"),
		filepath.Join(dir, name, "mod.rs"),
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

// rustNearestModFile walks up from file's directory looking for
// mod.rs or lib.rs or main.rs, in that order of preference.
func rustNearestModFile(file string) string {
	dir := filepath.Dir(file)
	for i := 0; i < 8; i++ {
		for _, name := range []string{"mod.rs", "lib.rs", "main.rs"} {
			p := filepath.Join(dir, name)
			if info, err := os.Stat(p); err == nil && !info.IsDir() {
				return p
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
	return ""
}
