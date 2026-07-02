package main

// studio_invoke_test.go — audit A039: studio.* capabilities must ride
// the studioclient Transport through Backends.Invoke, gated on
// studio_config. A mock Studio HTTP server (httptest) stands in for
// the real Actium Studio service; no external call is made.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/config"
	"github.com/RelayOne/r1/internal/studioclient"
)

func newStudioTestBackends(t *testing.T) *Backends {
	t.Helper()
	b, err := NewBackends(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatalf("new backends: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func TestInvokeStudioViaMockHTTPTransport(t *testing.T) {
	t.Setenv("STUDIO_TEST_TOKEN", "sekret-token")

	var gotAuth, gotScopes, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotScopes = r.Header.Get("X-Studio-Scopes")
		gotPath = r.Method + " " + r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"site-1","name":"demo"}`))
	}))
	defer srv.Close()

	b := newStudioTestBackends(t)
	b.StudioConfig = config.StudioConfig{
		Enabled:   true,
		Transport: config.StudioTransportHTTP,
		HTTP: config.StudioHTTPConfig{
			BaseURL:  srv.URL,
			TokenEnv: "STUDIO_TEST_TOKEN",
		},
	}

	resp, err := b.Invoke(context.Background(), "m-studio", "studio.create_site",
		json.RawMessage(`{"name":"demo"}`), "")
	if err != nil {
		t.Fatalf("Invoke studio.create_site: %v", err)
	}

	if gotPath != "POST /api/sites" {
		t.Errorf("studio server saw %q, want POST /api/sites", gotPath)
	}
	if gotAuth != "Bearer sekret-token" {
		t.Errorf("Authorization = %q, want bearer token from TokenEnv", gotAuth)
	}
	if gotScopes != config.DefaultStudioScopes {
		t.Errorf("X-Studio-Scopes = %q, want default %q", gotScopes, config.DefaultStudioScopes)
	}
	if gotBody["name"] != "demo" {
		t.Errorf("request body = %v, want name=demo", gotBody)
	}

	out, ok := resp["output"].(json.RawMessage)
	if !ok {
		t.Fatalf("resp[output] = %T (%v), want json.RawMessage", resp["output"], resp["output"])
	}
	if !strings.Contains(string(out), "site-1") {
		t.Errorf("output = %s, want the mock server payload", out)
	}
	if resp["studio_transport"] != "http" {
		t.Errorf("studio_transport = %v, want http", resp["studio_transport"])
	}

	// Second call reuses the session-cached transport (no re-resolve
	// error path) — exercise a GET tool with a path field.
	if _, err := b.Invoke(context.Background(), "m-studio", "studio.get_site",
		json.RawMessage(`{"siteId":"site-1"}`), ""); err != nil {
		t.Fatalf("second Invoke: %v", err)
	}
	if gotPath != "GET /api/sites/site-1" {
		t.Errorf("studio server saw %q, want GET /api/sites/site-1", gotPath)
	}
}

func TestInvokeStudioDisabledSurfacesErrStudioDisabled(t *testing.T) {
	b := newStudioTestBackends(t)
	b.StudioConfig = config.DefaultStudioConfig() // Enabled=false

	_, err := b.Invoke(context.Background(), "m-studio", "studio.create_site",
		json.RawMessage(`{"name":"demo"}`), "")
	if err == nil {
		t.Fatal("disabled studio_config must not silently no-op studio.* invocations")
	}
	if !errors.Is(err, studioclient.ErrStudioDisabled) {
		t.Errorf("err = %v, want errors.Is ErrStudioDisabled", err)
	}
}

func TestSeedSkillPackRootsGatesActiumStudioPack(t *testing.T) {
	packRoot := filepath.Clean(filepath.Join("..", "..", ".stoke", "skills", "packs"))

	// Disabled (default): studio.* manifests must not be advertised.
	b := newStudioTestBackends(t)
	if b.StudioConfig.Enabled {
		t.Fatal("test precondition: studio must default to disabled")
	}
	if _, _, err := b.SeedSkillPackRoots([]string{packRoot}); err != nil {
		t.Fatalf("SeedSkillPackRoots (disabled): %v", err)
	}
	if _, ok := b.ManifestRegistry.Get("studio.create_page"); ok {
		t.Error("studio.create_page registered while studio_config.enabled=false")
	}

	// Enabled: the pack registers.
	b2 := newStudioTestBackends(t)
	b2.StudioConfig.Enabled = true
	if _, _, err := b2.SeedSkillPackRoots([]string{packRoot}); err != nil {
		t.Fatalf("SeedSkillPackRoots (enabled): %v", err)
	}
	if _, ok := b2.ManifestRegistry.Get("studio.create_page"); !ok {
		t.Error("studio.create_page not registered despite studio_config.enabled=true")
	}
}
