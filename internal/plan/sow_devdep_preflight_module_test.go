package plan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFixtureFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readPkgType(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var pkg map[string]any
	if err := json.Unmarshal(b, &pkg); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	s, _ := pkg["type"].(string)
	return s
}

func TestModuleType_AddsWhenMissing(t *testing.T) {
	root := t.TempDir()
	pkgDir := filepath.Join(root, "packages", "types")
	pkgPath := filepath.Join(pkgDir, "package.json")
	writeFixtureFile(t, pkgPath, `{"name":"@app/types","version":"0.0.0"}`)
	writeFixtureFile(t, filepath.Join(pkgDir, "src", "index.ts"), "export const x = 1;\n")

	diag := PreflightFixModuleType(root)
	if len(diag) != 1 {
		t.Fatalf("expected 1 diag line, got %d: %v", len(diag), diag)
	}
	if !strings.Contains(diag[0], "packages/types/package.json") {
		t.Errorf("diag missing package path: %q", diag[0])
	}
	if !strings.Contains(diag[0], "was missing") {
		t.Errorf("diag should mention previous value 'missing': %q", diag[0])
	}
	if got := readPkgType(t, pkgPath); got != "module" {
		t.Errorf("type = %q, want %q", got, "module")
	}
}

func TestModuleType_UpgradesCommonJS(t *testing.T) {
	root := t.TempDir()
	pkgDir := filepath.Join(root, "packages", "utils")
	pkgPath := filepath.Join(pkgDir, "package.json")
	writeFixtureFile(t, pkgPath, `{"name":"@app/utils","type":"commonjs"}`)
	writeFixtureFile(t, filepath.Join(pkgDir, "src", "a.ts"), "import { z } from 'zod';\nexport const y = z;\n")

	diag := PreflightFixModuleType(root)
	if len(diag) != 1 {
		t.Fatalf("expected 1 diag line, got %d: %v", len(diag), diag)
	}
	if !strings.Contains(diag[0], `"commonjs"`) {
		t.Errorf("diag should mention previous commonjs value: %q", diag[0])
	}
	if got := readPkgType(t, pkgPath); got != "module" {
		t.Errorf("type = %q, want %q", got, "module")
	}
}

func TestModuleType_SkipsWhenAlreadyModule(t *testing.T) {
	root := t.TempDir()
	pkgDir := filepath.Join(root, "packages", "esm")
	pkgPath := filepath.Join(pkgDir, "package.json")
	original := `{"name":"@app/esm","type":"module"}`
	writeFixtureFile(t, pkgPath, original)
	writeFixtureFile(t, filepath.Join(pkgDir, "src", "i.ts"), "export const v = 1;\n")

	diag := PreflightFixModuleType(root)
	if len(diag) != 0 {
		t.Fatalf("expected no diag, got %v", diag)
	}
	b, _ := os.ReadFile(pkgPath)
	if string(b) != original {
		t.Errorf("package.json should be untouched, got %q", string(b))
	}
}

func TestModuleType_IgnoresNodeModules(t *testing.T) {
	root := t.TempDir()
	rootPkg := filepath.Join(root, "package.json")
	writeFixtureFile(t, rootPkg, `{"name":"root","private":true}`)
	// only file at the root level is the package.json itself, so the
	// fallback "scan root" should yield 0 ESM hits and not rewrite.
	nestedPkg := filepath.Join(root, "node_modules", "foo", "package.json")
	writeFixtureFile(t, nestedPkg, `{"name":"foo"}`)
	writeFixtureFile(t, filepath.Join(root, "node_modules", "foo", "index.ts"), "export const a = 1;\n")

	diag := PreflightFixModuleType(root)
	if len(diag) != 0 {
		t.Fatalf("expected no diag, got %v", diag)
	}
	if readPkgType(t, nestedPkg) != "" {
		t.Errorf("nested node_modules package.json was modified")
	}
	if readPkgType(t, rootPkg) != "" {
		t.Errorf("root package.json was unexpectedly modified")
	}
}

func TestModuleType_NoESMSyntax(t *testing.T) {
	root := t.TempDir()
	pkgDir := filepath.Join(root, "packages", "cjs")
	pkgPath := filepath.Join(pkgDir, "package.json")
	original := `{"name":"@app/cjs"}`
	writeFixtureFile(t, pkgPath, original)
	writeFixtureFile(t, filepath.Join(pkgDir, "src", "x.js"), "const z = require('zod');\nmodule.exports = z;\n")

	diag := PreflightFixModuleType(root)
	if len(diag) != 0 {
		t.Fatalf("expected no diag for pure-CJS, got %v", diag)
	}
	b, _ := os.ReadFile(pkgPath)
	if string(b) != original {
		t.Errorf("package.json should be untouched, got %q", string(b))
	}
}

func TestModuleType_IgnoresNestedDist(t *testing.T) {
	root := t.TempDir()
	pkgDir := filepath.Join(root, "packages", "lib")
	pkgPath := filepath.Join(pkgDir, "package.json")
	original := `{"name":"@app/lib"}`
	writeFixtureFile(t, pkgPath, original)
	// no src/, so fallback scans pkg root. dist/ contains generated
	// ESM that must NOT trigger a rewrite.
	writeFixtureFile(t, filepath.Join(pkgDir, "dist", "out.js"), "export const generated = 1;\n")
	writeFixtureFile(t, filepath.Join(pkgDir, "build", "out.js"), "export const generated = 2;\n")

	diag := PreflightFixModuleType(root)
	if len(diag) != 0 {
		t.Fatalf("expected no diag (dist/build should be skipped), got %v", diag)
	}
	b, _ := os.ReadFile(pkgPath)
	if string(b) != original {
		t.Errorf("package.json should be untouched, got %q", string(b))
	}
}

