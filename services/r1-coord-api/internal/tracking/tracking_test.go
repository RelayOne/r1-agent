package tracking

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPostHogCaptureAndIdentifySendsBody(t *testing.T) {
	bodies := []map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		bodies = append(bodies, m)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ph := NewPostHog("phc_test_xyz_long_enough", srv.URL)
	ph.Now = func() time.Time { return time.Unix(1_000_000_000, 0) }

	// Capture: assert event + distinct_id round-trip.
	if err := ph.Capture(context.Background(), "user-1", "page_view", map[string]any{"path": "/dash"}); err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if bodies[0]["event"] != "page_view" {
		t.Errorf("Capture event=%v", bodies[0]["event"])
	}
	if bodies[0]["distinct_id"] != "user-1" {
		t.Errorf("Capture distinct_id=%v", bodies[0]["distinct_id"])
	}

	// Identify: assert $identify event + $set trait round-trip.
	if err := ph.Identify(context.Background(), "user-1", map[string]any{"plan": "pro"}); err != nil {
		t.Fatalf("Identify: %v", err)
	}
	if bodies[1]["event"] != "$identify" {
		t.Errorf("Identify event=%v", bodies[1]["event"])
	}
	props := bodies[1]["properties"].(map[string]any)
	set := props["$set"].(map[string]any)
	if set["plan"] != "pro" {
		t.Errorf("Identify $set.plan=%v", set["plan"])
	}
}

func TestPostHogNoopWhenAPIKeyEmpty(t *testing.T) {
	ph := NewPostHog("", "https://example.com")
	if ph.Enabled() {
		t.Errorf("expected disabled when API key empty")
	}
	if err := ph.Capture(context.Background(), "u", "e", nil); err != nil {
		t.Errorf("noop should not error, got %v", err)
	}
}

func TestPostHogReturnsErrorOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer srv.Close()
	ph := NewPostHog("phc_test_xyz_long_enough", srv.URL)
	err := ph.Capture(context.Background(), "u", "e", nil)
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Errorf("expected 400 in error, got %v", err)
	}
}

func TestCustomerIOIdentifyUsesPUTAndBasicAuth(t *testing.T) {
	var method, auth string
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		auth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	cio := NewCustomerIO("site-1", "key-1", "us")
	cio.BaseURL = srv.URL
	if err := cio.Identify(context.Background(), "user-1", map[string]any{"email": "u@e.com"}); err != nil {
		t.Fatalf("Identify: %v", err)
	}
	if method != http.MethodPut {
		t.Errorf("method=%s want PUT", method)
	}
	if !strings.HasPrefix(auth, "Basic ") {
		t.Errorf("Authorization=%q want Basic prefix", auth)
	}
	if got["email"] != "u@e.com" {
		t.Errorf("email=%v", got["email"])
	}
}

func TestCustomerIOTrackUsesPOST(t *testing.T) {
	var method string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	cio := NewCustomerIO("site-1", "key-1", "us")
	cio.BaseURL = srv.URL
	if err := cio.Track(context.Background(), "user-1", "signup", nil); err != nil {
		t.Fatalf("Track: %v", err)
	}
	if method != http.MethodPost {
		t.Errorf("method=%s want POST", method)
	}
}

func TestCustomerIORegionEU(t *testing.T) {
	cio := NewCustomerIO("s", "k", "eu")
	if !strings.Contains(cio.BaseURL, "track-eu.customer.io") {
		t.Errorf("EU region BaseURL=%q want track-eu.customer.io", cio.BaseURL)
	}
}

func TestCustomerIONoopWhenCredsEmpty(t *testing.T) {
	cio := NewCustomerIO("", "", "us")
	if cio.Enabled() {
		t.Errorf("expected disabled")
	}
	if err := cio.Track(context.Background(), "u", "e", nil); err != nil {
		t.Errorf("noop should not error, got %v", err)
	}
}

func TestCodeRadarParseDSNAcceptsFullURL(t *testing.T) {
	cr := NewCodeRadar("https://cr_key_123@api.coderadar.app", "r1-coord-api", "prod", "abc1234")
	if !cr.Enabled() {
		t.Fatalf("expected enabled when DSN valid")
	}
	if cr.APIKey != "cr_key_123" {
		t.Errorf("APIKey=%q", cr.APIKey)
	}
	if cr.BaseURL != "https://api.coderadar.app/v1" {
		t.Errorf("BaseURL=%q", cr.BaseURL)
	}
}

