// Package plan — integrity_gate.go
//
// Workspace-level integrity checks that run AFTER a session completes
// successfully. Ecosystem-agnostic by design: three universal
// scenarios that apply to every language stack, with per-stack
// implementations of the primitive probes.
//
// Universal scenarios the gate catches:
//
//  1. Unresolved manifest imports. A session wrote a file that
//     references an external symbol (import, use, require, #include)
//     which is not declared in the ecosystem's dependency manifest.
//     TS: `import x from 'jose'` with no entry in package.json.
//     Go: `import "github.com/x/y"` with no line in go.mod.
//     Rust: `use foo::bar` with no [dependencies].foo in Cargo.toml.
//     Python: `import jose` with no jose in pyproject.toml /
//     requirements.txt.
//
//  2. Public-surface drift. A session created a public symbol in a
//     package's public-export location but did not wire the re-export.
//     TS: src/components/Foo.tsx not re-exported from src/index.ts.
//     Rust: src/foo.rs without `mod foo;` in lib.rs / mod.rs.
//     Python: package/foo.py without an __init__.py entry (when the
//     package uses explicit __all__).
//     Go has no barrel concept; the check is a no-op there.
//
//  3. Compile regression. Run the ecosystem's static-check command
//     scoped to the packages this session wrote, diff the error set
//     against a baseline captured before the session ran, and surface
//     only NEW errors as this session's responsibility.
//     TS: tsc --noEmit.
//     Go: go vet ./<pkgs>; go build ./<pkgs>.
//     Rust: cargo check --manifest-path <...>.
//     Python: pyright / mypy (when configured).
//
// An ecosystem that doesn't support a scenario returns an empty
// result; the gate never reports spurious directives.
//
// Ecosystem dispatch is automatic based on what files the session
// wrote. A single session can span multiple ecosystems (e.g., a Go
// backend plus a TS frontend monorepo); the gate runs every matching
// ecosystem's probes and unions the results.
package plan

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// IntegrityReport is the combined output of every matching ecosystem's
// probes. Empty Directives + nil Err means the session is clean.
type IntegrityReport struct {
	// Directives is a flat list of remediation items suitable for
	// promoting into a fix session. One directive per issue.
	Directives []string
	// Issues is the structured detail (per-ecosystem, per-category)
	// for logging / telemetry. Directives is the human-readable
	// projection of this.
	Issues []IntegrityIssue
}

// IntegrityIssue is one structured finding. Category is one of
// "manifest-import", "public-surface", "compile-regression".
type IntegrityIssue struct {
	Ecosystem  string // e.g., "typescript", "go", "rust", "python"
	Category   string
	SourceFile string // repo-relative file that surfaced the issue
	// TargetFile is the file whose CONTENT the fix session must
	// modify to resolve the issue. Differs from SourceFile for
	// manifest-import (target = manifest like package.json / go.mod)
	// and public-surface (target = barrel / mod file), matches
	// SourceFile for compile-regression. Used by the fix-session
	// AC synthesis so hash-change verification targets the file
	// that should actually change.
	TargetFile string
	Detail     string // ecosystem-specific detail (symbol name, error code, etc.)
	Fix        string // suggested remediation (single sentence)
}

