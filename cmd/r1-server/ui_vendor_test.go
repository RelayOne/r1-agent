// Package main — ui_vendor_test.go
//
// work-stoke TASK 15 AC #6 — "No CDN dependency — works offline".
//
// Spec D (D-UI2-7) deleted the legacy graph.html shell. The same
// CDN-ban / vendor-path contracts now live in the v2 base.html +
// import-map partial (which every v2 page composes). The two tests
// below assert the contract against the v2 source-of-truth files.
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

// forbiddenCDNHosts are the substrings that must NOT appear in the
// v2 shell templates once the vendoring cut-over has shipped. Keep
// this list in sync with the CDN hosts called out in
// ui/vendor/README.md.
var forbiddenCDNHosts = []string{
	"unpkg.com",
	"cdn.jsdelivr.net",
}

// TestGraphHTMLNoCDNRefs enforces work-stoke TASK 15 AC #6 by reading
// the embedded v2 shell templates (base.html + import-map partial)
// and asserting they contain none of the known CDN hosts. Runs
// against the //go:embed filesystem so the assertion holds for the
// as-shipped binary, not just the on-disk source tree.
//
// Post-Spec-D the contract surface is: base.html (which every v2
// page extends) and partials/import-map.html (the bare-specifier →
// vendor-path map every v2 module loader hits). The legacy
// graph.html that used to anchor this test is gone.
func TestGraphHTMLNoCDNRefs(t *testing.T) {
	for _, path := range []string{"base.html", "partials/import-map.html", "session-graph.html"} {
		raw, err := fs.ReadFile(uiFS, path)
		if err != nil {
			t.Fatalf("read %s from embedded uiFS: %v", path, err)
		}
		body := string(raw)
		for _, host := range forbiddenCDNHosts {
			if strings.Contains(body, host) {
				t.Errorf("%s still references forbidden CDN host %q — "+
					"the v2 shell must load Three.js / 3d-force-graph / "+
					"three-spritetext from /ui/vendor/* instead. See "+
					"cmd/r1-server/ui/vendor/README.md.", path, host)
			}
		}
	}
}

// TestGraphHTMLReferencesVendorPaths is the positive companion to
// TestGraphHTMLNoCDNRefs: the v2 import-map partial must wire every
// bare specifier the renderer expects to its corresponding ESM
// vendor blob under /ui/vendor/. If a future refactor switches the
// vendoring strategy (back to UMD, or to a different module
// loader), update this list alongside the partial.
func TestGraphHTMLReferencesVendorPaths(t *testing.T) {
	raw, err := fs.ReadFile(uiFS, "partials/import-map.html")
	if err != nil {
		t.Fatalf("read partials/import-map.html from embedded uiFS: %v", err)
	}
	body := string(raw)
	// v2 shifted from UMD `.min.js` blobs to ESM modules; the
	// import map points bare specifiers at the new file names.
	required := []string{
		"/ui/vendor/three.module.js",
		"/ui/vendor/three-spritetext.js",
		"/ui/vendor/3d-force-graph.js",
		"/ui/vendor/d3-force-3d.js",
	}
	for _, path := range required {
		if !strings.Contains(body, path) {
			t.Errorf("import-map partial missing expected vendored script reference %q", path)
		}
	}
}
