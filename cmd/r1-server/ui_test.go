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

func TestUIServesIndex(t *testing.T) {
	s := newUIServer(t)
	resp, err := http.Get(s.URL + "/")
	if err != nil {
		t.Fatalf("get /: %v", err)
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
	if !strings.Contains(string(body), "r1-server") {
		t.Error("index.html missing title")
	}
	if !strings.Contains(string(body), "/ui/app.js") {
		t.Error("index.html missing app.js script tag")
	}
}

func TestUIServesStaticAssets(t *testing.T) {
	s := newUIServer(t)
	for _, asset := range []string{"/ui/app.js", "/ui/style.css"} {
		resp, err := http.Get(s.URL + asset)
		if err != nil {
			t.Fatalf("get %s: %v", asset, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s status=%d", asset, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestUISPAFallbackForSessionPath(t *testing.T) {
	// SPA route — no server-side matching; index.html is served and
	// client-side JS handles the /session/:id view.
	s := newUIServer(t)
	resp, err := http.Get(s.URL + "/session/r1-abcdef")
	if err != nil {
		t.Fatalf("get session path: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "r1-server") {
		t.Error("SPA shell not served for /session/:id")
	}
}

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
// serves the dedicated 3D visualizer shell, not the plain SPA
// index.html. The body must reference graph.js so we know the
// embed picked up the new file.
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
	if !strings.Contains(bs, "graph.js") {
		t.Error("graph.html missing graph.js script reference")
	}
	if !strings.Contains(bs, "Ledger Graph") {
		t.Error("graph.html missing page title marker")
	}
	// Make sure we didn't accidentally serve the regular SPA shell.
	if strings.Contains(bs, "/ui/app.js") {
		t.Error("graph path served index.html (app.js present) instead of graph.html")
	}
}

// TestUIGraphJSServed verifies the static asset handler picks up the
// new graph.js file from the embed FS under /ui/.
func TestUIGraphJSServed(t *testing.T) {
	s := newUIServer(t)
	resp, err := http.Get(s.URL + "/ui/graph.js")
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
	if !strings.Contains(string(body), "ForceGraph3D") {
		t.Error("graph.js body missing ForceGraph3D reference")
	}
}

// TestUIGraphCSSServed confirms /ui/graph.css is served by the embed
// static handler with a CSS-ish content type. The 3D visualizer
// depends on this stylesheet for its full-viewport layout and side
// panel; if the embed misses it the page is unusable.
func TestUIGraphCSSServed(t *testing.T) {
	s := newUIServer(t)
	resp, err := http.Get(s.URL + "/ui/graph.css")
	if err != nil {
		t.Fatalf("get graph.css: %v", err)
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
	// Spot-check selectors that the JS depends on. If someone
	// renames these in graph.css without updating graph.js, the
	// visualizer silently breaks; pinning here catches that.
	for _, marker := range []string{"#graph", "#sidepanel", "#tooltip", "#fallback"} {
		if !strings.Contains(bs, marker) {
			t.Errorf("graph.css missing %q selector", marker)
		}
	}
}

// TestUIGraphHTMLLoadsVendoredLibs asserts that graph.html loads the
// three WebGL libraries from the local /ui/vendor/ tree instead of a
// public CDN (work-stoke TASK 15 AC #6 — no CDN, works offline). The
// blobs themselves are pinned at checkout time via the committed
// copies under cmd/r1-server/ui/vendor/; this test guards the HTML
// shell's <script src> contract against accidental re-introduction
// of unpkg / jsdelivr references.
func TestUIGraphHTMLLoadsVendoredLibs(t *testing.T) {
	s := newUIServer(t)
	resp, err := http.Get(s.URL + "/session/abc/graph")
	if err != nil {
		t.Fatalf("get graph: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	bs := string(body)
	// Positive: the three vendored UMD bundles must be referenced.
	for _, want := range []string{
		"/ui/vendor/three.min.js",
		"/ui/vendor/three-spritetext.min.js",
		"/ui/vendor/3d-force-graph.min.js",
	} {
		if !strings.Contains(bs, want) {
			t.Errorf("graph.html missing vendored script reference %q", want)
		}
	}
	// Negative: no CDN host may sneak back in (AC #6 gate).
	for _, bad := range []string{"unpkg.com", "cdn.jsdelivr.net", "@latest"} {
		if strings.Contains(bs, bad) {
			t.Errorf("graph.html still references forbidden CDN/float marker %q", bad)
		}
	}
}

// TestUIGraphJSHasNodeStyleContract asserts the graph.js style map
// covers every node_type the RS-4 item-20 spec enumerates. Catches
// regressions where someone deletes a shape without updating the
// visualizer (or the other way around — the spec is the contract).
func TestUIGraphJSHasNodeStyleContract(t *testing.T) {
	s := newUIServer(t)
	resp, err := http.Get(s.URL + "/ui/graph.js")
	if err != nil {
		t.Fatalf("get graph.js: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	bs := string(body)
	// Node types (spec RS-4 item 20). Keep this list in sync with
	// the NODE_STYLES table in graph.js.
	for _, nodeType := range []string{
		"task", "decision_internal", "decision_repo",
		"verification_evidence", "hitl_request", "hitl_response",
		"escalation", "judge_verdict",
		"research_request", "research_report",
		"agree", "dissent", "draft", "loop", "skill",
		"supervisor_state_checkpoint",
	} {
		if !strings.Contains(bs, nodeType) {
			t.Errorf("graph.js NODE_STYLES missing %q", nodeType)
		}
	}
	// Edge types.
	for _, edgeType := range []string{
		"supersedes", "depends_on", "contradicts", "extends",
		"references", "resolves", "distills",
	} {
		if !strings.Contains(bs, edgeType) {
			t.Errorf("graph.js EDGE_STYLES missing %q", edgeType)
		}
	}
	// WebGL fallback path must exist.
	if !strings.Contains(bs, "detectWebGL") {
		t.Error("graph.js missing WebGL feature-detect function")
	}
	if !strings.Contains(bs, "showFallback") {
		t.Error("graph.js missing fallback UI handler")
	}
}

// TestUIGraphRouteDoesNotShadowSPA guards the Go 1.22 ServeMux
// pattern-precedence assumption: /session/{id}/graph must win over
// the /session/ SPA fallback, while unrelated /session/:id paths
// still serve the plain index.html.
func TestUIGraphRouteDoesNotShadowSPA(t *testing.T) {
	s := newUIServer(t)

	// /session/abc/graph -> graph.html
	resp, err := http.Get(s.URL + "/session/abc/graph")
	if err != nil {
		t.Fatalf("get graph: %v", err)
	}
	graphBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(graphBody), "graph.js") {
		t.Error("graph path should serve graph.html with graph.js reference")
	}

	// /session/abc (no /graph) -> SPA index.html
	resp2, err := http.Get(s.URL + "/session/abc")
	if err != nil {
		t.Fatalf("get spa: %v", err)
	}
	spaBody, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if !strings.Contains(string(spaBody), "/ui/app.js") {
		t.Error("plain session path should still serve index.html with app.js")
	}
	if strings.Contains(string(spaBody), "Ledger Graph") {
		t.Error("plain session path accidentally served graph.html")
	}
}
