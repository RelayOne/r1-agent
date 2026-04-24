package main

// agent_serve_cmd_test.go — TASK-T20 flag-surface coverage. The
// listener itself is exercised by internal/agentserve tests; here we
// only verify that --trustplane-register (and its --trustplane-endpoint
// / R1_TRUSTPLANE_ENDPOINT fallbacks) survive flag.Parse and reach
// agentServeOpts in their expected shape.

import (
	"testing"
	"time"
)

// TestAgentServe_TrustPlaneRegisterFlag_ParsedOK asserts the boolean
// flag is exposed on the agent-serve flag set and flows through into
// agentServeOpts with defaults preserved for every other field. The
// test also covers --trustplane-endpoint and the agent-id flag so a
// future revert that drops any of the three fails here first.
func TestAgentServe_TrustPlaneRegisterFlag_ParsedOK(t *testing.T) {
	t.Setenv("R1_TRUSTPLANE_ENDPOINT", "")
	t.Setenv("R1_TRUSTPLANE_DID", "")
	t.Setenv("R1_TRUSTPLANE_AGENT_ID", "")

	opts, err := parseAgentServeFlags([]string{
		"--trustplane-register",
		"--trustplane-endpoint", "https://gateway.example.com",
		"--trustplane-did", "did:plc:stoke-test",
		"--trustplane-agent-id", "stoke-t20-test",
		"--addr", ":19440",
		"--task-timeout", "42s",
		"--caps", "research,browser",
	})
	if err != nil {
		t.Fatalf("parseAgentServeFlags: %v", err)
	}

	if !opts.trustplaneRegister {
		t.Fatalf("--trustplane-register should be true after flag.Parse")
	}
	if opts.trustplaneEndpoint != "https://gateway.example.com" {
		t.Fatalf("trustplaneEndpoint = %q, want gateway url", opts.trustplaneEndpoint)
	}
	if opts.trustplaneDID != "did:plc:stoke-test" {
		t.Fatalf("trustplaneDID = %q, want did:plc:stoke-test", opts.trustplaneDID)
	}
	if opts.trustplaneAgentID != "stoke-t20-test" {
		t.Fatalf("trustplaneAgentID = %q, want stoke-t20-test", opts.trustplaneAgentID)
	}
	if opts.addr != ":19440" {
		t.Fatalf("addr = %q, want :19440", opts.addr)
	}
	if opts.timeout != 42*time.Second {
		t.Fatalf("timeout = %v, want 42s", opts.timeout)
	}
	if got, want := opts.advertised, []string{"research", "browser"}; !equalStringSlice(got, want) {
		t.Fatalf("advertised = %v, want %v", got, want)
	}

	// Env fallback path — --trustplane-endpoint unset should pick up
	// R1_TRUSTPLANE_ENDPOINT when provided.
	t.Setenv("R1_TRUSTPLANE_ENDPOINT", "https://env.example.com")
	opts, err = parseAgentServeFlags([]string{"--trustplane-register"})
	if err != nil {
		t.Fatalf("parseAgentServeFlags (env fallback): %v", err)
	}
	if opts.trustplaneEndpoint != "https://env.example.com" {
		t.Fatalf("env fallback endpoint = %q, want https://env.example.com", opts.trustplaneEndpoint)
	}
	if !opts.trustplaneRegister {
		t.Fatalf("env fallback: --trustplane-register should remain true")
	}

	// Default path — no flag, no env. Register stays false and the
	// listener behaves like a standalone hireable agent.
	t.Setenv("R1_TRUSTPLANE_ENDPOINT", "")
	opts, err = parseAgentServeFlags(nil)
	if err != nil {
		t.Fatalf("parseAgentServeFlags (defaults): %v", err)
	}
	if opts.trustplaneRegister {
		t.Fatalf("default trustplaneRegister should be false")
	}
	if opts.trustplaneEndpoint != "" {
		t.Fatalf("default endpoint should be empty, got %q", opts.trustplaneEndpoint)
	}
}

// equalStringSlice is a tiny helper so the test doesn't pull in
// reflect.DeepEqual for a two-element slice.
func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
