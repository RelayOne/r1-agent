package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebFetchBasic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(200)
		w.Write([]byte(`<html><head><title>Test Page</title></head><body><p>Hello world</p></body></html>`)) //nolint:errcheck
	}))
	defer srv.Close()

	reg := NewRegistry(t.TempDir())
	result, err := reg.Handle(context.Background(), "web_fetch", toJSON(map[string]string{"url": srv.URL}))
	if err != nil {
		t.Fatalf("web_fetch returned error: %v", err)
	}
	if !strings.Contains(result, "Hello world") {
		t.Errorf("result should contain page text, got: %s", result)
	}
	if !strings.Contains(result, "Test Page") {
		t.Errorf("result should contain page title, got: %s", result)
	}
	if !strings.Contains(result, "200") {
		t.Errorf("result should contain HTTP status 200, got: %s", result)
	}
}

func TestWebFetchEmptyURL(t *testing.T) {
	reg := NewRegistry(t.TempDir())
	_, err := reg.Handle(context.Background(), "web_fetch", toJSON(map[string]string{"url": ""}))
	if err == nil {
		t.Error("web_fetch with empty url should return error")
	}
	if !strings.Contains(err.Error(), "url is required") {
		t.Errorf("error should mention url is required, got: %v", err)
	}
}

func TestWebFetchTimeout(t *testing.T) {
	// A server that blocks forever — fetch with 1ms timeout should fail gracefully.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until client disconnects.
		<-r.Context().Done()
	}))
	defer srv.Close()

	reg := NewRegistry(t.TempDir())
	result, err := reg.Handle(context.Background(), "web_fetch",
		toJSON(map[string]interface{}{"url": srv.URL, "timeout": 1}))
	// Should not return a Go error — graceful degradation returns error text in result.
	if err != nil {
		t.Fatalf("web_fetch timeout should return graceful result, not error: %v", err)
	}
	if !strings.Contains(result, "web_fetch error") {
		t.Errorf("result should contain web_fetch error message, got: %s", result)
	}
}

func TestWebFetchHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte("not found")) //nolint:errcheck
	}))
	defer srv.Close()

	reg := NewRegistry(t.TempDir())
	result, err := reg.Handle(context.Background(), "web_fetch", toJSON(map[string]string{"url": srv.URL}))
	if err != nil {
		t.Fatalf("web_fetch with 404 should not return Go error: %v", err)
	}
	// 4xx is not a Go error — status code is in the result.
	if !strings.Contains(result, "404") {
		t.Errorf("result should contain status 404, got: %s", result)
	}
}

func TestWebSearchNoProvider(t *testing.T) {
	// Without TAVILY_API_KEY or WEBSEARCH_COMMAND, should return informational string.
	t.Setenv("TAVILY_API_KEY", "")
	t.Setenv("WEBSEARCH_COMMAND", "")

	reg := NewRegistry(t.TempDir())
	result, err := reg.Handle(context.Background(), "web_search", toJSON(map[string]string{"query": "golang testing"}))
	if err != nil {
		t.Fatalf("web_search without provider should not error: %v", err)
	}
	if !strings.Contains(result, "unavailable") {
		t.Errorf("result should say unavailable, got: %s", result)
	}
}

func TestWebSearchEmptyQuery(t *testing.T) {
	reg := NewRegistry(t.TempDir())
	_, err := reg.Handle(context.Background(), "web_search", toJSON(map[string]string{"query": ""}))
	if err == nil {
		t.Error("web_search with empty query should return error")
	}
	if !strings.Contains(err.Error(), "query is required") {
		t.Errorf("error should mention query is required, got: %v", err)
	}
}
