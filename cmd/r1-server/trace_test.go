package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1-agent/internal/session"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// seedSession registers a session row so the trace handlers have a
// valid {id} to look up in session_events even when no events exist.
func seedSession(t *testing.T, db *DB, id string) {
	t.Helper()
	sig := session.SignatureFile{
		InstanceID: id,
		Status:     "running",
		StartedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
	}
	if err := db.UpsertSession(sig); err != nil {
		t.Fatalf("upsert session: %v", err)
	}
}

// seedTierEvent writes one tier-bearing NDJSON line directly via
// InsertEvent so the handler has real data to render. `data` is the
// raw JSON object body (includes "tier" + "acid" + "name" fields).
func seedTierEvent(t *testing.T, db *DB, id string, tier, acid, name string, durMs int64, ts time.Time) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"type":        "descent.tier",
		"tier":        tier,
		"acid":        acid,
		"name":        name,
		"duration_ms": durMs,
		"started_at":  ts.Format(time.RFC3339Nano),
		"ended_at":    ts.Add(time.Duration(durMs) * time.Millisecond).Format(time.RFC3339Nano),
		"ts":          ts.Format(time.RFC3339Nano),
	})
	if err := db.InsertEvent(id, "descent.tier", body, ts); err != nil {
		t.Fatalf("insert event: %v", err)
	}
}

