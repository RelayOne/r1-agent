package studioclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/config"
)

// testCfg returns a baseline StudioConfig that points at the given
// fake server. Helper used throughout.
func testCfg(baseURL, tokenEnv string) config.StudioConfig {
	return config.StudioConfig{
		Enabled:   true,
		Transport: config.StudioTransportHTTP,
		HTTP: config.StudioHTTPConfig{
			BaseURL:      baseURL,
			ScopesHeader: "studio:sites:scaffold",
			TokenEnv:     tokenEnv,
		},
	}
}

// captureEvents returns a publisher that appends everything to a slice
// plus a pointer so tests can inspect what landed.
func captureEvents() (EventPublisher, *[]InvocationEvent) {
	var out []InvocationEvent
	return PublisherFunc(func(ev InvocationEvent) {
		out = append(out, ev)
	}), &out
}

func TestHTTPTransport_InvokeSuccess_Scaffold(t *testing.T) {
	var gotAuth, gotScopes, gotMethod, gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotScopes = r.Header.Get("X-Studio-Scopes")
		gotMethod = r.Method
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"site_id":"s-1","status":"ready","steps":[]}`))
	}))
	t.Cleanup(srv.Close)

	t.Setenv("STUDIO_TEST_TOKEN", "tok-123")
	pub, events := captureEvents()
	cfg := testCfg(srv.URL, "STUDIO_TEST_TOKEN")
	tr, err := NewHTTPTransport(cfg, srv.Client(), pub)
	if err != nil {
		t.Fatalf("NewHTTPTransport: %v", err)
	}

	body, err := tr.Invoke(context.Background(), "studio.scaffold_site", map[string]any{
		"brief": "A landing page for ACME widgets",
		"brand": map[string]any{"name": "ACME"},
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	var parsed struct {
		SiteID string `json:"site_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if parsed.SiteID != "s-1" || parsed.Status != "ready" {
		t.Errorf("decoded body = %+v", parsed)
	}
	if gotAuth != "Bearer tok-123" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotScopes != "studio:sites:scaffold" {
		t.Errorf("X-Studio-Scopes = %q", gotScopes)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/studio/sites:scaffold" {
		t.Errorf("method/path = %s %s", gotMethod, gotPath)
	}
	if !strings.Contains(gotBody, "ACME") {
		t.Errorf("body lost brand: %q", gotBody)
	}
	if len(*events) != 1 || !(*events)[0].OK || (*events)[0].Tool != "studio.scaffold_site" {
		t.Errorf("events = %+v", *events)
	}
}

func TestHTTPTransport_InvokeSuccess_GetWithPathAndQuery(t *testing.T) {
	var gotURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"from":"a","to":"b","diff":[]}`))
	}))
	t.Cleanup(srv.Close)

	t.Setenv("STUDIO_TEST_TOKEN", "tok")
	pub, _ := captureEvents()
	tr, _ := NewHTTPTransport(testCfg(srv.URL, "STUDIO_TEST_TOKEN"), srv.Client(), pub)

	_, err := tr.Invoke(context.Background(), "studio.diff_versions", map[string]any{
		"siteId": "site-42",
		"from":   "snap-1",
		"to":     "snap-2",
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !strings.Contains(gotURL, "/api/sites/site-42/snapshots:diff") {
		t.Errorf("path missing siteId substitution: %s", gotURL)
	}
	if !strings.Contains(gotURL, "from=snap-1") || !strings.Contains(gotURL, "to=snap-2") {
		t.Errorf("query missing fields: %s", gotURL)
	}
}

func TestHTTPTransport_Error_401_AuthMapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad token"}`))
	}))
	t.Cleanup(srv.Close)

	t.Setenv("STUDIO_TEST_TOKEN", "tok")
	pub, events := captureEvents()
	tr, _ := NewHTTPTransport(testCfg(srv.URL, "STUDIO_TEST_TOKEN"), srv.Client(), pub)
	_, err := tr.Invoke(context.Background(), "studio.list_sites", nil)
	if !errors.Is(err, ErrStudioAuth) {
		t.Fatalf("err = %v, want ErrStudioAuth", err)
	}
	if len(*events) != 1 || (*events)[0].OK || (*events)[0].ErrorKind != "auth" {
		t.Errorf("events = %+v", *events)
	}
}

func TestHTTPTransport_Error_403_ScopeMapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("STUDIO_TEST_TOKEN", "tok")
	tr, _ := NewHTTPTransport(testCfg(srv.URL, "STUDIO_TEST_TOKEN"), srv.Client(), nil)
	_, err := tr.Invoke(context.Background(), "studio.list_sites", nil)
	if !errors.Is(err, ErrStudioScope) {
		t.Fatalf("err = %v, want ErrStudioScope", err)
	}
}

