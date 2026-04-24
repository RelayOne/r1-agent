// Package main — trace.go
//
// work-stoke TASK 13: waterfall + indented tree default trace view.
//
// Two HTML routes render the same session as complementary layouts:
//
//	GET /session/{id}         — waterfall (default), time-anchored bars
//	GET /session/{id}/tree    — indented tree with <details> expand/collapse
//
// Both views are backed by the existing session_events table populated
// by scanner.go's NDJSON tailer. Rows whose data JSON carries a
// descent-tier envelope (tier T1..T8 from internal/plan/verification_descent.go
// emitted through streamjson) are surfaced as spans; the rest are
// bucketed under their event_type so a mission that never descended
// still produces a usable timeline.
//
// ## Feature gating
//
// The task is part of the spec 27 r1-server-ui-v2 retrofit. Like
// /memories + /share, the trace views are gated behind R1_SERVER_UI_V2.
// When the flag is off the handlers delegate to the existing SPA shell
// (serveIndex) — so /session/{id} keeps serving the vanilla-JS index
// until operators opt in. That means the AC-verification commands in
// the task spec (`curl /session/test-id`) need R1_SERVER_UI_V2=1 set
// on the server, which matches precedent for every other v2 surface.
//
// ## Data source + stub fallback
//
// ListSpans queries session_events grouped by descent tier. When a
// session has no events at all the handler returns a small stub set
// (three synthetic T1/T2/T4 spans) so the scaffold renders something
// useful during development — the task spec explicitly allows this as
// a feature-flagged stand-in until Task 12 finalizes the ingest path.
// The stub is opt-in via R1_SERVER_TRACE_STUB=1; by default an empty
// session renders an empty-state banner.
//
// ## Template FuncMap contract
//
//	tierColor(tier)       — "T4" -> "tier-t4" CSS class; unknown -> "tier-unknown"
//	formatTime(t)         — "15:04:05.000" (UTC); zero time -> "—"
//	truncateJSON(v, max)  — json.Marshal then rune-truncate with "…"
//
// All three are pure + deterministic so tests can assert against exact
// output substrings.
package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

//go:embed templates/*.tmpl
var embeddedTemplates embed.FS

// traceFuncMap is the FuncMap registered on every trace template.
// Registered at init so the first request doesn't race on parse.
var traceFuncMap = template.FuncMap{
	"tierColor":    tierColor,
	"formatTime":   formatTime,
	"truncateJSON": truncateJSON,
}

// waterfallTmpl + treeTmpl are parsed once at package init. A parse
// failure panics because a missing/broken template is a build-time bug
// that would otherwise only surface on first HTTP request.
var (
	waterfallTmpl = template.Must(
		template.New("trace_waterfall.tmpl").
			Funcs(traceFuncMap).
			ParseFS(embeddedTemplates, "templates/trace_waterfall.tmpl"),
	)
	treeTmpl = template.Must(
		template.New("trace_tree.tmpl").
			Funcs(traceFuncMap).
			ParseFS(embeddedTemplates, "templates/trace_tree.tmpl"),
	)
)

// traceV2Enabled reports whether the spec-27 trace views should serve
// content for /session/{id}. Off-by-default matches every other v2
// surface (memories, share) so MVP clients keep seeing the SPA shell.
func traceV2Enabled() bool {
	return os.Getenv("R1_SERVER_UI_V2") == "1"
}

// traceStubEnabled reports whether the handler should fabricate demo
// spans when a session has zero events. Used during bring-up so the
// template scaffold is visually verifiable without wiring the full
// descent-tier ingest path.
func traceStubEnabled() bool {
	return os.Getenv("R1_SERVER_TRACE_STUB") == "1"
}

// Span is one rendered bar in the waterfall / leaf in the tree.
// Times are UTC; durations are milliseconds (int64 because the
// template's printf-via-{{.DurationMs}}ms expects an integer).
type Span struct {
	ID         int64
	Tier       string
	Name       string
	ACID       string
	StartedAt  time.Time
	EndedAt    time.Time
	DurationMs int64
	OffsetPct  float64
	WidthPct   float64
	Preview    any
	Children   []*Span
}

// waterfallView is the root template context for trace_waterfall.tmpl.
type waterfallView struct {
	InstanceID      string
	Spans           []*Span
	TotalDurationMs int64
	QuarterMs       int64
	HalfMs          int64
	ThreeQuarterMs  int64
	Stubbed         bool
}

// treeView is the root template context for trace_tree.tmpl.
type treeView struct {
	InstanceID string
	Roots      []*Span
	Stubbed    bool
}

// tierColor maps a tier label like "T4" (or "T4-code-repair") to its
// CSS class. Empty/unknown input lands on "tier-unknown" so the
// template's {{tierColor .Tier}} always emits a valid class.
func tierColor(tier string) string {
	t := strings.TrimSpace(tier)
	if t == "" {
		return "tier-unknown"
	}
	// Accept "T4", "t4", "T4-code-repair", "tier-4" shapes. The first
	// TrimPrefix pass strips the "tier-" alias; a second pass strips
	// the "t" so we land on a bare digit regardless of input form.
	t = strings.ToLower(t)
	t = strings.TrimPrefix(t, "tier-")
	t = strings.TrimPrefix(t, "t")
	if len(t) >= 1 && t[0] >= '1' && t[0] <= '8' {
		return "tier-t" + string(t[0])
	}
	return "tier-unknown"
}

