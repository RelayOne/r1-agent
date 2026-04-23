package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"testing/fstest"
)

// TestCheckVendoredLibs_Present verifies the happy path: when the
// sentinel file exists under vendor/, the check returns true and emits
// an INFO line, not a WARNING.
func TestCheckVendoredLibs_Present(t *testing.T) {
	fs := fstest.MapFS{
		vendorSentinel: &fstest.MapFile{Data: []byte("// fake three.module.js for test")},
	}
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	present := checkVendoredLibs(fs, logger)
	if !present {
		t.Fatalf("expected present=true when sentinel exists")
	}
	out := buf.String()
	if !strings.Contains(out, "vendor bundle present") {
		t.Errorf("missing INFO line in log output:\n%s", out)
	}
	if strings.Contains(out, "level=WARN") {
		t.Errorf("unexpected WARN line when sentinel is present:\n%s", out)
	}
}

// TestCheckVendoredLibs_Missing verifies the WARNING path: when the
// sentinel is absent, the check returns false and the log contains the
// docs pointer so the operator knows how to remedy it.
func TestCheckVendoredLibs_Missing(t *testing.T) {
	fs := fstest.MapFS{
		// Other files present, but NOT the sentinel.
		"graph.html": &fstest.MapFile{Data: []byte("<html></html>")},
	}
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	present := checkVendoredLibs(fs, logger)
	if present {
		t.Fatalf("expected present=false when sentinel missing")
	}
	out := buf.String()
	if !strings.Contains(out, "level=WARN") {
		t.Errorf("expected WARN-level log line, got:\n%s", out)
	}
	if !strings.Contains(out, vendorSentinel) {
		t.Errorf("WARN line should name the missing file %q, got:\n%s", vendorSentinel, out)
	}
	if !strings.Contains(out, vendorDocsURL) {
		t.Errorf("WARN line should point to docs %q, got:\n%s", vendorDocsURL, out)
	}
}

// TestCheckVendoredLibs_NilFS covers the defensive branch where the
// caller passes a nil filesystem (e.g. a broken embed Sub call). It
// must not panic and must log a WARNING.
func TestCheckVendoredLibs_NilFS(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	present := checkVendoredLibs(nil, logger)
	if present {
		t.Fatalf("expected present=false for nil fs")
	}
	if !strings.Contains(buf.String(), "vendor check skipped") {
		t.Errorf("expected 'vendor check skipped' warning, got:\n%s", buf.String())
	}
}

// TestCheckVendoredLibs_NilLogger ensures the function tolerates a nil
// logger by falling back to slog.Default — callers from main.go wire a
// real logger, but tests / other callers may not.
func TestCheckVendoredLibs_NilLogger(t *testing.T) {
	fs := fstest.MapFS{
		vendorSentinel: &fstest.MapFile{Data: []byte("// fake content for test")},
	}
	// Should not panic.
	if !checkVendoredLibs(fs, nil) {
		t.Fatalf("expected present=true with nil logger")
	}
}

// TestVendorSentinelConstants is a light smoke check that the exported
// constants stay in sync with the README paths — if someone renames the
// sentinel but forgets to update ui/vendor/README.md, this catches the
// drift at test time.
func TestVendorSentinelConstants(t *testing.T) {
	if !strings.HasPrefix(vendorSentinel, "vendor/") {
		t.Errorf("vendorSentinel should live under vendor/, got %q", vendorSentinel)
	}
	if !strings.HasSuffix(vendorSentinel, ".js") {
		t.Errorf("vendorSentinel should be a .js file, got %q", vendorSentinel)
	}
	if !strings.HasSuffix(vendorDocsURL, "README.md") {
		t.Errorf("vendorDocsURL should point to a README.md, got %q", vendorDocsURL)
	}
}