func TestHTTPTransport_Error_404_NotFoundMapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("STUDIO_TEST_TOKEN", "tok")
	tr, _ := NewHTTPTransport(testCfg(srv.URL, "STUDIO_TEST_TOKEN"), srv.Client(), nil)
	_, err := tr.Invoke(context.Background(), "studio.get_site", map[string]any{"siteId": "missing"})
	if !errors.Is(err, ErrStudioNotFound) {
		t.Fatalf("err = %v, want ErrStudioNotFound", err)
	}
}

func TestHTTPTransport_Error_400_ValidationMapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`brief too short`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("STUDIO_TEST_TOKEN", "tok")
	tr, _ := NewHTTPTransport(testCfg(srv.URL, "STUDIO_TEST_TOKEN"), srv.Client(), nil)
	_, err := tr.Invoke(context.Background(), "studio.scaffold_site", map[string]any{
		"brief": "short",
		"brand": map[string]any{"name": "X"},
	})
	if !errors.Is(err, ErrStudioValidation) {
		t.Fatalf("err = %v, want ErrStudioValidation", err)
	}
	var se *StudioError
	if !errors.As(err, &se) {
		t.Fatalf("err not *StudioError")
	}
	if !strings.Contains(se.BodyExcerpt, "brief too short") {
		t.Errorf("body excerpt lost: %q", se.BodyExcerpt)
	}
}

func TestHTTPTransport_Error_500_RetriesThenFails(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("STUDIO_TEST_TOKEN", "tok")
	tr, _ := NewHTTPTransport(testCfg(srv.URL, "STUDIO_TEST_TOKEN"), srv.Client(), nil)
	tr.Sleep = func(time.Duration) {} // no real backoff
	_, err := tr.Invoke(context.Background(), "studio.list_sites", nil)
	if !errors.Is(err, ErrStudioServer) {
		t.Fatalf("err = %v, want ErrStudioServer", err)
	}
	if atomic.LoadInt32(&hits) != int32(DefaultRetries) {
		t.Errorf("hits = %d, want %d (retries)", hits, DefaultRetries)
	}
}

func TestHTTPTransport_Error_500_RetriesThenSucceeds(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n < 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("STUDIO_TEST_TOKEN", "tok")
	tr, _ := NewHTTPTransport(testCfg(srv.URL, "STUDIO_TEST_TOKEN"), srv.Client(), nil)
	tr.Sleep = func(time.Duration) {}
	body, err := tr.Invoke(context.Background(), "studio.list_sites", nil)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if string(body) != "[]" {
		t.Errorf("body = %s", body)
	}
	if atomic.LoadInt32(&hits) != 2 {
		t.Errorf("hits = %d, want 2", hits)
	}
}

func TestHTTPTransport_TokenEnvUnset_401LikeError(t *testing.T) {
	// Server should never be reached — fail pre-flight.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server reached despite missing token")
	}))
	t.Cleanup(srv.Close)
	t.Setenv("STUDIO_TEST_TOKEN", "") // explicitly empty
	tr, _ := NewHTTPTransport(testCfg(srv.URL, "STUDIO_TEST_TOKEN"), srv.Client(), nil)
	_, err := tr.Invoke(context.Background(), "studio.list_sites", nil)
	if !errors.Is(err, ErrStudioAuth) {
		t.Fatalf("err = %v, want ErrStudioAuth", err)
	}
}

func TestHTTPTransport_Unreachable_UnavailableDegradation(t *testing.T) {
	// Point at an invalid URL — dial will fail immediately.
	cfg := testCfg("http://127.0.0.1:1", "")
	tr, err := NewHTTPTransport(cfg, &http.Client{Timeout: 500 * time.Millisecond}, nil)
	if err != nil {
		t.Fatalf("NewHTTPTransport: %v", err)
	}
	_, err = tr.Invoke(context.Background(), "studio.list_sites", nil)
	if !IsUnavailable(err) {
		t.Fatalf("IsUnavailable(%v) = false, want true (degradation)", err)
	}
}

