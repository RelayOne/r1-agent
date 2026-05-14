package ideinstall

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestInstallWindsurf_FreshHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	var stderr bytes.Buffer
	res, err := InstallWindsurf(&stderr)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if res.Action != ActionAdded {
		t.Fatalf("Action = %q, want %q", res.Action, ActionAdded)
	}
	wantPath := filepath.Join(home, ".codeium", "windsurf", "mcp_config.json")
	if res.Path != wantPath {
		t.Fatalf("Path = %q, want %q", res.Path, wantPath)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("did not exist")) {
		t.Fatalf("expected warning about missing .codeium dir, got: %s", stderr.String())
	}
}

func TestUninstallWindsurf_NotInstalled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if _, err := UninstallWindsurf(); err == nil {
		t.Fatal("expected error for missing config")
	}
}
