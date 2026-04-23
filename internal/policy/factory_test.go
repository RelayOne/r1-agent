package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// minimalYAML is a trivially valid policy accepted by NewYAMLClient.
// Modeled on the fixture used in yaml_engine_test.go.
const minimalYAML = `rules:
  - id: allow-bash
    effect: permit
    actions: [bash]
`

// writeFactoryYAML drops minimalYAML in a t-scoped tempdir and returns its path.
func writeFactoryYAML(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.yaml")
	if err := os.WriteFile(path, []byte(minimalYAML), 0o600); err != nil {
		t.Fatalf("write factory yaml: %v", err)
	}
	return path
}

// TestFactory_PrefersCedarWhenEndpointSet verifies the tier-2 cedar-agent
// backend wins when CLOUDSWARM_POLICY_ENDPOINT is set.
func TestFactory_PrefersCedarWhenEndpointSet(t *testing.T) {
	t.Setenv(EnvCloudSwarmEndpoint, "http://127.0.0.1:8180")
	t.Setenv(EnvCloudSwarmToken, "")
	// Ensure no file override is inherited from the host env.
	t.Setenv(EnvStokePolicyFile, "")

	c, backend, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: unexpected error: %v", err)
	}
	if c == nil {
		t.Fatalf("NewFromEnv: got nil Client")
	}
	if _, ok := c.(*HTTPClient); !ok {
		t.Fatalf("want *HTTPClient, got %T", c)
	}
	if !strings.HasPrefix(backend, "cedar-agent ") {
		t.Fatalf("backend want prefix %q, got %q", "cedar-agent ", backend)
	}
}

// TestFactory_FallsBackToYAMLWhenOnlyFileSet verifies that when only
// STOKE_POLICY_FILE is set the tier-1 in-process YAML backend is returned.
func TestFactory_FallsBackToYAMLWhenOnlyFileSet(t *testing.T) {
	path := writeFactoryYAML(t)
	t.Setenv(EnvCloudSwarmEndpoint, "")
	t.Setenv(EnvCloudSwarmToken, "")
	t.Setenv(EnvStokePolicyFile, path)

	c, backend, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: unexpected error: %v", err)
	}
	if _, ok := c.(*YAMLClient); !ok {
		t.Fatalf("want *YAMLClient, got %T", c)
	}
	if !strings.HasPrefix(backend, "yaml ") {
		t.Fatalf("backend want prefix %q, got %q", "yaml ", backend)
	}
	if !strings.Contains(backend, path) {
		t.Fatalf("backend %q should contain file path %q", backend, path)
	}
}

// TestFactory_FallsBackToNullWhenNeitherSet verifies the dev-mode NullClient
// fallback when neither env var is set.
func TestFactory_FallsBackToNullWhenNeitherSet(t *testing.T) {
	t.Setenv(EnvCloudSwarmEndpoint, "")
	t.Setenv(EnvCloudSwarmToken, "")
	t.Setenv(EnvStokePolicyFile, "")

	c, backend, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: unexpected error: %v", err)
	}
	if _, ok := c.(*NullClient); !ok {
		t.Fatalf("want *NullClient, got %T", c)
	}
	if backend != "null" {
		t.Fatalf("backend want %q, got %q", "null", backend)
	}
}

// TestFactory_ReturnsErrorOnUnreadableFile verifies an error is returned
// when STOKE_POLICY_FILE points at a path that does not exist.
func TestFactory_ReturnsErrorOnUnreadableFile(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "does-not-exist.yaml")

	t.Setenv(EnvCloudSwarmEndpoint, "")
	t.Setenv(EnvCloudSwarmToken, "")
	t.Setenv(EnvStokePolicyFile, missing)

	c, backend, err := NewFromEnv()
	if err == nil {
		t.Fatalf("NewFromEnv: want non-nil error for missing file, got nil (client=%T backend=%q)", c, backend)
	}
	if c != nil {
		t.Fatalf("NewFromEnv: want nil Client on error, got %T", c)
	}
}

// TestFactory_CedarTakesPrecedenceOverFile verifies that even when both
// env vars are set, the cedar-agent endpoint wins per the documented
// precedence order.
func TestFactory_CedarTakesPrecedenceOverFile(t *testing.T) {
	path := writeFactoryYAML(t)
	t.Setenv(EnvCloudSwarmEndpoint, "http://127.0.0.1:8180")
	t.Setenv(EnvCloudSwarmToken, "tok-abc")
	t.Setenv(EnvStokePolicyFile, path)

	c, backend, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: unexpected error: %v", err)
	}
	if _, ok := c.(*HTTPClient); !ok {
		t.Fatalf("want *HTTPClient (cedar precedence), got %T", c)
	}
	if !strings.HasPrefix(backend, "cedar-agent ") {
		t.Fatalf("backend want prefix %q, got %q", "cedar-agent ", backend)
	}
	if strings.HasPrefix(backend, "yaml ") {
		t.Fatalf("backend must not be yaml when endpoint is set: %q", backend)
	}
}
