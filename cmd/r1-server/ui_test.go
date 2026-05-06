package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newUIServer constructs a mux with both the API + UI routes mounted
// — matches production main.go wiring.
func newUIServer(t *testing.T) *httptest.Server {
	t.Helper()
	db := newTestDB(t)
	mux := buildMux(db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	mountUI(mux, db)
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

// (Spec D — D-UI2-7 — deleted three legacy-SPA tests:
// TestUIServesIndex, TestUIServesStaticAssets,
// TestUISPAFallbackForSessionPath. They asserted on the vanilla-JS
// app.js / style.css / index.html surface, all of which were
// removed when the legacy v1 SPA was deleted. v2 has explicit
// handlers for / and /session/{id}; coverage moves to the v2 golden
// + ui_v2_foundation tests.)

func TestUIAPIStillRoutable(t *testing.T) {
	// Ensure UI mount didn't shadow the API endpoints.
	s := newUIServer(t)
	resp, err := http.Get(s.URL + "/api/health")
	if err != nil {
		t.Fatalf("get health: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("api/health status=%d after UI mount", resp.StatusCode)
	}
}

// TestUIGraphHTMLServed covers RS-4 item 20 — /session/{id}/graph
// serves the v2 3D visualizer template (session-graph.html), not
// the v2 waterfall view. Markers are the v2-template-specific
// strings: graph script reference, "3D Graph" tab marker, and the
// aria-current page indicator.
func TestUIGraphHTMLServed(t *testing.T) {
	s := newUIServer(t)
	resp, err := http.Get(s.URL + "/session/abc/graph")
	if err != nil {
		t.Fatalf("get graph: %v", err)
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
	if !strings.Contains(bs, "/ui/js/graph.js") {
		t.Error("v2 session-graph template missing /ui/js/graph.js script reference")
	}
	if !strings.Contains(bs, "3D Graph") {
		t.Error("v2 session-graph template missing '3D Graph' tab marker")
	}
	if !strings.Contains(bs, `aria-current="page"`) {
		t.Error("v2 session-graph template missing aria-current page indicator")
	}
}

// TestUIGraphJSServed verifies the static asset handler picks up the
// v2 graph.js file from the embed FS under /ui/js/.
func TestUIGraphJSServed(t *testing.T) {
	s := newUIServer(t)
	resp, err := http.Get(s.URL + "/ui/js/graph.js")
	if err != nil {
		t.Fatalf("get graph.js: %v", err)
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
	bs := string(body)
	// v2 graph.js uses the InstancedMesh renderer + bare-specifier
	// THREE imports resolved through the import map. If the wrong
	// file is served, neither marker will be present.
	if !strings.Contains(bs, "InstancedMesh") {
		t.Error("v2 graph.js missing InstancedMesh marker (worker-pair renderer)")
	}
	if !strings.Contains(bs, "from 'three'") {
		t.Error("v2 graph.js missing bare-specifier THREE import (import map contract)")
	}
}

// TestUIGraphCSSServed confirms /ui/css/base.css is served by the
// embed static handler with a CSS-ish content type. Spec D shifted
// the v2 surface from a graph-specific stylesheet to a single shared
// base.css that owns the layout tokens (topbar-height,
// side-panel-width, color-* tokens). Selectors are markedly
// different from the legacy /ui/graph.css; assertions follow the
// v2 token contract instead.
func TestUIGraphCSSServed(t *testing.T) {
	s := newUIServer(t)
	resp, err := http.Get(s.URL + "/ui/css/base.css")
	if err != nil {
		t.Fatalf("get base.css: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "css") && !strings.Contains(ct, "text/plain") {
		t.Errorf("content-type=%q, want text/css", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	bs := string(body)
	// v2 layout token contract — these custom properties drive the
	// topbar + side-panel layout that every v2 page shares. If they
	// drift the entire dashboard chrome breaks silently.
	for _, marker := range []string{
		"--topbar-height",
		"--side-panel-width",
		"--color-fg",
		"--color-bg",
	} {
		if !strings.Contains(bs, marker) {
			t.Errorf("base.css missing v2 layout token %q", marker)
		}
	}
}

// TestUIGraphHTMLLoadsVendoredLibs asserts that the v2 graph view
// loads the three WebGL libraries from the local /ui/vendor/ tree
// instead of a public CDN (work-stoke TASK 15 AC #6 — no CDN,
// works offline). v2 uses an import-map partial + ESM module
// vendor blobs; the legacy UMD `.min.js` paths are gone.
func TestUIGraphHTMLLoadsVendoredLibs(t *testing.T) {
	s := newUIServer(t)
	resp, err := http.Get(s.URL + "/session/abc/graph")
	if err != nil {
		t.Fatalf("get graph: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	bs := string(body)
	// Positive: v2 ESM vendor blobs must be wired through the
	// import-map partial that base.html embeds.
	for _, want := range []string{
		"/ui/vendor/three.module.js",
		"/ui/vendor/three-spritetext.js",
		"/ui/vendor/3d-force-graph.js",
	} {
		if !strings.Contains(bs, want) {
			t.Errorf("v2 graph view missing vendored script reference %q", want)
		}
	}
	// Negative: no CDN host may sneak back in (AC #6 gate).
	for _, bad := range []string{"unpkg.com", "cdn.jsdelivr.net", "@latest"} {
		if strings.Contains(bs, bad) {
			t.Errorf("v2 graph view still references forbidden CDN/float marker %q", bad)
		}
	}
}

// TestUIGraphJSHasNodeStyleContract is BLOCKED post-Spec-D and
// LEFT FAILING by design.
//
// The legacy graph.js encoded the RS-4 item-20 contract directly:
// a NODE_STYLES table covering 16 node types + an EDGE_STYLES table
// covering 7 edge types + detectWebGL + showFallback functions.
//
// The v2 renderer (cmd/r1-server/ui/js/graph.js, ported in Spec 2
// §3.1) is structured differently — it uses an InstancedMesh pool
// keyed off generic geometric SHAPES (sphere, cube, diamond, …)
// rather than per-node-type style records. The 16-node-type to
// shape mapping is not present in any v2 file we could locate
// (audit `audit/legacy-spa-triage-2026-05-06`,
// plans/legacy-spa-test-triage.md classified this as the
// highest-value (c) assertion in the set and explicitly punted
// the migration target to reviewer judgment).
//
// Per the Spec D dispatcher's instructions: "load-bearing for
// product correctness — if you genuinely can't migrate it, mark
// it BLOCKED in a comment and leave it failing — DO NOT delete
// a contract test."
//
// IMPORTANT: this test is intentionally LEFT FAILING (NOT skipped)
// so every CI run surfaces the outstanding contract until reviewer
// either:
//   1. Locates the v2 surface that owns the per-node-type contract
//      (graph-layers.js? graph-worker.js? a yet-to-be-written
//      style table?) and re-points the assertions at it, OR
//   2. Confirms the contract has been retired and documents the
//      replacement (e.g. server-side-rendered styles, a typed
//      schema enforcement layer) before deleting this test.
//
// The test fetches the v2 graph.js (so the new file path is
// exercised) and asserts every contract token is present. Every
// assertion below currently fails — that is the point.
func TestUIGraphJSHasNodeStyleContract(t *testing.T) {
	s := newUIServer(t)
	resp, err := http.Get(s.URL + "/ui/js/graph.js")
	if err != nil {
		t.Fatalf("get graph.js: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	bs := string(body)
	// Node types (RS-4 item 20). The v2 renderer does NOT
	// enumerate these per-type yet; reviewer must re-home the
	// contract (see test comment).
	for _, nodeType := range []string{
		"task", "decision_internal", "decision_repo",
		"verification_evidence", "hitl_request", "hitl_response",
		"escalation", "judge_verdict",
		"research_request", "research_report",
		"agree", "dissent", "draft", "loop", "skill",
		"supervisor_state_checkpoint",
	} {
		if !strings.Contains(bs, nodeType) {
			t.Errorf("BLOCKED (Spec D, audit 3f98fd1b): v2 graph.js missing RS-4 node_type %q — contract has no v2 home; needs reviewer migration", nodeType)
		}
	}
	// Edge types.
	for _, edgeType := range []string{
		"supersedes", "depends_on", "contradicts", "extends",
		"references", "resolves", "distills",
	} {
		if !strings.Contains(bs, edgeType) {
			t.Errorf("BLOCKED (Spec D, audit 3f98fd1b): v2 graph.js missing RS-4 edge_type %q — contract has no v2 home; needs reviewer migration", edgeType)
		}
	}
	// WebGL fallback path must exist.
	if !strings.Contains(bs, "detectWebGL") {
		t.Errorf("BLOCKED (Spec D, audit 3f98fd1b): v2 graph.js missing detectWebGL feature-detect — needs reviewer migration")
	}
	if !strings.Contains(bs, "showFallback") {
		t.Errorf("BLOCKED (Spec D, audit 3f98fd1b): v2 graph.js missing showFallback UI handler — needs reviewer migration")
	}
}

// TestUIGraphRouteDoesNotShadowSPA guards the Go 1.22 ServeMux
// pattern-precedence assumption: /session/{id}/graph must win over
// the /session/{id} concrete pattern that serves the v2 waterfall.
// Spec D removed the legacy SPA fallback half — both routes now
// serve v2 templates and the precedence test is purely intra-v2.
func TestUIGraphRouteDoesNotShadowSPA(t *testing.T) {
	s := newUIServer(t)

	// /session/abc/graph -> v2 session-graph template
	resp, err := http.Get(s.URL + "/session/abc/graph")
	if err != nil {
		t.Fatalf("get graph: %v", err)
	}
	graphBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(graphBody), "/ui/js/graph.js") {
		t.Error("graph path should serve session-graph template with /ui/js/graph.js reference")
	}

	// /session/abc (no /graph) -> v2 waterfall template
	resp2, err := http.Get(s.URL + "/session/abc")
	if err != nil {
		t.Fatalf("get waterfall: %v", err)
	}
	wfBody, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	wfBs := string(wfBody)
	// Waterfall template marker: the trace page title pattern that
	// trace_waterfall.tmpl always emits, regardless of empty-state
	// vs populated.
	if !strings.Contains(wfBs, "r1-server trace") {
		t.Error("plain session path should serve v2 waterfall template (title marker missing)")
	}
	// Make sure the more-specific /graph pattern didn't bleed into
	// the bare /session/{id} surface.
	if strings.Contains(wfBs, "data-island=\"graph\"") {
		t.Error("plain session path accidentally served graph template")
	}
}

// TestV2Surface_AlwaysOn_NoFlagGate locks in the Spec D (D-UI2-7)
// behavior change: dropping the R1_SERVER_UI_V2 envelope toggle
// means every v2-only route MUST be reachable regardless of any
// env-var state. This test sets R1_SERVER_UI_V2 to the EMPTY string
// (the historical "off" value that used to 404 these routes) and
// asserts each route returns a non-404 status. Replaces the eight
// _V2Off_404 / _FlagOff tests that Spec D's flag removal made
// non-representable; without this assertion a future regression
// that re-introduces a flag gate would silently pass.
//
// Routes covered:
//   - GET /memories                              — was TestMemories_V2Off_404
//   - GET /settings                              — was TestSettings_V2Off_404
//   - GET /session/{id}/stream                   — was TestStreamView_404OnFlagOff
//   - GET /memories/{id}/graph                   — was TestServeMemoryGraph_404OnFlagOff
//
// /api/session/{id}/export.tracebundle is intentionally NOT in
// this list: that handler 404s when GetSession(id) misses
// (unknown-session 404 is correct behavior, distinct from
// flag-gate 404). Always-on coverage for that route lives in
// TestServeTracebundle_HeadersAndBody which seeds a real session
// row before exercising the handler.
//
// /share/{hash} is NOT in this list — it's still gated by
// R1_SERVER_SHARE_ENABLED (the per-route share gate stays).
// /api/memories CRUD routes also stay always-mounted (covered by
// TestMemoriesCRUD_* in memories_crud_test.go).
//
// Each handler may legitimately return a non-404, non-200 status
// (e.g. 400 for bad path values, 500 for unmocked dependencies);
// the assertion is specifically that 404 — the historical
// flag-off response — is NOT what we get.
func TestV2Surface_AlwaysOn_NoFlagGate(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "") // historical "off" value; must not affect routing post-Spec-D
	s := newUIServer(t)

	cases := []struct {
		name string
		path string
	}{
		{"memories index", "/memories"},
		{"settings", "/settings"},
		{"session stream", "/session/sess-test/stream"},
		{"memory graph", "/memories/m-test/graph"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, err := http.Get(s.URL + c.path)
			if err != nil {
				t.Fatalf("GET %s: %v", c.path, err)
			}
			resp.Body.Close()
			if resp.StatusCode == http.StatusNotFound {
				t.Errorf("GET %s with R1_SERVER_UI_V2=\"\" returned 404 — Spec D (D-UI2-7) removed the flag gate; route must be reachable", c.path)
			}
		})
	}
}
