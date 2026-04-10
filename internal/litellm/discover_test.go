package litellm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestResolveAPIKey(t *testing.T) {
	// Clear all relevant env vars
	for _, k := range []string{"LITELLM_API_KEY", "LITELLM_MASTER_KEY", "ANTHROPIC_API_KEY"} {
		t.Setenv(k, "")
	}

	if got := resolveAPIKey(); got != "" {
		t.Errorf("expected empty, got %q", got)
	}

	t.Setenv("ANTHROPIC_API_KEY", "ak-123")
	if got := resolveAPIKey(); got != "ak-123" {
		t.Errorf("expected ak-123, got %q", got)
	}

	// LITELLM_MASTER_KEY takes priority over ANTHROPIC_API_KEY
	t.Setenv("LITELLM_MASTER_KEY", "sk-master")
	if got := resolveAPIKey(); got != "sk-master" {
		t.Errorf("expected sk-master, got %q", got)
	}

	// LITELLM_API_KEY takes highest priority
	t.Setenv("LITELLM_API_KEY", "sk-litellm")
	if got := resolveAPIKey(); got != "sk-litellm" {
		t.Errorf("expected sk-litellm, got %q", got)
	}
}

func TestProbeModels(t *testing.T) {
	// Server that returns a valid /v1/models response
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{
				{"id": "claude-sonnet-4-6"},
				{"id": "claude-sonnet-4-6"}, // duplicate
				{"id": "claude-haiku-4-5"},
			},
		})
	}))
	defer srv.Close()

	models := probeModels(srv.URL, "")
	if len(models) != 2 {
		t.Fatalf("expected 2 deduplicated models, got %d: %v", len(models), models)
	}
	if models[0] != "claude-sonnet-4-6" || models[1] != "claude-haiku-4-5" {
		t.Errorf("unexpected models: %v", models)
	}
}

func TestProbeModelsAuthRequired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"id": "model-1"}},
		})
	}))
	defer srv.Close()

	// Without key — should fail
	if models := probeModels(srv.URL, ""); models != nil {
		t.Errorf("expected nil without key, got %v", models)
	}

	// With key — should succeed
	models := probeModels(srv.URL, "test-key")
	if len(models) != 1 || models[0] != "model-1" {
		t.Errorf("expected [model-1], got %v", models)
	}
}

func TestDiscoverExplicitEnv(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"id": "test-model"}},
		})
	}))
	defer srv.Close()

	t.Setenv("LITELLM_BASE_URL", srv.URL)
	t.Setenv("LITELLM_API_KEY", "key-1")

	d := Discover()
	if d == nil {
		t.Fatal("expected discovery result")
	}
	if d.BaseURL != srv.URL {
		t.Errorf("BaseURL = %q, want %q", d.BaseURL, srv.URL)
	}
	if d.APIKey != "key-1" {
		t.Errorf("APIKey = %q, want key-1", d.APIKey)
	}
	if len(d.Models) != 1 || d.Models[0] != "test-model" {
		t.Errorf("Models = %v, want [test-model]", d.Models)
	}
}

func TestDiscoverNoProxy(t *testing.T) {
	// Clear everything so no discovery can succeed
	for _, k := range []string{"LITELLM_BASE_URL", "LITELLM_API_KEY", "LITELLM_MASTER_KEY", "ANTHROPIC_API_KEY"} {
		t.Setenv(k, "")
	}
	// Override home to prevent config file discovery
	t.Setenv("HOME", os.TempDir())

	// Override CommonPorts to use a port nothing listens on
	origPorts := CommonPorts
	CommonPorts = []int{19999}
	defer func() { CommonPorts = origPorts }()

	d := Discover()
	if d != nil {
		t.Errorf("expected nil when no proxy running, got %+v", d)
	}
}
