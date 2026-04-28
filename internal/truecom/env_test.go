package truecom

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

func capture(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	origOut := log.Writer()
	origFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(origOut)
		log.SetFlags(origFlags)
	})
	fn()
	return buf.String()
}

func TestEnvOrFallback_CanonicalOnly(t *testing.T) {
	t.Setenv("TRUECOM_API_URL", "https://truecom.example")
	t.Setenv("TRUSTPLANE_API_URL", "")
	var got string
	out := capture(t, func() {
		got = envOrFallback("TRUECOM_API_URL", "TRUSTPLANE_API_URL")
	})
	if got != "https://truecom.example" {
		t.Fatalf("value = %q", got)
	}
	if out != "" {
		t.Fatalf("expected no WARN, got: %q", out)
	}
}

func TestEnvOrFallback_LegacyOnly(t *testing.T) {
	t.Setenv("TRUECOM_API_URL", "")
	t.Setenv("TRUSTPLANE_API_URL", "https://legacy.example")
	var got string
	out := capture(t, func() {
		got = envOrFallback("TRUECOM_API_URL", "TRUSTPLANE_API_URL")
	})
	if got != "https://legacy.example" {
		t.Fatalf("value = %q", got)
	}
	if !strings.Contains(out, "TRUSTPLANE_API_URL is deprecated") {
		t.Fatalf("expected deprecation WARN, got: %q", out)
	}
	if !strings.Contains(out, "TRUECOM_API_URL") {
		t.Fatalf("WARN should name canonical replacement, got: %q", out)
	}
}

func TestEnvOrFallback_BothSet(t *testing.T) {
	t.Setenv("TRUECOM_API_URL", "https://canonical.example")
	t.Setenv("TRUSTPLANE_API_URL", "https://legacy.example")
	var got string
	out := capture(t, func() {
		got = envOrFallback("TRUECOM_API_URL", "TRUSTPLANE_API_URL")
	})
	if got != "https://canonical.example" {
		t.Fatalf("canonical should win, got = %q", got)
	}
	if out != "" {
		t.Fatalf("expected no WARN when canonical set, got: %q", out)
	}
}

func TestEnvOrFallback_Neither(t *testing.T) {
	t.Setenv("TRUECOM_API_URL", "")
	t.Setenv("TRUSTPLANE_API_URL", "")
	got := envOrFallback("TRUECOM_API_URL", "TRUSTPLANE_API_URL")
	if got != "" {
		t.Fatalf("expected \"\", got %q", got)
	}
}

func TestEnvOrFallback_WhitespaceOnlyTreatedAsUnset(t *testing.T) {
	t.Setenv("TRUECOM_API_URL", "   ")
	t.Setenv("TRUSTPLANE_API_URL", "https://legacy.example")
	got := envOrFallback("TRUECOM_API_URL", "TRUSTPLANE_API_URL")
	if got != "https://legacy.example" {
		t.Fatalf("whitespace-only canonical should fall through to legacy; got %q", got)
	}
}
