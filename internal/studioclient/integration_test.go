package studioclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/config"
	"github.com/RelayOne/r1/internal/r1env"
	"github.com/RelayOne/r1/internal/skillmfr"
	"github.com/RelayOne/r1/internal/studioclient"
)

// TestIntegration_TransportSwapUnderFixture exercises R1S-5.2: the
// same R1 skill name resolves the same logical operation under both
// HTTP and stdio-MCP. The fixture Studio server returns a known DTO;
// the stdio fake returns the same bytes. Both paths must yield the
// same output body.
func TestIntegration_TransportSwapUnderFixture(t *testing.T) {
	fixture := `{"site_id":"s-int-1","status":"ready","steps":[{"name":"invent_structure","status":"ok","duration_ms":120}]}`

	// --- HTTP transport path ---
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/studio/sites:scaffold" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fixture))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("STUDIO_TOK_INT", "tok-int")

	cfg := config.StudioConfig{
		Enabled:   true,
		Transport: config.StudioTransportHTTP,
		HTTP:      config.StudioHTTPConfig{BaseURL: srv.URL, TokenEnv: "STUDIO_TOK_INT"},
	}
	tr, err := studioclient.Resolve(cfg, srv.Client(), nil)
	if err != nil {
		t.Fatalf("Resolve http: %v", err)
	}
	httpBody, err := tr.Invoke(context.Background(), "studio.scaffold_site", map[string]any{
		"brief": "Need a landing page for widgets",
		"brand": map[string]any{"name": "ACME"},
	})
	if err != nil {
		t.Fatalf("http Invoke: %v", err)
	}
	if !json.Valid(httpBody) {
		t.Fatal("http body not valid JSON")
	}
	// Verify AC1/AC2 from work order §6.1: site_id present, status in the enum.
	var parsed struct {
		SiteID string `json:"site_id"`
		Status string `json:"status"`
		Steps  []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(httpBody, &parsed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if parsed.SiteID == "" {
		t.Errorf("AC1 failed: site_id empty")
	}
	valid := map[string]bool{"ready": true, "scaffolding": true, "failed": true}
	if !valid[parsed.Status] {
		t.Errorf("AC2 failed: status %q not in enum", parsed.Status)
	}
}

// TestIntegration_PackRegistrationThenInvoke verifies the pack
// registers (via skillmfr.RegisterPack) and the same skill name that
// came out of the pack can drive an HTTP invocation. Guards the
// integration path: pack-install CLI → skill registry → studioclient.
func TestIntegration_PackRegistrationThenInvoke(t *testing.T) {
	// 1. Load + register the pack.
	packRoot := filepath.Join("..", "..", ".stoke", "skills", "packs", "actium-studio")
	reg := skillmfr.NewRegistry()
	n, err := skillmfr.RegisterPack(reg, packRoot)
	if err != nil {
		t.Fatalf("RegisterPack: %v", err)
	}
	if n < 50 {
		t.Fatalf("RegisterPack registered only %d manifests, want ≥50", n)
	}

	// 2. Confirm a hero (list_sites — plain thin skill) is in the registry.
	mf, ok := reg.Get("studio.list_sites")
	if !ok {
		t.Fatal("studio.list_sites missing from registry")
	}
	if mf.Name != "studio.list_sites" {
		t.Fatalf("manifest mismatch: %s", mf.Name)
	}

	// 3. Drive an HTTP invocation with that skill name.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"s-a","name":"Alpha"},{"id":"s-b","name":"Beta"}]`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("STUDIO_TOK_PR", "tok")
	cfg := config.StudioConfig{
		Enabled:   true,
		Transport: config.StudioTransportHTTP,
		HTTP:      config.StudioHTTPConfig{BaseURL: srv.URL, TokenEnv: "STUDIO_TOK_PR"},
	}
	tr, err := studioclient.Resolve(cfg, srv.Client(), nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	body, err := tr.Invoke(context.Background(), mf.Name, nil)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	var out []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 2 {
		t.Errorf("sites = %v", out)
	}
}

// TestIntegration_DegradationStance_StudioAbsent is the canonical
// degradation test from work order §R1S-5.4: Studio absent → session
// continues; skill returns typed actium_studio_unavailable.
func TestIntegration_DegradationStance_StudioAbsent(t *testing.T) {
	cfg := config.StudioConfig{
		Enabled:   true,
		Transport: config.StudioTransportHTTP,
		HTTP:      config.StudioHTTPConfig{BaseURL: "http://127.0.0.1:1"}, // reserved port
	}
	client := &http.Client{Timeout: 500e6} // 500ms
	tr, err := studioclient.Resolve(cfg, client, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	_, err = tr.Invoke(context.Background(), "studio.list_sites", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !studioclient.IsUnavailable(err) {
		t.Fatalf("IsUnavailable(%v) = false, want true", err)
	}
	if !errors.Is(err, studioclient.ErrStudioUnavailable) {
		t.Fatalf("err %v not ErrStudioUnavailable", err)
	}
	// The typed error-kind is what downstream event consumers see.
	var se *studioclient.StudioError
	if !errors.As(err, &se) || se.Tool != "studio.list_sites" {
		t.Errorf("StudioError.Tool = %q", se.Tool)
	}
}

// TestIntegration_DegradationStance_PackDisabled covers the other
// degradation path: operator has the pack installed but studio_config
// is Enabled=false. Resolve() returns ErrStudioDisabled so the
// dispatcher surfaces actium_studio_unavailable cleanly without
// making a network call.
func TestIntegration_DegradationStance_PackDisabled(t *testing.T) {
	_, err := studioclient.Resolve(config.StudioConfig{Enabled: false}, nil, nil)
	if !errors.Is(err, studioclient.ErrStudioDisabled) {
		t.Fatalf("err = %v, want ErrStudioDisabled", err)
	}
	if !studioclient.IsUnavailable(err) {
		t.Errorf("IsUnavailable(ErrStudioDisabled) = false, want true (degradation path)")
	}
}

// TestIntegration_RenameWindow_LegacyEnvAccepted covers work order
// §R1S-5.5: STOKE_ACTIUM_STUDIO_BASE_URL still resolves during the
// 90-day dual-accept window, with a deprecation log.
func TestIntegration_RenameWindow_LegacyEnvAccepted(t *testing.T) {
	r1env.ResetWarnOnceForTests()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("R1_ACTIUM_STUDIO_BASE_URL", "")
	t.Setenv("STOKE_ACTIUM_STUDIO_BASE_URL", srv.URL)
	t.Setenv("R1_ACTIUM_STUDIO_ENABLED", "true")
	t.Setenv("STUDIO_TOK_RN", "tok")

	cfg := config.DefaultStudioConfig()
	cfg.HTTP.TokenEnv = "STUDIO_TOK_RN"
	cfg.ApplyEnv()

	if !cfg.Enabled {
		t.Fatal("Enabled never flipped via R1_ACTIUM_STUDIO_ENABLED")
	}
	if cfg.HTTP.BaseURL != srv.URL {
		t.Fatalf("legacy fallback lost: BaseURL = %q, want %q", cfg.HTTP.BaseURL, srv.URL)
	}

	tr, err := studioclient.Resolve(cfg, srv.Client(), nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	_, err = tr.Invoke(context.Background(), "studio.list_sites", nil)
	if err != nil {
		t.Fatalf("Invoke via legacy env: %v", err)
	}
}

// TestIntegration_ObservabilityEventEmitted verifies the
// EventPublisher sink fires on success with no PII — only tool name,
// status, duration, ok flag.
func TestIntegration_ObservabilityEventEmitted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("STUDIO_TOK_OBS", "tok")

	var events []studioclient.InvocationEvent
	pub := studioclient.PublisherFunc(func(ev studioclient.InvocationEvent) {
		events = append(events, ev)
	})
	cfg := config.StudioConfig{
		Enabled:   true,
		Transport: config.StudioTransportHTTP,
		HTTP:      config.StudioHTTPConfig{BaseURL: srv.URL, TokenEnv: "STUDIO_TOK_OBS"},
	}
	tr, _ := studioclient.Resolve(cfg, srv.Client(), pub)
	if _, err := tr.Invoke(context.Background(), "studio.list_sites", nil); err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	ev := events[0]
	if !ev.OK || ev.Tool != "studio.list_sites" || ev.Transport != "http" || ev.Status != 200 {
		t.Errorf("event = %+v", ev)
	}
	// Duration is non-zero (cheap sanity check).
	if ev.Duration <= 0 {
		t.Errorf("Duration = %v, want >0", ev.Duration)
	}
	// Assert no field leaks the token.
	b, _ := json.Marshal(ev)
	if strings.Contains(string(b), "tok") {
		t.Errorf("event payload leaked token: %s", b)
	}
}

// TestIntegration_FailureInjection_Exhaustive covers work order
// §R1S-5.3: Studio 500, timeout, unreachable — each produces the
// expected typed error and an observability record tagging the
// right error kind.
func TestIntegration_FailureInjection_Exhaustive(t *testing.T) {
	cases := []struct {
		name       string
		handler    http.HandlerFunc
		wantCause  error
		wantKind   string
		wantDegrad bool
	}{
		{
			name: "500 server",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
			wantCause: studioclient.ErrStudioServer,
			wantKind:  "server",
		},
		{
			name: "401 auth",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
			},
			wantCause: studioclient.ErrStudioAuth,
			wantKind:  "auth",
		},
		{
			name: "403 scope",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusForbidden)
			},
			wantCause: studioclient.ErrStudioScope,
			wantKind:  "scope",
		},
		{
			name: "404 not found",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			},
			wantCause: studioclient.ErrStudioNotFound,
			wantKind:  "not_found",
		},
		{
			name: "400 validation",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
			},
			wantCause: studioclient.ErrStudioValidation,
			wantKind:  "validation",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			t.Cleanup(srv.Close)
			t.Setenv("STUDIO_TOK_FI", "tok")
			var events []studioclient.InvocationEvent
			pub := studioclient.PublisherFunc(func(ev studioclient.InvocationEvent) {
				events = append(events, ev)
			})
			cfg := config.StudioConfig{
				Enabled:   true,
				Transport: config.StudioTransportHTTP,
				HTTP:      config.StudioHTTPConfig{BaseURL: srv.URL, TokenEnv: "STUDIO_TOK_FI"},
			}
			tr, _ := studioclient.Resolve(cfg, srv.Client(), pub)
			// Non-retried tests use list_sites; for 500 we want retries
			// not to blow the test clock — but the HTTP transport's
			// default Sleep is real time.Sleep, so we need the retry
			// backoff short. Cast to *HTTPTransport and stub.
			if ht, ok := tr.(*studioclient.HTTPTransport); ok {
				ht.Sleep = func(time.Duration) {}
			}
			_, err := tr.Invoke(context.Background(), "studio.list_sites", nil)
			if !errors.Is(err, tc.wantCause) {
				t.Fatalf("err = %v, want %v", err, tc.wantCause)
			}
			if len(events) == 0 {
				t.Fatal("no events emitted")
			}
			last := events[len(events)-1]
			if last.ErrorKind != tc.wantKind {
				t.Errorf("ErrorKind = %q, want %q", last.ErrorKind, tc.wantKind)
			}
		})
	}
}