// Ecosystem is the plug-in interface for a language/stack. Each
// implementation knows how to detect which files it owns, validate
// manifest imports, validate public-surface re-exports, and capture
// compile-error snapshots.
//
// Every method is optional in spirit: an ecosystem that doesn't have
// a concept (e.g., Go has no barrel re-exports) returns an empty
// result. Callers never hardcode per-ecosystem assumptions; the
// registry dispatches based on file ownership.
type Ecosystem interface {
	// Name is the ecosystem identifier ("typescript", "go", ...).
	Name() string
	// Owns reports whether this ecosystem should process the file.
	// Typically based on extension and/or sibling manifest.
	Owns(absPath string) bool
	// UnresolvedImports returns every (file, specifier) pair where
	// the file imports a symbol not declared in the manifest chain.
	UnresolvedImports(projectRoot string, files []string) ([]ManifestMiss, error)
	// MissingPublicSurface returns every public file that should be
	// re-exported from a barrel/mod-declaration/__init__ but isn't.
	// Ecosystems without the concept return nil.
	MissingPublicSurface(projectRoot string, files []string) ([]PublicSurfaceMiss, error)
	// CompileErrors runs the ecosystem's static checker scoped to
	// the given files' owning packages and returns the parsed error
	// set. Used both for baseline capture (before session) and
	// regression detection (after session).
	CompileErrors(ctx context.Context, projectRoot string, files []string) ([]CompileErr, error)
}

// ManifestMiss is a bare import that resolves to no manifest entry.
type ManifestMiss struct {
	SourceFile string // repo-relative
	ImportPath string // the bare specifier ("jose", "github.com/x/y", ...)
	Manifest   string // the manifest file that should declare it (repo-relative)
	AddCommand string // ecosystem-specific "how to add" one-liner
}

// PublicSurfaceMiss is a public file that should be re-exported but
// isn't.
type PublicSurfaceMiss struct {
	SourceFile string // repo-relative
	TargetFile string // the barrel/mod-root/__init__ to edit (repo-relative)
	FixLine    string // the line to insert (ecosystem-appropriate syntax)
}

// CompileErr is one ecosystem static-check error.
type CompileErr struct {
	File    string // repo-relative
	Line    int
	Column  int
	Code    string // ecosystem-native code (TS1234, vet, E0425, ...)
	Message string
}

// AlwaysRunner is an optional companion interface: when an ecosystem
// returns AlwaysRun()==true, the gate dispatches its probes even if
// its Owns() never matches any file. Used by cross-cutting gates
// (infra-policy, tauri cross-ecosystem contract check, mobile
// plugin-requirement check) that scan workspace-wide rather than
// claiming per-file ownership. The file list passed is the full
// session file set, letting the probe decide its own scope.
type AlwaysRunner interface {
	AlwaysRun() bool
}

// ecosystemRegistry is the set of ecosystems the gate knows about.
// Ordered by priority: more specific ecosystems first. New
// ecosystems plug in via RegisterEcosystem (see integrity_ts.go,
// integrity_go.go, etc. in the same package).
var ecosystemRegistry []Ecosystem

// RegisterEcosystem adds an ecosystem implementation to the gate.
// Called from each ecosystem file's init() so the default set is
// available without explicit wiring at the call site. Tests can
// register mocks via this function too.
func RegisterEcosystem(e Ecosystem) {
	ecosystemRegistry = append(ecosystemRegistry, e)
}

