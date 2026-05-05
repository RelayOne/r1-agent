package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthzReturnsOK(t *testing.T) {
	rr := httptest.NewRecorder()
	handleHealthz(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rr.Code)
	}
	var body healthz
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !body.OK || body.Service != serviceName {
		t.Fatalf("body=%+v", body)
	}
}

func TestLicenseVerifyAcceptsLongKey(t *testing.T) {
	body := strings.NewReader(`{"key":"valid-key-1234"}`)
	rr := httptest.NewRecorder()
	handleLicenseVerify(rr, httptest.NewRequest(http.MethodPost, "/v1/license/verify", body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rr.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["valid"] != true {
		t.Fatalf("expected valid=true, got %+v", resp)
	}
}

func TestLicenseVerifyRejectsShortKey(t *testing.T) {
	body := strings.NewReader(`{"key":"x"}`)
	rr := httptest.NewRecorder()
	handleLicenseVerify(rr, httptest.NewRequest(http.MethodPost, "/v1/license/verify", body))
	var resp map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["valid"] != false {
		t.Fatalf("expected valid=false for short key, got %+v", resp)
	}
}

func TestTelemetryOptInIncrementsSeq(t *testing.T) {
	rr1 := httptest.NewRecorder()
	handleTelemetryOptIn(rr1, httptest.NewRequest(http.MethodPost, "/v1/telemetry/opt-in", nil))
	rr2 := httptest.NewRecorder()
	handleTelemetryOptIn(rr2, httptest.NewRequest(http.MethodPost, "/v1/telemetry/opt-in", nil))

	var b1, b2 map[string]any
	_ = json.Unmarshal(rr1.Body.Bytes(), &b1)
	_ = json.Unmarshal(rr2.Body.Bytes(), &b2)
	s1 := b1["seq"].(float64)
	s2 := b2["seq"].(float64)
	if s2 <= s1 {
		t.Fatalf("seq did not increment: s1=%v s2=%v", s1, s2)
	}
}

func TestRootReturnsServiceMetadata(t *testing.T) {
	rr := httptest.NewRecorder()
	handleRoot(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rr.Code)
	}
	var resp map[string]string
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["service"] != serviceName {
		t.Fatalf("expected service=%q, got %q", serviceName, resp["service"])
	}
}

func TestRootUnknownPathReturns404(t *testing.T) {
	rr := httptest.NewRecorder()
	handleRoot(rr, httptest.NewRequest(http.MethodGet, "/unknown", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", rr.Code)
	}
}
