package policy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	policytesting "github.com/ericmacdougall/stoke/internal/policy/testing"
)

// writeIntegrationYAML drops body in a t-scoped tempdir and returns the path.
// Kept local to avoid coupling to helpers in yaml_engine_test.go / factory_test.go.
func writeIntegrationYAML(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write integration yaml: %v", err)
	}
	return path
}

// TestIntegration_YAMLDispatch exercises the full Client-interface flow
// through a tier-1 YAMLClient loaded from disk: an explicit allow, an
// explicit deny (ordered before a would-be-allow to prove first-match
// wins), and a fall-through to default-deny.
func TestIntegration_YAMLDispatch(t *testing.T) {
	t.Parallel()

	body := `rules:
  - id: allow-file-read
    effect: permit
    actions: [file_read]
  - id: deny-bash-rm
    effect: forbid
    actions: [bash]
    when:
      - command matches "^rm "
  - id: allow-bash
    effect: permit
    actions: [bash]
`
	path := writeIntegrationYAML(t, body)

	var client Client
	yc, err := NewYAMLClient(path)
	if err != nil {
		t.Fatalf("NewYAMLClient: %v", err)
	}
	client = yc // exercise through the interface

	cases := []struct {
		name     string
		req      Request
		wantDec  Decision
		wantReas string
	}{
		{
			name:     "allow-file-read",
			req:      Request{Action: "file_read"},
			wantDec:  DecisionAllow,
			wantReas: "allow-file-read",
		},
		{
			name: "deny-bash-rm-wins-over-allow-bash",
			req: Request{
				Action:  "bash",
				Context: map[string]any{"command": "rm -rf /tmp/x"},
			},
			wantDec:  DecisionDeny,
			wantReas: "deny-bash-rm",
		},
		{
			name: "no-rule-matches-defaults-to-deny",
			req: Request{
				Action:  "mcp_linear_create_issue",
				Context: map[string]any{},
			},
			wantDec:  DecisionDeny,
			wantReas: "default-deny",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, err := client.Check(context.Background(), tc.req)
			if err != nil {
				t.Fatalf("Check: unexpected error: %v", err)
			}
			if got.Decision != tc.wantDec {
				t.Fatalf("decision: want %v, got %v (reasons=%v)", tc.wantDec, got.Decision, got.Reasons)
			}
			if len(got.Reasons) != 1 || got.Reasons[0] != tc.wantReas {
				t.Fatalf("reasons: want [%q], got %v", tc.wantReas, got.Reasons)
			}
		})
	}
}

// TestIntegration_CedarEmulatorRoundtrip stands up the in-repo PARC
// emulator and drives HTTPClient against it across three cases:
// explicit allow rule, explicit deny rule, and no-rule-matches (the
// emulator's canonical deny with reason "no-match").
func TestIntegration_CedarEmulatorRoundtrip(t *testing.T) {
	t.Parallel()

	srv := policytesting.NewServer([]policytesting.Rule{
		{
			Action:   "bash.allowed",
			Resource: "Tool::\"bash\"",
			Decision: "Allow",
			Reason:   "policy-allow-bash",
		},
		{
			Action:   "bash.denied",
			Resource: "Tool::\"bash\"",
			Decision: "Deny",
			Reason:   "policy-deny-bash",
		},
	})
	t.Cleanup(srv.Close)

	hc, err := NewHTTPClient(srv.URL, "")
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	var client Client = hc // exercise through the interface

	cases := []struct {
		name     string
		req      Request
		wantDec  Decision
		wantReas string
		wantErr  bool
	}{
		{
			name: "emulator-allow-rule-match",
			req: Request{
				Principal: "User::\"alice\"",
				Action:    "bash.allowed",
				Resource:  "Tool::\"bash\"",
				Context:   map[string]any{"trust_level": 3},
			},
			wantDec:  DecisionAllow,
			wantReas: "policy-allow-bash",
		},
		{
			name: "emulator-deny-rule-match",
			req: Request{
				Principal: "User::\"alice\"",
				Action:    "bash.denied",
				Resource:  "Tool::\"bash\"",
				Context:   map[string]any{"trust_level": 3},
			},
			wantDec:  DecisionDeny,
			wantReas: "policy-deny-bash",
		},
		{
			name: "emulator-no-match-fails-closed",
			req: Request{
				Principal: "User::\"alice\"",
				Action:    "bash.unknown",
				Resource:  "Tool::\"bash\"",
			},
			wantDec:  DecisionDeny,
			wantReas: "no-match",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, err := client.Check(context.Background(), tc.req)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Check err: want err=%v, got %v", tc.wantErr, err)
			}
			if got.Decision != tc.wantDec {
				t.Fatalf("decision: want %v, got %v (reasons=%v errors=%v)",
					tc.wantDec, got.Decision, got.Reasons, got.Errors)
			}
			if len(got.Reasons) != 1 || got.Reasons[0] != tc.wantReas {
				t.Fatalf("reasons: want [%q], got %v", tc.wantReas, got.Reasons)
			}
		})
	}
}