// RunIntegrityGate runs the three scenarios across every ecosystem
// that owns any of the session's declared files. The baseline
// argument maps ecosystem name → pre-session compile errors; passing
// nil skips the regression check.
//
// A non-nil err here means the gate itself failed (filesystem /
// process failure), not that the session produced issues. Issues
// are surfaced via report.Directives.
func RunIntegrityGate(ctx context.Context, projectRoot string, session Session, baseline map[string][]CompileErr) (*IntegrityReport, error) {
	report := &IntegrityReport{}
	files := collectSessionFiles(projectRoot, session)
	if len(files) == 0 {
		return report, nil
	}

	// Bucket files by ecosystem. A file can be owned by at most one
	// ecosystem; the first registered ecosystem that claims it wins.
	// In addition, cross-cutting ecosystems (those that implement
	// AlwaysRunner and return true) get dispatched once per session
	// regardless of file attribution — infra-policy, tauri, and
	// mobile-plugin checks span the whole workspace and can't be
	// driven by per-file Owns().
	byEco := map[string][]string{}
	ecoLookup := map[string]Ecosystem{}
	for _, f := range files {
		for _, eco := range ecosystemRegistry {
			if eco.Owns(f) {
				byEco[eco.Name()] = append(byEco[eco.Name()], f)
				ecoLookup[eco.Name()] = eco
				break
			}
		}
	}
	for _, eco := range ecosystemRegistry {
		ar, ok := eco.(AlwaysRunner)
		if !ok || !ar.AlwaysRun() {
			continue
		}
		if _, dup := ecoLookup[eco.Name()]; dup {
			continue
		}
		// Cross-cutting probes accept the full session file set so
		// they can decide what to scan themselves.
		byEco[eco.Name()] = files
		ecoLookup[eco.Name()] = eco
	}

	// Probe each ecosystem in deterministic order (by name) so
	// repeated runs produce stable output.
	var ecoNames []string
	for name := range byEco {
		ecoNames = append(ecoNames, name)
	}
	sort.Strings(ecoNames)

	for _, name := range ecoNames {
		eco := ecoLookup[name]
		ecoFiles := byEco[name]

		// (1) Manifest imports.
		if miss, err := eco.UnresolvedImports(projectRoot, ecoFiles); err == nil {
			for _, m := range miss {
				fix := m.AddCommand
				if fix == "" {
					fix = fmt.Sprintf("add %q to %s", m.ImportPath, m.Manifest)
				}
				// TargetFile must be a real editable file. Some
				// ecosystems (infra-policy, tauri cross-ecosystem)
				// emit Manifest strings that are prose labels or
				// directory paths, not file paths — fall back to
				// SourceFile in those cases so the hash-based AC
				// targets something the worker can actually mutate.
				target := m.Manifest
				if !isRealFilePath(projectRoot, target) {
					target = m.SourceFile
				}
				report.Issues = append(report.Issues, IntegrityIssue{
					Ecosystem:  name,
					Category:   "manifest-import",
					SourceFile: m.SourceFile,
					TargetFile: target,
					Detail:     m.ImportPath,
					Fix:        fix,
				})
				report.Directives = append(report.Directives, fmt.Sprintf(
					"[%s] %s imports %q but %s does not declare it. Fix: %s",
					name, m.SourceFile, m.ImportPath, m.Manifest, fix))
			}
		} else {
			// A probe failure is logged via directive so the caller
			// sees it; we don't abort the whole gate.
			report.Directives = append(report.Directives, fmt.Sprintf(
				"[%s] manifest-import probe failed: %v", name, err))
		}

		// (2) Public surface.
		if miss, err := eco.MissingPublicSurface(projectRoot, ecoFiles); err == nil {
			for _, m := range miss {
				report.Issues = append(report.Issues, IntegrityIssue{
					Ecosystem:  name,
					Category:   "public-surface",
					SourceFile: m.SourceFile,
					TargetFile: m.TargetFile, // edit the barrel/mod file
					Detail:     m.FixLine,
					Fix:        fmt.Sprintf("insert `%s` into %s", m.FixLine, m.TargetFile),
				})
				report.Directives = append(report.Directives, fmt.Sprintf(
					"[%s] %s is public but not re-exported. Fix: insert `%s` into %s",
					name, m.SourceFile, m.FixLine, m.TargetFile))
			}
		} else {
			report.Directives = append(report.Directives, fmt.Sprintf(
				"[%s] public-surface probe failed: %v", name, err))
		}

		// (3) Compile regression.
		if baseline != nil {
			cur, err := eco.CompileErrors(ctx, projectRoot, ecoFiles)
			if err != nil {
				report.Directives = append(report.Directives, fmt.Sprintf(
					"[%s] compile-regression probe failed: %v", name, err))
				continue
			}
			newErrs := compileErrDelta(baseline[name], cur)
			// Group by file for compact directives.
			byFile := map[string][]CompileErr{}
			for _, e := range newErrs {
				byFile[e.File] = append(byFile[e.File], e)
			}
			var sortedFiles []string
			for f := range byFile {
				sortedFiles = append(sortedFiles, f)
			}
			sort.Strings(sortedFiles)
			for _, f := range sortedFiles {
				errs := byFile[f]
				var lines []string
				for _, e := range errs {
					lines = append(lines, fmt.Sprintf("line %d: %s %s", e.Line, e.Code, e.Message))
				}
				report.Issues = append(report.Issues, IntegrityIssue{
					Ecosystem:  name,
					Category:   "compile-regression",
					SourceFile: f,
					TargetFile: f, // the file with the error IS what to edit
					Detail:     fmt.Sprintf("%d new error(s)", len(errs)),
					Fix:        "resolve new errors introduced in this session",
				})
				report.Directives = append(report.Directives, fmt.Sprintf(
					"[%s] new compile errors in %s introduced by this session:\n  - %s",
					name, f, strings.Join(lines, "\n  - ")))
			}
		}
	}
	return report, nil
}

