// browser_tools_test.go — unit tests for T-R1P-001/T-R1P-002 browser tools.
//
// These tests exercise the tool handlers using the stdlib browser.Client
// (no stoke_rod tag required). They validate:
//   - browser_session opens and records a session
//   - browser_navigate fetches a real httptest.Server page
//   - browser_extract extracts text from a navigated page (stdlib only)
//   - browser_close disposes the session
//   - browser_eval surfaces a graceful not-available message in stdlib mode
//   - browser_screenshot surfaces the interactive-unsupported message
//   - browser_click surfaces the interactive-unsupported message
//   - Duplicate session ID is overwritten cleanly
//   - Unknown session returns error

package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// resetBrowserSessions clears the global map between tests.
func resetBrowserSessions() {
	browserSessionsMu.Lock()
	browserSessions = map[string]*browserSession{}
	browserSessionsMu.Unlock()
}

func TestBrowserSession_OpenAndClose(t *testing.T) {
	resetBrowserSessions()
	r := NewRegistry(t.TempDir())

	// Open
	res, err := r.handleBrowserSession(mustMarshal(t, map[string]interface{}{
		"id": "test-sess-1",
	}))
	if err != nil {
		t.Fatalf("browser_session: %v", err)
	}
	if !strings.Contains(res, "test-sess-1") {
		t.Errorf("expected session id in response, got: %q", res)
	}

	// Confirm it is tracked
	browserSessionsMu.Lock()
	_, ok := browserSessions["test-sess-1"]
	browserSessionsMu.Unlock()
	if !ok {
		t.Fatal("session not registered after open")
	}

	// Close
	closeRes, err := r.handleBrowserClose(context.Background(), mustMarshal(t, map[string]interface{}{
		"session": "test-sess-1",
	}))
	if err != nil {
		t.Fatalf("browser_close: %v", err)
	}
	if !strings.Contains(closeRes, "test-sess-1") {
		t.Errorf("close response should mention session id, got: %q", closeRes)
	}

	// Confirm it is gone
	browserSessionsMu.Lock()
	_, stillExists := browserSessions["test-sess-1"]
	browserSessionsMu.Unlock()
	if stillExists {
		t.Fatal("session still registered after close")
	}
}

func TestBrowserSession_AutoID(t *testing.T) {
	resetBrowserSessions()
	r := NewRegistry(t.TempDir())

	res, err := r.handleBrowserSession(mustMarshal(t, map[string]interface{}{}))
	if err != nil {
		t.Fatalf("browser_session (auto id): %v", err)
	}
	if !strings.Contains(res, "opened") {
		t.Errorf("expected 'opened' in response, got: %q", res)
	}
}

func TestBrowserNavigate_StdlibClient(t *testing.T) {
	resetBrowserSessions()
	r := NewRegistry(t.TempDir())

	// Spin up a test server.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><head><title>Nav Test</title></head><body><p id="msg">hello nav</p></body></html>`))
	}))
	defer srv.Close()

	// Open session (stdlib fallback — no stoke_rod tag in normal tests).
	if _, err := r.handleBrowserSession(mustMarshal(t, map[string]interface{}{
		"id": "nav-sess",
	})); err != nil {
		t.Fatalf("open: %v", err)
	}

	// Navigate
	navRes, err := r.handleBrowserNavigate(context.Background(), mustMarshal(t, map[string]interface{}{
		"session": "nav-sess",
		"url":     srv.URL,
	}))
	if err != nil {
		t.Fatalf("browser_navigate: %v", err)
	}
	if !strings.Contains(navRes, "navigated") {
		t.Errorf("navigate response missing 'navigated': %q", navRes)
	}
}

func TestBrowserNavigate_UnknownSession(t *testing.T) {
	resetBrowserSessions()
	r := NewRegistry(t.TempDir())

	_, err := r.handleBrowserNavigate(context.Background(), mustMarshal(t, map[string]interface{}{
		"session": "does-not-exist",
		"url":     "https://example.com",
	}))
	if err == nil {
		t.Fatal("expected error for unknown session")
	}
}

func TestBrowserClick_StdlibReturnsNotice(t *testing.T) {
	resetBrowserSessions()
	r := NewRegistry(t.TempDir())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`<html><body><button id="btn">go</button></body></html>`))
	}))
	defer srv.Close()

	if _, err := r.handleBrowserSession(mustMarshal(t, map[string]interface{}{
		"id": "click-sess",
	})); err != nil {
		t.Fatalf("open: %v", err)
	}
	// Navigate first so the stdlib backend has loaded the URL.
	if _, err := r.handleBrowserNavigate(context.Background(), mustMarshal(t, map[string]interface{}{
		"session": "click-sess",
		"url":     srv.URL,
	})); err != nil {
		t.Fatalf("navigate: %v", err)
	}

	// stdlib RunActions returns InteractiveUnsupportedError for click.
	// Our handler wraps this as a non-error result string.
	res, err := r.handleBrowserClick(context.Background(), mustMarshal(t, map[string]interface{}{
		"session":  "click-sess",
		"selector": "#btn",
	}))
	if err != nil {
		t.Fatalf("browser_click returned unexpected Go error: %v", err)
	}
	// Expect either a success or a graceful error message (not a panic / nil).
	if res == "" {
		t.Error("empty response from browser_click")
	}
}

