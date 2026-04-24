package plan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreflightScriptRecursion_RemovesSelfCollision(t *testing.T) {
	dir := t.TempDir()
	pkgPath := filepath.Join(dir, "package.json")
	err := os.WriteFile(pkgPath, []byte(`{
  "name": "web",
  "scripts": {
    "build": "next build",
    "vitest": "vitest run",
    "test": "vitest run",
    "lint": "eslint ."
  }
}
`), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	diag := PreflightScriptRecursion(dir)
	if len(diag) == 0 {
		t.Fatalf("expected at least one diagnostic; got none")
	}
	joined := strings.Join(diag, " | ")
	if !strings.Contains(joined, "vitest") {
		t.Fatalf("expected vitest recursion detected: %s", joined)
	}

	b, _ := os.ReadFile(pkgPath)
	var pkg map[string]any
	_ = json.Unmarshal(b, &pkg)
	scripts, _ := pkg["scripts"].(map[string]any)
	if _, has := scripts["vitest"]; has {
		t.Fatalf("vitest recursive script still present: %v", scripts)
	}
	if _, has := scripts["test"]; !has {
		t.Fatalf("'test' script should remain — it aliases vitest but is not self-recursive")
	}
}

func TestPreflightScriptRecursion_LintSelfCollision(t *testing.T) {
	dir := t.TempDir()
	pkgPath := filepath.Join(dir, "package.json")
	os.WriteFile(pkgPath, []byte(`{
  "name": "pkg",
  "scripts": {
    "eslint": "eslint .",
    "prettier": "prettier --check .",
    "format": "prettier --write ."
  }
}
`), 0o600)
	diag := PreflightScriptRecursion(dir)
	joined := strings.Join(diag, " | ")
	if !strings.Contains(joined, "eslint") || !strings.Contains(joined, "prettier") {
		t.Fatalf("expected both eslint + prettier detected: %s", joined)
	}
	b, _ := os.ReadFile(pkgPath)
	var pkg map[string]any
	_ = json.Unmarshal(b, &pkg)
	scripts, _ := pkg["scripts"].(map[string]any)
	if _, has := scripts["eslint"]; has {
		t.Fatalf("eslint should have been removed")
	}
	if _, has := scripts["format"]; !has {
		t.Fatalf("format should remain (not self-recursive)")
	}
}

func TestPreflightScriptRecursion_NoChangesWhenSafe(t *testing.T) {
	dir := t.TempDir()
	pkgPath := filepath.Join(dir, "package.json")
	os.WriteFile(pkgPath, []byte(`{
  "name": "safe",
  "scripts": {
    "build": "next build",
    "test": "jest",
    "lint": "eslint ."
  }
}
`), 0o600)
	diag := PreflightScriptRecursion(dir)
	// "test": "jest" is NOT self-recursive even though jest is in the
	// risks set — the script name is "test", not "jest", so running
	// `pnpm test` resolves the test script which calls jest binary.
	for _, d := range diag {
		if strings.Contains(d, "test") {
			t.Fatalf("unexpected removal of 'test' script: %s", d)
		}
	}
}

func TestPreflightScriptRecursion_SkipsNodeModules(t *testing.T) {
	dir := t.TempDir()
	nm := filepath.Join(dir, "node_modules", "some-pkg")
	os.MkdirAll(nm, 0755)
	nmPkg := filepath.Join(nm, "package.json")
	os.WriteFile(nmPkg, []byte(`{
  "name": "some-pkg",
  "scripts": {"vitest": "vitest run"}
}
`), 0o600)
	diag := PreflightScriptRecursion(dir)
	if len(diag) != 0 {
		t.Fatalf("expected no diagnostics (node_modules should be skipped); got: %v", diag)
	}
}
