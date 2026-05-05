package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeCatalogFixture writes a minimal r1.* catalog JSON to dir/cat.json
// containing the supplied tool names. Returns the path.
func writeCatalogFixture(t *testing.T, dir string, names []string) string {
	t.Helper()
	type tool struct {
		Name string `json:"name"`
	}
	out := make([]tool, len(names))
	for i, n := range names {
		out[i] = tool{Name: n}
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "cat.json")
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// writeAllowlistFixture writes a minimal allowlist YAML.
func writeAllowlistFixture(t *testing.T, dir string, headlessOnly map[string]string) string {
	t.Helper()
	var sb strings.Builder
	sb.WriteString("headless_only:\n")
	for name, just := range headlessOnly {
		sb.WriteString("  ")
		sb.WriteString(name)
		sb.WriteString(": \"")
		sb.WriteString(just)
		sb.WriteString("\"\n")
	}
	p := filepath.Join(dir, "allow.yaml")
	if err := os.WriteFile(p, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadCatalog_ReadsJSONFile(t *testing.T) {
	dir := t.TempDir()
	p := writeCatalogFixture(t, dir, []string{"r1.session.start", "r1.lanes.kill"})
	got, err := loadCatalog(p)
	if err != nil {
		t.Fatalf("loadCatalog: %v", err)
	}
	if !got["r1.session.start"] || !got["r1.lanes.kill"] {
		t.Errorf("missing expected names: %+v", got)
	}
}

func TestLoadCatalog_RejectsEmpty(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(p, []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := loadCatalog(p)
	if err == nil {
		t.Error("empty catalog should error")
	}
}

func TestLoadAllowlist_ParsesEntries(t *testing.T) {
	dir := t.TempDir()
	p := writeAllowlistFixture(t, dir, map[string]string{
		"r1.cli.invoke": "headless-only by definition",
	})
	got, err := loadAllowlist(p)
	if err != nil {
		t.Fatalf("loadAllowlist: %v", err)
	}
	if got.HeadlessOnly["r1.cli.invoke"] == "" {
		t.Errorf("entry not parsed: %+v", got.HeadlessOnly)
	}
}

func TestScanReact_PositiveBlock(t *testing.T) {
	// Construct a fake web/src/Foo.tsx with a button that has BOTH
	// data-testid AND mcp_tool referencing a known catalog entry.
	dir := t.TempDir()
	src := filepath.Join(dir, "web", "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	fixture := `import React from 'react';

export function Foo() {
  return (
    <button
      onClick={() => doKill()}
      data-testid="kill-lane-x"
      data-mcp_tool="r1.lanes.kill"
    >Kill</button>
  );
}
`
	if err := os.WriteFile(filepath.Join(src, "Foo.tsx"), []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog := map[string]bool{"r1.lanes.kill": true}
	findings := scanReact(dir, catalog)
	for _, f := range findings {
		if f.Severity == "FAIL" {
			t.Errorf("did not expect FAIL: %+v", f)
		}
	}
}

func TestScanReact_FailsWithoutDataTestid(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "web", "src")
	_ = os.MkdirAll(src, 0o755)
	fixture := `export function Bar() {
  return <button onClick={() => x()}>Click</button>;
}
`
	if err := os.WriteFile(filepath.Join(src, "Bar.tsx"), []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog := map[string]bool{"r1.lanes.kill": true}
	findings := scanReact(dir, catalog)
	gotFail := false
	for _, f := range findings {
		if f.Severity == "FAIL" {
			gotFail = true
		}
	}
	if !gotFail {
		t.Error("expected FAIL for button without data-testid")
	}
}

func TestScanReact_FailsForUnknownMCPTool(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "web", "src")
	_ = os.MkdirAll(src, 0o755)
	fixture := `export function Baz() {
  return (
    <button
      onClick={() => x()}
      data-testid="baz"
      data-mcp_tool="r1.does.not.exist"
    >Click</button>
  );
}
`
	if err := os.WriteFile(filepath.Join(src, "Baz.tsx"), []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog := map[string]bool{"r1.lanes.kill": true}
	findings := scanReact(dir, catalog)
	got := false
	for _, f := range findings {
		if f.Severity == "FAIL" && strings.Contains(f.Message, "unknown MCP tool") {
			got = true
		}
	}
	if !got {
		t.Error("expected FAIL for unknown MCP tool reference")
	}
}

func TestScanReact_BlockedWhenWebSrcMissing(t *testing.T) {
	dir := t.TempDir() // no web/src/ exists
	findings := scanReact(dir, map[string]bool{})
	got := false
	for _, f := range findings {
		if f.Severity == "INFO" && strings.Contains(f.Message, "BLOCKED") {
			got = true
		}
	}
	if !got {
		t.Error("expected STATUS: BLOCKED INFO finding when web/src/ is missing")
	}
}

func TestScanBubbleTea_FailsWhenA11yMissing(t *testing.T) {
	dir := t.TempDir()
	tui := filepath.Join(dir, "internal", "tui")
	_ = os.MkdirAll(tui, 0o755)
	fixture := `package tui

import tea "github.com/charmbracelet/bubbletea"

type model struct{}

func (m model) ` + `Init` + `() tea.Cmd { return nil }
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    if _, ok := msg.(tea.KeyMsg); ok { return m, nil }
    return m, nil
}
func (m model) View() string { return "" }
`
	if err := os.WriteFile(filepath.Join(tui, "bad.go"), []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	findings := scanBubbleTea(dir, map[string]bool{})
	got := false
	for _, f := range findings {
		if f.Severity == "FAIL" {
			got = true
		}
	}
	if !got {
		t.Error("expected FAIL for tea.KeyMsg model without A11yEmitter")
	}
}

func TestScanBubbleTea_PassesWithA11yImpl(t *testing.T) {
	dir := t.TempDir()
	tui := filepath.Join(dir, "internal", "tui")
	_ = os.MkdirAll(tui, 0o755)
	fixture := `package tui

import tea "github.com/charmbracelet/bubbletea"

type model struct{}
func (m model) ` + `Init` + `() tea.Cmd { return nil }
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    if _, ok := msg.(tea.KeyMsg); ok { return m, nil }
    return m, nil
}
func (m model) View() string { return "" }
func (m model) StableID() string { return "demo" }
func (m model) A11y() A11yNode { return A11yNode{} }
`
	if err := os.WriteFile(filepath.Join(tui, "good.go"), []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	findings := scanBubbleTea(dir, map[string]bool{})
	for _, f := range findings {
		if f.Severity == "FAIL" {
			t.Errorf("did not expect FAIL: %+v", f)
		}
	}
}

func TestScanUnusedTools_AllowlistSilencesWarning(t *testing.T) {
	dir := t.TempDir()
	catalog := map[string]bool{
		"r1.session.start": true,
		"r1.cli.invoke":    true,
	}
	allow := &Allowlist{HeadlessOnly: map[string]string{"r1.cli.invoke": "headless-only"}}
	findings := scanUnusedTools(dir, catalog, allow)
	for _, f := range findings {
		if f.Tool == "r1.cli.invoke" {
			t.Errorf("allowlisted tool should not produce a finding: %+v", f)
		}
	}
}

func TestScanUnusedTools_WarnsForReferencedCategories(t *testing.T) {
	dir := t.TempDir()
	// No UI surface dirs exist; r1.session.start has no reference.
	catalog := map[string]bool{"r1.session.start": true}
	findings := scanUnusedTools(dir, catalog, &Allowlist{})
	got := false
	for _, f := range findings {
		if f.Tool == "r1.session.start" && f.Severity == "WARN" {
			got = true
		}
	}
	if !got {
		t.Error("expected WARN for unreferenced UI-category tool")
	}
}

func TestRun_ExitsZeroWithNoFindings(t *testing.T) {
	dir := t.TempDir()
	cat := writeCatalogFixture(t, dir, []string{"r1.cli.invoke"})
	allow := writeAllowlistFixture(t, dir, map[string]string{
		"r1.cli.invoke": "headless-only",
	})
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--root", dir,
		"--catalog", cat,
		"--allowlist", allow,
	}, &stdout, &stderr)
	if code != 0 {
		t.Errorf("expected exit 0; got %d. stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func TestRun_JSONModeEmitsArray(t *testing.T) {
	dir := t.TempDir()
	cat := writeCatalogFixture(t, dir, []string{"r1.cli.invoke"})
	allow := writeAllowlistFixture(t, dir, map[string]string{"r1.cli.invoke": "h"})
	var stdout, stderr bytes.Buffer
	_ = run([]string{
		"--root", dir,
		"--catalog", cat,
		"--allowlist", allow,
		"--json",
	}, &stdout, &stderr)
	out := stdout.Bytes()
	if len(out) == 0 {
		t.Fatal("JSON mode should produce output")
	}
	var got []Finding
	if err := json.Unmarshal(out, &got); err != nil {
		t.Errorf("output is not a JSON array of Finding: %v\nout=%s", err, out)
	}
}
