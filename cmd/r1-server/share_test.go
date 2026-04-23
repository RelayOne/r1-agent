package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestShare_BothGatesOff_404 — the default state. Neither feature
// flag is set, share/* must 404 indistinguishably from "no route".
func TestShare_BothGatesOff_404(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "")
	t.Setenv("R1_SERVER_SHARE_ENABLED", "")
	s := newUIServer(t)

	resp, err := http.Get(s.URL + "/share/deadbeef00")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status=%d, want 404", resp.StatusCode)
	}
}

// TestShare_OnlyV2On_404 — partial enablement. v2 is on but the
// per-route share toggle is off. Spec §5.3 requires share to stay
// dark in this state.
func TestShare_OnlyV2On_404(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	t.Setenv("R1_SERVER_SHARE_ENABLED", "")
	s := newUIServer(t)

	resp, err := http.Get(s.URL + "/share/deadbeef00")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status=%d, want 404 when only v2 is on", resp.StatusCode)
	}
}

// TestShare_OnlyShareToggleOn_404 — symmetric: the share toggle is on
// but the umbrella v2 flag is off. The whole v2 surface stays dark
// per spec §2.3.
func TestShare_OnlyShareToggleOn_404(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "")
	t.Setenv("R1_SERVER_SHARE_ENABLED", "1")
	s := newUIServer(t)

	resp, err := http.Get(s.URL + "/share/deadbeef00")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status=%d, want 404 when only share toggle is on", resp.StatusCode)
	}
}

// TestShare_BadHash_400 — gates on, but the hash isn't lowercase hex.
// Distinguishing 400 from 404 lets a misconfigured client see why its
// URL failed instead of guessing.
func TestShare_BadHash_400(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	t.Setenv("R1_SERVER_SHARE_ENABLED", "1")
	s := newUIServer(t)

	for _, bad := range []string{
		"DEADBEEF",     // uppercase
		"abc",          // too short (<8)
		"zzzzzzzz",     // not hex
		"abc123!",      // punctuation
		strings.Repeat("a", 65), // too long (>64)
	} {
		resp, err := http.Get(s.URL + "/share/" + bad)
		if err != nil {
			t.Fatalf("get %q: %v", bad, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("hash=%q: status=%d, want 400", bad, resp.StatusCode)
		}
	}
}

// TestShare_HappyPath — both gates on + a well-formed hash. Verifies
// the rendered HTML contains the hash, the canonical share URL, and
// every security header the handler is contracted to emit.
func TestShare_HappyPath(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	t.Setenv("R1_SERVER_SHARE_ENABLED", "1")
	s := newUIServer(t)

	const hash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	resp, err := http.Get(s.URL + "/share/" + hash)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", resp.StatusCode, body)
	}

	bs := string(body)
	if !strings.Contains(bs, hash) {
		t.Error("body missing the chain root hash")
	}
	if !strings.Contains(bs, "/share/"+hash) {
		t.Error("body missing canonical share URL")
	}
	if !strings.Contains(bs, "Read-only snapshot") {
		t.Error("body missing read-only banner heading")
	}
	if !strings.Contains(bs, "<!doctype html>") {
		t.Error("body not a complete HTML document")
	}

	// Security headers — every one of these is contracted in
	// share.go and missing any of them silently weakens the route.
	headerChecks := map[string]string{
		"Content-Type":            "text/html",
		"X-Robots-Tag":            "noindex",
		"Referrer-Policy":         "no-referrer",
		"X-Content-Type-Options":  "nosniff",
		"Content-Security-Policy": "frame-ancestors 'none'",
		"Cache-Control":           "max-age=3600",
	}
	for name, want := range headerChecks {
		if got := resp.Header.Get(name); !strings.Contains(got, want) {
			t.Errorf("header %s = %q, want substring %q", name, got, want)
		}
	}
}

// TestShare_HashShortPrefix — 8-char hex prefixes are allowed for
// debug/share UX. Verifies the shape regex doesn't reject them.
func TestShare_HashShortPrefix(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	t.Setenv("R1_SERVER_SHARE_ENABLED", "1")
	s := newUIServer(t)

	resp, err := http.Get(s.URL + "/share/deadbeef")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("8-char prefix status=%d, want 200", resp.StatusCode)
	}
}

// TestShare_TemplateAutoEscape — the chain hash is path-sourced, so
// even though the regex bounds it tightly we verify html/template
// escapes the value rather than emitting it raw. A regression that
// switches to text/template (or string concat) would fail this test
// even when the regex still rejects markup.
func TestShare_TemplateAutoEscape(t *testing.T) {
	// Render directly via the template to bypass the regex (which
	// would 400 anything containing < or >). This locks in the
	// escaping behaviour at the template layer specifically.
	view := shareView{
		Hash:         "<script>alert(1)</script>",
		CanonicalURL: "/share/<script>",
	}
	var buf strings.Builder
	if err := shareTmpl.Execute(&buf, view); err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "<script>alert(1)</script>") {
		t.Error("template emitted raw <script> tag — auto-escape broken")
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Error("template did not HTML-escape the angle brackets")
	}
}

// TestV2Enabled — direct unit test for the env-driven flag helper.
// Other handlers will gate on this; locking the parse in keeps a
// future "0" → true regression from quietly enabling everything.
func TestV2Enabled(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"", false},
		{"0", false},
		{"true", false}, // only "1" enables; explicit and unambiguous
		{"1", true},
	}
	for _, c := range cases {
		t.Setenv("R1_SERVER_UI_V2", c.val)
		if got := v2Enabled(); got != c.want {
			t.Errorf("v2Enabled with %q = %v, want %v", c.val, got, c.want)
		}
	}
}

// TestShareEnabled — symmetric direct test for the share toggle.
func TestShareEnabled(t *testing.T) {
	t.Setenv("R1_SERVER_SHARE_ENABLED", "")
	if shareEnabled() {
		t.Error("shareEnabled should be false when env unset")
	}
	t.Setenv("R1_SERVER_SHARE_ENABLED", "1")
	if !shareEnabled() {
		t.Error("shareEnabled should be true for value 1")
	}
}