// formatTime renders a time as HH:MM:SS.mmm UTC. The zero value — used
// as a "not set" sentinel inside Span — renders as an em-dash so the
// template surface is obvious to the operator.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.UTC().Format("15:04:05.000")
}

// truncateJSON marshals v to compact JSON then truncates to max runes,
// appending "…" when truncated. Useful for content previews in the
// tree view's leaf nodes — larger payloads would break the layout.
// Returns "" for nil so {{if .Preview}} guards still work.
func truncateJSON(v any, max int) string {
	if v == nil {
		return ""
	}
	// RawMessage is already JSON; everything else (including bare
	// strings) runs through json.Marshal so the emitted form is a
	// valid JSON literal. That matches the template's "content
	// preview" intent — consumers expect quotes on strings, not
	// naked bodies that collide with surrounding HTML.
	var raw string
	if rm, ok := v.(json.RawMessage); ok {
		raw = string(rm)
	} else {
		b, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		raw = string(b)
	}
	if max <= 0 || len([]rune(raw)) <= max {
		return raw
	}
	rs := []rune(raw)
	return string(rs[:max]) + "…"
}

// ListSpans builds the span set for a session by grouping
// session_events into descent-tier buckets. The current ingest path
// doesn't materialize a parent/child span tree, so every span is a
// root; the tree view renders this as a flat list of leaves, which is
// still useful for scanning a descent history. Future work: derive
// parent relationships from ledger_edges so T4 repairs nest under
// their parent T3 classification.
//
// When stub=true and the session has zero events, a deterministic
// three-span demo set is returned so the UI has something to render.
func (d *DB) ListSpans(instanceID string, stub bool) ([]*Span, bool, error) {
	rows, err := d.ListEvents(instanceID, 0, 10000)
	if err != nil {
		return nil, false, fmt.Errorf("list events for spans: %w", err)
	}

	spans := make([]*Span, 0, len(rows))
	for _, ev := range rows {
		s := spanFromEvent(ev)
		if s == nil {
			continue
		}
		spans = append(spans, s)
	}

	if len(spans) == 0 && stub {
		return stubSpans(), true, nil
	}

	// Sort by start time ASC so the waterfall reads top-down in
	// chronological order.
	sort.SliceStable(spans, func(i, j int) bool {
		if spans[i].StartedAt.Equal(spans[j].StartedAt) {
			return spans[i].ID < spans[j].ID
		}
		return spans[i].StartedAt.Before(spans[j].StartedAt)
	})
	return spans, false, nil
}

// spanFromEvent extracts a Span from a session_events row. Returns nil
// for rows that don't describe a tier event — the waterfall is scoped
// to the verification descent, not every tool call.
func spanFromEvent(ev EventRow) *Span {
	var payload struct {
		Tier     string          `json:"tier"`
		ACID     string          `json:"acid"`
		Name     string          `json:"name"`
		Message  string          `json:"message"`
		Duration int64           `json:"duration_ms"`
		StartedAt string         `json:"started_at"`
		EndedAt   string         `json:"ended_at"`
		Preview  json.RawMessage `json:"preview"`
	}
	// Best-effort parse; missing fields are fine.
	_ = json.Unmarshal(ev.Data, &payload)

	// Only surface descent tier events. Accept both "descent.tier" and
	// "tier" naming so forward-compatible ingest still renders.
	if !strings.Contains(ev.EventType, "tier") && payload.Tier == "" {
		return nil
	}

	started, _ := time.Parse(time.RFC3339Nano, firstNonEmpty(payload.StartedAt, ev.Timestamp))
	ended, _ := time.Parse(time.RFC3339Nano, firstNonEmpty(payload.EndedAt, ev.Timestamp))
	dur := payload.Duration
	if dur == 0 && !ended.IsZero() && !started.IsZero() {
		dur = ended.Sub(started).Milliseconds()
	}
	if dur < 0 {
		dur = 0
	}
	name := firstNonEmpty(payload.Name, payload.Message, ev.EventType)
	tier := payload.Tier
	if tier == "" {
		tier = "T?"
	}

	return &Span{
		ID:         ev.ID,
		Tier:       normalizeTier(tier),
		Name:       name,
		ACID:       payload.ACID,
		StartedAt:  started,
		EndedAt:    ended,
		DurationMs: dur,
		Preview:    payload.Preview,
	}
}

// normalizeTier folds variants like "T4-code-repair", "tier-4", "t4"
// into "T4" so the template's class lookup sees a consistent shape.
func normalizeTier(raw string) string {
	t := strings.TrimSpace(raw)
	if t == "" {
		return "T?"
	}
	t = strings.ToUpper(t)
	t = strings.TrimPrefix(t, "TIER-")
	t = strings.TrimPrefix(t, "T")
	if len(t) >= 1 && t[0] >= '1' && t[0] <= '8' {
		return "T" + string(t[0])
	}
	return "T?"
}

