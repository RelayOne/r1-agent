package coderadar

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseDSNURL(t *testing.T) {
	apiKey, baseURL, ok := parseDSN("https://cw_test_123@api.coderadar.app")
	if !ok {
		t.Fatalf("parseDSN returned ok=false")
	}
	if apiKey != "cw_test_123" {
		t.Fatalf("apiKey=%q", apiKey)
	}
	if baseURL != "https://api.coderadar.app/v1" {
		t.Fatalf("baseURL=%q", baseURL)
	}
}

func TestParseDSNRawKey(t *testing.T) {
	apiKey, baseURL, ok := parseDSN("cw_test_456")
	if !ok {
		t.Fatalf("parseDSN returned ok=false")
	}
	if apiKey != "cw_test_456" {
		t.Fatalf("apiKey=%q", apiKey)
	}
	if baseURL != "https://ingest.coderadar.app/v1" {
		t.Fatalf("baseURL=%q", baseURL)
	}
}

func TestCaptureErrorUsesDSNDerivedKeyAndBaseURL(t *testing.T) {
	var (
		gotPath    string
		gotAPIKey  string
		gotService string
		gotEnv     string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAPIKey = r.Header.Get("x-coderadar-key")
		var payload struct {
			ServiceName string `json:"service_name"`
			Environment string `json:"environment"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		gotService = payload.ServiceName
		gotEnv = payload.Environment
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	dsn := strings.Replace(srv.URL, "http://", "http://cw_test_789@", 1)
	client := New(dsn, "stoke", "test")
	if err := client.CaptureError(context.Background(), errors.New("boom"), map[string]any{"phase": "startup"}); err != nil {
		t.Fatalf("CaptureError: %v", err)
	}
	if gotPath != "/v1/errors" {
		t.Fatalf("path=%q", gotPath)
	}
	if gotAPIKey != "cw_test_789" {
		t.Fatalf("x-coderadar-key=%q", gotAPIKey)
	}
	if gotService != "stoke" {
		t.Fatalf("service_name=%q", gotService)
	}
	if gotEnv != "test" {
		t.Fatalf("environment=%q", gotEnv)
	}
}