// TestIntegration_NewFromEnvRoutesToYAML sets STOKE_POLICY_FILE and
// asserts NewFromEnv returns a working tier-1 backend with the
// expected "yaml <path>" banner. Uses t.Setenv so must not run in
// parallel.
func TestIntegration_NewFromEnvRoutesToYAML(t *testing.T) {
	body := `rules:
  - id: integration-yaml
    effect: permit
    actions: [bash]
`
	path := writeIntegrationYAML(t, body)

	// Clear the cedar endpoint so the YAML branch is reachable.
	t.Setenv(EnvCloudSwarmEndpoint, "")
	t.Setenv(EnvCloudSwarmToken, "")
	t.Setenv(EnvStokePolicyFile, path)

	client, backend, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: unexpected error: %v", err)
	}
	if client == nil {
		t.Fatalf("NewFromEnv: got nil client")
	}
	if !strings.HasPrefix(backend, "yaml ") {
		t.Fatalf("backend: want prefix %q, got %q", "yaml ", backend)
	}
	if !strings.Contains(backend, path) {
		t.Fatalf("backend %q should contain file path %q", backend, path)
	}

	got, err := client.Check(context.Background(), Request{Action: "bash"})
	if err != nil {
		t.Fatalf("Check: unexpected error: %v", err)
	}
	if got.Decision != DecisionAllow {
		t.Fatalf("decision: want Allow, got %v (reasons=%v)", got.Decision, got.Reasons)
	}
	if len(got.Reasons) != 1 || got.Reasons[0] != "integration-yaml" {
		t.Fatalf("reasons: want [integration-yaml], got %v", got.Reasons)
	}
}

// TestIntegration_NewFromEnvRoutesToCedar stands up the PARC
// emulator, points CLOUDSWARM_POLICY_ENDPOINT at it, and asserts
// NewFromEnv returns a working tier-2 backend with the expected
// "cedar-agent <url>" banner. Uses t.Setenv so must not run in
// parallel.
func TestIntegration_NewFromEnvRoutesToCedar(t *testing.T) {
	srv := policytesting.NewServer([]policytesting.Rule{
		{
			Action:   "bash.exec",
			Decision: "Allow",
			Reason:   "policy-allow-bash",
		},
	})
	t.Cleanup(srv.Close)

	t.Setenv(EnvCloudSwarmEndpoint, srv.URL)
	t.Setenv(EnvCloudSwarmToken, "")
	// Ensure the YAML branch is not inadvertently preferred.
	t.Setenv(EnvStokePolicyFile, "")

	client, backend, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: unexpected error: %v", err)
	}
	if client == nil {
		t.Fatalf("NewFromEnv: got nil client")
	}
	if !strings.HasPrefix(backend, "cedar-agent ") {
		t.Fatalf("backend: want prefix %q, got %q", "cedar-agent ", backend)
	}
	if !strings.Contains(backend, srv.URL) {
		t.Fatalf("backend %q should contain emulator URL %q", backend, srv.URL)
	}

	got, err := client.Check(context.Background(), Request{
		Principal: "User::\"alice\"",
		Action:    "bash.exec",
		Resource:  "Tool::\"bash\"",
	})
	if err != nil {
		t.Fatalf("Check: unexpected error: %v", err)
	}
	if got.Decision != DecisionAllow {
		t.Fatalf("decision: want Allow, got %v (reasons=%v errors=%v)",
			got.Decision, got.Reasons, got.Errors)
	}
	if len(got.Reasons) != 1 || got.Reasons[0] != "policy-allow-bash" {
		t.Fatalf("reasons: want [policy-allow-bash], got %v", got.Reasons)
	}
}
