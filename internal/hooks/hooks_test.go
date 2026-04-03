package hooks

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallCreatesHookFiles(t *testing.T) {
	dir := t.TempDir()
	if err := Install(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pre-tool-use.sh", "post-tool-use.sh"} {
		path := filepath.Join(dir, "hooks", name)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("hook %s missing: %v", name, err)
		}
		if info.Mode()&0111 == 0 {
			t.Errorf("hook %s not executable", name)
		}
	}
}

func TestHooksConfig(t *testing.T) {
	cfg := HooksConfig("/tmp/runtime")
	hooksRaw, ok := cfg["hooks"]
	if !ok {
		t.Fatal("no hooks key")
	}
	hooks, ok := hooksRaw.(map[string]interface{})
	if !ok {
		t.Fatalf("hooks is %T, expected map[string]interface{}", hooksRaw)
	}
	pre, ok := hooks["PreToolUse"]
	if !ok {
		t.Fatal("no PreToolUse")
	}
	arr, ok := pre.([]interface{})
	if !ok || len(arr) == 0 {
		t.Fatalf("PreToolUse should have entries, got %T", pre)
	}
	first, ok := arr[0].(map[string]interface{})
	if !ok {
		t.Fatalf("first entry should be map, got %T", arr[0])
	}
	if first["type"] != "command" {
		t.Error("hook type should be command")
	}
}

func TestCleanup(t *testing.T) {
	dir := t.TempDir()
	Install(dir)
	Cleanup(dir)
	_, err := os.Stat(filepath.Join(dir, "hooks"))
	if !os.IsNotExist(err) {
		t.Error("hooks dir should be removed")
	}
}
