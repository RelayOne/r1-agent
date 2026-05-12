// main_test.go: r1-browser service unit tests.
//
// We do NOT exercise the Chromium subprocess from tests — that's
// what the inhouse_live integration test (T13b) is for. These
// tests cover livez/readyz semantics and the bearer-auth gate.

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLivez(t *testing.T) {
	t.Parallel()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/livez", nil)
	livezHandler(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Errorf("body = %s, want ok:true", w.Body.String())
	}
}

func TestReadyz_NotReady(t *testing.T) {
	// Cannot run in parallel — mutates the global flag.
	chromiumReady.Store(false)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	readyzHandler(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

func TestReadyz_Ready(t *testing.T) {
	chromiumReady.Store(true)
	defer chromiumReady.Store(false)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	readyzHandler(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestVerifyBearer(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		hdr  string
		want bool
	}{
		{"missing", "", false},
		{"non-bearer", "Basic abc", false},
		{"short bearer", "Bearer xyz", false},
		// 3-segment JWT structural pass.
		{"jwt-shape", "Bearer aaaaaaaa.bbbbbbbb.cccccccc", true},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if c.hdr != "" {
				r.Header.Set("Authorization", c.hdr)
			}
			if got := verifyBearer(r); got != c.want {
				t.Errorf("verifyBearer(%q) = %v, want %v", c.hdr, got, c.want)
			}
		})
	}
}

func TestRandomUUID_NonEmpty(t *testing.T) {
	t.Parallel()
	id := randomUUID()
	if len(id) != 16 {
		t.Errorf("randomUUID len = %d, want 16", len(id))
	}
}

func TestChromiumBinary_DefaultPath(t *testing.T) {
	t.Setenv("CHROMIUM_BINARY", "")
	if got := chromiumBinary(); got != "/usr/bin/chromium" {
		t.Errorf("default chromium path = %q", got)
	}
	t.Setenv("CHROMIUM_BINARY", "/opt/chrome/chrome")
	if got := chromiumBinary(); got != "/opt/chrome/chrome" {
		t.Errorf("override chromium path = %q", got)
	}
}
