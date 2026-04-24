package plugins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverNoDir(t *testing.T) {
	reg := NewRegistry("/tmp/stoke-test-nonexistent-dir-abc123")
	if err := reg.Discover(); err != nil {
		t.Fatalf("expected no error for non-existent dir, got: %v", err)
	}
	if len(reg.List()) != 0 {
		t.Fatalf("expected empty list, got %d plugins", len(reg.List()))
	}
}

func TestDiscoverWithPlugin(t *testing.T) {
	root := t.TempDir()

	// Create plugin directory structure: root/my-plugin/.stoke-plugin/plugin.json
	pluginDir := filepath.Join(root, "my-plugin", ".stoke-plugin")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatal(err)
	}

	manifest := Manifest{
		Name:        "my-plugin",
		Version:     "1.0.0",
		Description: "A test plugin",
		Hooks: map[string]string{
			"PreToolUse": "hooks/pre.sh",
		},
	}
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry(root)
	if err := reg.Discover(); err != nil {
		t.Fatalf("Discover failed: %v", err)
	}

	ps := reg.List()
	if len(ps) != 1 {
		t.Fatalf("expected 1 plugin, got %d", len(ps))
	}
	if ps[0].Manifest.Name != "my-plugin" {
		t.Errorf("expected name 'my-plugin', got %q", ps[0].Manifest.Name)
	}
	if ps[0].Manifest.Version != "1.0.0" {
		t.Errorf("expected version '1.0.0', got %q", ps[0].Manifest.Version)
	}
	if !ps[0].Enabled {
		t.Error("expected plugin to be enabled by default")
	}
}

func TestHooksForEvent(t *testing.T) {
	root := t.TempDir()

	// Create a plugin with a PreToolUse hook
	pluginDir := filepath.Join(root, "guard-plugin", ".stoke-plugin")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatal(err)
	}

	manifest := Manifest{
		Name:    "guard-plugin",
		Version: "0.1.0",
		Hooks: map[string]string{
			"PreToolUse":  "hooks/pre.sh",
			"PostToolUse": "hooks/post.sh",
		},
	}
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry(root)
	if err := reg.Discover(); err != nil {
		t.Fatal(err)
	}

	scripts := reg.HooksForEvent("PreToolUse")
	if len(scripts) != 1 {
		t.Fatalf("expected 1 hook script for PreToolUse, got %d", len(scripts))
	}
	expected := filepath.Join(root, "guard-plugin", "hooks", "pre.sh")
	if scripts[0] != expected {
		t.Errorf("expected script path %q, got %q", expected, scripts[0])
	}

	// Event with no hooks
	scripts = reg.HooksForEvent("SessionStart")
	if len(scripts) != 0 {
		t.Errorf("expected 0 scripts for SessionStart, got %d", len(scripts))
	}
}

func TestEnabledFilter(t *testing.T) {
	root := t.TempDir()

	// Create two plugins
	for _, name := range []string{"enabled-plugin", "disabled-plugin"} {
		pluginDir := filepath.Join(root, name, ".stoke-plugin")
		if err := os.MkdirAll(pluginDir, 0755); err != nil {
			t.Fatal(err)
		}
		manifest := Manifest{
			Name:    name,
			Version: "1.0.0",
		}
		data, _ := json.Marshal(manifest)
		if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	reg := NewRegistry(root)
	if err := reg.Discover(); err != nil {
		t.Fatal(err)
	}

	// All should be enabled by default
	if len(reg.Enabled()) != 2 {
		t.Fatalf("expected 2 enabled plugins, got %d", len(reg.Enabled()))
	}

	// Disable one plugin
	for i := range reg.plugins {
		if reg.plugins[i].Manifest.Name == "disabled-plugin" {
			reg.plugins[i].Enabled = false
		}
	}

	enabled := reg.Enabled()
	if len(enabled) != 1 {
		t.Fatalf("expected 1 enabled plugin, got %d", len(enabled))
	}
	if enabled[0].Manifest.Name != "enabled-plugin" {
		t.Errorf("expected 'enabled-plugin', got %q", enabled[0].Manifest.Name)
	}
}
