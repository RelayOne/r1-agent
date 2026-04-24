package mcp

import (
	"errors"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/logging"
)

// TestRedact_RoundTrip is the canonical happy path from
// specs/mcp-client.md §Auth / Secret Handling: register a fake
// secret, generate a log line containing it, redact via the
// logging-package helper, assert the secret is replaced with
// "<redacted>". Then unregister and re-run: the same text passes
// through untouched.
func TestRedact_RoundTrip(t *testing.T) {
	const envName = "STOKE_TEST_MCP_AUTH_REDACT"
	const secret = "sekret123"

	t.Setenv(envName, secret)

	cfgs := []ServerConfig{
		{Name: "fake", Transport: "stdio", AuthEnv: envName},
	}

	unregister, err := RegisterAuthSecrets(cfgs)
	if err != nil {
		t.Fatalf("RegisterAuthSecrets: unexpected error: %v", err)
	}
	if unregister == nil {
		t.Fatal("RegisterAuthSecrets: unregister closure is nil")
	}

	// Simulated log line: would normally be routed through the
	// logging package's redactor before egress.
	logLine := "MCP call failed with token=" + secret + " trailing"
	redacted := logging.Redact(logLine)

	if strings.Contains(redacted, secret) {
		t.Fatalf("redacted output still contains secret: %q", redacted)
	}
	if !strings.Contains(redacted, "<redacted>") {
		t.Fatalf("redacted output missing <redacted> marker: %q", redacted)
	}

	// Unregister: the redactor should now be a no-op for this
	// secret (the value is free to reappear in subsequent logs).
	unregister()

	afterUnreg := logging.Redact(logLine)
	if !strings.Contains(afterUnreg, secret) {
		t.Fatalf("after unregister, redactor still transformed output: %q", afterUnreg)
	}
	if strings.Contains(afterUnreg, "<redacted>") {
		t.Fatalf("after unregister, output unexpectedly contains <redacted>: %q", afterUnreg)
	}
}

// TestRedact_AuthMissing asserts that a configured AuthEnv whose
// env var resolves to empty string surfaces ErrAuthMissing without
// registering anything in the logging redactor.
func TestRedact_AuthMissing(t *testing.T) {
	const envName = "STOKE_TEST_MCP_AUTH_MISSING"
	// Explicitly unset in case the environment pollutes us.
	t.Setenv(envName, "")

	cfgs := []ServerConfig{
		{Name: "needs-auth", Transport: "stdio", AuthEnv: envName},
	}

	unregister, err := RegisterAuthSecrets(cfgs)
	if !errors.Is(err, ErrAuthMissing) {
		t.Fatalf("expected ErrAuthMissing, got %v", err)
	}
	if unregister != nil {
		t.Fatal("expected nil unregister closure on failure")
	}
}

// TestRedact_AuthMissingIsPositional asserts that when a later cfg
// triggers ErrAuthMissing, earlier successfully-resolved secrets
// are NOT left registered (fail-closed construction: nothing
// changed).
func TestRedact_AuthMissingIsPositional(t *testing.T) {
	const okEnv = "STOKE_TEST_MCP_AUTH_OK"
	const missEnv = "STOKE_TEST_MCP_AUTH_MISS"
	const secret = "early-secret-that-must-not-linger"
	t.Setenv(okEnv, secret)
	t.Setenv(missEnv, "")

	cfgs := []ServerConfig{
		{Name: "first", Transport: "stdio", AuthEnv: okEnv},
		{Name: "second", Transport: "stdio", AuthEnv: missEnv},
	}
	unregister, err := RegisterAuthSecrets(cfgs)
	if !errors.Is(err, ErrAuthMissing) {
		t.Fatalf("expected ErrAuthMissing, got %v", err)
	}
	if unregister != nil {
		t.Fatal("expected nil unregister on failure")
	}
	// The earlier secret must NOT have been registered.
	if got := logging.Redact("x=" + secret); !strings.Contains(got, secret) {
		t.Fatalf("early secret was registered despite later failure: %q", got)
	}
}

// TestRedact_DeduplicatesSharedAuthEnv asserts that two server
// configs pointing at the SAME AuthEnv are handled by the Register
// API correctly — one register call should fully tear down via a
// single unregister. We verify through observable redaction
// behavior rather than poking the private map.
func TestRedact_DeduplicatesSharedAuthEnv(t *testing.T) {
	const envName = "STOKE_TEST_MCP_AUTH_SHARED"
	const secret = "shared-token-xyz-deadbeef"
	t.Setenv(envName, secret)

	cfgs := []ServerConfig{
		{Name: "a", Transport: "stdio", AuthEnv: envName},
		{Name: "b", Transport: "stdio", AuthEnv: envName},
	}

	unregister, err := RegisterAuthSecrets(cfgs)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	// Sanity: redaction works while registered.
	line := "value=" + secret
	if got := logging.Redact(line); strings.Contains(got, secret) {
		t.Fatalf("secret leaked through redactor: %q", got)
	}

	unregister()

	// After a SINGLE unregister, the secret must no longer be
	// redacted — if Register had fired twice (once per cfg) we'd
	// need two unregisters to fully drop it.
	if got := logging.Redact(line); !strings.Contains(got, secret) {
		t.Fatalf("dedup broken: one unregister call did not fully drop the secret: %q", got)
	}
}

// TestRedact_EmptyAuthEnv asserts that configs without any AuthEnv
// field don't touch the registry and return a valid (no-op)
// unregister closure.
func TestRedact_EmptyAuthEnv(t *testing.T) {
	cfgs := []ServerConfig{
		{Name: "no-auth", Transport: "stdio"},
	}
	unregister, err := RegisterAuthSecrets(cfgs)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if unregister == nil {
		t.Fatal("unregister closure is nil")
	}
	// A line containing a non-registered value is passed through.
	const nonce = "unregistered-marker-value"
	if got := logging.Redact(nonce); got != nonce {
		t.Fatalf("registry mutated for cfg with no AuthEnv — got %q want %q", got, nonce)
	}
	unregister() // must not panic
}
