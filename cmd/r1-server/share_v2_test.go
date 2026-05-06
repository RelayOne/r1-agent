// Package main — share_v2_test.go
//
// Spec 4 §10 T13 + T14: triple gate semantics + banner source order.
package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// (Spec D — D-UI2-7 — deleted TestShare_404WhenUIv2Off. The
// R1_SERVER_UI_V2 umbrella toggle was removed; the share gate now
// collapses to ShareEnabled alone.
// TestShare_404WhenShareDisabledEvenWithUIv2 still asserts the
// remaining gate from the other direction.)

func TestShare_404WhenShareDisabledEvenWithUIv2(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	t.Setenv("R1_SERVER_SHARE_ENABLED", "")
	req := httptest.NewRequest("GET", "/share/abc12345", nil)
	req.SetPathValue("hash", "abc12345")
	rec := httptest.NewRecorder()
	serveShare(rec, req)
	if rec.Code != 404 {
		t.Errorf("share disabled: status = %d, want 404", rec.Code)
	}
}

func TestShare_400OnInvalidHash(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	t.Setenv("R1_SERVER_SHARE_ENABLED", "1")
	req := httptest.NewRequest("GET", "/share/NOTAHASH!", nil)
	req.SetPathValue("hash", "NOTAHASH!")
	rec := httptest.NewRecorder()
	serveShare(rec, req)
	if rec.Code != 400 {
		t.Errorf("invalid hash: status = %d, want 400", rec.Code)
	}
}

func TestShare_200WithBothGatesOn(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	t.Setenv("R1_SERVER_SHARE_ENABLED", "1")
	t.Setenv("R1_SERVER_SHARE_TEMPLATE_V2", "")
	req := httptest.NewRequest("GET", "/share/abc12345", nil)
	req.SetPathValue("hash", "abc12345")
	rec := httptest.NewRecorder()
	serveShare(rec, req)
	if rec.Code != 200 {
		t.Errorf("both gates on: status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "abc12345") {
		t.Errorf("body should contain the hash; got %q", body[:min(200, len(body))])
	}
}

// T14: banner is the FIRST rendered child of <main> so screen
// readers announce read-only context BEFORE any waterfall content.
func TestShareV2_BannerPrecedesWaterfall(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	t.Setenv("R1_SERVER_SHARE_ENABLED", "1")
	t.Setenv("R1_SERVER_SHARE_TEMPLATE_V2", "1")
	req := httptest.NewRequest("GET", "/share/abc12345", nil)
	req.SetPathValue("hash", "abc12345")
	rec := httptest.NewRecorder()
	serveShare(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	bannerIdx := strings.Index(body, `data-testid="share-banner"`)
	waterfallIdx := strings.Index(body, `data-testid="share-waterfall"`)
	if bannerIdx < 0 {
		t.Fatalf("banner not found in body")
	}
	if waterfallIdx < 0 {
		t.Fatalf("waterfall not found in body")
	}
	if bannerIdx >= waterfallIdx {
		t.Errorf("banner (offset %d) should appear before waterfall (offset %d) in source order", bannerIdx, waterfallIdx)
	}
	// Banner copy must mention "Read-only" so screen readers picking
	// up the role="note" element actually convey the constraint.
	if !strings.Contains(body[bannerIdx:waterfallIdx], "Read-only") {
		t.Errorf("share banner missing 'Read-only' copy")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
