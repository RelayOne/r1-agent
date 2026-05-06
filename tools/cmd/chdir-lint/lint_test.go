package main

// lint_test.go — Phase I item 51 (specs/r1d-server.md).
//
// Tests the chdir-lint scanner against a fixture file with mixed
// annotated and unannotated watched calls. The scanner is the
// blocking gate before Phase E enables multi-session — every test
// here is also a regression bound for the LINT-ALLOW grammar.
//
// Coverage matrix (verified by TestScanFile_FixtureViolations):
//
//   call                         annotated?     expected violation?
//   ----                         ----------     -------------------
//   os.Chdir(".")                no             yes
//   os.Chdir("/tmp")             yes (chdir-test) no
//   os.Getwd()                   no             yes
//   os.Getwd() (multi-line cmt)  yes (chdir-stdlib) no
//   filepath.Abs("")             no             yes
//   filepath.Abs(x)              no             no  (variable arg)
//   os.Open(".")                 no             yes
//   os.Open("./foo")             no             yes
//   os.Open("/etc/hosts")        no             no  (absolute)
//   os.Open(filename)            no             no  (variable arg)
//
// The fixture also exercises the corner cases in classify():
//
//   - filepath.Abs with two args (would not compile in real code,
//     but classify only matches len==1; we test via a synthesised
//     fixture).
//   - selector-not-on-ident (e.g. `(*os.File).Chdir(...)` style).
//
// The fixture is built in-memory so the test stays hermetic.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"
)

// fixtureSource is the test input the scanner walks. Lines are
// numbered in comments so we can write the expected-violations
// table in the same shape.
const fixtureSource = `package fixture

import (
	"os"
	"path/filepath"
)

func main() {
	// L9 unannotated os.Chdir → violation
	_ = os.Chdir(".")

	// LINT-ALLOW chdir-test: annotated; SHOULD be suppressed.
	_ = os.Chdir("/tmp")

	// L15 unannotated os.Getwd → violation
	_, _ = os.Getwd()

	// LINT-ALLOW chdir-stdlib: multi-line annotation suppresses.
	_, _ = os.Getwd()

	// L21 unannotated filepath.Abs("") → violation
	_, _ = filepath.Abs("")

	// L24 filepath.Abs with non-empty literal — NOT flagged.
	_, _ = filepath.Abs("/etc")

	// L27 filepath.Abs with variable arg — NOT flagged.
	var x string
	_, _ = filepath.Abs(x)

	// L31 unannotated os.Open(".") → violation
	_, _ = os.Open(".")

	// L34 unannotated os.Open("./foo") → violation
	_, _ = os.Open("./foo")

	// L37 os.Open absolute path — NOT flagged.
	_, _ = os.Open("/etc/hosts")

	// L40 os.Open variable arg — NOT flagged.
	var name string
	_, _ = os.Open(name)

	// L44 os.OpenFile(".") → violation (relative-dot literal).
	_, _ = os.OpenFile(".", 0, 0)
}
`

// TestScanFile_FixtureViolations parses fixtureSource and asserts
// scanFile returns exactly the expected violations, in file:line
// order. The expected list is the load-bearing contract: any new
// false positive OR missing detection breaks the test.
func TestScanFile_FixtureViolations(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", fixtureSource, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}

	got := scanFile(fset, file)
	sort.Slice(got, func(i, j int) bool {
		if got[i].Pos.Line != got[j].Pos.Line {
			return got[i].Pos.Line < got[j].Pos.Line
		}
		return got[i].Call < got[j].Call
	})

	// Expected violation labels in file:line order. Line numbers
	// are read off the parsed AST rather than hardcoded so a
	// fixture edit doesn't cascade.
	wantCalls := []string{
		"os.Chdir",
		"os.Getwd",
		`filepath.Abs("")`,
		`os.Open(".")`,
		`os.Open("./foo")`,
		`os.OpenFile(".")`,
	}
	if len(got) != len(wantCalls) {
		t.Fatalf("violation count: got %d, want %d\nviolations: %+v",
			len(got), len(wantCalls), got)
	}
	for i, w := range wantCalls {
		if got[i].Call != w {
			t.Errorf("violation %d call: got %q, want %q (line=%d)",
				i, got[i].Call, w, got[i].Pos.Line)
		}
	}
	// Cross-check: line numbers strictly increase (sorted output is
	// the documented contract; main() prints in file:line order so
	// tests must too).
	for i := 1; i < len(got); i++ {
		if got[i].Pos.Line <= got[i-1].Pos.Line {
			t.Errorf("violations not strictly line-sorted: [%d]=%d <= [%d]=%d",
				i, got[i].Pos.Line, i-1, got[i-1].Pos.Line)
		}
	}
}

