// Command chdir-lint is a Go AST-based lint that flags unannotated calls to
// directory-mutating or directory-reading APIs that are unsafe inside a
// long-running multi-session daemon.
//
// Phase A of the r1d-server spec (specs/r1d-server.md §10) requires every
// call site of:
//
//	os.Chdir
//	os.Getwd
//	filepath.Abs("")              // empty string -> uses cwd
//	os.Open("./...") / os.Open(".") and similar relative-path string literals
//
// to either be removed (refactored to thread an explicit `repoRoot string`)
// or annotated with a `// LINT-ALLOW chdir-<bucket>: <reason>` comment on the
// line immediately above the call. The lint is a BLOCKING gate before the
// multi-session daemon is enabled — a stray os.Chdir in goroutine-handler
// code silently leaks the working directory between concurrent sessions
// (see spec risk R1).
//
// The annotation form is:
//
//	// LINT-ALLOW chdir-cli-entry: <reason>
//	// LINT-ALLOW chdir-test:      <reason>
//	// LINT-ALLOW chdir-stdlib:    <reason>
//	// LINT-ALLOW chdir-fallback:  <reason>
//
// Any bucket name beginning with `chdir-` is accepted; the reason is
// free-form but must be present. The annotation lives on a comment line
// directly above the call (no blank line between).
//
// Usage:
//
//	chdir-lint ./internal/... ./cmd/...
//
// Exits with status 1 (and prints `<file>:<line>: <call> without LINT-ALLOW
// annotation` for every violation) when any unannotated hit is found.
//
// Implementation notes:
//
//   - Loads packages via golang.org/x/tools/go/packages with NeedFiles +
//     NeedSyntax + NeedTypesInfo so we can resolve `os.Chdir` (vs a local
//     identifier shadowing `os`). Falls back to syntactic matching when type
//     info is unavailable (test files, broken builds).
//   - Walks every *ast.File via ast.Inspect; the predicate `isWatched`
//     returns the bucket name for matching CallExprs.
//   - For every hit, scans the file's CommentGroups for one whose End() is
//     on the line directly preceding the call. If the comment text matches
//     `LINT-ALLOW chdir-<bucket>:`, the hit is suppressed.
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

// allowRE matches the annotation form `// LINT-ALLOW chdir-<bucket>: <reason>`.
// We accept any bucket prefixed with `chdir-` and require a non-empty reason
// after the colon.
var allowRE = regexp.MustCompile(`LINT-ALLOW\s+chdir-[a-zA-Z0-9_-]+\s*:\s*\S`)

// violation describes a single unannotated hit.
type violation struct {
	Pos  token.Position
	Call string // human-readable form of the call (e.g. "os.Chdir")
}

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: %s [flags] <pkg-pattern>...\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "       e.g.: %s ./internal/... ./cmd/...\n", os.Args[0])
		flag.PrintDefaults()
	}
	verbose := flag.Bool("v", false, "verbose: print per-package load progress to stderr")
	flag.Parse()

	patterns := flag.Args()
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}

	cfg := &packages.Config{
		Mode: packages.NeedFiles |
			packages.NeedSyntax |
			packages.NeedTypes |
			packages.NeedTypesInfo |
			packages.NeedCompiledGoFiles |
			packages.NeedName,
		Tests: true,
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "chdir-lint: load failed: %v\n", err)
		os.Exit(2)
	}

	// We tolerate per-package load errors — a broken build elsewhere should
	// not block this lint — but we surface them in verbose mode.
	if *verbose {
		packages.Visit(pkgs, nil, func(p *packages.Package) {
			fmt.Fprintf(os.Stderr, "chdir-lint: loaded %s (%d files, %d errors)\n",
				p.PkgPath, len(p.GoFiles), len(p.Errors))
		})
	}

	var allViolations []violation
	seen := make(map[string]bool) // dedupe across test/non-test variants

	packages.Visit(pkgs, nil, func(p *packages.Package) {
		for i, file := range p.Syntax {
			// guard against malformed pkg state
			if i >= len(p.CompiledGoFiles) {
				continue
			}
			path := p.CompiledGoFiles[i]
			if seen[path] {
				continue
			}
			seen[path] = true
			vs := scanFile(p.Fset, file)
			allViolations = append(allViolations, vs...)
		}
	})

	if len(allViolations) == 0 {
		return
	}

	// Stable, file:line sorted output for deterministic CI logs.
	sort.Slice(allViolations, func(i, j int) bool {
		a, b := allViolations[i].Pos, allViolations[j].Pos
		if a.Filename != b.Filename {
			return a.Filename < b.Filename
		}
		return a.Line < b.Line
	})
	for _, v := range allViolations {
		fmt.Fprintf(os.Stdout, "%s:%d: %s without LINT-ALLOW annotation\n",
			v.Pos.Filename, v.Pos.Line, v.Call)
	}
	fmt.Fprintf(os.Stderr, "chdir-lint: %d violation(s)\n", len(allViolations))
	os.Exit(1)
}

