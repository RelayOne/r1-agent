package main

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSettings_V2Off_404 — the default state. v2 flag unset, the
// route 404s consistently with share.go + /memories.
func TestSettings_V2Off_404(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "")
	s := newUIServer(t)

	resp, err := http.Get(s.URL + "/settings")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status=%d, want 404 when v2 is off", resp.StatusCode)
	}
}

// TestSettings_Defaults_WhenNoConfigFile — point R1_CONFIG_PATH at a
// non-existent file; the handler must render the built-in defaults
// and label the page accordingly instead of 500ing.
func TestSettings_Defaults_WhenNoConfigFile(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	t.Setenv("R1_CONFIG_PATH", filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	s := newUIServer(t)

	resp, err := http.Get(s.URL + "/settings")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", resp.StatusCode, body)
	}
	bs := string(body)
	if !strings.Contains(bs, "built-in defaults") {
		t.Error("defaults banner missing when config file is absent")
	}
	// Default port must appear as the Server.port value.
	if !strings.Contains(bs, "3948") {
		t.Error("default port 3948 missing from rendered output")
	}
	// Default retention days must be rendered.
	if !strings.Contains(bs, "memory_bus_days") {
		t.Error("retention.memory_bus_days field missing")
	}
	// Basic security headers.
	for _, name := range []string{"X-Content-Type-Options", "Referrer-Policy", "Cache-Control"} {
		if resp.Header.Get(name) == "" {
			t.Errorf("missing security header %s", name)
		}
	}
}

// TestSettings_LoadsFromFile — write a real config file and verify
// the values overlay the defaults in the rendered HTML. Proves the
// yaml.Unmarshal path lands correctly.
func TestSettings_LoadsFromFile(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")

	path := filepath.Join(t.TempDir(), "config.yaml")
	body := []byte(`
server:
  port: 4242
  data_dir: /custom/path
ui:
  v2_enabled: true
  share_enabled: true
retention:
  memory_bus_days: 7
  ledger_days: 60
`)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("R1_CONFIG_PATH", path)
	s := newUIServer(t)

	resp, err := http.Get(s.URL + "/settings")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	rendered, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", resp.StatusCode, rendered)
	}
	rs := string(rendered)
	// Loaded-from banner.
	if !strings.Contains(rs, "Loaded from") {
		t.Error("loaded-from banner missing when config file is present")
	}
	// Overridden values must appear.
	for _, want := range []string{"4242", "/custom/path", "true", "7", "60"} {
		if !strings.Contains(rs, want) {
			t.Errorf("rendered output missing overridden value %q", want)
		}
	}
	// Default 3948 must NOT appear any more — the port override
	// replaced it.
	if strings.Contains(rs, "3948") {
		t.Error("default port still present after override — overlay failed")
	}
}

// TestSettings_MalformedYAML_500 — a parse error is louder than a
// silent fallback. A config file with bad YAML must produce an HTTP
// 500 so operators don't keep using defaults and wonder why their
// edits aren't taking effect.
func TestSettings_MalformedYAML_500(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")

	path := filepath.Join(t.TempDir(), "bad.yaml")
	// Unbalanced YAML: an opening brace with no close.
	if err := os.WriteFile(path, []byte("server: {port: 80\nretention: ["), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("R1_CONFIG_PATH", path)
	s := newUIServer(t)

	resp, err := http.Get(s.URL + "/settings")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status=%d, want 500 on malformed YAML", resp.StatusCode)
	}
}

// TestDefaultConfig_Values — lock the documented defaults in place.
// A future refactor that drops a field or changes a constant must
// update this test and the retention-policies spec together.
func TestDefaultConfig_Values(t *testing.T) {
	c := defaultConfig()
	if c.Server.Port != defaultPort {
		t.Errorf("default port=%d, want %d", c.Server.Port, defaultPort)
	}
	if c.UI.V2Enabled || c.UI.ShareEnabled {
		t.Error("UI flags default to true — both should be off by default")
	}
	if c.Retention.MemoryBusDays == 0 {
		t.Error("memory_bus_days default is 0 — spec expects a positive floor")
	}
	if c.Retention.LedgerDays == 0 {
		t.Error("ledger_days default is 0 — spec expects a positive floor")
	}
}

// TestConfigPath_Override — R1_CONFIG_PATH wins over the default
// $HOME/.r1/config.yaml resolution so tests (and container deploys)
// have a clean override path.
func TestConfigPath_Override(t *testing.T) {
	t.Setenv("R1_CONFIG_PATH", "/tmp/override.yaml")
	got, err := configPath()
	if err != nil {
		t.Fatalf("configPath: %v", err)
	}
	if got != "/tmp/override.yaml" {
		t.Errorf("configPath=%q, want /tmp/override.yaml", got)
	}
}

// TestFlattenConfig_FieldsSorted — rendered field ordering must be
// deterministic so golden-HTML tests don't flap on map iteration
// order. Lock the sort invariant in explicitly.
func TestFlattenConfig_FieldsSorted(t *testing.T) {
	sections := flattenConfig(defaultConfig())
	for _, sec := range sections {
		for i := 1; i < len(sec.Fields); i++ {
			if sec.Fields[i-1].Key > sec.Fields[i].Key {
				t.Errorf("section %q fields not sorted: %q > %q",
					sec.Title, sec.Fields[i-1].Key, sec.Fields[i].Key)
			}
		}
	}
}
