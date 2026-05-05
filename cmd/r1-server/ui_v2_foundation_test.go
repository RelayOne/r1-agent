// Package main — ui_v2_foundation_test.go
//
// Smoke + contract tests for the foundation pieces in
// ui_v2_foundation.go. Spec 1 §9 calls for a parse test of base.html
// + import-map.html and a render assertion that exercises the
// context shape Specs 2-4 will populate.
package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseV2Templates_ParsesCleanly(t *testing.T) {
	tmpl, err := parseV2Templates()
	if err != nil {
		t.Fatalf("parseV2Templates: %v", err)
	}
	if tmpl == nil {
		t.Fatal("parseV2Templates returned nil template")
	}
	for _, want := range []string{"base", "import-map"} {
		if got := tmpl.Lookup(want); got == nil {
			t.Errorf("template %q not registered (defined templates: %v)", want, tmpl.DefinedTemplates())
		}
	}
}

func TestParseV2Templates_BaseRenderShape(t *testing.T) {
	tmpl, err := parseV2Templates()
	if err != nil {
		t.Fatalf("parseV2Templates: %v", err)
	}
	ctx := struct {
		Title       string
		HtmxSRI     string
		HtmxSseSRI  string
		SessionID   string
	}{
		Title:      "test",
		HtmxSRI:    "sha384-AAAA",
		HtmxSseSRI: "sha384-BBBB",
		SessionID:  "sess-xyz",
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "base", ctx); err != nil {
		t.Fatalf("execute base: %v", err)
	}
	out := buf.String()
	want := []string{
		`<script type="importmap">`,
		`/ui/web/vendor/htmx.min.js`,
		`integrity="sha384-AAAA"`,
		`/ui/web/vendor/htmx-ext-sse.js`,
		`integrity="sha384-BBBB"`,
		`hx-ext="sse"`,
		`data-session-id="sess-xyz"`,
		`<link rel="stylesheet" href="/ui/web/css/base.css">`,
		`"three": "/ui/web/vendor/three.module.js"`,
	}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("rendered base missing fragment: %s", w)
		}
	}
	if strings.Contains(strings.ToLower(out), "<script>") {
		t.Errorf("rendered base contains an inline <script> block — CSP forbids it")
	}
}

func TestSetV2CSP_StrictPolicy(t *testing.T) {
	rec := httptest.NewRecorder()
	setV2CSP(rec.Header())
	got := rec.Header().Get("Content-Security-Policy")
	if got == "" {
		t.Fatal("Content-Security-Policy header not set")
	}
	for _, want := range []string{
		"default-src 'self'",
		"script-src 'self'",
		"object-src 'none'",
		"frame-ancestors 'none'",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("CSP header missing directive: %s\n  full: %s", want, got)
		}
	}
	for _, banned := range []string{"unsafe-inline", "unsafe-eval", "data: script-src", "*"} {
		if strings.Contains(got, banned) {
			t.Errorf("CSP header relaxes a guarantee: %s\n  full: %s", banned, got)
		}
	}
	if rec.Code != http.StatusOK {
		t.Errorf("setV2CSP unexpectedly wrote status code %d", rec.Code)
	}
}
