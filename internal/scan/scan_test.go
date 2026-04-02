package scan

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanTSIgnore(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "bad.ts"), []byte("// @ts-ignore\nconst x: any = 1;\n"), 0644)

	result, err := ScanFiles(dir, DefaultRules(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.FilesScanned != 1 {
		t.Errorf("scanned=%d", result.FilesScanned)
	}
	found := false
	for _, f := range result.Findings {
		if f.Rule == "no-ts-ignore" { found = true }
	}
	if !found {
		t.Error("expected no-ts-ignore finding")
	}
}

func TestScanAsAny(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "cast.ts"), []byte("const x = foo as any;\n"), 0644)

	result, _ := ScanFiles(dir, DefaultRules(), nil)
	found := false
	for _, f := range result.Findings {
		if f.Rule == "no-as-any" { found = true }
	}
	if !found {
		t.Error("expected no-as-any finding")
	}
}

func TestScanConsoleLog(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "debug.js"), []byte("console.log('debug');\n"), 0644)

	result, _ := ScanFiles(dir, DefaultRules(), nil)
	found := false
	for _, f := range result.Findings {
		if f.Rule == "no-console-log" { found = true }
	}
	if !found {
		t.Error("expected no-console-log finding")
	}
}

func TestScanTestOnly(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.ts"), []byte("it.only('should work', () => {});\n"), 0644)

	result, _ := ScanFiles(dir, DefaultRules(), nil)
	found := false
	for _, f := range result.Findings {
		if f.Rule == "no-test-only" { found = true }
	}
	if !found {
		t.Error("expected no-test-only finding")
	}
}

func TestScanHardcodedSecret(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.go"), []byte(`password := "supersecretpassword123"`+"\n"), 0644)

	result, _ := ScanFiles(dir, DefaultRules(), nil)
	found := false
	for _, f := range result.Findings {
		if f.Rule == "no-hardcoded-secret" { found = true }
	}
	if !found {
		t.Error("expected no-hardcoded-secret finding")
	}
}

func TestScanCleanFile(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "clean.go"), []byte("package main\n\nfunc main() {}\n"), 0644)

	result, _ := ScanFiles(dir, DefaultRules(), nil)
	if len(result.Findings) != 0 {
		t.Errorf("clean file should have 0 findings, got %d", len(result.Findings))
	}
}

func TestScanModifiedOnly(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "bad.ts"), []byte("// @ts-ignore\n"), 0644)
	os.WriteFile(filepath.Join(dir, "also_bad.ts"), []byte("// @ts-ignore\n"), 0644)

	// Only scan bad.ts
	result, _ := ScanFiles(dir, DefaultRules(), []string{"bad.ts"})
	if result.FilesScanned != 1 {
		t.Errorf("should only scan 1 file, scanned %d", result.FilesScanned)
	}
}

func TestScanPythonNoqa(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "bad.py"), []byte("x = 1  # noqa: E501\n"), 0644)

	result, _ := ScanFiles(dir, DefaultRules(), nil)
	found := false
	for _, f := range result.Findings {
		if f.Rule == "no-noqa" { found = true }
	}
	if !found {
		t.Error("expected no-noqa finding")
	}
}

func TestScanGoNolint(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "bad.go"), []byte("package main\n\nvar x = 1 // nolint:unused\n"), 0644)

	result, _ := ScanFiles(dir, DefaultRules(), nil)
	found := false
	for _, f := range result.Findings {
		if f.Rule == "no-nolint" { found = true }
	}
	if !found {
		t.Error("expected no-nolint finding")
	}
}

func TestHasBlocking(t *testing.T) {
	r := &ScanResult{Findings: []Finding{{Severity: "low"}}}
	if r.HasBlocking() {
		t.Error("low severity should not block")
	}
	r.Findings = append(r.Findings, Finding{Severity: "critical"})
	if !r.HasBlocking() {
		t.Error("critical severity should block")
	}
}

func TestScanSkipsNodeModules(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "node_modules", "pkg"), 0755)
	os.WriteFile(filepath.Join(dir, "node_modules", "pkg", "bad.js"), []byte("eval('x')\n"), 0644)
	os.WriteFile(filepath.Join(dir, "src.js"), []byte("var x = 1;\n"), 0644)

	result, _ := ScanFiles(dir, DefaultRules(), nil)
	for _, f := range result.Findings {
		if f.File == "node_modules/pkg/bad.js" {
			t.Error("should skip node_modules")
		}
	}
}
