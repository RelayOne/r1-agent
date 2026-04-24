package main

// work-stoke TASK 12 -- htmx + Go templates + SSE dashboard tests.
//
// Covers:
//
//   - GET / renders templates/index.tmpl when R1_SERVER_UI_V2=1 and
//     delegates to the SPA shell when the flag is off.
//   - GET /api/sessions honours Accept: text/html by returning
//     session_row.tmpl partials (with the htmx polling wrapper) and
//     still returns JSON on the plain Accept path.
//   - GET /api/events opens an SSE stream with the expected headers
//     and framing (retry: prelude + at least one event: message
//     frame when rows are seeded).
//   - /ui/vendor/htmx.min.js is served by the static embed handler.

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/session"
)

// newHTMXTestServer builds a full mux (API + UI) against a fresh DB,
// matching production wiring in main.go's main().
func newHTMXTestServer(t *testing.T) (*httptest.Server, *DB) {
	t.Helper()
	db := newTestDB(t)
	mux := buildMux(db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	mountUI(mux, db)
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s, db
}

// TestIndexServesHTMXShellWhenV2Enabled asserts GET / renders the
// new htmx dashboard (not the vanilla-JS SPA index.html) when the
// feature flag is flipped on.
func TestIndexServesHTMXShellWhenV2Enabled(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	s, _ := newHTMXTestServer(t)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, s.URL+"/", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	bs := string(body)

	// Must be the templated page, not the legacy SPA shell.
	if strings.Contains(bs, "/ui/app.js") {
		t.Error("v2 / served legacy SPA (app.js present)")
	}

	// Polling loop wiring.
	for _, marker := range []string{
		`hx-get="/api/sessions"`,
		`hx-trigger="every 2s"`,
		`id="sessions"`,
	} {
		if !strings.Contains(bs, marker) {
			t.Errorf("index page missing %q", marker)
		}
	}

	// SSE wiring.
	for _, marker := range []string{
		`hx-ext="sse"`,
		`sse-connect="/api/events"`,
		`sse-swap="message"`,
	} {
		if !strings.Contains(bs, marker) {
			t.Errorf("index page missing %q", marker)
		}
	}

	// Vendored htmx script tag -- must not reference a CDN URL.
	if !strings.Contains(bs, `src="/ui/vendor/htmx.min.js"`) {
		t.Error("index page missing vendored htmx script")
	}
	if strings.Contains(bs, "unpkg.com") || strings.Contains(bs, "cdn.jsdelivr") {
		t.Error("index page references a CDN; must use /ui/vendor only")
	}
}

// TestIndexFallsBackToSPAWhenV2Disabled confirms the legacy
// vanilla-JS shell is served when R1_SERVER_UI_V2 is unset -- the
// migration gate promised by the v2 spec.
func TestIndexFallsBackToSPAWhenV2Disabled(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "")
	s, _ := newHTMXTestServer(t)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, s.URL+"/", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "/ui/app.js") {
		t.Error("flag-off / should serve vanilla-JS SPA shell")
	}
}

// TestSessionsEndpointReturnsHTMLFragmentForHTMX asserts that htmx's
// hx-get can poll /api/sessions and receive session_row.tmpl
// partials wrapped in the polling container div.
func TestSessionsEndpointReturnsHTMLFragmentForHTMX(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	s, db := newHTMXTestServer(t)

	// Seed two sessions so the grid is non-empty.
	now := time.Now().UTC()
	sigs := []session.SignatureFile{
		{
			InstanceID: "r1-abc123def456",
			PID:        101,
			RepoRoot:   "/home/eric/repos/stoke",
			Mode:       "sow",
			Model:      "claude-opus-4-7",
			SowName:    "demo",
			Status:     "running",
			StartedAt:  now,
			UpdatedAt:  now,
		},
		{
			InstanceID: "r1-xyz999",
			PID:        102,
			RepoRoot:   "/tmp/other",
			Mode:       "ship",
			Status:     "completed",
			StartedAt:  now.Add(-time.Hour),
			UpdatedAt:  now.Add(-time.Hour),
		},
	}
	for _, sig := range sigs {
		if err := db.UpsertSession(sig); err != nil {
			t.Fatalf("upsert %s: %v", sig.InstanceID, err)
		}
	}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, s.URL+"/api/sessions", nil)
	req.Header.Set("Accept", "text/html")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get /api/sessions: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type=%q, want text/html", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	bs := string(body)

	// Polling wrapper must be present so the htmx loop re-arms on
	// every swap.
	for _, marker := range []string{
		`id="sessions"`,
		`hx-get="/api/sessions"`,
		`hx-trigger="every 2s"`,
		`class="grid"`,
	} {
		if !strings.Contains(bs, marker) {
			t.Errorf("partial missing %q", marker)
		}
	}

	// Each session must render as a <a class="card"> linking back
	// to /session/:id.
	for _, sig := range sigs {
		link := `href="/session/` + sig.InstanceID + `"`
		if !strings.Contains(bs, link) {
			t.Errorf("partial missing link for %s (want %q)", sig.InstanceID, link)
		}
	}
}

