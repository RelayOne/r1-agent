package ideinstall

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallJetBrains_UnknownIDE(t *testing.T) {
	_, err := InstallJetBrains(InstallJetBrainsOpts{IDE: "Eclipse"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "Eclipse") {
		t.Fatalf("error missing IDE name: %v", err)
	}
	for _, v := range ValidJetBrainsIDEs() {
		if !strings.Contains(err.Error(), v) {
			t.Fatalf("error missing valid IDE %s: %v", v, err)
		}
	}
}

func TestInstallJetBrains_JarOverrideCopy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))

	payload := []byte("fake-jar-content")
	res, err := InstallJetBrains(InstallJetBrainsOpts{
		IDE:         "IntelliJIdea",
		Version:     "2026.1",
		JarOverride: payload,
	})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if res.Action != ActionAdded {
		t.Fatalf("Action = %q, want %q", res.Action, ActionAdded)
	}
	got, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatalf("read jar: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("jar bytes mismatch")
	}
}

func TestInstallJetBrains_SkipDownloadWhenMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	// Ensure no jar at the env-var override path.
	t.Setenv("R1_JETBRAINS_JAR", "")

	// Run from a sub-directory so the developer-checkout path
	// search (ide/jetbrains/build/distributions) misses.
	wd := t.TempDir()
	prev, _ := os.Getwd()
	defer func() { _ = os.Chdir(prev) }()
	_ = os.Chdir(wd)

	_, err := InstallJetBrains(InstallJetBrainsOpts{
		IDE:          "IntelliJIdea",
		Version:      "2026.1",
		SkipDownload: true,
	})
	if err == nil {
		t.Fatal("expected error when jar absent and download disabled")
	}
}

func TestUninstallJetBrains_NotInstalled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	_, err := UninstallJetBrains("IntelliJIdea", "2026.1")
	if err == nil {
		t.Fatal("expected error for missing jar")
	}
	if !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("expected 'not installed' in error, got: %v", err)
	}
}

func TestUninstallJetBrains_RemovesJar(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	// Install via override, then uninstall.
	if _, err := InstallJetBrains(InstallJetBrainsOpts{
		IDE:         "IntelliJIdea",
		Version:     "2026.1",
		JarOverride: []byte("x"),
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	res, err := UninstallJetBrains("IntelliJIdea", "2026.1")
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if res.Action != ActionRemoved {
		t.Fatalf("Action = %q, want %q", res.Action, ActionRemoved)
	}
	if _, err := os.Stat(res.Path); !os.IsNotExist(err) {
		t.Fatalf("jar still present: %v", err)
	}
}

func TestJetBrainsPluginsDir_VersionAutoDetect(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	// Seed two versions; expect the highest to win.
	configRoots, _ := jetbrainsConfigRoots()
	for _, ver := range []string{"2026.1", "2026.2"} {
		if err := os.MkdirAll(filepath.Join(configRoots[0], "IntelliJIdea"+ver), 0o755); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	dir, version, err := JetBrainsPluginsDir("IntelliJIdea", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if version != "2026.2" {
		t.Fatalf("version = %q, want 2026.2", version)
	}
	if !strings.Contains(dir, "IntelliJIdea2026.2") {
		t.Fatalf("dir = %q does not include version", dir)
	}
	// On Linux the plugins root differs from the config root; the
	// other OSes use the same root for both. Sanity check that the
	// per-OS layout was applied.
	if runtime.GOOS == "linux" {
		if !strings.Contains(dir, ".local/share/JetBrains") {
			t.Fatalf("linux plugins dir mismatch: %q", dir)
		}
	}
}

func TestVerify_ReportsMixedState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	// Seed Cursor as registered (global), Windsurf parent missing,
	// VS Code parent present but unregistered, JetBrains jar absent.
	cursorPath := filepath.Join(home, ".cursor", "mcp.json")
	writeJSON(t, cursorPath, map[string]any{
		"mcpServers": map[string]any{"r1": r1EntryCursor()},
	})
	// VS Code parent exists but file empty/missing.
	switch runtime.GOOS {
	case "darwin":
		_ = os.MkdirAll(filepath.Join(home, "Library", "Application Support", "Code", "User"), 0o755)
	case "windows":
		_ = os.MkdirAll(filepath.Join(home, "AppData", "Roaming", "Code", "User"), 0o755)
	default:
		_ = os.MkdirAll(filepath.Join(home, ".config", "Code", "User"), 0o755)
	}

	rows := Verify()
	if len(rows) != len(SupportedIDEs) {
		t.Fatalf("rows = %d, want %d", len(rows), len(SupportedIDEs))
	}
	by := map[string]VerifyResult{}
	for _, r := range rows {
		by[r.IDE] = r
	}
	if by[IDECursor].Status != VerifyStatusRegistered {
		t.Fatalf("cursor status = %q, want registered", by[IDECursor].Status)
	}
	if by[IDEWindsurf].Status != VerifyStatusNotFound {
		t.Fatalf("windsurf status = %q, want not-found", by[IDEWindsurf].Status)
	}
	if by[IDEVSCode].Status != VerifyStatusUnregistered {
		t.Fatalf("vscode status = %q, want unregistered", by[IDEVSCode].Status)
	}
	if by[IDEJetBrains].Status == VerifyStatusRegistered {
		t.Fatalf("jetbrains must not be registered (no jar seeded)")
	}
}