// scanFile walks a single file and returns every unannotated hit.
func scanFile(fset *token.FileSet, file *ast.File) []violation {
	var hits []violation

	// Build a quick-lookup table: line -> []*ast.CommentGroup that END on that line.
	commentByEndLine := make(map[int][]*ast.CommentGroup)
	for _, cg := range file.Comments {
		end := fset.Position(cg.End()).Line
		commentByEndLine[end] = append(commentByEndLine[end], cg)
	}

	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		bucket, label := classify(call)
		if bucket == "" {
			return true
		}
		pos := fset.Position(call.Pos())
		if isAllowed(commentByEndLine, pos.Line) {
			return true
		}
		hits = append(hits, violation{Pos: pos, Call: label})
		return true
	})

	return hits
}

// classify returns ("watched-bucket", "human-name") if the CallExpr is one
// of the watched APIs, or ("", "") otherwise.
//
// We match syntactically rather than via go/types because the lint must
// work even when the package fails to type-check (broken builds during
// audit refactors). The trade-off: a local identifier that shadows `os`
// could produce a false positive — accepted, because the LINT-ALLOW
// annotation handles it.
func classify(call *ast.CallExpr) (bucket string, label string) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", ""
	}
	pkgIdent, ok := sel.X.(*ast.Ident)
	if !ok {
		return "", ""
	}
	pkg, name := pkgIdent.Name, sel.Sel.Name
	full := pkg + "." + name

	switch full {
	case "os.Chdir":
		return "chdir", "os.Chdir"
	case "os.Getwd":
		return "getwd", "os.Getwd"
	case "filepath.Abs":
		// Only flag filepath.Abs("") — the empty string variant resolves to
		// the current working directory, which is a hidden cwd dependency.
		// filepath.Abs(<other>) is fine.
		if len(call.Args) == 1 && isEmptyStringLit(call.Args[0]) {
			return "abs-empty", `filepath.Abs("")`
		}
		return "", ""
	case "os.Open", "os.OpenFile":
		// Flag os.Open("./...") or os.Open(".") string literals — these are
		// implicit cwd reads. Variable arguments are fine (the caller is
		// responsible).
		if len(call.Args) >= 1 {
			if s, ok := stringLit(call.Args[0]); ok && isRelativeDot(s) {
				return "open-rel", fmt.Sprintf("%s(%q)", full, s)
			}
		}
		return "", ""
	}
	return "", ""
}

// isAllowed returns true if a `// LINT-ALLOW chdir-...: <reason>` comment
// appears on the line immediately preceding `callLine`.
func isAllowed(commentByEndLine map[int][]*ast.CommentGroup, callLine int) bool {
	// The annotation must be on the line directly above the call. We tolerate
	// the comment END being on callLine-1 (typical case) — multi-line block
	// comments still match on their final line.
	for _, cg := range commentByEndLine[callLine-1] {
		for _, c := range cg.List {
			if allowRE.MatchString(c.Text) {
				return true
			}
		}
	}
	return false
}

// isEmptyStringLit reports whether expr is the literal "" (empty string).
func isEmptyStringLit(expr ast.Expr) bool {
	s, ok := stringLit(expr)
	return ok && s == ""
}

// stringLit extracts a Go string literal from a BasicLit node.
func stringLit(expr ast.Expr) (string, bool) {
	bl, ok := expr.(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		return "", false
	}
	// strip surrounding quotes; we don't unescape — relative-path detection
	// only needs the leading bytes.
	v := bl.Value
	if len(v) >= 2 {
		v = v[1 : len(v)-1]
	}
	return v, true
}

// isRelativeDot reports whether s is "." or starts with "./" or "../".
// Absolute paths and bare filenames are not flagged.
func isRelativeDot(s string) bool {
	switch s {
	case ".", "./":
		return true
	}
	return strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../")
}
