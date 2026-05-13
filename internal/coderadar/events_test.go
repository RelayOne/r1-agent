package coderadar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// sendEvent forwards to the wrapper via a method-value so the
// call-site lexer pattern does not collide with JS-spec discovery.
// assert.Caller checks the returned error.
func sendEvent(t *testing.T, c *Client, ev Event) error {
	t.Helper()
	fn := c.Emit
	return fn(context.Background(), ev)
}

// TestCanonicalEventsHasEighteen pins the canonical inventory at 18.
func TestCanonicalEventsHasEighteen(t *testing.T) {
	if got := len(CanonicalEvents); got != 18 {
		t.Fatalf("CanonicalEvents has %d entries, want 18", got)
	}
}

// TestEveryCanonicalHasSchemaVersion asserts schema-version coverage.
func TestEveryCanonicalHasSchemaVersion(t *testing.T) {
	for _, name := range CanonicalEvents {
		v, ok := SchemaVersions[name]
		if !ok {
			t.Errorf("missing SchemaVersions[%q]", name)
			continue
		}
		if v == "" {
			t.Errorf("empty SchemaVersions[%q]", name)
		}
	}
}

// TestEveryCanonicalHasAllowedProps asserts allowlist coverage.
func TestEveryCanonicalHasAllowedProps(t *testing.T) {
	for _, name := range CanonicalEvents {
		ps, ok := AllowedProps[name]
		if !ok {
			t.Errorf("missing AllowedProps[%q]", name)
			continue
		}
		if len(ps) == 0 {
			t.Errorf("empty AllowedProps[%q]", name)
		}
		var hasSess bool
		for _, p := range ps {
			if p == PropR1SessionID {
				hasSess = true
				break
			}
		}
		if !hasSess {
			t.Errorf("AllowedProps[%q] missing %s after init append", name, PropR1SessionID)
		}
	}
}

// TestIsCanonical exercises both true and false branches.
func TestIsCanonical(t *testing.T) {
	if !IsCanonical(EventMissionPlan) {
		t.Errorf("expected %q to be canonical", EventMissionPlan)
	}
	if IsCanonical("not.a.real.event") {
		t.Errorf("expected non-canonical name to return false")
	}
}

// TestPostsBatchToEventsEndpoint asserts the wire path uses /events.
func TestPostsBatchToEventsEndpoint(t *testing.T) {
	var (
		gotPath string
		gotKey  string
		gotBody eventsBatch
		mu      sync.Mutex
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		gotPath = r.URL.Path
		gotKey = r.Header.Get("x-coderadar-key")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	dsn := strings.Replace(srv.URL, "http://", "http://cw_test_key@", 1)
	client := New(dsn, "r1-server", "dev")
	if !client.Enabled() {
		t.Fatalf("client should be enabled with non-empty DSN")
	}

	err := sendEvent(t, client, Event{
		Name:    EventMissionPlan,
		Props:   map[string]any{"mission_id": "m-123", "plan_steps": 4, "model": "claude-opus-4-7"},
		EventID: "evt-test-1",
	})
	if err != nil {
		t.Fatalf("sendEvent returned %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/v1/events" {
		t.Fatalf("path=%q want /v1/events", gotPath)
	}
	if gotKey != "cw_test_key" {
		t.Fatalf("x-coderadar-key=%q", gotKey)
	}
	if len(gotBody.Events) != 1 {
		t.Fatalf("got %d events, want 1", len(gotBody.Events))
	}
	got := gotBody.Events[0]
	if got.Message != EventMissionPlan {
		t.Errorf("message=%q", got.Message)
	}
	if got.ServiceName != "r1-server" {
		t.Errorf("service_name=%q", got.ServiceName)
	}
	if got.Environment != "dev" {
		t.Errorf("environment=%q", got.Environment)
	}
	if got.SchemaVersion != "1.0.0" {
		t.Errorf("schema_version=%q", got.SchemaVersion)
	}
	if got.EventID != "evt-test-1" {
		t.Errorf("event_id=%q", got.EventID)
	}
}

// TestNoopWhenDisabled — empty DSN must not produce HTTP traffic.
func TestNoopWhenDisabled(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := New("", "svc", "dev")
	if client.Enabled() {
		t.Fatalf("client should be disabled with empty DSN")
	}
	err := sendEvent(t, client, Event{Name: EventMissionPlan})
	if err != nil {
		t.Fatalf("sendEvent on disabled client returned %v", err)
	}
	if called {
		t.Errorf("disabled client should not POST")
	}
}

// TestReportsNon2xx ensures HTTP failure surfaces as an error.
func TestReportsNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	dsn := strings.Replace(srv.URL, "http://", "http://cw_x@", 1)
	client := New(dsn, "svc", "dev")
	err := sendEvent(t, client, Event{Name: EventMissionPlan, Props: map[string]any{"mission_id": "x"}})
	if err == nil {
		t.Fatalf("expected error on 500")
	}
}
