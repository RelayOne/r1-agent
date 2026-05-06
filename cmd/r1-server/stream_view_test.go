package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// (Spec D — D-UI2-7 — deleted TestStreamView_404OnFlagOff. The
// R1_SERVER_UI_V2 toggle was removed once the legacy v1 SPA was
// deleted; serveStreamView is the only surface for /session/{id}/stream.)

func TestStreamView_RendersSSEHookup(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	req := httptest.NewRequest("GET", "/session/sess-y/stream", nil)
	req.SetPathValue("id", "sess-y")
	rec := httptest.NewRecorder()
	serveStreamView(rec, req)
	if rec.Code != 200 {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, frag := range []string{
		`hx-ext="sse"`,
		`/api/session/sess-y/events/stream?last_event_id=`,
		`data-testid="raw-stream"`,
		`data-testid="stream-pre"`,
		`aria-current="page"`,
	} {
		if !strings.Contains(body, frag) {
			t.Errorf("body missing %q\n--- body ---\n%s", frag, body)
		}
	}
	if got := rec.Header().Get("Content-Security-Policy"); got == "" {
		t.Error("CSP header not set")
	}
}

func TestStreamView_404OnEmptyPathID(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "1")
	req := httptest.NewRequest("GET", "/session//stream", nil)
	req.SetPathValue("id", "")
	rec := httptest.NewRecorder()
	serveStreamView(rec, req)
	if rec.Code != 404 {
		t.Errorf("empty id: status = %d, want 404", rec.Code)
	}
}
