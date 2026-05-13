package ideinstall

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMerge_AddsToEmptyDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".cursor", "mcp.json")

	entry := map[string]any{
		"command": "r1",
		"args":    []any{"mcp", "serve"},
		"env":     map[string]any{},
	}
	res, err := Merge(MergeOpts{
		Path:     path,
		RootKey:  "mcpServers",
		EntryKey: "r1",
		Entry:    entry,
	})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if res.Action != ActionAdded {
		t.Fatalf("Action = %q, want %q", res.Action, ActionAdded)
	}
	doc := readJSON(t, path)
	root, ok := doc["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("mcpServers not a map: %#v", doc["mcpServers"])
	}
	if _, ok := root["r1"]; !ok {
		t.Fatalf("r1 entry missing: %#v", root)
	}
	if _, err := os.Stat(path + BackupSuffix); err == nil {
		t.Fatalf("backup written for fresh install: %s", path+BackupSuffix)
	}
}

func TestMerge_PreservesUnrelatedEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	existing := map[string]any{
		"mcpServers": map[string]any{
			"github": map[string]any{
				"command": "github-mcp",
				"args":    []any{},
			},
		},
	}
	writeJSON(t, path, existing)

	_, err := Merge(MergeOpts{
		Path:     path,
		RootKey:  "mcpServers",
		EntryKey: "r1",
		Entry: map[string]any{
			"command": "r1",
			"args":    []any{"mcp", "serve"},
			"env":     map[string]any{},
		},
	})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	doc := readJSON(t, path)
	root, _ := doc["mcpServers"].(map[string]any)
	if len(root) != 2 {
		t.Fatalf("expected 2 servers, got %d: %#v", len(root), root)
	}
	if _, ok := root["github"]; !ok {
		t.Fatalf("github entry lost: %#v", root)
	}
	if _, ok := root["r1"]; !ok {
		t.Fatalf("r1 entry not added: %#v", root)
	}
	// Backup must exist now that we mutated a real file.
	if _, err := os.Stat(path + BackupSuffix); err != nil {
		t.Fatalf("backup not written: %v", err)
	}
}

func TestMerge_NoopWhenIdentical(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	entry := map[string]any{
		"command": "r1",
		"args":    []any{"mcp", "serve"},
		"env":     map[string]any{},
	}
	// Seed the file already-correctly-installed.
	doc := map[string]any{"mcpServers": map[string]any{"r1": entry}}
	writeJSON(t, path, doc)
	preStat, _ := os.Stat(path)

	res, err := Merge(MergeOpts{
		Path:     path,
		RootKey:  "mcpServers",
		EntryKey: "r1",
		Entry:    entry,
	})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if res.Action != ActionNoop {
		t.Fatalf("Action = %q, want %q", res.Action, ActionNoop)
	}
	if _, err := os.Stat(path + BackupSuffix); err == nil {
		t.Fatalf("noop must not write backup")
	}
	postStat, _ := os.Stat(path)
	if !preStat.ModTime().Equal(postStat.ModTime()) {
		t.Fatalf("noop must not change mtime")
	}
}

func TestMerge_UpdateWritesBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	original := map[string]any{
		"mcpServers": map[string]any{
			"r1": map[string]any{"command": "old-r1", "args": []any{}, "env": map[string]any{}},
		},
	}
	writeJSON(t, path, original)
	origBytes, _ := os.ReadFile(path)

	res, err := Merge(MergeOpts{
		Path:     path,
		RootKey:  "mcpServers",
		EntryKey: "r1",
		Entry: map[string]any{
			"command": "r1",
			"args":    []any{"mcp", "serve"},
			"env":     map[string]any{},
		},
	})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if res.Action != ActionUpdated {
		t.Fatalf("Action = %q, want %q", res.Action, ActionUpdated)
	}
	backupBytes, err := os.ReadFile(path + BackupSuffix)
	if err != nil {
		t.Fatalf("backup not written: %v", err)
	}
	if !reflect.DeepEqual(origBytes, backupBytes) {
		t.Fatalf("backup bytes do not match original (orig=%d backup=%d)",
			len(origBytes), len(backupBytes))
	}
}

