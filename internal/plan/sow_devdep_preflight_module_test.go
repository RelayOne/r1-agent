package plan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeModuleTestFile mkdir -p's the parent then writes content.
// Named distinctly to avoid collision with declared_symbols_test.go.
func writeModuleTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// readPkgType parses package.json at path and returns pkg["type"]
// as a string (empty if unset). Also returns the full decoded map
// so tests can assert other fields were preserved.
func readPkgType(t *testing.T, path string) (string, map[string]any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	t2, _ := m["type"].(string)
	return t2, m
}

func joinDiag(lines []string) string { return strings.Join(lines, "\n") }

func TestPreflightFixModuleType_AddsTypeWhenMissing(t *testing.T) {
	root := t.TempDir()
	pkg := filepath.Join(root, "package.json")
	writeModuleTestFile(t, pkg, `{"name":"x","version":"0.0.1"}`)
	writeModuleTestFile(t, filepath.Join(root, "src", "index.ts"), `import x from "y"
export const a = 1
`)

	diag := PreflightFixModuleType(root)
	if len(diag) == 0 {
		t.Fatalf("expected a diagnostic, got none")
	}
	if !strings.Contains(joinDiag(diag), `set "type":"module"`) {
		t.Fatalf("diagnostic missing set-type phrase: %v", diag)
	}
	got, m := readPkgType(t, pkg)
	if got != "module" {
		t.Fatalf(`expected "type":"module", got %q`, got)
	}
	if m["name"] != "x" || m["version"] != "0.0.1" {
		t.Fatalf("unrelated fields not preserved: %v", m)
	}
}

func TestPreflightFixModuleType_UpgradesFromCommonJS(t *testing.T) {
	root := t.TempDir()
	pkg := filepath.Join(root, "package.json")
	writeModuleTestFile(t, pkg, `{"name":"x","type":"commonjs"}`)
	writeModuleTestFile(t, filepath.Join(root, "src", "a.ts"), `export const z = 1
`)

	diag := PreflightFixModuleType(root)
	if !strings.Contains(joinDiag(diag), `upgraded "type":"commonjs" → "module"`) {
		t.Fatalf("diagnostic missing upgrade phrase: %v", diag)
	}
	got, _ := readPkgType(t, pkg)
	if got != "module" {
		t.Fatalf("expected module, got %q", got)
	}
}

func TestPreflightFixModuleType_SkipsAlreadyModule(t *testing.T) {
	root := t.TempDir()
	pkg := filepath.Join(root, "package.json")
	orig := `{"name":"x","type":"module"}`
	writeModuleTestFile(t, pkg, orig)
	writeModuleTestFile(t, filepath.Join(root, "src", "a.ts"), `import x from "y"
`)

	diag := PreflightFixModuleType(root)
	if len(diag) != 0 {
		t.Fatalf("expected no diagnostic, got %v", diag)
	}
	b, _ := os.ReadFile(pkg)
	if string(b) != orig {
		t.Fatalf("file mutated unexpectedly:\nwant %q\n got %q", orig, string(b))
	}
}

func TestPreflightFixModuleType_SkipsNodeModules(t *testing.T) {
	root := t.TempDir()
	// Root is a plain CJS package with no ESM syntax — should remain untouched.
	rootPkg := filepath.Join(root, "package.json")
	writeModuleTestFile(t, rootPkg, `{"name":"root"}`)

	// Nested package under node_modules with ESM syntax — must be ignored.
	nmPkg := filepath.Join(root, "node_modules", "foo", "package.json")
	writeModuleTestFile(t, nmPkg, `{"name":"foo"}`)
	writeModuleTestFile(t, filepath.Join(root, "node_modules", "foo", "src", "i.ts"),
		`export const a = 1
`)

	diag := PreflightFixModuleType(root)
	if len(diag) != 0 {
		t.Fatalf("expected no diagnostic (node_modules skipped), got %v", diag)
	}
	got, _ := readPkgType(t, nmPkg)
	if got == "module" {
		t.Fatalf("node_modules package was rewritten — should have been skipped")
	}
}

