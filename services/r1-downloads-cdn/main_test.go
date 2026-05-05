package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// We don't connect to GCS in tests — the server requires a storage
// client at start time. We test the handler-level routing logic that
// runs before any GCS call.

func TestHealthzReturns200(t *testing.T) {
	s := &server{}
	rr := httptest.NewRecorder()
	s.handleHealthz(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr.Code != http.StatusOK {
		t.Errorf("status=%d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"service":"r1-downloads-cdn"`) {
		t.Errorf("missing service marker, got %q", rr.Body.String())
	}
}

func TestHandleObjectRejectsInvalidChannel(t *testing.T) {
	s := &server{}
	rr := httptest.NewRecorder()
	s.handleObject(rr, httptest.NewRequest(http.MethodGet, "/mainline/r1-linux-amd64", nil))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "invalid channel") {
		t.Errorf("missing error marker, got %q", rr.Body.String())
	}
}

func TestHandleObjectRejectsTooShortPath(t *testing.T) {
	s := &server{}
	rr := httptest.NewRecorder()
	s.handleObject(rr, httptest.NewRequest(http.MethodGet, "/prod", nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("status=%d, want 404", rr.Code)
	}
}

func TestHandleObjectRejectsParentTraversal(t *testing.T) {
	s := &server{}
	rr := httptest.NewRecorder()
	s.handleObject(rr, httptest.NewRequest(http.MethodGet, "/prod/../etc/passwd", nil))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", rr.Code)
	}
}

func TestAllowedChannelsExactlyThree(t *testing.T) {
	want := map[string]bool{"prod": true, "staging": true, "dev": true}
	if len(allowed) != 3 {
		t.Errorf("allowed has %d entries, want 3", len(allowed))
	}
	for k := range want {
		if !allowed[k] {
			t.Errorf("missing allowed channel %q", k)
		}
	}
}
