//go:build windows

package ideinstall

import (
	"path/filepath"
	"testing"
)

func TestVSCodeConfigPath_Windows_AppDataPrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	appdata := filepath.Join(home, "AppData", "Roaming")
	t.Setenv("APPDATA", appdata)
	got, err := vscodeUserConfigPath()
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	want := filepath.Join(appdata, "Code", "User", "mcp.json")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestVSCodeConfigPath_Windows_FallsBackToUserProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", "")
	got, err := vscodeUserConfigPath()
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	want := filepath.Join(home, "AppData", "Roaming", "Code", "User", "mcp.json")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestJetBrainsConfigRoot_Windows(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	appdata := filepath.Join(home, "AppData", "Roaming")
	t.Setenv("APPDATA", appdata)
	roots, err := jetbrainsConfigRoots()
	if err != nil {
		t.Fatalf("roots: %v", err)
	}
	if roots[0] != filepath.Join(appdata, "JetBrains") {
		t.Fatalf("got %q", roots[0])
	}
}