func TestPreflightFixModuleType_SkipsWhenMainIsCJS(t *testing.T) {
	root := t.TempDir()
	pkg := filepath.Join(root, "package.json")
	writeModuleTestFile(t, pkg, `{"name":"x","main":"./dist/index.cjs"}`)
	writeModuleTestFile(t, filepath.Join(root, "src", "index.ts"), `export default 1
`)

	diag := PreflightFixModuleType(root)
	if len(diag) != 0 {
		t.Fatalf("expected no diagnostic (main:.cjs CJS emit), got %v", diag)
	}
	got, _ := readPkgType(t, pkg)
	if got == "module" {
		t.Fatalf("package with main:.cjs was rewritten — unsafe")
	}
}

func TestPreflightFixModuleType_SkipsWhenExportsRequireIsCJS(t *testing.T) {
	root := t.TempDir()
	pkg := filepath.Join(root, "package.json")
	writeModuleTestFile(t, pkg, `{
  "name":"x",
  "exports": {
    ".": {
      "require": "./dist/x.cjs",
      "import":  "./dist/x.mjs"
    }
  }
}`)
	writeModuleTestFile(t, filepath.Join(root, "src", "index.ts"), `import x from "y"
`)

	diag := PreflightFixModuleType(root)
	if len(diag) != 0 {
		t.Fatalf("expected no diagnostic (exports.require CJS), got %v", diag)
	}
	got, _ := readPkgType(t, pkg)
	if got == "module" {
		t.Fatalf("package with exports CJS was rewritten — unsafe")
	}
}

func TestPreflightFixModuleType_SkipsWhenTsconfigCommonJS(t *testing.T) {
	root := t.TempDir()
	pkg := filepath.Join(root, "package.json")
	writeModuleTestFile(t, pkg, `{"name":"x"}`)
	writeModuleTestFile(t, filepath.Join(root, "tsconfig.json"),
		`{"compilerOptions":{"module":"CommonJS","target":"es2022"}}`)
	writeModuleTestFile(t, filepath.Join(root, "src", "i.ts"), `export const z = 1
`)

	diag := PreflightFixModuleType(root)
	if len(diag) != 0 {
		t.Fatalf("expected no diagnostic (tsconfig module:commonjs), got %v", diag)
	}
	got, _ := readPkgType(t, pkg)
	if got == "module" {
		t.Fatalf("package with tsconfig module:commonjs was rewritten — unsafe")
	}
}

func TestPreflightFixModuleType_SkipsWhenOnlyConfigFilesAreESM(t *testing.T) {
	root := t.TempDir()
	pkg := filepath.Join(root, "package.json")
	writeModuleTestFile(t, pkg, `{"name":"x"}`)
	// Monorepo root with only tooling configs using ESM syntax — must NOT flip.
	writeModuleTestFile(t, filepath.Join(root, "vite.config.ts"),
		`import { defineConfig } from "vite"
export default defineConfig({})
`)
	writeModuleTestFile(t, filepath.Join(root, "eslint.config.js"),
		`export default []
`)

	diag := PreflightFixModuleType(root)
	if len(diag) != 0 {
		t.Fatalf("expected no diagnostic (only config files ESM), got %v", diag)
	}
	got, _ := readPkgType(t, pkg)
	if got == "module" {
		t.Fatalf("package rewritten based on config files only — unsafe")
	}
}

func TestPreflightFixModuleType_FallsBackToPackageRootWhenNoSrc(t *testing.T) {
	root := t.TempDir()
	pkg := filepath.Join(root, "package.json")
	writeModuleTestFile(t, pkg, `{"name":"x"}`)
	// No src/ dir. One real ESM file + one tooling config — should trigger.
	writeModuleTestFile(t, filepath.Join(root, "index.ts"), `import x from "y"
`)
	writeModuleTestFile(t, filepath.Join(root, "vite.config.ts"),
		`import { defineConfig } from "vite"
export default defineConfig({})
`)

	diag := PreflightFixModuleType(root)
	if len(diag) == 0 {
		t.Fatalf("expected a diagnostic, got none")
	}
	got, _ := readPkgType(t, pkg)
	if got != "module" {
		t.Fatalf("expected module, got %q", got)
	}
}