func TestCodeRadarParseDSNAcceptsBareKey(t *testing.T) {
	cr := NewCodeRadar("cr_bare_456", "svc", "dev", "v")
	if !cr.Enabled() {
		t.Fatalf("expected enabled on bare key")
	}
	if cr.APIKey != "cr_bare_456" {
		t.Errorf("APIKey=%q", cr.APIKey)
	}
}

func TestCodeRadarParseDSNRejectsEmpty(t *testing.T) {
	cr := NewCodeRadar("", "svc", "dev", "v")
	if cr.Enabled() {
		t.Errorf("expected disabled on empty DSN")
	}
}

func TestCodeRadarCaptureErrorPostsToErrors(t *testing.T) {
	var path, key string
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		key = r.Header.Get("x-coderadar-key")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	cr := &CodeRadar{
		APIKey: "k", BaseURL: srv.URL, ServiceName: "r1-coord-api",
		Env: "prod", Version: "abc1234",
		HTTP: &http.Client{Timeout: 5 * time.Second}, Now: time.Now, enabled: true,
	}
	cr.CaptureError(context.Background(), errors.New("boom"), map[string]any{"endpoint": "/v1/license/verify"})
	if path != "/v1/errors" {
		t.Errorf("path=%q", path)
	}
	if key != "k" {
		t.Errorf("x-coderadar-key=%q", key)
	}
	if got["message"] != "boom" {
		t.Errorf("message=%v", got["message"])
	}
}

func TestCodeRadarTrackPostsAnalyticsBatchToTrack(t *testing.T) {
	var (
		method string
		path   string
		auth   string
		got    map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		path = r.URL.Path
		auth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	cr := &CodeRadar{
		APIKey:      "cr_track_key",
		BaseURL:     srv.URL + "/v1",
		ServiceName: "r1-coord-api",
		Env:         "dev",
		Version:     "abc1234",
		HTTP:        &http.Client{Timeout: 5 * time.Second},
		Now:         func() time.Time { return time.Unix(1_000_000_000, 0) },
		enabled:     true,
	}
	err := cr.Track(context.Background(), "anon_browser_user", "telemetry_opt_in", map[string]any{
		"source":          "settings_modal",
		"enabled":         true,
		"install_channel": "web",
		"session_id":      "browser-session-1",
		"device":          "browser",
		"user_agent":      "Mozilla/5.0",
		"region":          "ca-bc",
	})
	if err != nil {
		t.Fatalf("Track: %v", err)
	}
	if method != http.MethodPost {
		t.Fatalf("method=%s want POST", method)
	}
	if path != "/v1/track" {
		t.Fatalf("path=%q want /v1/track", path)
	}
	if auth != "Bearer cr_track_key" {
		t.Fatalf("Authorization=%q", auth)
	}
	events, ok := got["events"].([]any)
	if !ok || len(events) != 1 {
		t.Fatalf("events=%#v want single event batch", got["events"])
	}
	event, ok := events[0].(map[string]any)
	if !ok {
		t.Fatalf("event=%#v want object", events[0])
	}
	if event["event_name"] != "telemetry_opt_in" {
		t.Fatalf("event_name=%v", event["event_name"])
	}
	if event["distinct_id"] != "anon_browser_user" {
		t.Fatalf("distinct_id=%v", event["distinct_id"])
	}
	if event["session_id"] != "browser-session-1" {
		t.Fatalf("session_id=%v", event["session_id"])
	}
	if event["device"] != "browser" {
		t.Fatalf("device=%v", event["device"])
	}
	if event["user_agent"] != "Mozilla/5.0" {
		t.Fatalf("user_agent=%v", event["user_agent"])
	}
	if event["region"] != "ca-bc" {
		t.Fatalf("region=%v", event["region"])
	}
	if event["timestamp"] != "2001-09-09T01:46:40Z" {
		t.Fatalf("timestamp=%v", event["timestamp"])
	}
	properties, ok := event["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties=%#v want object", event["properties"])
	}
	if properties["source"] != "settings_modal" {
		t.Fatalf("properties.source=%v", properties["source"])
	}
	if properties["enabled"] != true {
		t.Fatalf("properties.enabled=%v", properties["enabled"])
	}
	if _, exists := properties["session_id"]; exists {
		t.Fatalf("properties.session_id should be omitted from analytics properties")
	}
}

func TestCodeRadarTrackNoopWhenDSNEmpty(t *testing.T) {
	cr := NewCodeRadar("", "svc", "dev", "v")
	if cr.Enabled() {
		t.Fatalf("expected disabled on empty DSN")
	}
	err := cr.Track(context.Background(), "user-1", "telemetry_opt_in", map[string]any{"enabled": true})
	if err != nil {
		t.Fatalf("disabled Track should not error, got %v", err)
	}
}