// CaptureCompileBaseline runs CompileErrors for every ecosystem that
// owns any file in the project and returns the result keyed by
// ecosystem name. Used by the caller to capture pre-session state
// for later regression diff. Scope mirrors the session's file set.
func CaptureCompileBaseline(ctx context.Context, projectRoot string, files []string) map[string][]CompileErr {
	out := map[string][]CompileErr{}
	if len(files) == 0 {
		return out
	}
	byEco := map[string][]string{}
	ecoLookup := map[string]Ecosystem{}
	for _, f := range files {
		for _, eco := range ecosystemRegistry {
			if eco.Owns(f) {
				byEco[eco.Name()] = append(byEco[eco.Name()], f)
				ecoLookup[eco.Name()] = eco
				break
			}
		}
	}
	for name, fs := range byEco {
		errs, err := ecoLookup[name].CompileErrors(ctx, projectRoot, fs)
		if err != nil {
			continue
		}
		out[name] = errs
	}
	return out
}

// isRealFilePath reports whether the given relative or absolute path
// points at an actual file inside projectRoot. Used by the manifest-
// import issue synthesis to reject prose labels ("SOW architecture
// policy"), directory paths ("src-tauri/src/"), and path strings
// with parenthetical clarifications ("src-tauri/src/main.rs
// (invoke_handler)") — any of which would break the hash-based
// acceptance check downstream.
func isRealFilePath(projectRoot, p string) bool {
	if p == "" {
		return false
	}
	// Parenthetical clarifications / whitespace mean not a clean
	// file path. Similarly for backticks or other punctuation.
	if strings.ContainsAny(p, "() \t`") {
		return false
	}
	abs := p
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(projectRoot, p)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// collectSessionFiles returns absolute paths of every file the
// session's tasks declared, filtered to files that exist on disk.
func collectSessionFiles(projectRoot string, session Session) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, t := range session.Tasks {
		for _, f := range t.Files {
			abs := f
			if !filepath.IsAbs(abs) {
				abs = filepath.Join(projectRoot, f)
			}
			abs = filepath.Clean(abs)
			if _, dup := seen[abs]; dup {
				continue
			}
			if info, err := os.Stat(abs); err != nil || info.IsDir() {
				continue
			}
			seen[abs] = struct{}{}
			out = append(out, abs)
		}
	}
	sort.Strings(out)
	return out
}

// compileErrDelta returns errors present in cur but not in base.
// Keyed on (file, code, message) so line shifts don't hide
// regressions and identical duplicates dedupe.
func compileErrDelta(base, cur []CompileErr) []CompileErr {
	key := func(e CompileErr) string {
		return e.File + "\x00" + e.Code + "\x00" + e.Message
	}
	seen := map[string]struct{}{}
	for _, e := range base {
		seen[key(e)] = struct{}{}
	}
	var out []CompileErr
	for _, e := range cur {
		if _, ok := seen[key(e)]; ok {
			continue
		}
		out = append(out, e)
	}
	return out
}