// TestTierColor pins the FuncMap's tierColor behavior. The waterfall
// template relies on this emitting "tier-tN" for any N in 1..8 and
// "tier-unknown" otherwise — see templates/trace_waterfall.tmpl.
func TestTierColor(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"T1", "tier-t1"},
		{"T8", "tier-t8"},
		{"t4", "tier-t4"},
		{"T4-code-repair", "tier-t4"},
		{"tier-3", "tier-t3"},
		{"", "tier-unknown"},
		{"T9", "tier-unknown"},
		{"garbage", "tier-unknown"},
	}
	for _, c := range cases {
		if got := tierColor(c.in); got != c.want {
			t.Errorf("tierColor(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatTime(t *testing.T) {
	// Zero value -> em-dash sentinel.
	if got := formatTime(time.Time{}); got != "—" {
		t.Errorf("zero time = %q, want —", got)
	}
	// Known UTC instant -> HH:MM:SS.mmm.
	ts := time.Date(2026, 4, 22, 15, 4, 5, int(250*time.Millisecond), time.UTC)
	if got := formatTime(ts); got != "15:04:05.250" {
		t.Errorf("formatTime = %q, want 15:04:05.250", got)
	}
}

func TestTruncateJSON(t *testing.T) {
	// Nil -> empty.
	if got := truncateJSON(nil, 100); got != "" {
		t.Errorf("nil -> %q", got)
	}
	// Short string -> passthrough.
	if got := truncateJSON("hello", 100); got != `"hello"` {
		t.Errorf("short string = %q", got)
	}
	// Long map -> truncation with ellipsis.
	big := map[string]string{"a": strings.Repeat("x", 200)}
	got := truncateJSON(big, 40)
	if !strings.HasSuffix(got, "…") {
		t.Errorf("expected ellipsis suffix, got %q", got)
	}
	if len([]rune(got)) != 41 {
		t.Errorf("truncated len = %d runes, want 41 (40 + ellipsis)", len([]rune(got)))
	}
}

func TestNormalizeTier(t *testing.T) {
	cases := map[string]string{
		"T1":             "T1",
		"t8":             "T8",
		"tier-4":         "T4",
		"T4-code-repair": "T4",
		"":               "T?",
		"garbage":        "T?",
		"T9":             "T?",
	}
	for in, want := range cases {
		if got := normalizeTier(in); got != want {
			t.Errorf("normalizeTier(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestTraceWaterfallFlagOffServesSPA ensures the handler delegates to
// the SPA shell when R1_SERVER_UI_V2 is not set. Operator clients on
// the old surface must not suddenly see a different page when the
// binary is upgraded.
func TestTraceWaterfallFlagOffServesSPA(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "")
	s := newUIServer(t)
	resp, err := http.Get(s.URL + "/session/r1-flagoff")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	// SPA shell has /ui/app.js; waterfall template has tier-t1 swatches.
	if !strings.Contains(string(body), "/ui/app.js") {
		t.Error("flag-off should serve SPA (app.js reference missing)")
	}
	if strings.Contains(string(body), "class=\"waterfall\"") {
		t.Error("flag-off should not render waterfall template")
	}
}

// TestTraceWaterfallRendersWithStub exercises the AC: curl
// /session/test-id returns an HTML waterfall with tier colors.
func TestTraceWaterfallRendersWithStub(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	t.Setenv("R1_SERVER_TRACE_STUB", "1")
	s := newUIServer(t)
	resp, err := http.Get(s.URL + "/session/test-id")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type=%q", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	bs := string(body)
	// AC #3: tier colors visible.
	for _, cls := range []string{"tier-t1", "tier-t2", "tier-t4"} {
		if !strings.Contains(bs, cls) {
			t.Errorf("rendered body missing %q", cls)
		}
	}
	// Waterfall grid container is present.
	if !strings.Contains(bs, `class="waterfall"`) {
		t.Error("rendered body missing waterfall container")
	}
	// Navigation link to tree view.
	if !strings.Contains(bs, `/session/test-id/tree`) {
		t.Error("waterfall missing nav link to tree view")
	}
	// Stub banner should be visible when R1_SERVER_TRACE_STUB=1.
	if !strings.Contains(bs, "stub data") {
		t.Error("stub banner missing when R1_SERVER_TRACE_STUB=1")
	}
}

// TestTraceTreeRendersWithStub covers AC #2 + the tree template's
// <details> expand/collapse structure.
func TestTraceTreeRendersWithStub(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	t.Setenv("R1_SERVER_TRACE_STUB", "1")
	s := newUIServer(t)
	resp, err := http.Get(s.URL + "/session/test-id/tree")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	bs := string(body)
	// Tier color classes flow through the tier-badge.
	for _, cls := range []string{"tier-t1", "tier-t2", "tier-t4"} {
		if !strings.Contains(bs, cls) {
			t.Errorf("tree missing %q", cls)
		}
	}
	// Tree list wrapper present.
	if !strings.Contains(bs, `class="tree"`) {
		t.Error("tree template missing .tree container")
	}
	// Link back to the waterfall view.
	if !strings.Contains(bs, "/session/test-id\"") {
		t.Error("tree missing nav link back to waterfall")
	}
}

// TestTraceWaterfallWithRealEvents drops three tier events into the
// DB and confirms the waterfall picks them up in chronological order
// with the correct tier classes derived from the JSON payload.
func TestTraceWaterfallWithRealEvents(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	db := newTestDB(t)
	mux := buildMux(db, discardLogger())
	mountUI(mux, db)

	seedSession(t, db, "r1-real")
	base := time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC)
	seedTierEvent(t, db, "r1-real", "T1", "AC-1", "intent-match", 50, base)
	seedTierEvent(t, db, "r1-real", "T3", "AC-1", "classify", 100, base.Add(50*time.Millisecond))
	seedTierEvent(t, db, "r1-real", "T4", "AC-1", "code-repair", 400, base.Add(150*time.Millisecond))

	// buildMux + mountUI assemble the full route set. Test directly via
	// ServeHTTP to avoid race between handler registration and request.
	req, _ := http.NewRequest(http.MethodGet, "/session/r1-real", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body)
	}
	bs := rr.Body.String()
	// Three distinct tier classes from the seeded events.
	for _, cls := range []string{"tier-t1", "tier-t3", "tier-t4"} {
		if !strings.Contains(bs, cls) {
			t.Errorf("body missing %q", cls)
		}
	}
	// Span names surface in the waterfall labels.
	for _, name := range []string{"intent-match", "classify", "code-repair"} {
		if !strings.Contains(bs, name) {
			t.Errorf("body missing span name %q", name)
		}
	}
	// AC ID shown once per span.
	if !strings.Contains(bs, "AC AC-1") {
		t.Error("body missing AC label")
	}
	// Stub banner should NOT appear (real events present).
	if strings.Contains(bs, "stub data") {
		t.Error("stub banner leaked into real-event render")
	}
}

// TestTraceTreeWithRealEvents mirrors the waterfall test against the
// /tree route to prove both templates render off the same ListSpans
// source.
func TestTraceTreeWithRealEvents(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	db := newTestDB(t)
	mux := buildMux(db, discardLogger())
	mountUI(mux, db)

	seedSession(t, db, "r1-tree")
	base := time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC)
	seedTierEvent(t, db, "r1-tree", "T2", "AC-2", "run-ac", 75, base)
	seedTierEvent(t, db, "r1-tree", "T8", "AC-2", "soft-pass", 10, base.Add(75*time.Millisecond))

	req, _ := http.NewRequest(http.MethodGet, "/session/r1-tree/tree", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	bs := rr.Body.String()
	for _, cls := range []string{"tier-t2", "tier-t8"} {
		if !strings.Contains(bs, cls) {
			t.Errorf("body missing %q", cls)
		}
	}
	for _, name := range []string{"run-ac", "soft-pass"} {
		if !strings.Contains(bs, name) {
			t.Errorf("body missing %q", name)
		}
	}
}

// TestTraceEmptyRendersBanner confirms that when a session exists but
// has no events and stub mode is off, the handler renders the empty-
// state marker instead of 500ing or erroring.
func TestTraceEmptyRendersBanner(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	t.Setenv("R1_SERVER_TRACE_STUB", "")
	db := newTestDB(t)
	mux := buildMux(db, discardLogger())
	mountUI(mux, db)

	seedSession(t, db, "r1-empty")

	req, _ := http.NewRequest(http.MethodGet, "/session/r1-empty", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	bs := rr.Body.String()
	if !strings.Contains(bs, "No spans yet") {
		t.Error("empty-state banner missing")
	}
	if strings.Contains(bs, "stub data") {
		t.Error("stub banner leaked without R1_SERVER_TRACE_STUB=1")
	}
}

// TestComputeOffsets locks in the bar positioning math so a future
// refactor can't silently break the waterfall geometry.
func TestComputeOffsets(t *testing.T) {
	base := time.Unix(0, 0).UTC()
	spans := []*Span{
		{StartedAt: base, EndedAt: base.Add(100 * time.Millisecond), DurationMs: 100},
		{StartedAt: base.Add(100 * time.Millisecond), EndedAt: base.Add(300 * time.Millisecond), DurationMs: 200},
	}
	total := computeOffsets(spans)
	if total != 300 {
		t.Errorf("total = %d, want 300", total)
	}
	// First span starts at 0%, spans a third.
	if spans[0].OffsetPct != 0 {
		t.Errorf("span0 offset = %v, want 0", spans[0].OffsetPct)
	}
	gotW0 := spans[0].WidthPct
	wantW0 := 100.0 * 100.0 / 300.0
	if !floatEq(gotW0, wantW0) {
		t.Errorf("span0 width = %v, want %v", gotW0, wantW0)
	}
	// Second span offset should be 1/3 of axis.
	gotO1 := spans[1].OffsetPct
	wantO1 := 100.0 * 100.0 / 300.0
	if !floatEq(gotO1, wantO1) {
		t.Errorf("span1 offset = %v, want %v", gotO1, wantO1)
	}
}

func floatEq(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 0.001
}