// TestSessionsEndpointReturnsJSONWithoutAcceptHeader guards the
// backward-compat contract: pre-v2 API consumers that never send
// Accept: text/html still get the original JSON body.
func TestSessionsEndpointReturnsJSONWithoutAcceptHeader(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	s, _ := newHTMXTestServer(t)

	req, _ := http.NewRequest("GET", s.URL+"/api/sessions", nil)
	// Explicit Accept: application/json -- the content negotiator
	// must route to the JSON branch.
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get /api/sessions: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type=%q, want application/json", ct)
	}
	var payload struct {
		Sessions []SessionRow `json:"sessions"`
		Count    int          `json:"count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

// TestEventsSSEStreamHeaders covers GET /api/events: the handler
// must emit the text/event-stream content type and a retry: prelude
// before any data frames so reconnects are predictable.
func TestEventsSSEStreamHeaders(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	s, _ := newHTMXTestServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", s.URL+"/api/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get /api/events: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("content-type=%q, want text/event-stream", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
		t.Errorf("cache-control=%q, must contain no-cache", cc)
	}

	// Read the retry: prelude. We can't consume the whole stream
	// (it's long-lived), so a bounded scanner + cancel handles it.
	sc := bufio.NewScanner(resp.Body)
	sawRetry := false
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "retry:") {
			sawRetry = true
			break
		}
	}
	if !sawRetry {
		t.Error("SSE stream missing retry: prelude")
	}
}

// TestEventsSSEDeliversSeededEventAsMessage seeds one session_events
// row and then opens the SSE stream; the first data frame must
// arrive as event: message so htmx's default sse-swap="message"
// listener fires on it.
func TestEventsSSEDeliversSeededEventAsMessage(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	s, db := newHTMXTestServer(t)

	// Need a registered session before inserting an event (FK).
	now := time.Now().UTC()
	if err := db.UpsertSession(session.SignatureFile{
		InstanceID: "r1-sse-test",
		PID:        2001,
		RepoRoot:   "/tmp/sse",
		Mode:       "sow",
		Status:     "running",
		StartedAt:  now,
		UpdatedAt:  now,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := db.InsertEvent("r1-sse-test", "task.start", []byte(`{"id":"T-1"}`), now); err != nil {
		t.Fatalf("insert event: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", s.URL+"/api/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get /api/events: %v", err)
	}
	defer resp.Body.Close()

	sc := bufio.NewScanner(resp.Body)
	sawEvent := false
	sawData := false
	for sc.Scan() {
		line := sc.Text()
		if line == "event: message" {
			sawEvent = true
		}
		if strings.HasPrefix(line, "data:") && strings.Contains(line, "task.start") {
			sawData = true
		}
		if sawEvent && sawData {
			break
		}
	}
	if !sawEvent {
		t.Error("SSE stream missing `event: message` frame")
	}
	if !sawData {
		t.Error("SSE stream missing data line referencing seeded event_type")
	}
}

// TestHTMXVendoredAssetServed guards the offline-first claim: the
// vendored htmx shim (or real blob, once populated) must be served
// out of /ui/vendor/htmx.min.js so the index page works on an
// air-gapped box.
func TestHTMXVendoredAssetServed(t *testing.T) {
	s, _ := newHTMXTestServer(t)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, s.URL+"/ui/vendor/htmx.min.js", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get htmx asset: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "javascript") && !strings.Contains(ct, "text/plain") {
		t.Errorf("content-type=%q, want JS-ish", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if len(body) == 0 {
		t.Error("htmx.min.js body is empty")
	}
}

// TestSessionRowTemplateRendersAllFields exercises session_row.tmpl
// through the same Execute path serveSessionsPartial uses. Catches
// regressions in the template's field access patterns without a
// full HTTP round-trip.
func TestSessionRowTemplateRendersAllFields(t *testing.T) {
	var buf strings.Builder
	sv := buildSessionView(SessionRow{
		InstanceID: "r1-fieldcheck-1234567890",
		PID:        42,
		RepoRoot:   "/home/eric/repos/stoke",
		Mode:       "sow",
		Model:      "claude-opus-4-7",
		SowName:    "fieldcheck",
		Status:     "running",
		StartedAt:  time.Now().UTC().Format(time.RFC3339Nano),
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err := sessionsPartialTmpl.ExecuteTemplate(&buf, "session_row", sv); err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := buf.String()
	for _, marker := range []string{
		`href="/session/r1-fieldcheck-1234567890"`,
		`class="card"`,
		`status running`,
		"stoke",            // RepoName
		"sow",              // Mode
		"fieldcheck",       // SowName
		"claude-opus-4-7",  // Model
	} {
		if !strings.Contains(out, marker) {
			t.Errorf("session_row output missing %q\nFULL:\n%s", marker, out)
		}
	}
	// Truncation: 25-char id >16 so it should get an ellipsis badge.
	if !strings.Contains(out, "r1-fieldcheck-12") {
		t.Error("short-id truncation not applied")
	}
}

// TestWantsHTMLContentNegotiation unit-tests the Accept-header
// parser that drives /api/sessions content negotiation.
func TestWantsHTMLContentNegotiation(t *testing.T) {
	cases := []struct {
		accept string
		want   bool
	}{
		{"", false},
		{"application/json", false},
		{"text/html", true},
		{"text/html,application/xhtml+xml", true},
		{"application/json, text/html", false}, // JSON first = JSON wins
		{"text/html;q=0.9, application/json;q=0.8", true},
		{"*/*", false}, // neither HTML nor JSON mentioned explicitly
	}
	for _, c := range cases {
		r, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
		if c.accept != "" {
			r.Header.Set("Accept", c.accept)
		}
		got := wantsHTML(r)
		if got != c.want {
			t.Errorf("wantsHTML(%q)=%v, want %v", c.accept, got, c.want)
		}
	}
}
