package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHealthEndpoint_StatusOK verifies that GET /health returns HTTP 200.
func TestHealthEndpoint_StatusOK(t *testing.T) {
	mux := http.NewServeMux()
	SetupRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /health: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestHealthEndpoint_ContentType verifies the Content-Type is application/json.
func TestHealthEndpoint_ContentType(t *testing.T) {
	mux := http.NewServeMux()
	SetupRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Fatalf("GET /health: expected Content-Type application/json, got %q", ct)
	}
}

// TestHealthEndpoint_JSONBody verifies the response body is {"status":"ok"}.
func TestHealthEndpoint_JSONBody(t *testing.T) {
	mux := http.NewServeMux()
	SetupRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("GET /health: failed to decode JSON response: %v", err)
	}

	status, ok := body["status"]
	if !ok {
		t.Fatal("GET /health: response JSON missing \"status\" key")
	}
	if status != "ok" {
		t.Fatalf("GET /health: expected status \"ok\", got %q", status)
	}

	// Ensure no extra keys in the response.
	if len(body) != 1 {
		t.Fatalf("GET /health: expected exactly 1 key in response, got %d: %v", len(body), body)
	}
}

// TestUsersStillWorks verifies that adding /health did not break /api/users.
func TestUsersStillWorks(t *testing.T) {
	mux := http.NewServeMux()
	SetupRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/users: expected 200, got %d", rec.Code)
	}
}
