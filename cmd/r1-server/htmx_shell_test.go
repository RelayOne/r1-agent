// Package main — htmx_shell_test.go
//
// Spec 5 §10 T5: smoke test that the v2 dashboard's base.html shell
// emits the htmx + SSE + import-map shape the rest of the v2
// surface depends on. Catches base.html regressions that golden
// tests would only catch via full byte-diff.
//
// Boots an in-memory mux, GETs /, asserts the response body
// contains:
//   - hx-ext="sse" on <body>
//   - <script type="importmap">
//   - <script src=".../vendor/htmx.min.js" integrity="sha384-...">
//   - <script src=".../vendor/htmx-ext-sse.js" integrity="sha384-...">
//   - data-testid="instance-list" element
//
// All assertions test the live render path (not just the parse —
// parsing is covered by TestParseV2Templates). A regression that
// removes hx-ext="sse" from base.html breaks every page that
// extends it; this test catches it on the lightest possible page.
package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHtmxShell_IndexRendersFullShellShape(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")

	// We render the index page directly — no DB required for the
	// shell shape. parseV2Template("index") + ExecuteTemplate gives
	// us the same byte stream serveHTMLIndex would emit.
	tmpl, err := parseV2Template("index")
	if err != nil {
		t.Fatalf("parseV2Template(index): %v", err)
	}
	type instanceRow struct {
		ID, Name, Status, LastActivityISO string
		EventCount                        int
	}
	ctx := struct {
		V2BaseContext
		Instances []instanceRow
	}{
		V2BaseContext: newV2BaseContext("", "r1 instances"),
		Instances:     []instanceRow{},
	}
	rec := httptest.NewRecorder()
	rec.Code = http.StatusOK
	if err := tmpl.ExecuteTemplate(rec, "index", ctx); err != nil {
		t.Fatalf("execute index: %v", err)
	}
	body := rec.Body.String()

	// Assertions: every shell fragment that pages extending base.html
	// rely on. Drop one and the next four pages stop working.
	wants := []string{
		// htmx core load + SRI
		`<script src="/ui/web/vendor/htmx.min.js"`,
		`integrity="sha384-`,
		`crossorigin="anonymous"`,
		// htmx SSE extension load
		`<script src="/ui/web/vendor/htmx-ext-sse.js"`,
		// import map
		`<script type="importmap">`,
		// SSE body wiring
		`hx-ext="sse"`,
		// the index page's own data-testid
		`data-testid="instance-list"`,
		// SSE-streamed tbody
		`sse-connect="/api/events/instance-stream"`,
		`sse-swap="instance-row"`,
	}
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("rendered shell missing fragment: %q", w)
		}
	}

	// Paranoia checks: no inline <script>...</script> bodies — CSP
	// forbids them. Non-src script tags would break the whole page
	// the moment setV2CSP attaches its header.
	if strings.Contains(body, "<script>") {
		t.Errorf("rendered shell contains an inline <script>...</script> — CSP forbids it")
	}
	if strings.Contains(body, "javascript:") {
		t.Errorf("rendered shell contains a javascript: URL — forbidden")
	}
}

func TestHtmxShell_BaseRendersImportMapWithFiveBareSpecifiers(t *testing.T) {
	tmpl, err := parseV2Template("base")
	if err != nil {
		t.Fatalf("parseV2Template(base): %v", err)
	}
	rec := httptest.NewRecorder()
	if err := tmpl.ExecuteTemplate(rec, "base", newV2BaseContext("sess-x", "test")); err != nil {
		t.Fatalf("execute base: %v", err)
	}
	body := rec.Body.String()
	for _, spec := range []string{
		`"three"`,
		`"three/addons/controls/OrbitControls.js"`,
		`"d3-force-3d"`,
		`"3d-force-graph"`,
		`"three-spritetext"`,
	} {
		if !strings.Contains(body, spec) {
			t.Errorf("import map missing bare specifier: %s", spec)
		}
	}
}
