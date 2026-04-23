package provider

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// captureLogger returns a log-compatible func and a pointer to the slice
// where each formatted line is appended.
func captureLogger() (func(string, ...any), *[]string) {
	var lines []string
	return func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	}, &lines
}

// roundTrip spins up an httptest server that replies with the requested
// response headers, then returns the resulting *http.Response so each
// test can hand it to ReadTierHeaders unmodified. The body is closed
// synchronously inside the helper — ReadTierHeaders only touches
// resp.Header, so a closed body is fine, and closing here (rather
// than via t.Cleanup on the caller) keeps the bodyclose linter happy.
func roundTrip(t *testing.T, headers map[string]string) *http.Response {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("httptest Get: %v", err)
	}
	_ = resp.Body.Close()
	return resp
}

func TestReadTierHeaders_BothPresent(t *testing.T) {
	resp := roundTrip(t, map[string]string{ //nolint:bodyclose // closed inside roundTrip before return
		"X-Model-Tier":     "reasoning",
		"X-Model-Resolved": "claude-opus-4-7",
	})

	log, lines := captureLogger()
	ReadTierHeaders(resp, "tier:reasoning", log)

	if len(*lines) != 2 {
		t.Fatalf("expected 2 log lines, got %d: %#v", len(*lines), *lines)
	}
	if !strings.Contains((*lines)[0], "model tier resolved: reasoning") {
		t.Errorf("tier line missing/mis-shaped: %q", (*lines)[0])
	}
	if !strings.Contains((*lines)[1], "alias=tier:reasoning") || !strings.Contains((*lines)[1], "resolved=claude-opus-4-7") {
		t.Errorf("resolved line missing alias/resolved pair: %q", (*lines)[1])
	}
}

func TestReadTierHeaders_NothingEmitted_WhenHeadersAbsent(t *testing.T) {
	resp := roundTrip(t, nil) //nolint:bodyclose // closed inside roundTrip before return

	log, lines := captureLogger()
	ReadTierHeaders(resp, "tier:reasoning", log)

	if len(*lines) != 0 {
		t.Fatalf("expected 0 log lines when headers absent, got %d: %#v", len(*lines), *lines)
	}
}

func TestReadTierHeaders_OnlyTier(t *testing.T) {
	resp := roundTrip(t, map[string]string{ //nolint:bodyclose // closed inside roundTrip before return
		"X-Model-Tier": "smart",
	})

	log, lines := captureLogger()
	ReadTierHeaders(resp, "smart", log)

	if len(*lines) != 1 {
		t.Fatalf("expected exactly 1 log line for partial presence, got %d: %#v", len(*lines), *lines)
	}
	if !strings.Contains((*lines)[0], "model tier resolved: smart") {
		t.Errorf("unexpected tier-only line: %q", (*lines)[0])
	}
}

func TestReadTierHeaders_NilResponseAndNilLogger(t *testing.T) {
	// Both nil-resp and nil-logger must be no-ops (guard against misuse
	// by non-HTTP providers like Ember, or tests that pass nil log).
	ReadTierHeaders(nil, "x", func(string, ...any) {
		t.Fatalf("log should not be called when resp is nil")
	})

	//nolint:bodyclose // closed inside roundTrip before return
	resp := roundTrip(t, map[string]string{"X-Model-Tier": "reasoning"})
	ReadTierHeaders(resp, "x", nil) // must not panic
}