func TestPreflightFixModuleType_SkipsNestedWorkspace(t *testing.T) {
	root := t.TempDir()
	// Parent package has no src/ — falls back to scanning root, but
	// the only ESM source lives inside a nested package which has
	// its own package.json. That source must NOT count for the
	// parent package.
	parentPkg := filepath.Join(root, "package.json")
	writeModuleTestFile(t, parentPkg, `{"name":"parent"}`)

	childPkg := filepath.Join(root, "packages", "child", "package.json")
	writeModuleTestFile(t, childPkg, `{"name":"child"}`)
	writeModuleTestFile(t, filepath.Join(root, "packages", "child", "src", "i.ts"),
		`import x from "y"
`)

	diag := PreflightFixModuleType(root)
	// The child package SHOULD be rewritten (it has its own package.json
	// and its own src with ESM). The parent MUST NOT be, because its
	// scan must stop at the nested boundary.
	parentType, _ := readPkgType(t, parentPkg)
	if parentType == "module" {
		t.Fatalf("parent was rewritten — should have stopped at nested package.json boundary")
	}
	childType, _ := readPkgType(t, childPkg)
	if childType != "module" {
		t.Fatalf("child package expected module, got %q (diag: %v)", childType, diag)
	}
}

func TestPreflightFixModuleType_IdempotentOnSecondRun(t *testing.T) {
	root := t.TempDir()
	pkg := filepath.Join(root, "package.json")
	writeModuleTestFile(t, pkg, `{"name":"x"}`)
	writeModuleTestFile(t, filepath.Join(root, "src", "i.ts"), `import x from "y"
`)

	d1 := PreflightFixModuleType(root)
	if len(d1) == 0 {
		t.Fatalf("first run: expected diagnostic, got none")
	}
	// Capture rewritten content, then run again and assert no
	// additional diagnostic + no further mutation.
	b1, _ := os.ReadFile(pkg)
	d2 := PreflightFixModuleType(root)
	if len(d2) != 0 {
		t.Fatalf("second run: expected no diagnostic, got %v", d2)
	}
	b2, _ := os.ReadFile(pkg)
	if string(b1) != string(b2) {
		t.Fatalf("package.json changed on idempotent second run:\nwas %q\nnow %q", b1, b2)
	}
}

func TestPreflightFixModuleType_InvalidPackageJSONIsNonFatal(t *testing.T) {
	root := t.TempDir()
	// One malformed, one valid-and-should-be-fixed. The malformed
	// must not halt the scan.
	bad := filepath.Join(root, "packages", "bad", "package.json")
	writeModuleTestFile(t, bad, `{"name":`) // truncated JSON

	good := filepath.Join(root, "packages", "good", "package.json")
	writeModuleTestFile(t, good, `{"name":"good"}`)
	writeModuleTestFile(t, filepath.Join(root, "packages", "good", "src", "i.ts"),
		`import x from "y"
`)

	diag := PreflightFixModuleType(root)
	joined := joinDiag(diag)
	if !strings.Contains(joined, "bad") || !strings.Contains(joined, "skipped") {
		t.Fatalf("expected 'skipped' diagnostic mentioning bad package, got %v", diag)
	}
	goodType, _ := readPkgType(t, good)
	if goodType != "module" {
		t.Fatalf("good package should still be rewritten alongside the malformed one, got %q", goodType)
	}
}

