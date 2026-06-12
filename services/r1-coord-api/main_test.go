package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1-coord-api/internal/tracking"
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
	tc := &trackingClients{} // empty: all 3 vendor clients are nil-safe via Enabled() checks
	h := handleTelemetryOptIn(tc)
	rr1 := httptest.NewRecorder()
	h(rr1, httptest.NewRequest(http.MethodPost, "/v1/telemetry/opt-in", nil))
	rr2 := httptest.NewRecorder()
	h(rr2, httptest.NewRequest(http.MethodPost, "/v1/telemetry/opt-in", nil))

	var b1, b2 map[string]any
	_ = json.Unmarshal(rr1.Body.Bytes(), &b1)
	_ = json.Unmarshal(rr2.Body.Bytes(), &b2)
	s1 := b1["seq"].(float64)
	s2 := b2["seq"].(float64)
	if s2 <= s1 {
		t.Fatalf("seq did not increment: s1=%v s2=%v", s1, s2)
	}
}

func TestTelemetryOptInPropagatesBrowserAttributionToCodeRadar(t *testing.T) {
	telSeqCtr.Store(0)

	gotBody := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/track" {
			t.Fatalf("path=%q want /v1/track", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Fatalf("unmarshal body: %v", err)
		}
		gotBody <- decoded
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	cr := tracking.NewCodeRadar("https://cr_test_key@placeholder.invalid", serviceName, envName, versionStr)
	cr.BaseURL = srv.URL + "/v1"
	cr.HTTP = &http.Client{Timeout: 2 * time.Second}
	cr.Now = func() time.Time { return time.Unix(1_000_000_000, 0) }

	tc := &trackingClients{coderadar: cr}
	h := handleTelemetryOptIn(tc)

	body := strings.NewReader(`{
		"distinct_id":"anon-browser-1",
		"source":"settings_modal",
		"enabled":true,
		"install_channel":"web",
		"session_id":"browser-session-1",
		"device":"browser",
		"user_agent":"Mozilla/5.0",
		"region":"ca-bc",
		"attribution":{
			"utm_source":"relaygate",
			"utm_medium":"portfolio_link",
			"utm_campaign":"2026q2_portfolio",
			"utm_term":"ai-agent",
			"utm_content":"footer_cta",
			"ref":"partner-page",
			"gclid":"gclid-123",
			"fbclid":"fbclid-123",
			"msclkid":"msclkid-123",
			"referrer":"https://relaygate.ai/about",
			"landing_path":"/pricing",
			"ts":"2026-05-15T01:02:03Z"
		}
	}`)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/telemetry/opt-in", body)
	h(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%q", rr.Code, rr.Body.String())
	}

	select {
	case payload := <-gotBody:
		events, ok := payload["events"].([]any)
		if !ok || len(events) != 1 {
			t.Fatalf("events=%#v want single event batch", payload["events"])
		}
		event, ok := events[0].(map[string]any)
		if !ok {
			t.Fatalf("event=%#v want object", events[0])
		}
		properties, ok := event["properties"].(map[string]any)
		if !ok {
			t.Fatalf("properties=%#v want object", event["properties"])
		}
		for key, want := range map[string]any{
			"utm_source":     "relaygate",
			"utm_medium":     "portfolio_link",
			"utm_campaign":   "2026q2_portfolio",
			"utm_term":       "ai-agent",
			"utm_content":    "footer_cta",
			"ref":            "partner-page",
			"gclid":          "gclid-123",
			"fbclid":         "fbclid-123",
			"msclkid":        "msclkid-123",
			"referrer":       "https://relaygate.ai/about",
			"landing_path":   "/pricing",
			"attribution_ts": "2026-05-15T01:02:03Z",
		} {
			if properties[key] != want {
				t.Fatalf("properties[%q]=%v want %v", key, properties[key], want)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for CodeRadar request")
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
