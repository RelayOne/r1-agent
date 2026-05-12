//go:build linux

package ideinstall

import (
	"path/filepath"
	"testing"
)

func TestVSCodeConfigPath_Linux(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := vscodeUserConfigPath()
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	want := filepath.Join(home, ".config", "Code", "User", "mcp.json")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestJetBrainsConfigRoot_Linux(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	roots, err := jetbrainsConfigRoots()
	if err != nil {
		t.Fatalf("roots: %v", err)
	}
	if len(roots) != 1 {
		t.Fatalf("len=%d, want 1", len(roots))
	}
	if roots[0] != filepath.Join(home, ".config", "JetBrains") {
		t.Fatalf("got %q", roots[0])
	}
	pluginsRoots, _ := jetbrainsPluginsRoots()
	if pluginsRoots[0] != filepath.Join(home, ".local", "share", "JetBrains") {
		t.Fatalf("plugins got %q", pluginsRoots[0])
	}
}