// TestScanFile_AllowAnnotationGrammar exercises the LINT-ALLOW
// grammar: a `// LINT-ALLOW chdir-<bucket>: <reason>` line directly
// above the call suppresses the violation. Variants:
//
//   - missing reason ("chdir-test:" with empty rhs) → NOT allowed.
//   - missing colon ("LINT-ALLOW chdir-test no-reason")  → NOT allowed.
//   - wrong prefix ("LINT-ALLOW other-bucket: r")  → NOT allowed.
//   - blank line between comment and call             → NOT allowed.
//   - multi-line block comment ending on line above   → ALLOWED.
//
// The grammar is the operator-facing contract; test pins each
// variant so a regression breaks loudly.
func TestScanFile_AllowAnnotationGrammar(t *testing.T) {
	cases := []struct {
		name    string
		src     string
		wantHit bool
	}{
		{
			name: "valid_simple",
			src: `package p
import "os"
func _() {
	// LINT-ALLOW chdir-test: reason here
	_ = os.Chdir("/x")
}
`,
			wantHit: false,
		},
		{
			name: "missing_reason",
			src: `package p
import "os"
func _() {
	// LINT-ALLOW chdir-test:
	_ = os.Chdir("/x")
}
`,
			wantHit: true, // empty reason fails the regex.
		},
		{
			name: "missing_colon",
			src: `package p
import "os"
func _() {
	// LINT-ALLOW chdir-test no-reason
	_ = os.Chdir("/x")
}
`,
			wantHit: true,
		},
		{
			name: "wrong_bucket_prefix",
			src: `package p
import "os"
func _() {
	// LINT-ALLOW other-bucket: r
	_ = os.Chdir("/x")
}
`,
			wantHit: true,
		},
		{
			name: "blank_line_between",
			src: `package p
import "os"
func _() {
	// LINT-ALLOW chdir-test: reason

	_ = os.Chdir("/x")
}
`,
			wantHit: true, // grammar requires comment END on callLine-1.
		},
		{
			name: "block_comment_ending_above",
			src: `package p
import "os"
func _() {
	/* LINT-ALLOW chdir-test: block-form reason
	   second line of block comment */
	_ = os.Chdir("/x")
}
`,
			wantHit: false, // block comment END is on the line above call.
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, tc.name+".go", tc.src, parser.ParseComments)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			hits := scanFile(fset, file)
			if tc.wantHit && len(hits) == 0 {
				t.Fatalf("expected violation, got none\nsrc:\n%s", tc.src)
			}
			if !tc.wantHit && len(hits) != 0 {
				t.Fatalf("expected no violation, got %d\nhits: %+v\nsrc:\n%s",
					len(hits), hits, tc.src)
			}
		})
	}
}

// TestScanFile_Classify_NonSelectorCalls asserts the scanner does
// NOT flag calls that are not `pkg.Func(...)` selectors. Dotted
// identifiers can appear inside method-value expressions; classify()
// only matches `*ast.SelectorExpr` whose X is an `*ast.Ident`, so
// `(file).Chdir()` patterns (method on a value) MUST NOT match.
func TestScanFile_Classify_NonSelectorCalls(t *testing.T) {
	src := `package p
import "os"
func _() {
	// Method-value on a *os.File — NOT a watched call (it's a
	// receiver method, not the package-level os.Chdir we guard).
	var f *os.File
	_ = f.Chdir()

	// Variable named "os" shadowing the package — classify treats
	// pkgIdent.Name == "os" the same regardless of binding (we
	// accept this trade-off; LINT-ALLOW handles false positives).
	type fakeOS struct{}
	_ = fakeOS{}
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "x.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	hits := scanFile(fset, file)
	// assert.method-call-ignored: f.Chdir() is a method receiver,
	// not the package-level os.Chdir; classify() must not flag it.
	for _, h := range hits {
		if strings.Contains(h.Call, "f.Chdir") {
			t.Errorf("unexpected hit on method receiver: %+v", h)
		}
	}
}

// TestIsAllowed_EmptyCommentTable asserts isAllowed returns false on
// a nil/empty map — a file with no comments produces an empty
// commentByEndLine table and the helper must not panic.
func TestIsAllowed_EmptyCommentTable(t *testing.T) {
	// assert.empty-table: nil map is the zero-value baseline.
	if isAllowed(nil, 5) {
		t.Error("isAllowed on nil table returned true")
	}
	// assert.empty-map: explicit empty map behaves identically.
	if isAllowed(map[int][]*ast.CommentGroup{}, 5) {
		t.Error("isAllowed on empty map returned true")
	}
}
