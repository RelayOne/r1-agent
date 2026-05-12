package ideinstall

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestInstallVSCode_WorkspaceRootKeyIsServers(t *testing.T) {
	dir := t.TempDir()
	res, err := InstallVSCode(ScopeWorkspace, dir)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if res.Action != ActionAdded {
		t.Fatalf("Action = %q, want %q", res.Action, ActionAdded)
	}
	wantPath := filepath.Join(dir, ".vscode", "mcp.json")
	if res.Path != wantPath {
		t.Fatalf("Path = %q, want %q", res.Path, wantPath)
	}
	doc := readJSON(t, res.Path)
	if _, ok := doc["mcpServers"]; ok {
		t.Fatalf("VS Code config must not use mcpServers key")
	}
	root, ok := doc["servers"].(map[string]any)
	if !ok {
		t.Fatalf("servers key missing: %#v", doc)
	}
	r1, ok := root["r1"].(map[string]any)
	if !ok {
		t.Fatalf("r1 entry missing")
	}
	if r1["type"] != "stdio" {
		t.Fatalf("r1.type = %v, want stdio", r1["type"])
	}
}

func TestPrintVSCodeConfirmation_IncludesCopilotReminder(t *testing.T) {
	var buf bytes.Buffer
	PrintVSCodeConfirmation(&buf, Result{Path: "/x/.vscode/mcp.json", Action: ActionAdded})
	out := buf.String()
	if !bytes.Contains(buf.Bytes(), []byte("installed: r1")) {
		t.Fatalf("missing install line: %s", out)
	}
	if !bytes.Contains(buf.Bytes(), []byte("Copilot")) ||
		!bytes.Contains(buf.Bytes(), []byte("Agent")) {
		t.Fatalf("missing Copilot Agent reminder: %s", out)
	}
}

func TestInstallVSCode_DoesNotMigrateMcpServers(t *testing.T) {
	// Per spec §T5.1: a config using mcpServers is treated as if
	// `servers` is empty — installer adds `servers`; does NOT
	// migrate the existing entries.
	dir := t.TempDir()
	path := filepath.Join(dir, ".vscode", "mcp.json")
	writeJSON(t, path, map[string]any{
		"mcpServers": map[string]any{
			"github": map[string]any{"command": "github-mcp"},
		},
	})
	if _, err := InstallVSCode(ScopeWorkspace, dir); err != nil {
		t.Fatalf("install: %v", err)
	}
	doc := readJSON(t, path)
	// mcpServers should still be present untouched.
	mcpServers, _ := doc["mcpServers"].(map[string]any)
	if _, ok := mcpServers["github"]; !ok {
		t.Fatalf("mcpServers.github lost: %#v", mcpServers)
	}
	servers, _ := doc["servers"].(map[string]any)
	if _, ok := servers["r1"]; !ok {
		t.Fatalf("servers.r1 missing: %#v", servers)
	}
	if _, ok := servers["github"]; ok {
		t.Fatalf("installer must not migrate mcpServers entries into servers: %#v", servers)
	}
}