func TestUninstallJSON_RestoresBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	original := []byte(`{"mcpServers":{"github":{"command":"github-mcp"}}}`)
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := Merge(MergeOpts{
		Path:     path,
		RootKey:  "mcpServers",
		EntryKey: "r1",
		Entry:    map[string]any{"command": "r1", "args": []any{"mcp", "serve"}, "env": map[string]any{}},
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	res, err := UninstallJSON("cursor", path, "mcpServers", "r1")
	if err != nil {
		t.Fatalf("UninstallJSON: %v", err)
	}
	if res.Action != ActionRestored {
		t.Fatalf("Action = %q, want %q", res.Action, ActionRestored)
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(original) {
		t.Fatalf("restore mismatch:\n  want: %s\n  got:  %s", original, got)
	}
}

func TestUninstallJSON_NotInstalled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	if _, err := UninstallJSON("cursor", path, "mcpServers", "r1"); err == nil {
		t.Fatalf("expected error for missing config")
	}
}

func TestUninstallJSON_RemovesKeyNoBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	doc := map[string]any{
		"mcpServers": map[string]any{
			"r1":     map[string]any{"command": "r1", "args": []any{"mcp", "serve"}, "env": map[string]any{}},
			"github": map[string]any{"command": "github-mcp"},
		},
	}
	writeJSON(t, path, doc)
	res, err := UninstallJSON("cursor", path, "mcpServers", "r1")
	if err != nil {
		t.Fatalf("UninstallJSON: %v", err)
	}
	if res.Action != ActionRemoved {
		t.Fatalf("Action = %q, want %q", res.Action, ActionRemoved)
	}
	got := readJSON(t, path)
	root, _ := got["mcpServers"].(map[string]any)
	if _, ok := root["r1"]; ok {
		t.Fatalf("r1 entry not removed: %#v", root)
	}
	if _, ok := root["github"]; !ok {
		t.Fatalf("unrelated entry lost: %#v", root)
	}
}

func TestInstallUninstallRoundTrip(t *testing.T) {
	// Per spec §T10.2: seed a config, install + uninstall, then
	// confirm JSON-canonical byte identity with the original.
	dir := t.TempDir()
	cases := []struct {
		name     string
		path     string
		rootKey  string
		entryKey string
		entry    map[string]any
	}{
		{
			name:     "cursor",
			path:     filepath.Join(dir, "cursor", "mcp.json"),
			rootKey:  "mcpServers",
			entryKey: "r1",
			entry:    r1EntryCursor(),
		},
		{
			name:     "windsurf",
			path:     filepath.Join(dir, "windsurf", "mcp.json"),
			rootKey:  "mcpServers",
			entryKey: "r1",
			entry:    r1EntryWindsurf(),
		},
		{
			name:     "vscode",
			path:     filepath.Join(dir, "vscode", "mcp.json"),
			rootKey:  "servers",
			entryKey: "r1",
			entry:    r1EntryVSCode(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.MkdirAll(filepath.Dir(tc.path), 0o755); err != nil {
				t.Fatalf("seed dir: %v", err)
			}
			original := map[string]any{
				tc.rootKey: map[string]any{
					"github": map[string]any{"command": "github-mcp"},
				},
			}
			writeJSON(t, tc.path, original)
			origCanonical := canonicalJSON(t, tc.path)
			if _, err := Merge(MergeOpts{
				Path:     tc.path,
				RootKey:  tc.rootKey,
				EntryKey: tc.entryKey,
				Entry:    tc.entry,
			}); err != nil {
				t.Fatalf("install: %v", err)
			}
			if _, err := UninstallJSON(tc.name, tc.path, tc.rootKey, tc.entryKey); err != nil {
				t.Fatalf("uninstall: %v", err)
			}
			gotCanonical := canonicalJSON(t, tc.path)
			if origCanonical != gotCanonical {
				t.Fatalf("round-trip diff:\n  orig: %s\n  got:  %s", origCanonical, gotCanonical)
			}
		})
	}
}

func TestIsRegistered(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	if reg, err := IsRegistered(path, "mcpServers", "r1"); err != nil || reg {
		t.Fatalf("missing file: reg=%v err=%v", reg, err)
	}
	doc := map[string]any{"mcpServers": map[string]any{"r1": map[string]any{}}}
	writeJSON(t, path, doc)
	if reg, err := IsRegistered(path, "mcpServers", "r1"); err != nil || !reg {
		t.Fatalf("registered: reg=%v err=%v", reg, err)
	}
}

// --- helpers ------------------------------------------------------

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("parse: %v", err)
	}
	return out
}

func canonicalJSON(t *testing.T, path string) string {
	t.Helper()
	doc := readJSON(t, path)
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("canonicalise: %v", err)
	}
	return string(b)
}