func TestHTTPTransport_Timeout_TimeoutErrAndUnavailablePredicate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("STUDIO_TEST_TOKEN", "tok")
	cfg := testCfg(srv.URL, "STUDIO_TEST_TOKEN")
	tr, _ := NewHTTPTransport(cfg, srv.Client(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := tr.Invoke(ctx, "studio.list_sites", nil)
	if !errors.Is(err, ErrStudioTimeout) {
		t.Fatalf("err = %v, want ErrStudioTimeout", err)
	}
	if !IsUnavailable(err) {
		t.Fatalf("IsUnavailable(timeout) = false, want true")
	}
}

func TestHTTPTransport_UnknownTool_Validation(t *testing.T) {
	tr, _ := NewHTTPTransport(testCfg("http://example", "X"), nil, nil)
	_, err := tr.Invoke(context.Background(), "studio.does_not_exist", nil)
	if !errors.Is(err, ErrStudioValidation) {
		t.Fatalf("err = %v, want ErrStudioValidation", err)
	}
}

func TestHTTPTransport_MissingPathField_Validation(t *testing.T) {
	tr, _ := NewHTTPTransport(testCfg("http://example", "X"), nil, nil)
	_, err := tr.Invoke(context.Background(), "studio.get_site", map[string]any{})
	if !errors.Is(err, ErrStudioValidation) {
		t.Fatalf("err = %v, want ErrStudioValidation", err)
	}
	if !strings.Contains(err.Error(), "siteId") {
		t.Errorf("err missing siteId diagnostic: %v", err)
	}
}

func TestHTTPTransport_NewHTTPTransport_DisabledCfg(t *testing.T) {
	cfg := config.StudioConfig{Enabled: false}
	_, err := NewHTTPTransport(cfg, nil, nil)
	if !errors.Is(err, ErrStudioDisabled) {
		t.Fatalf("err = %v, want ErrStudioDisabled", err)
	}
}

func TestHTTPTransport_NewHTTPTransport_WrongTransport(t *testing.T) {
	cfg := config.StudioConfig{
		Enabled:   true,
		Transport: config.StudioTransportStdioMCP,
		StdioMCP:  config.StudioStdioMCPConfig{Command: []string{"x"}},
	}
	_, err := NewHTTPTransport(cfg, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "stdio-mcp") {
		t.Fatalf("err = %v, want wrong-transport diagnostic", err)
	}
}

func TestHTTPTransport_Name(t *testing.T) {
	tr := &HTTPTransport{}
	if tr.Name() != "http" {
		t.Errorf("Name = %q", tr.Name())
	}
	if err := tr.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestSanitizeBodyAndClassifyHTTPStatus(t *testing.T) {
	// sanitizeBody drops control chars and caps length.
	b := make([]byte, 700)
	for i := range b {
		b[i] = 'a' + byte(i%26)
	}
	b[0] = 0x01 // control
	b[10] = '\n'
	s := sanitizeBody(b)
	if len(s) > 512 {
		t.Errorf("sanitize length = %d, want ≤ 512", len(s))
	}
	if strings.ContainsRune(s, 0x01) {
		t.Errorf("control char survived")
	}
	// classifyHTTPStatus is exhaustive for the codes it cares about.
	cases := map[int]error{
		200: nil,
		204: nil,
		400: ErrStudioValidation,
		401: ErrStudioAuth,
		403: ErrStudioScope,
		404: ErrStudioNotFound,
		408: ErrStudioTimeout,
		422: ErrStudioValidation,
		500: ErrStudioServer,
		502: ErrStudioServer,
		504: ErrStudioTimeout,
	}
	for code, want := range cases {
		if got := classifyHTTPStatus(code); got != want {
			t.Errorf("classifyHTTPStatus(%d) = %v, want %v", code, got, want)
		}
	}
}

func TestToQueryStringScalars(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{"hello", "hello"},
		{true, "true"},
		{false, "false"},
		{float64(42), "42"},
		{float64(3.5), "3.5"},
		{int(7), "7"},
		{int64(8), "8"},
		{nil, ""},
	}
	for _, tc := range cases {
		if got := toQueryString(tc.in); got != tc.want {
			t.Errorf("toQueryString(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestDegradationStance simulates the §Degradation-stance path: Studio
// absent, session continues. A caller that wraps Invoke in the typed-
// error pattern below sees IsUnavailable==true and surfaces the
// actionable message.
func TestDegradationStance_StudioAbsent(t *testing.T) {
	tr, err := NewHTTPTransport(testCfg("http://127.0.0.1:1", ""), &http.Client{Timeout: 500 * time.Millisecond}, nil)
	if err != nil {
		t.Fatalf("NewHTTPTransport: %v", err)
	}
	_, err = tr.Invoke(context.Background(), "studio.list_sites", nil)
	if err == nil {
		t.Fatal("expected error when Studio is unreachable")
	}
	if !IsUnavailable(err) {
		t.Fatalf("IsUnavailable(%v) = false; degradation path broken", err)
	}
	// Demonstrate the caller-side shape an R1 skill wrapper would use:
	msg := surfaceToAgent(err)
	if !strings.Contains(msg, "not reachable") {
		t.Errorf("degraded message = %q, want reachability diagnostic", msg)
	}
}

// surfaceToAgent is the degradation-friendly translation used in tests
// and (elsewhere) by the skill dispatcher. Kept here so the test alone
// documents the required shape.
func surfaceToAgent(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrStudioDisabled):
		return "actium_studio_unavailable: pack disabled in studio_config"
	case IsUnavailable(err):
		return fmt.Sprintf("actium_studio_unavailable: Studio endpoint not reachable — check studio_config or disable this step (%v)", err)
	default:
		return err.Error()
	}
}
