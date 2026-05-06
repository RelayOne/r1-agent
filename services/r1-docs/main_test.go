package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRenderMarkdownEscapesAndStructures(t *testing.T) {
	in := []byte("# Title\n\nSome **bold** and `code` and a [link](http://example.com).\n\n```go\nfunc f() {}\n```\n")
	out := string(renderMarkdown(in))
	if !strings.Contains(out, "<h1>Title</h1>") {
		t.Errorf("missing <h1>, got %q", out)
	}
	if !strings.Contains(out, "<strong>bold</strong>") {
		t.Errorf("missing <strong>, got %q", out)
	}
	if !strings.Contains(out, "<code>code</code>") {
		t.Errorf("missing <code>, got %q", out)
	}
	if !strings.Contains(out, `<a href="http://example.com">link</a>`) {
		t.Errorf("missing <a>, got %q", out)
	}
	if !strings.Contains(out, `data-lang="go"`) {
		t.Errorf("missing language tag on code fence, got %q", out)
	}
}

func TestRenderMarkdownEscapesHTML(t *testing.T) {
	in := []byte("<script>alert(1)</script>\n")
	out := string(renderMarkdown(in))
	if strings.Contains(out, "<script>") {
		t.Errorf("did not escape <script>, got %q", out)
	}
}

func TestHandleDocServesEmbeddedReadme(t *testing.T) {
	rr := httptest.NewRecorder()
	handleDoc(rr, httptest.NewRequest(http.MethodGet, "/README.html", nil))
	// We don't require it to find the file (depends on what was
	// embedded at build time) but if it's 200, the body should be HTML.
	if rr.Code == http.StatusOK {
		if !strings.Contains(rr.Body.String(), "<!DOCTYPE html>") {
			t.Errorf("expected HTML body, got %q", rr.Body.String()[:200])
		}
	} else if rr.Code != http.StatusNotFound {
		t.Errorf("unexpected status %d", rr.Code)
	}
}

func TestHandleHealthzReturns200JSON(t *testing.T) {
	rr := httptest.NewRecorder()
	handleHealthz(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr.Code != http.StatusOK {
		t.Errorf("status=%d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"service":"r1-docs"`) {
		t.Errorf("missing service marker, got %q", rr.Body.String())
	}
}

func TestTitleFromMarkdownPicksFirstH1(t *testing.T) {
	got := titleFromMarkdown([]byte("# Hello World\n\nSome body\n"), "fallback")
	if got != "Hello World" {
		t.Errorf("title=%q, want Hello World", got)
	}
}

func TestTitleFromMarkdownFallbackWhenNoH1(t *testing.T) {
	got := titleFromMarkdown([]byte("Just a paragraph\n"), "fallback-name")
	if got != "fallback-name" {
		t.Errorf("title=%q, want fallback-name", got)
	}
}
