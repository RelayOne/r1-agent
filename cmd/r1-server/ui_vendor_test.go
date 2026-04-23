// Package main — ui_vendor_test.go
//
// work-stoke TASK 15 AC #6 — "No CDN dependency — works offline".
//
// The 3D ledger visualiser shell (cmd/r1-server/ui/graph.html) must
// load Three.js, three-spritetext, and 3d-force-graph from the local
// /ui/vendor/ tree rather than a public CDN. This test asserts the
// CDN escape hatch stays removed: any reintroduction of an unpkg.com
// or cdn.jsdelivr.net <script src> in graph.html trips the gate.
//
// The check is deliberately substring-based (not AST-parsed) because
// the failure mode we are guarding against is a copy-paste reversion
// of the original CDN markup. A stray comment mentioning those hosts
// would also trip — that is acceptable: the cheapest fix is to rename
// the mention (e.g. link to the README rather than the CDN host).
//
// Sibling tests (vendor_check_test.go) cover the runtime vendor-
// sentinel probe; this test covers the source-of-truth HTML shell.

package main

import (
	"io/fs"
	"strings"
	"testing"
)

// forbiddenCDNHosts are the substrings that must NOT appear in
// graph.html once the vendoring cut-over has shipped. Keep this list
// in sync with the CDN hosts called out in ui/vendor/README.md.
var forbiddenCDNHosts = []string{
	"unpkg.com",
	"cdn.jsdelivr.net",
}

// TestGraphHTMLNoCDNRefs enforces work-stoke TASK 15 AC #6 by reading
// the embedded graph.html shell and asserting it contains none of the
// known CDN hosts. Runs against the //go:embed filesystem so the
// assertion holds for the as-shipped binary, not just the on-disk
// source tree.
func TestGraphHTMLNoCDNRefs(t *testing.T) {
	raw, err := fs.ReadFile(uiFS, "graph.html")
	if err != nil {
		t.Fatalf("read graph.html from embedded uiFS: %v", err)
	}
	body := string(raw)
	for _, host := range forbiddenCDNHosts {
		if strings.Contains(body, host) {
			t.Errorf("graph.html still references forbidden CDN host %q — "+
				"the 3D visualiser must load Three.js / 3d-force-graph / "+
				"three-spritetext from /ui/vendor/* instead. See "+
				"cmd/r1-server/ui/vendor/README.md.", host)
		}
	}
}

// TestGraphHTMLReferencesVendorPaths is the positive companion to
// TestGraphHTMLNoCDNRefs: the shell should load the three vendored
// UMD bundles from /ui/vendor/. If a future refactor switches to ESM
// imports or an import map, update this list alongside the HTML.
func TestGraphHTMLReferencesVendorPaths(t *testing.T) {
	raw, err := fs.ReadFile(uiFS, "graph.html")
	if err != nil {
		t.Fatalf("read graph.html from embedded uiFS: %v", err)
	}
	body := string(raw)
	required := []string{
		"/ui/vendor/three.min.js",
		"/ui/vendor/three-spritetext.min.js",
		"/ui/vendor/3d-force-graph.min.js",
	}
	for _, path := range required {
		if !strings.Contains(body, path) {
			t.Errorf("graph.html missing expected vendored script reference %q", path)
		}
	}
}
