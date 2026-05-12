package ideinstall

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectorFor_AllValid(t *testing.T) {
	for _, ide := range SupportedIDEs {
		d, err := DetectorFor(ide)
		if err != nil {
			t.Fatalf("DetectorFor(%q): %v", ide, err)
		}
		if d.IDE() != ide {
			t.Fatalf("d.IDE() = %q, want %q", d.IDE(), ide)
		}
	}
}

func TestDetectorFor_UnknownIDE(t *testing.T) {
	_, err := DetectorFor("nope")
	if err == nil {
		t.Fatal("expected error for unknown ide")
	}
	for _, ide := range SupportedIDEs {
		if !strings.Contains(err.Error(), ide) {
			t.Fatalf("error %q missing valid ide %q", err, ide)
		}
	}
}

func TestSupportsScope(t *testing.T) {
	// Cursor + VS Code support both; Windsurf + JetBrains are
	// global-only.
	for _, ide := range []string{IDECursor, IDEVSCode} {
		d, _ := DetectorFor(ide)
		if !d.SupportsScope(ScopeWorkspace) {
			t.Fatalf("%s should support workspace", ide)
		}
		if !d.SupportsScope(ScopeGlobal) {
			t.Fatalf("%s should support global", ide)
		}
	}
	for _, ide := range []string{IDEWindsurf, IDEJetBrains} {
		d, _ := DetectorFor(ide)
		if d.SupportsScope(ScopeWorkspace) {
			t.Fatalf("%s must reject workspace scope", ide)
		}
		if !d.SupportsScope(ScopeGlobal) {
			t.Fatalf("%s should support global", ide)
		}
	}
}

func TestDefaultScopeFor(t *testing.T) {
	cases := map[string]Scope{
		IDECursor:    ScopeWorkspace,
		IDEVSCode:    ScopeWorkspace,
		IDEWindsurf:  ScopeGlobal,
		IDEJetBrains: ScopeGlobal,
	}
	for ide, want := range cases {
		if got := DefaultScopeFor(ide); got != want {
			t.Fatalf("DefaultScopeFor(%q) = %q, want %q", ide, got, want)
		}
	}
}

func TestCursorPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	d := cursorDetector{}
	globalPath, _, err := d.DetectConfig(ScopeGlobal, "")
	if err != nil {
		t.Fatalf("global: %v", err)
	}
	if want := filepath.Join(home, ".cursor", "mcp.json"); globalPath != want {
		t.Fatalf("global = %q, want %q", globalPath, want)
	}
	ws := filepath.Join(home, "proj")
	wsPath, _, err := d.DetectConfig(ScopeWorkspace, ws)
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	if want := filepath.Join(ws, ".cursor", "mcp.json"); wsPath != want {
		t.Fatalf("workspace = %q, want %q", wsPath, want)
	}
}

func TestWindsurfPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	d := windsurfDetector{}
	p, _, err := d.DetectConfig(ScopeGlobal, "")
	if err != nil {
		t.Fatalf("global: %v", err)
	}
	if want := filepath.Join(home, ".codeium", "windsurf", "mcp_config.json"); p != want {
		t.Fatalf("global = %q, want %q", p, want)
	}
	if _, _, err := d.DetectConfig(ScopeWorkspace, ""); err == nil {
		t.Fatal("windsurf workspace must error")
	}
}

func TestJetBrainsVersionDetection_FallbackWhenEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	v, err := detectHighestJetBrainsVersion("IntelliJIdea")
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if v != "2026.1" {
		t.Fatalf("expected fallback 2026.1, got %q", v)
	}
}

func TestPadJetBrainsVersion(t *testing.T) {
	cases := map[string]string{
		"2026.1":  "2026.01",
		"2026.2":  "2026.02",
		"2026.10": "2026.10",
		"2025.3":  "2025.03",
	}
	for in, want := range cases {
		if got := padJetBrainsVersion(in); got != want {
			t.Fatalf("padJetBrainsVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidJetBrainsIDE(t *testing.T) {
	for _, ide := range ValidJetBrainsIDEs() {
		if !validJetBrainsIDE(ide) {
			t.Fatalf("%s should be valid", ide)
		}
	}
	if validJetBrainsIDE("Eclipse") {
		t.Fatal("Eclipse must not be valid")
	}
}
