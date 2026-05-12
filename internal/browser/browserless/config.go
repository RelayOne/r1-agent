// config.go: Browserless provider configuration + token resolution
// chain (spec C6 T2 + T2d).
//
// Token resolution order:
//   1. cfg.Token (explicit field; highest precedence)
//   2. BROWSERLESS_TOKEN env var
//   3. ~/.r1/browser.json:browserless_token (mirrors cloud.json)
//
// Per-tenant tokens override the default. If TenantTokens[tenantID]
// is set, it wins over Token / env / file.

package browserless

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config is the Browserless provider's external configuration shape.
// Caller-friendly: every field is optional except the absolute
// minimum (an endpoint URL).
type Config struct {
	// Endpoint is the WebSocket URL (wss:// in prod; ws:// allowed
	// only when AllowInsecure=true for local docker development).
	// Examples:
	//   "wss://chrome.browserless.io"
	//   "wss://browserless.r1.run"
	Endpoint string

	// Token is the global Browserless token. Resolves via the chain
	// documented at the top of this file when empty.
	Token string

	// TenantTokens lets the operator scope tokens per tenant. When
	// SessionOpts.TenantID is in this map, the corresponding token
	// is used instead of Token / env / file.
	TenantTokens map[string]string

	// MaxConcurrent caps the number of concurrent CDP sessions. 0 → 5.
	MaxConcurrent int

	// AllowInsecure permits ws:// endpoints. False rejects with an
	// error at construction; true is for local-docker dev only.
	AllowInsecure bool

	// HTTPProxy lets the operator route the WS handshake through an
	// outbound proxy (e.g., for VPC-egress controls). Empty → direct.
	HTTPProxy string

	// DialTimeoutSeconds caps the WS handshake duration. 0 → 10s.
	DialTimeoutSeconds int

	// FileTokenPath overrides ~/.r1/browser.json for tests. Empty →
	// default location resolution.
	FileTokenPath string
}

// fileTokenShape is the on-disk record at ~/.r1/browser.json.
type fileTokenShape struct {
	BrowserlessToken string            `json:"browserless_token,omitempty"`
	TenantTokens     map[string]string `json:"browserless_tenant_tokens,omitempty"`
}

// ResolveToken walks the precedence chain to find the token for the
// given tenant. Returns the empty string when nothing is configured.
func (c Config) ResolveToken(tenantID string) string {
	if tenantID != "" {
		if t, ok := c.TenantTokens[tenantID]; ok && t != "" {
			return t
		}
	}
	if c.Token != "" {
		return c.Token
	}
	if v := strings.TrimSpace(os.Getenv("BROWSERLESS_TOKEN")); v != "" {
		return v
	}
	// File chain.
	if t := readFileToken(c.FileTokenPath, tenantID); t != "" {
		return t
	}
	return ""
}

// readFileToken loads ~/.r1/browser.json and returns the per-tenant
// token if present, else the global browserless_token.
func readFileToken(override, tenantID string) string {
	path := override
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		path = filepath.Join(home, ".r1", "browser.json")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var shape fileTokenShape
	if err := json.Unmarshal(b, &shape); err != nil {
		return ""
	}
	if tenantID != "" {
		if t, ok := shape.TenantTokens[tenantID]; ok && t != "" {
			return t
		}
	}
	return shape.BrowserlessToken
}

// Validate enforces non-empty endpoint + scheme rules. Returns nil
// when the config is usable. Callers run this at construction.
func (c Config) Validate() error {
	if strings.TrimSpace(c.Endpoint) == "" {
		return errors.New("browserless: endpoint is required")
	}
	scheme := schemeOf(c.Endpoint)
	switch scheme {
	case "wss":
		return nil
	case "ws":
		if !c.AllowInsecure {
			return fmt.Errorf("browserless: endpoint %q uses ws://; set AllowInsecure=true to permit (local-dev only)", c.Endpoint)
		}
		return nil
	default:
		return fmt.Errorf("browserless: endpoint %q must use wss:// (or ws:// with AllowInsecure)", c.Endpoint)
	}
}

// schemeOf returns the URL scheme component or "".
func schemeOf(u string) string {
	idx := strings.Index(u, "://")
	if idx <= 0 {
		return ""
	}
	return strings.ToLower(u[:idx])
}
