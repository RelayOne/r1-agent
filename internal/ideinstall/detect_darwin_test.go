//go:build darwin

package ideinstall

import (
	"path/filepath"
	"testing"
)

func TestVSCodeConfigPath_Darwin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := vscodeUserConfigPath()
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	want := filepath.Join(home, "Library", "Application Support", "Code", "User", "mcp.json")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestJetBrainsConfigRoot_Darwin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	roots, err := jetbrainsConfigRoots()
	if err != nil {
		t.Fatalf("roots: %v", err)
	}
	if roots[0] != filepath.Join(home, "Library", "Application Support", "JetBrains") {
		t.Fatalf("got %q", roots[0])
	}
}
