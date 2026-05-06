package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/ledger"
	ledgernodes "github.com/RelayOne/r1/internal/ledger/nodes"
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

// TestRS4Item20NodeAndEdgeTypeContract — re-homed from
// TestUIGraphJSHasNodeStyleContract during Spec D (legacy SPA cleanup).
//
// Pre-Spec-D this test scanned the v1 graph.js for hardcoded NODE_STYLES
// + EDGE_STYLES tables. That coupling encoded the RS-4 item-20 contract
// in JavaScript decoration code, which was both (a) the wrong location
// (the contract is over the ledger schema, not the renderer) and
// (b) silently retired when the v2 renderer was ported in Spec 2 §3.1
// to use generic SHAPES (sphere/cube/diamond) keyed off shape kinds
// rather than per-type style records.
//
// The contract still exists — but in Go, where it always belonged.
// internal/ledger/nodes/ defines a NodeTyper for each of the 16 RS-4
// node types, and internal/ledger/ledger.go declares the 7 edge-type
// constants. Asserting against those is the durable shape: it survives
// renderer rewrites, catches accidental deletion of a node/edge kind,
// and runs in pure Go (no HTTP fixture, no UI flake surface).
//
// detectWebGL / showFallback were UI fallback affordances of the
// retired SPA, not part of the data-shape contract. Their replacement
// in v2 is the React surface's WebGL feature-detect (web/src/...),
// which is covered by web/vitest tests independently — out of scope
// for this Go-side contract test.
func TestRS4Item20NodeAndEdgeTypeContract(t *testing.T) {
	// Each NodeTyper in internal/ledger/nodes/* must satisfy ledger.NodeTyper
	// and return one of these stable strings. Constructing zero-value
	// instances and reading NodeType() is the cheapest way to assert
	// the constants without round-tripping through ledger.AddNode.
	wantNodeTypes := map[string]bool{
		"task":                        false,
		"decision_internal":           false,
		"decision_repo":               false,
		"verification_evidence":       false,
		"hitl_request":                false,
		"hitl_response":               false,
		"escalation":                  false,
		"judge_verdict":               false,
		"research_request":            false,
		"research_report":             false,
		"agree":                       false,
		"dissent":                     false,
		"draft":                       false,
		"loop":                        false,
		"skill":                       false,
		"supervisor_state_checkpoint": false,
	}
	for _, n := range []ledgernodes.NodeTyper{
		&ledgernodes.Task{}, &ledgernodes.DecisionInternal{}, &ledgernodes.DecisionRepo{},
		&ledgernodes.VerificationEvidence{}, &ledgernodes.HITLRequest{}, &ledgernodes.HITLResponse{},
		&ledgernodes.Escalation{}, &ledgernodes.JudgeVerdict{},
		&ledgernodes.ResearchRequest{}, &ledgernodes.ResearchReport{},
		&ledgernodes.Agree{}, &ledgernodes.Dissent{}, &ledgernodes.Draft{}, &ledgernodes.Loop{},
		&ledgernodes.Skill{}, &ledgernodes.SupervisorStateCheckpoint{},
	} {
		nt := n.NodeType()
		if _, ok := wantNodeTypes[nt]; !ok {
			t.Errorf("RS-4 item 20: unexpected node type %q from %T (contract drift — update wantNodeTypes or the NodeType() impl)", nt, n)
			continue
		}
		wantNodeTypes[nt] = true
	}
	for nt, seen := range wantNodeTypes {
		if !seen {
			t.Errorf("RS-4 item 20: node type %q has no NodeTyper struct asserted in this test", nt)
		}
	}
	// Edge types are package-level constants in internal/ledger/ledger.go.
	wantEdges := []ledger.EdgeType{
		ledger.EdgeSupersedes, ledger.EdgeDependsOn, ledger.EdgeContradicts,
		ledger.EdgeExtends, ledger.EdgeReferences, ledger.EdgeResolves,
		ledger.EdgeDistills,
	}
	wantEdgeStrings := []string{
		"supersedes", "depends_on", "contradicts",
		"extends", "references", "resolves",
		"distills",
	}
	for i, e := range wantEdges {
		if string(e) != wantEdgeStrings[i] {
			t.Errorf("RS-4 item 20: edge constant drift at index %d — want %q, got %q (this fires when ledger.EdgeXxx string values change)", i, wantEdgeStrings[i], string(e))
		}
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
//   - GET /session/{id}                          — was TestTraceWaterfallFlagOffServesSPA
//   - GET /session/{id}/stream                   — was TestStreamView_404OnFlagOff
//   - GET /memories/{id}/graph                   — was TestServeMemoryGraph_404OnFlagOff
//
// /api/session/{id}/export.tracebundle is NOT in this list because
// the handler legitimately 404s on unknown-session lookups
// (route-level, distinct from flag-gate). Always-on coverage for
// that route lives in TestServeTracebundleAdapter_AlwaysOn_KnownSession_200
// (in tracebundle_source_test.go) which seeds a real session row +
// real ledger.Store and asserts 200 with R1_SERVER_UI_V2="".
//
// /share/{hash} is NOT in this list — it's still gated by
// R1_SERVER_SHARE_ENABLED (the per-route share gate stays).
// /api/memories CRUD routes stay always-mounted; covered by
// TestMemoriesPOST/PUT/DELETE_* in memories_crud_test.go.
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
		{"session waterfall", "/session/sess-test"},
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