func TestModuleType_FallbackScansPackageRoot(t *testing.T) {
	root := t.TempDir()
	pkgDir := filepath.Join(root, "packages", "flat")
	pkgPath := filepath.Join(pkgDir, "package.json")
	writeFixtureFile(t, pkgPath, `{"name":"@app/flat"}`)
	// no src/ — the file lives directly at the package root.
	writeFixtureFile(t, filepath.Join(pkgDir, "index.ts"), "export const flat = 1;\n")

	diag := PreflightFixModuleType(root)
	if len(diag) != 1 {
		t.Fatalf("expected 1 diag for fallback root scan, got %v", diag)
	}
	if got := readPkgType(t, pkgPath); got != "module" {
		t.Errorf("type = %q, want %q", got, "module")
	}
}

func TestModuleType_MalformedJSONNonFatal(t *testing.T) {
	root := t.TempDir()
	bad := filepath.Join(root, "packages", "broken", "package.json")
	writeFixtureFile(t, bad, `{not json`)
	writeFixtureFile(t, filepath.Join(root, "packages", "broken", "src", "i.ts"), "export const v = 1;\n")

	good := filepath.Join(root, "packages", "ok", "package.json")
	writeFixtureFile(t, good, `{"name":"ok"}`)
	writeFixtureFile(t, filepath.Join(root, "packages", "ok", "src", "i.ts"), "export const v = 1;\n")

	diag := PreflightFixModuleType(root)
	if len(diag) != 1 {
		t.Fatalf("expected 1 diag (broken skipped, ok fixed), got %v", diag)
	}
	if !strings.Contains(diag[0], "packages/ok/package.json") {
		t.Errorf("expected diag for ok package, got %q", diag[0])
	}
	if got := readPkgType(t, good); got != "module" {
		t.Errorf("ok package type = %q, want %q", got, "module")
	}
}

func TestModuleType_MJSAloneDoesNotTrigger(t *testing.T) {
	root := t.TempDir()
	pkgDir := filepath.Join(root, "packages", "mjsonly")
	pkgPath := filepath.Join(pkgDir, "package.json")
	original := `{"name":"@app/mjsonly"}`
	writeFixtureFile(t, pkgPath, original)
	// .mjs files are intrinsically ESM regardless of package.json
	// type. Their presence alone must NOT promote sibling .js files
	// to ESM.
	writeFixtureFile(t, filepath.Join(pkgDir, "src", "esm.mjs"), "export const m = 1;\n")
	// One CJS .js file — but that isn't ESM syntax either.
	writeFixtureFile(t, filepath.Join(pkgDir, "src", "cjs.js"), "module.exports = 1;\n")

	diag := PreflightFixModuleType(root)
	if len(diag) != 0 {
		t.Fatalf("expected no diag for .mjs-only package, got %v", diag)
	}
	b, _ := os.ReadFile(pkgPath)
	if string(b) != original {
		t.Errorf("package.json should be untouched, got %q", string(b))
	}
}

func TestModuleType_MultiPackageOnlyAffectedRewritten(t *testing.T) {
	root := t.TempDir()
	// Package A: needs the fix.
	aPath := filepath.Join(root, "packages", "a", "package.json")
	writeFixtureFile(t, aPath, `{"name":"a"}`)
	writeFixtureFile(t, filepath.Join(root, "packages", "a", "src", "i.ts"), "export const a = 1;\n")
	// Package B: pure CJS, must remain untouched.
	bPath := filepath.Join(root, "packages", "b", "package.json")
	bOriginal := `{"name":"b"}`
	writeFixtureFile(t, bPath, bOriginal)
	writeFixtureFile(t, filepath.Join(root, "packages", "b", "src", "i.js"), "module.exports = 1;\n")
	// Package C: already module.
	cPath := filepath.Join(root, "packages", "c", "package.json")
	cOriginal := `{"name":"c","type":"module"}`
	writeFixtureFile(t, cPath, cOriginal)
	writeFixtureFile(t, filepath.Join(root, "packages", "c", "src", "i.ts"), "export const c = 1;\n")

	diag := PreflightFixModuleType(root)
	if len(diag) != 1 {
		t.Fatalf("expected exactly 1 diag (only package a), got %v", diag)
	}
	if !strings.Contains(diag[0], "packages/a/package.json") {
		t.Errorf("expected diag for package a, got %q", diag[0])
	}
	if got := readPkgType(t, aPath); got != "module" {
		t.Errorf("a type = %q, want module", got)
	}
	if b, _ := os.ReadFile(bPath); string(b) != bOriginal {
		t.Errorf("package b unexpectedly modified: %q", string(b))
	}
	if b, _ := os.ReadFile(cPath); string(b) != cOriginal {
		t.Errorf("package c unexpectedly modified: %q", string(b))
	}
}
