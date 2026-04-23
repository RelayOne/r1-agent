package policy

import (
	"fmt"
	"os"
	"strings"
)

// Environment variables consulted by NewFromEnv. Documented here so
// operators can grep for the exact names used in production.
const (
	// EnvCloudSwarmEndpoint, when set, selects the tier-2 cedar-agent
	// HTTP backend. Its value is the base URL of the sidecar (e.g.
	// "http://127.0.0.1:8180").
	EnvCloudSwarmEndpoint = "CLOUDSWARM_POLICY_ENDPOINT"

	// EnvCloudSwarmToken is the optional Bearer credential sent with
	// every cedar-agent request. Empty means "no Authorization header".
	EnvCloudSwarmToken = "CLOUDSWARM_POLICY_TOKEN"

	// EnvStokePolicyFile, when set, selects the tier-1 in-process YAML
	// backend. Its value is a filesystem path to the policy file.
	EnvStokePolicyFile = "STOKE_POLICY_FILE"
)

// NewFromEnv reads policy configuration from the environment and returns
// the appropriate Client. Precedence:
//  1. CLOUDSWARM_POLICY_ENDPOINT → tier-2 cedar-agent HTTPClient (token from CLOUDSWARM_POLICY_TOKEN)
//  2. STOKE_POLICY_FILE → tier-1 local YAMLClient
//  3. (none) → NullClient (dev-mode banner, allow-all)
//
// Returns a constructed Client and a one-line "backend" string for logs
// (e.g. "cedar-agent https://…", "yaml /path/to/policy.yaml", "null").
func NewFromEnv() (Client, string, error) {
	if ep := strings.TrimSpace(os.Getenv(EnvCloudSwarmEndpoint)); ep != "" {
		token := os.Getenv(EnvCloudSwarmToken)
		hc, err := NewHTTPClient(ep, token)
		if err != nil {
			return nil, "", fmt.Errorf("policy: %s=%q: %w", EnvCloudSwarmEndpoint, ep, err)
		}
		return hc, "cedar-agent " + ep, nil
	}

	if path := strings.TrimSpace(os.Getenv(EnvStokePolicyFile)); path != "" {
		yc, err := NewYAMLClient(path)
		if err != nil {
			return nil, "", err
		}
		return yc, "yaml " + path, nil
	}

	return &NullClient{}, "null", nil
}
