package ideinstall

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallCursor_FreshGlobal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	res, err := InstallCursor(ScopeGlobal, "")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if res.Action != ActionAdded {
		t.Fatalf("Action = %q, want %q", res.Action, ActionAdded)
	}
	wantPath := filepath.Join(home, ".cursor", "mcp.json")
	if res.Path != wantPath {
		t.Fatalf("Path = %q, want %q", res.Path, wantPath)
	}
	doc := readJSON(t, res.Path)
	root, _ := doc["mcpServers"].(map[string]any)
	r1, ok := root["r1"].(map[string]any)
	if !ok {
		t.Fatalf("r1 entry missing")
	}
	if r1["command"] != "r1" {
		t.Fatalf("command = %v", r1["command"])
	}
}

func TestInstallCursor_PreservesGithubEntry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".cursor", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	existing := []byte(`{"mcpServers":{"github":{"command":"github-mcp"}}}`)
	if err := os.WriteFile(path, existing, 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res, err := InstallCursor(ScopeWorkspace, dir)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if res.Action != ActionAdded {
		t.Fatalf("Action = %q, want %q", res.Action, ActionAdded)
	}
	doc := readJSON(t, path)
	root, _ := doc["mcpServers"].(map[string]any)
	if len(root) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(root))
	}
}

func TestPrintCursorConfirmation_Variants(t *testing.T) {
	for _, action := range []Action{ActionAdded, ActionUpdated, ActionNoop} {
		var buf bytes.Buffer
		PrintCursorConfirmation(&buf, Result{Path: "/x/.cursor/mcp.json", Action: action})
		out := buf.String()
		if !bytes.Contains(buf.Bytes(), []byte("Cursor")) {
			t.Fatalf("[%s] missing IDE name: %s", action, out)
		}
	}
}
