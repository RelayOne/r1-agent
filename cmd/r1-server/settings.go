// Package main — settings.go
//
// Spec 27 §10 (r1-server-ui-v2.md implementation checklist) calls for
// a GET /settings/retention page that renders the retention-policies
// config in a read-only form. This file lands a broader /settings
// skeleton ahead of that: it reads ~/.r1/config.yaml if present,
// otherwise surfaces the built-in defaults. The retention-policies
// section becomes one panel on this page once its handler lands.
//
// Behavior:
//
//   - Gated behind R1_SERVER_UI_V2=1 (per §2.3). Off → 404.
//   - No authentication, no writes. RBAC + edit controls are part of
//     the retention-policies spec, not this skeleton.
//   - Config path honors R1_CONFIG_PATH (test + ops override); the
//     default is <user-home>/.r1/config.yaml (note: not the global
//     data dir, which is platform-specific — the config file is a
//     dotfile in $HOME by operator convention).
//   - Missing file is not an error: the defaults struct renders and
//     the page notes "using built-in defaults" so operators know to
//     write a config file to override.
//   - Malformed YAML 500s with the parse error — this surfaces a
//     misconfiguration loudly instead of silently falling back to
//     defaults, which would mask the mistake.
package main

import (
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// r1Config mirrors the shape of ~/.r1/config.yaml. Fields are tagged
// for both yaml (file format) and the template. Defaults are set by
// defaultConfig(); callers pass the resulting struct to yaml.Unmarshal
// which overlays whatever the operator wrote, leaving unset fields at
// their default.
//
// The field set is deliberately conservative: only config surfaces
// actually referenced elsewhere in r1-server (port, data dir, v2
// feature flag, share toggle, retention window). Adding a field here
// without an underlying handler consumer would be dead config.
type r1Config struct {
	Server struct {
		Port    int    `yaml:"port"`
		DataDir string `yaml:"data_dir"`
	} `yaml:"server"`

	UI struct {
		V2Enabled     bool `yaml:"v2_enabled"`
		ShareEnabled  bool `yaml:"share_enabled"`
	} `yaml:"ui"`

	Retention struct {
		// Days after which memory-bus rows with no explicit expires_at
		// are garbage-collected. 0 means "no auto-GC".
		MemoryBusDays int `yaml:"memory_bus_days"`
		// Days after which ledger nodes older than the window are
		// candidates for chain-tier pruning (content-tier stays).
		LedgerDays int `yaml:"ledger_days"`
	} `yaml:"retention"`
}

// defaultConfig returns the built-in defaults surfaced when no config
// file is present. These match the behavior hardcoded elsewhere in
// r1-server (defaultPort=3948, v2 off, share off) so the settings
// page shows the same values the running process is using when no
// file is on disk.
func defaultConfig() r1Config {
	var c r1Config
	c.Server.Port = defaultPort
	c.Server.DataDir = "<platform default>"
	c.UI.V2Enabled = false
	c.UI.ShareEnabled = false
	c.Retention.MemoryBusDays = 30
	c.Retention.LedgerDays = 365
	return c
}

// configPath returns the path to the config YAML file. Override via
// R1_CONFIG_PATH for tests and non-standard layouts. The default
// location is $HOME/.r1/config.yaml per the task brief.
func configPath() (string, error) {
	if v := os.Getenv("R1_CONFIG_PATH"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".r1", "config.yaml"), nil
}

// loadConfig resolves the config file and returns the parsed struct
// plus whether the file existed (so the template can label the page
// "built-in defaults" vs. "from file <path>"). A missing file is not
// an error; an unreadable or malformed file is.
func loadConfig() (cfg r1Config, source string, fromFile bool, err error) {
	cfg = defaultConfig()

	path, err := configPath()
	if err != nil {
		return cfg, "", false, err
	}

	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return cfg, path, false, nil
		}
		return cfg, path, false, fmt.Errorf("read config: %w", readErr)
	}

	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return cfg, path, true, fmt.Errorf("parse config: %w", err)
	}
	return cfg, path, true, nil
}

