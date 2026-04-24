package cloud

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stokeCloudMux is a test HTTP server implementing Contract
// H1-H4 shapes. Used across tests to assert the client
// sends the right payloads + parses the right responses.
func stokeCloudMux(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	hits := &[]string{}
	mux := http.NewServeMux()

	mux.HandleFunc("/v1/auth/register", func(w http.ResponseWriter, r *http.Request) {
		*hits = append(*hits, r.Method+" "+r.URL.Path)
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body RegisterRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if body.APIKey == "" {
			http.Error(w, "api_key required", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(RegisterResponse{
			UserID: "u-123",
			OrgID:  "o-456",
			Status: "active",
		})
	})

	mux.HandleFunc("/v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		*hits = append(*hits, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "missing bearer", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body SubmitSessionRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(SubmitSessionResponse{
			SessionID:    body.SessionID,
			Status:       "queued",
			DashboardURL: "https://cloud.stoke.dev/d/" + body.SessionID,
		})
	})

	mux.HandleFunc("/v1/sessions/", func(w http.ResponseWriter, r *http.Request) {
		*hits = append(*hits, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "missing bearer", http.StatusUnauthorized)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/events") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(SessionEventsResponse{
				Events: []SessionEvent{
					{EventType: "phase.plan.started", Timestamp: "2026-04-16T10:00:00Z"},
				},
			})
			return
		}
		// Status
		id := strings.TrimPrefix(r.URL.Path, "/v1/sessions/")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(SessionStatus{
			SessionID:      id,
			Status:         "executing",
			CurrentPhase:   "plan",
			TurnsCompleted: 3,
			TurnsRemaining: 7,
			DashboardURL:   "https://cloud.stoke.dev/d/" + id,
		})
	})

	return httptest.NewServer(mux), hits
}

func TestRegister_Success(t *testing.T) {
	srv, hits := stokeCloudMux(t)
	defer srv.Close()

	c := New(srv.URL, "")
	resp, err := c.Register(context.Background(), "bootstrap-key")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if resp.UserID != "u-123" || resp.OrgID != "o-456" || resp.Status != "active" {
		t.Errorf("unexpected response: %+v", resp)
	}
	if len(*hits) != 1 || (*hits)[0] != "POST /v1/auth/register" {
		t.Errorf("hits=%v", *hits)
	}
}

func TestRegister_BadKey(t *testing.T) {
	srv, _ := stokeCloudMux(t)
	defer srv.Close()
	c := New(srv.URL, "")
	_, err := c.Register(context.Background(), "")
	if err == nil {
		t.Fatal("expected error on empty api key")
	}
}

func TestSubmitSession_Authorized(t *testing.T) {
	srv, hits := stokeCloudMux(t)
	defer srv.Close()
	c := New(srv.URL, "test-key")
	resp, err := c.SubmitSession(context.Background(), SubmitSessionRequest{
		SessionID:      "01HXY...",
		RepoURL:        "https://github.com/acme/app",
		Branch:         "main",
		TaskSpec:       "ship the thing",
		GovernanceTier: "community",
		Config:         SessionConfig{Model: "claude-sonnet-4-6", Verify: true, MaxTurns: 12},
	})
	if err != nil {
		t.Fatalf("SubmitSession: %v", err)
	}
	if resp.SessionID != "01HXY..." || resp.Status != "queued" {
		t.Errorf("unexpected response: %+v", resp)
	}
	found := false
	for _, h := range *hits {
		if h == "POST /v1/sessions" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected POST /v1/sessions, got %v", *hits)
	}
}

func TestSubmitSession_Unauthorized(t *testing.T) {
	srv, _ := stokeCloudMux(t)
	defer srv.Close()
	c := New(srv.URL, "wrong-key")
	_, err := c.SubmitSession(context.Background(), SubmitSessionRequest{SessionID: "x"})
	if err == nil {
		t.Fatal("expected unauthorized error")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected 401 in error, got: %v", err)
	}
}

func TestGetSession(t *testing.T) {
	srv, _ := stokeCloudMux(t)
	defer srv.Close()
	c := New(srv.URL, "test-key")
	status, err := c.GetSession(context.Background(), "sess-42")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if status.SessionID != "sess-42" || status.Status != "executing" {
		t.Errorf("unexpected status: %+v", status)
	}
}

func TestGetSessionEvents_WithSince(t *testing.T) {
	srv, hits := stokeCloudMux(t)
	defer srv.Close()
	c := New(srv.URL, "test-key")
	since := time.Date(2026, 4, 16, 9, 0, 0, 0, time.UTC)
	events, err := c.GetSessionEvents(context.Background(), "sess-1", since)
	if err != nil {
		t.Fatalf("GetSessionEvents: %v", err)
	}
	if len(events.Events) != 1 || events.Events[0].EventType != "phase.plan.started" {
		t.Errorf("unexpected events: %+v", events)
	}
	// Assert the since param was encoded into the URL.
	foundSince := false
	for _, h := range *hits {
		if strings.Contains(h, "/events") {
			foundSince = true
		}
	}
	if !foundSince {
		t.Errorf("expected /events hit, got %v", *hits)
	}
}

func TestConfig_SaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "cloud.json")
	t.Setenv("R1_CLOUD_CONFIG", path)

	cfg := &ConfigFile{
		Endpoint: "https://cloud.stoke.dev",
		APIKey:   "abc",
		UserID:   "u",
		OrgID:    "o",
		Status:   "active",
	}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Permission: API key must not be world-readable.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm()&0o044 != 0 {
		t.Errorf("cloud.json group/other readable: %v", info.Mode())
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got == nil {
		t.Fatal("Load returned nil")
	}
	if got.APIKey != "abc" || got.UserID != "u" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}

func TestLoad_MissingFileReturnsNilNoError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("R1_CLOUD_CONFIG", filepath.Join(dir, "does-not-exist.json"))
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load on missing file should not error: %v", err)
	}
	if cfg != nil {
		t.Errorf("expected nil config when file absent, got %+v", cfg)
	}
}

func TestIsLinked(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cloud.json")
	t.Setenv("R1_CLOUD_CONFIG", path)

	if IsLinked() {
		t.Error("IsLinked should be false when file absent")
	}
	_ = Save(&ConfigFile{Endpoint: "", APIKey: ""}) // empty
	if IsLinked() {
		t.Error("IsLinked should be false with empty fields")
	}
	_ = Save(&ConfigFile{Endpoint: "https://x", APIKey: "y"})
	if !IsLinked() {
		t.Error("IsLinked should be true with both fields set")
	}
}

func TestFromConfig_Errors(t *testing.T) {
	if _, err := FromConfig(nil); err == nil {
		t.Error("expected error on nil config")
	}
	if _, err := FromConfig(&ConfigFile{Endpoint: "https://x"}); err == nil {
		t.Error("expected error on missing api key")
	}
	if _, err := FromConfig(&ConfigFile{APIKey: "x"}); err == nil {
		t.Error("expected error on missing endpoint")
	}
	c, err := FromConfig(&ConfigFile{Endpoint: "https://x", APIKey: "y"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if c.Endpoint != "https://x" || c.APIKey != "y" {
		t.Errorf("client fields wrong: %+v", c)
	}
}
