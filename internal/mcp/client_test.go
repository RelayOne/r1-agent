package mcp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigFromFile(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "mcp.json")

	config := `{
		"mcpServers": {
			"filesystem": {
				"command": "npx",
				"args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
				"env": {"NODE_ENV": "production"}
			},
			"github": {
				"command": "npx",
				"args": ["-y", "@modelcontextprotocol/server-github"],
				"env": {"GITHUB_TOKEN": "test"}
			}
		}
	}`
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	configs, err := ConfigFromFile(configPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(configs) != 2 {
		t.Fatalf("expected 2 configs, got %d", len(configs))
	}

	// Find filesystem config
	found := false
	for _, c := range configs {
		if c.Name == "filesystem" {
			found = true
			if c.Command != "npx" {
				t.Errorf("expected npx, got %s", c.Command)
			}
			if len(c.Args) != 3 {
				t.Errorf("expected 3 args, got %d", len(c.Args))
			}
			if c.Env["NODE_ENV"] != "production" {
				t.Errorf("expected production, got %s", c.Env["NODE_ENV"])
			}
		}
	}
	if !found {
		t.Error("filesystem config not found")
	}
}

func TestConfigFromFileMissing(t *testing.T) {
	configs, err := ConfigFromFile("/nonexistent/path.json")
	if err != nil {
		t.Fatalf("expected nil error for missing file, got %v", err)
	}
	if configs != nil {
		t.Errorf("expected nil configs, got %v", configs)
	}
}

func TestEmptyConfigPath(t *testing.T) {
	dir := t.TempDir()
	path, err := EmptyConfigPath(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read: %v", err)
	}
	if string(data) != "{}" {
		t.Errorf("expected empty JSON, got %s", string(data))
	}
}