// settingsView is the template render context.
type settingsView struct {
	Path     string
	FromFile bool
	Sections []settingsSection
}

// settingsSection renders one panel on the page. Every value is
// pre-stringified by the handler so the template stays dumb — this
// avoids per-field yaml/template reflection at render time.
type settingsSection struct {
	Title  string
	Fields []settingsField
}

type settingsField struct {
	Key   string
	Value string
}

// settingsTmpl is the read-only viewer skeleton. No form inputs, no
// JS, no edit controls — edits land in the retention-policies spec
// with RBAC and CSRF protection.
var settingsTmpl = template.Must(template.New("settings").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>r1-server settings</title>
</head>
<body>
<header>
  <h1>Settings</h1>
  {{if .FromFile}}
    <p>Loaded from <code>{{.Path}}</code>.</p>
  {{else}}
    <p>No config file at <code>{{.Path}}</code> — showing built-in defaults. Create the file to override.</p>
  {{end}}
</header>
<main>
  {{range .Sections}}
    <section>
      <h2>{{.Title}}</h2>
      <dl>
        {{range .Fields}}
          <dt>{{.Key}}</dt>
          <dd><code>{{.Value}}</code></dd>
        {{end}}
      </dl>
    </section>
  {{end}}
  <p><em>Read-only. Edits land with the retention-policies spec (RBAC-gated).</em></p>
</main>
</body>
</html>
`))

// flattenConfig projects an r1Config into the section/field shape the
// template renders against. Kept separate from the handler so tests
// can assert the projection independently of the HTTP layer. Fields
// inside each section are sorted by key for deterministic render
// output — the `yaml.v3` struct-tag iteration order isn't guaranteed
// across Go versions, and an unstable render breaks golden-HTML tests.
func flattenConfig(cfg r1Config) []settingsSection {
	server := settingsSection{
		Title: "Server",
		Fields: []settingsField{
			{Key: "port", Value: fmt.Sprintf("%d", cfg.Server.Port)},
			{Key: "data_dir", Value: cfg.Server.DataDir},
		},
	}
	ui := settingsSection{
		Title: "UI",
		Fields: []settingsField{
			{Key: "v2_enabled", Value: fmt.Sprintf("%t", cfg.UI.V2Enabled)},
			{Key: "share_enabled", Value: fmt.Sprintf("%t", cfg.UI.ShareEnabled)},
		},
	}
	retention := settingsSection{
		Title: "Retention",
		Fields: []settingsField{
			{Key: "memory_bus_days", Value: fmt.Sprintf("%d", cfg.Retention.MemoryBusDays)},
			{Key: "ledger_days", Value: fmt.Sprintf("%d", cfg.Retention.LedgerDays)},
		},
	}
	out := []settingsSection{server, ui, retention}
	for i := range out {
		sort.Slice(out[i].Fields, func(a, b int) bool {
			return out[i].Fields[a].Key < out[i].Fields[b].Key
		})
	}
	return out
}

// serveSettings renders the read-only config viewer. Status codes:
//
//	404 — v2 flag is off (consistent with share + memories handlers)
//	500 — config file exists but fails to parse
//	200 — render succeeded (either from file or from defaults)
//
// The handler has no DB dependency — config is file-system-sourced.
// That keeps the endpoint available even when the SQLite DB is
// locked by a long-running write.
func serveSettings(w http.ResponseWriter, r *http.Request) {
	if !v2Enabled() {
		http.NotFound(w, r)
		return
	}

	cfg, path, fromFile, err := loadConfig()
	if err != nil {
		http.Error(w, "load settings: "+err.Error(), http.StatusInternalServerError)
		return
	}

	view := settingsView{
		Path:     path,
		FromFile: fromFile,
		Sections: flattenConfig(cfg),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")

	if err := settingsTmpl.Execute(w, view); err != nil {
		http.Error(w, "render settings: "+err.Error(), http.StatusInternalServerError)
		return
	}
}