func firstNonEmpty(xs ...string) string {
	for _, s := range xs {
		if s != "" {
			return s
		}
	}
	return ""
}

// computeOffsets walks the span set and fills in OffsetPct + WidthPct
// so the CSS-grid waterfall can position bars without JS. Also returns
// the end-to-end duration in milliseconds for the axis ticks.
func computeOffsets(spans []*Span) int64 {
	if len(spans) == 0 {
		return 0
	}
	var first time.Time
	var last time.Time
	for _, s := range spans {
		if first.IsZero() || (!s.StartedAt.IsZero() && s.StartedAt.Before(first)) {
			first = s.StartedAt
		}
		end := s.EndedAt
		if end.IsZero() {
			end = s.StartedAt.Add(time.Duration(s.DurationMs) * time.Millisecond)
		}
		if last.IsZero() || end.After(last) {
			last = end
		}
	}
	total := last.Sub(first).Milliseconds()
	if total <= 0 {
		// Fall back to sum of durations so bars stay visible on
		// single-timestamp event streams.
		for _, s := range spans {
			total += s.DurationMs
		}
	}
	if total <= 0 {
		total = 1
	}
	cursor := int64(0)
	for _, s := range spans {
		var offset int64
		if !first.IsZero() && !s.StartedAt.IsZero() {
			offset = s.StartedAt.Sub(first).Milliseconds()
		} else {
			offset = cursor
			cursor += s.DurationMs
		}
		if offset < 0 {
			offset = 0
		}
		width := s.DurationMs
		if width <= 0 {
			width = 1 // minimum visible pixel
		}
		s.OffsetPct = pct(offset, total)
		s.WidthPct = pct(width, total)
		if s.OffsetPct+s.WidthPct > 100 {
			s.WidthPct = 100 - s.OffsetPct
		}
		if s.WidthPct < 0.5 {
			s.WidthPct = 0.5
		}
	}
	return total
}

func pct(n, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(n) * 100.0 / float64(total)
}

// stubSpans returns a deterministic three-span demo set. Keeping it
// package-level + deterministic means the test suite can assert exact
// tier classes in the rendered HTML.
func stubSpans() []*Span {
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	return []*Span{
		{ID: 1, Tier: "T1", Name: "intent-match", ACID: "AC-1", StartedAt: base, EndedAt: base.Add(50 * time.Millisecond), DurationMs: 50},
		{ID: 2, Tier: "T2", Name: "run-ac", ACID: "AC-1", StartedAt: base.Add(50 * time.Millisecond), EndedAt: base.Add(200 * time.Millisecond), DurationMs: 150},
		{ID: 3, Tier: "T4", Name: "code-repair", ACID: "AC-1", StartedAt: base.Add(200 * time.Millisecond), EndedAt: base.Add(650 * time.Millisecond), DurationMs: 450},
	}
}

// serveTraceWaterfall renders the default session view. The feature
// flag is checked first; off means we hand off to the SPA shell so
// existing clients keep working during the migration window.
func (d *DB) serveTraceWaterfall(w http.ResponseWriter, r *http.Request) {
	if !traceV2Enabled() {
		serveIndex(w, r)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		http.NotFound(w, r)
		return
	}

	spans, stubbed, err := d.ListSpans(id, traceStubEnabled())
	if err != nil {
		http.Error(w, "list spans: "+err.Error(), http.StatusInternalServerError)
		return
	}
	total := computeOffsets(spans)

	view := waterfallView{
		InstanceID:      id,
		Spans:           spans,
		TotalDurationMs: total,
		QuarterMs:       total / 4,
		HalfMs:          total / 2,
		ThreeQuarterMs:  (total * 3) / 4,
		Stubbed:         stubbed,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if err := waterfallTmpl.Execute(w, view); err != nil {
		http.Error(w, "render waterfall: "+err.Error(), http.StatusInternalServerError)
		return
	}
}

// serveTraceTree renders the /session/{id}/tree view. Same gating as
// the waterfall — when v2 is off the SPA shell is served so
// /session/:id/tree remains a well-formed URL.
func (d *DB) serveTraceTree(w http.ResponseWriter, r *http.Request) {
	if !traceV2Enabled() {
		serveIndex(w, r)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		http.NotFound(w, r)
		return
	}

	spans, stubbed, err := d.ListSpans(id, traceStubEnabled())
	if err != nil {
		http.Error(w, "list spans: "+err.Error(), http.StatusInternalServerError)
		return
	}
	_ = computeOffsets(spans) // not used by tree, but keeps timings uniform
	// Current ingest surface is flat; treat every span as a root. Once
	// ledger_edges provides span parentage this can become a real DAG.
	view := treeView{
		InstanceID: id,
		Roots:      spans,
		Stubbed:    stubbed,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if err := treeTmpl.Execute(w, view); err != nil {
		http.Error(w, "render tree: "+err.Error(), http.StatusInternalServerError)
		return
	}
}
