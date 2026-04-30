package scan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPathMarkerCorruption_DetectsLiteralPathContent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "apps", "shopify", "src", "logger.ts")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("@apps/shopify/src/logger.ts\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	findings, err := DefaultPathMarkerRule().ScanPathMarker(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d (%+v)", len(findings), findings)
	}
	if findings[0].Rule != "path-marker-corruption" {
		t.Errorf("rule = %q", findings[0].Rule)
	}
	if !strings.Contains(findings[0].File, "logger.ts") {
		t.Errorf("file = %q", findings[0].File)
	}
}

func TestPathMarkerCorruption_SuspiciouslyShort(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "packages", "core", "src", "stub.go")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("package core\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	findings, err := DefaultPathMarkerRule().ScanPathMarker(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].Rule != "suspiciously-short-source" {
		t.Fatalf("expected 1 short-source finding, got %+v", findings)
	}
}

func TestPathMarkerCorruption_NormalCodeIgnored(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "apps", "api", "src", "ok.ts")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("export const x = 1;\n", 20)
	if err := os.WriteFile(target, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	findings, err := DefaultPathMarkerRule().ScanPathMarker(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("expected 0 findings, got %+v", findings)
	}
}

func TestPathMarkerCorruption_VendoredFileSkipped(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "node_modules", "lodash", "tiny.js")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("@anything\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	findings, err := DefaultPathMarkerRule().ScanPathMarker(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("vendored file should be skipped, got %+v", findings)
	}
}

func TestPathMarkerCorruption_TestFileSkippedForShortSource(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "apps", "api", "src", "thing_test.go")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	findings, err := DefaultPathMarkerRule().ScanPathMarker(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("test files should not trigger short-source, got %+v", findings)
	}
}

func TestPathMarkerCorruption_ModifiedOnlyFilter(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "apps", "api", "ok.ts")
	bad := filepath.Join(dir, "apps", "api", "bad.ts")
	for _, p := range []string{good, bad} {
		os.MkdirAll(filepath.Dir(p), 0o755)
	}
	os.WriteFile(good, []byte(strings.Repeat("export const a = 1;\n", 10)), 0o644)
	os.WriteFile(bad, []byte("@apps/api/bad.ts\n"), 0o644)

	findings, err := DefaultPathMarkerRule().ScanPathMarker(dir, []string{"apps/api/bad.ts"})
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || !strings.Contains(findings[0].File, "bad.ts") {
		t.Fatalf("expected only bad.ts flagged, got %+v", findings)
	}
}
