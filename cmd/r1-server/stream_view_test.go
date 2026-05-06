package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStreamView_404OnFlagOff(t *testing.T) {
	t.Setenv("R1_SERVER_UI_V2", "")
	req := httptest.NewRequest("GET", "/session/sess-x/stream", nil)
	req.SetPathValue("id", "sess-x")
	rec := httptest.NewRecorder()
	serveStreamView(rec, req)
	if rec.Code != 404 {
		t.Errorf("v2 off: status = %d, want 404", rec.Code)
	}
}

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