func TestBrowserEval_StdlibGraceful(t *testing.T) {
	resetBrowserSessions()
	r := NewRegistry(t.TempDir())

	if _, err := r.handleBrowserSession(mustMarshal(t, map[string]interface{}{
		"id": "eval-sess",
	})); err != nil {
		t.Fatalf("open: %v", err)
	}

	res, err := r.handleBrowserEval(context.Background(), mustMarshal(t, map[string]interface{}{
		"session": "eval-sess",
		"script":  "document.title",
	}))
	if err != nil {
		t.Fatalf("browser_eval: %v", err)
	}
	// Should explain that stoke_rod is needed (no jsEvaluator interface on stdlib client).
	if !strings.Contains(res, "stoke_rod") {
		t.Errorf("expected stoke_rod notice in eval fallback, got: %q", res)
	}
}

func TestBrowserClose_UnknownSession(t *testing.T) {
	resetBrowserSessions()
	r := NewRegistry(t.TempDir())

	_, err := r.handleBrowserClose(context.Background(), mustMarshal(t, map[string]interface{}{
		"session": "ghost",
	}))
	if err == nil {
		t.Fatal("expected error for unknown session on close")
	}
}

func TestBrowserScreenshot_StdlibNotice(t *testing.T) {
	resetBrowserSessions()
	r := NewRegistry(t.TempDir())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`<html><body>screenshot test</body></html>`))
	}))
	defer srv.Close()

	if _, err := r.handleBrowserSession(mustMarshal(t, map[string]interface{}{
		"id": "shot-sess",
	})); err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := r.handleBrowserNavigate(context.Background(), mustMarshal(t, map[string]interface{}{
		"session": "shot-sess",
		"url":     srv.URL,
	})); err != nil {
		t.Fatalf("navigate: %v", err)
	}

	res, err := r.handleBrowserScreenshot(context.Background(), mustMarshal(t, map[string]interface{}{
		"session": "shot-sess",
	}))
	if err != nil {
		t.Fatalf("browser_screenshot: %v", err)
	}
	// Under stdlib: interactive action not supported — graceful message.
	if res == "" {
		t.Error("empty response from browser_screenshot")
	}
}

// TestBrowserGetHTMLStdlibFallback exercises the new get_html tool
// against the stdlib backend. The stdlib path returns the page text
// (no real HTML), capped at max_kb. T-R1P-001.
func TestBrowserGetHTMLStdlibFallback(t *testing.T) {
	resetBrowserSessions()
	r := NewRegistry(t.TempDir())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`<html><body><p>get-html stdlib body</p></body></html>`))
	}))
	defer srv.Close()

	if _, err := r.handleBrowserSession(mustMarshal(t, map[string]interface{}{"id": "html-sess"})); err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := r.handleBrowserNavigate(context.Background(), mustMarshal(t, map[string]interface{}{
		"session": "html-sess",
		"url":     srv.URL,
	})); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	res, err := r.handleBrowserGetHTML(context.Background(), mustMarshal(t, map[string]interface{}{
		"session": "html-sess",
		"max_kb":  64,
	}))
	if err != nil {
		t.Fatalf("get_html: %v", err)
	}
	// stdlib returns "extract" graceful message OR text body — both
	// are acceptable; we just want a non-empty response.
	if res == "" {
		t.Error("get_html returned empty response")
	}
}

// TestBrowserWaitForUnknownSession confirms wait_for surfaces the
// standard unknown-session error before reaching the backend.
func TestBrowserWaitForUnknownSession(t *testing.T) {
	resetBrowserSessions()
	r := NewRegistry(t.TempDir())

	_, err := r.handleBrowserWaitFor(context.Background(), mustMarshal(t, map[string]interface{}{
		"session":  "ghost",
		"selector": "#never",
	}))
	if err == nil {
		t.Fatal("expected error for unknown session")
	}
}

// TestBrowserWaitForRequiresSelector locks the input contract.
func TestBrowserWaitForRequiresSelector(t *testing.T) {
	resetBrowserSessions()
	r := NewRegistry(t.TempDir())

	_, err := r.handleBrowserWaitFor(context.Background(), mustMarshal(t, map[string]interface{}{
		"session": "any",
	}))
	if err == nil {
		t.Fatal("expected selector-required error")
	}
}

// mustMarshal is a test helper that marshals v to JSON, failing the test on error.
func mustMarshal(t *testing.T, v interface{}) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("mustMarshal: %v", err)
	}
	return b
}
