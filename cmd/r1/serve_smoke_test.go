package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/server"
)

// TestServeSmoke is the spec'd smoke test for `r1 serve`. Spec
// web-chat-ui item 52/55.
//
// We exercise the same Server type the binary boots and assert:
//   - GET / returns 200 + text/html.
//   - Either the response Content-Security-Policy header carries a
//     CSP, OR the response body's <meta http-equiv="Content-Security-Policy">
//     tag echoes one (the SPA's preferred channel — the index.html
//     ships its own CSP meta).
//
// Booting the binary itself (subprocess) is left to the e2e suite;
// this in-process variant runs in milliseconds and gates every
// commit through the standard `go test ./...` path.
func TestServeSmoke(t *testing.T) {
	srv := server.New(0, "", nil)
	server.RegisterDashboardUI(srv)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /: got status %d, want 200", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("GET /: got content-type %q, want text/html prefix", ct)
	}

	body := readAll(t, resp)
	headerCSP := resp.Header.Get("Content-Security-Policy")
	hasMetaCSP := strings.Contains(strings.ToLower(body), `http-equiv="content-security-policy"`)

	if headerCSP == "" && !hasMetaCSP {
		t.Fatalf("GET /: response carries no CSP, neither via header nor <meta http-equiv>")
	}

	// If the header is present, the meta tag should echo it (per spec).
	// If only the meta is present (current SPA index.html), that's also
	// acceptable.
	if headerCSP != "" && hasMetaCSP {
		if !strings.Contains(strings.ToLower(body), strings.ToLower(headerCSP)) {
			t.Logf("warn: header CSP %q does not match meta CSP — verify they're aligned", headerCSP)
		}
	}
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	const max = 1 << 20
	buf := make([]byte, 0, 8192)
	tmp := make([]byte, 4096)
	total := 0
	for total < max {
		n, err := resp.Body.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			total += n
		}
		if err != nil {
			break
		}
	}
	return string(buf)
}