// Regression: a package whose only source is index.mjs must NOT be
// rewritten. .mjs is already explicitly ESM regardless of the
// package.json "type" field, so its presence is not evidence that
// type should be flipped.
func TestPreflightFixModuleType_SkipsWhenOnlySourceIsMjs(t *testing.T) {
	root := t.TempDir()
	pkg := filepath.Join(root, "package.json")
	orig := `{"name":"x"}`
	writeModuleTestFile(t, pkg, orig)
	writeModuleTestFile(t, filepath.Join(root, "index.mjs"),
		`import x from "y"
export default x
`)

	diag := PreflightFixModuleType(root)
	if len(diag) != 0 {
		t.Fatalf("expected no diagnostic (only .mjs source — already ESM by extension), got %v", diag)
	}
	got, _ := readPkgType(t, pkg)
	if got == "module" {
		t.Fatalf(`package was flipped to "type":"module" based on .mjs alone — unsafe`)
	}
	b, _ := os.ReadFile(pkg)
	if string(b) != orig {
		t.Fatalf("file mutated unexpectedly:\nwant %q\ngot  %q", orig, string(b))
	}
}

// Regression: same as above for .mts (TypeScript ESM extension).
func TestPreflightFixModuleType_SkipsWhenOnlySourceIsMts(t *testing.T) {
	root := t.TempDir()
	pkg := filepath.Join(root, "package.json")
	orig := `{"name":"x"}`
	writeModuleTestFile(t, pkg, orig)
	writeModuleTestFile(t, filepath.Join(root, "src", "index.mts"),
		`import x from "y"
export const a = 1
`)

	diag := PreflightFixModuleType(root)
	if len(diag) != 0 {
		t.Fatalf("expected no diagnostic (only .mts source — already ESM by extension), got %v", diag)
	}
	got, _ := readPkgType(t, pkg)
	if got == "module" {
		t.Fatalf(`package was flipped to "type":"module" based on .mts alone — unsafe`)
	}
	b, _ := os.ReadFile(pkg)
	if string(b) != orig {
		t.Fatalf("file mutated unexpectedly:\nwant %q\ngot  %q", orig, string(b))
	}
}

// Regression: a package whose only source is index.cts (explicitly
// CommonJS) must never be rewritten to "type":"module". .cts is
// always CJS regardless of the package-level type field.
func TestPreflightFixModuleType_SkipsWhenOnlySourceIsCts(t *testing.T) {
	root := t.TempDir()
	pkg := filepath.Join(root, "package.json")
	orig := `{"name":"x"}`
	writeModuleTestFile(t, pkg, orig)
	// .cts is CJS. We still throw some import-looking text at it to
	// ensure that even if someone accidentally matched it against
	// the ESM regex, the extension-filter alone keeps it out.
	writeModuleTestFile(t, filepath.Join(root, "src", "index.cts"),
		`import x = require("y")
export = x
`)

	diag := PreflightFixModuleType(root)
	if len(diag) != 0 {
		t.Fatalf("expected no diagnostic (only .cts source — explicitly CJS), got %v", diag)
	}
	got, _ := readPkgType(t, pkg)
	if got == "module" {
		t.Fatalf(`package with only .cts source was flipped to "type":"module" — unsafe`)
	}
	b, _ := os.ReadFile(pkg)
	if string(b) != orig {
		t.Fatalf("file mutated unexpectedly:\nwant %q\ngot  %q", orig, string(b))
	}
}

func TestPreflightFixModuleType_PureCJSSourceUntouched(t *testing.T) {
	root := t.TempDir()
	pkg := filepath.Join(root, "package.json")
	orig := `{"name":"x"}`
	writeModuleTestFile(t, pkg, orig)
	// Classic CJS require/module.exports — no import/export syntax.
	writeModuleTestFile(t, filepath.Join(root, "src", "i.js"),
		`const x = require("y")
module.exports = x
`)

	diag := PreflightFixModuleType(root)
	if len(diag) != 0 {
		t.Fatalf("expected no diagnostic for pure CJS, got %v", diag)
	}
	b, _ := os.ReadFile(pkg)
	if string(b) != orig {
		t.Fatalf("file mutated unexpectedly:\nwant %q\ngot  %q", orig, string(b))
	}
}
