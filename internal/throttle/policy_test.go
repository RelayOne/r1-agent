package throttle_test

import (
	"strings"
	"testing"

	"golang.org/x/time/rate"

	"github.com/RelayOne/r1/internal/throttle"
	throttlepolicy "github.com/RelayOne/r1/internal/throttle/policy"
)

// TestParseRate covers T5: rate-string grammar.
func TestParseRate(t *testing.T) {
	t.Parallel()
	var perMinute rate.Limit = 100.0 / 60.0
	var perHour rate.Limit = 5.0 / 3600.0
	cases := []struct {
		in       string
		wantRate rate.Limit
		wantErr  bool
	}{
		{"1/s", 1, false},
		{"100/s", 100, false},
		{"100/min", perMinute, false},
		{"5/hour", perHour, false},
		{"10 / s", 10, false},
		{"1/sec", 1, false},
		{"30/seconds", 30, false},
		{"", 0, true},
		{"1/", 0, true},
		{"/s", 0, true},
		{"-1/s", 0, true},
		{"abc/s", 0, true},
		{"1/day", 0, true},
		{"0/s", 0, true},
	}
	for _, tc := range cases {
		got, err := throttlepolicy.ParseRate(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseRate(%q) want error, got %v", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseRate(%q) unexpected error: %v", tc.in, err)
			continue
		}
		if float64(got) != float64(tc.wantRate) {
			t.Errorf("ParseRate(%q) = %v, want %v", tc.in, got, tc.wantRate)
		}
	}
}

// TestValidateRejectsMalformedGlob covers T6 + T18 (override
// rejection: bad bracket).
func TestValidateRejectsMalformedGlob(t *testing.T) {
	t.Parallel()
	cfg := throttlepolicy.Config{
		Defaults: throttlepolicy.Scoped{
			PerSession: throttlepolicy.Limit{Rate: "1/s", Burst: 1},
			PerTenant:  throttlepolicy.Limit{Rate: "1/s", Burst: 1},
		},
		Overrides: []throttlepolicy.Override{
			{Principal: "tenant:enterprise-[", Multiplier: 5},
		},
	}
	_, err := throttlepolicy.Validate(cfg)
	if err == nil {
		t.Fatalf("expected validation error for malformed glob")
	}
	if !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("error should mention 'malformed', got: %v", err)
	}
}

// TestValidateRejectsBadBurst covers T18 (burst=0 rejection).
func TestValidateRejectsBadBurst(t *testing.T) {
	t.Parallel()
	cfg := throttlepolicy.Config{
		Defaults: throttlepolicy.Scoped{
			PerSession: throttlepolicy.Limit{Rate: "1/s", Burst: 0},
		},
	}
	if _, err := throttlepolicy.Validate(cfg); err == nil {
		t.Fatalf("expected error for burst=0")
	}
}

// TestValidateRejectsBadMultiplier covers T18 (multiplier <= 0).
func TestValidateRejectsBadMultiplier(t *testing.T) {
	t.Parallel()
	cfg := throttlepolicy.Config{
		Overrides: []throttlepolicy.Override{
			{Principal: "ok", Multiplier: -1},
		},
	}
	if _, err := throttlepolicy.Validate(cfg); err == nil {
		t.Fatalf("expected error for multiplier=-1")
	}
}

// TestDefaultPolicyLoads ensures the embedded YAML parses and
// validates so a stale defaults file is caught at process start.
func TestDefaultPolicyLoads(t *testing.T) {
	t.Parallel()
	cfg := throttle.DefaultPolicy()
	if cfg.IsZero() {
		t.Fatalf("default policy unexpectedly zero")
	}
	if cfg.Defaults.PerSession.IsZero() || cfg.Defaults.PerTenant.IsZero() {
		t.Fatalf("default policy missing defaults: %+v", cfg.Defaults)
	}
	if len(cfg.Tools) < 30 {
		t.Fatalf("expected >=30 tool entries in bundled defaults (38 r1.* tools + diagnostics), got %d", len(cfg.Tools))
	}
}
