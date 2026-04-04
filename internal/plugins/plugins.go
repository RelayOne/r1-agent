package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Manifest is the plugin manifest format (.stoke-plugin/plugin.json).
type Manifest struct {
	Name        string            `json:"name"`
	Version     string            `json:"version"`
	Description string            `json:"description"`
	Hooks       map[string]string `json:"hooks"`      // event name -> script path (relative to plugin dir)
	ScanRules   string            `json:"scan_rules"` // path to additional scan rules file
}

// Plugin represents an installed plugin.
type Plugin struct {
	Manifest Manifest
	Dir      string // absolute path to plugin directory
	Enabled  bool
}

// Registry manages installed plugins.
type Registry struct {
	plugins []Plugin
	rootDir string // e.g. ~/.stoke/plugins/ or .stoke/plugins/
}

// NewRegistry creates a plugin registry that scans the given directory.
func NewRegistry(rootDir string) *Registry {
	return &Registry{rootDir: rootDir}
}

// Discover scans for plugins with .stoke-plugin/plugin.json manifests.
func (r *Registry) Discover() error {
	r.plugins = nil
	if _, err := os.Stat(r.rootDir); os.IsNotExist(err) {
		return nil // no plugins directory
	}
	entries, err := os.ReadDir(r.rootDir)
	if err != nil {
		return fmt.Errorf("read plugins dir: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		manifestPath := filepath.Join(r.rootDir, entry.Name(), ".stoke-plugin", "plugin.json")
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			continue // no manifest = not a plugin
		}

		var m Manifest
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}

		r.plugins = append(r.plugins, Plugin{
			Manifest: m,
			Dir:      filepath.Join(r.rootDir, entry.Name()),
			Enabled:  true, // default enabled
		})
	}
	return nil
}

// List returns all discovered plugins.
func (r *Registry) List() []Plugin { return r.plugins }

// Enabled returns only enabled plugins.
func (r *Registry) Enabled() []Plugin {
	var out []Plugin
	for _, p := range r.plugins {
		if p.Enabled {
			out = append(out, p)
		}
	}
	return out
}

// HooksForEvent returns absolute paths to hook scripts for a given event.
func (r *Registry) HooksForEvent(event string) []string {
	var scripts []string
	for _, p := range r.Enabled() {
		if script, ok := p.Manifest.Hooks[event]; ok {
			scripts = append(scripts, filepath.Join(p.Dir, script))
		}
	}
	return scripts
}
