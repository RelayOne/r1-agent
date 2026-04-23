package config

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ericmacdougall/stoke/internal/mcp"
	"gopkg.in/yaml.v3"
)

// mcpServerNameRE is the canonical name regex from specs/mcp-client.md
// §Data Models: lowercase ASCII start, alnum/underscore/hyphen tail,
// 1–32 chars total.
var mcpServerNameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// validMCPTransports is the transport enum accepted on
// ServerConfig.Transport. Any other value is rejected by
// ValidateMCPServers.
var validMCPTransports = map[string]struct{}{
	"stdio":           {},
	"http":            {},
	"streamable-http": {},
	"sse":             {},
}

// parseMCPServersBlock extracts the `mcp_servers:` top-level sequence
// from the raw policy YAML bytes using yaml.v3. Returns nil, nil when
// the block is absent (the field is optional). Structural errors (bad
// yaml, wrong node kind) return an error; semantic validation is
// ValidateMCPServers's job.
func parseMCPServersBlock(raw []byte) ([]mcp.ServerConfig, error) {
	// Use a permissive top-level map so we don't fail on any of the
	// existing custom-parsed sections (phases, files, verification,
	// skills, honesty). Only the `mcp_servers` key matters here.
	var doc struct {
		MCPServers []mcp.ServerConfig `yaml:"mcp_servers"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("mcp_servers: yaml parse: %w", err)
	}
	return doc.MCPServers, nil
}

// ValidateMCPServers enforces the rules from specs/mcp-client.md
// §Data Models across every ServerConfig. On first violation it
// returns a descriptive error identifying the offending entry by name
// (or by zero-based index when Name is empty / malformed). It also
// applies documented defaults in-place: Trust defaults to "untrusted",
// MaxConcurrent defaults to 8, Timeout defaults to "30s".
//
// Defaults are applied BEFORE validation so an operator can omit the
// three-tier timing knobs without failing the loader. Timeout is
// additionally verified as a parseable time.Duration so downstream
// transports can call time.ParseDuration without re-checking.
//
// Validation rules (spec §Data Models):
//   - Name matches ^[a-z][a-z0-9_-]{0,31}$
//   - Transport one of: stdio, http, streamable-http, sse
//   - stdio → Command non-empty
//   - http / streamable-http / sse → URL non-empty AND starts
//     with https:// unless the URL is http://localhost:*
//     or http://127.0.0.1:*
//   - Timeout parses via time.ParseDuration after default
func ValidateMCPServers(configs []mcp.ServerConfig) error {
	seen := map[string]int{}
	for i := range configs {
		cfg := &configs[i]
		label := serverLabel(cfg.Name, i)

		// Defaults first so an empty YAML field parses cleanly.
		if strings.TrimSpace(cfg.Trust) == "" {
			cfg.Trust = "untrusted"
		}
		if cfg.MaxConcurrent == 0 {
			cfg.MaxConcurrent = 8
		}
		if strings.TrimSpace(cfg.Timeout) == "" {
			cfg.Timeout = "30s"
		}

		// Name regex.
		if !mcpServerNameRE.MatchString(cfg.Name) {
			return fmt.Errorf("mcp_servers[%s]: invalid name %q: must match ^[a-z][a-z0-9_-]{0,31}$ (lowercase, alnum/underscore/hyphen, max 32 chars)", label, cfg.Name)
		}
		if prev, dup := seen[cfg.Name]; dup {
			return fmt.Errorf("mcp_servers[%s]: duplicate server name %q (also at index %d)", label, cfg.Name, prev)
		}
		seen[cfg.Name] = i

		// Transport enum.
		if _, ok := validMCPTransports[cfg.Transport]; !ok {
			return fmt.Errorf("mcp_servers[%s]: invalid transport %q: must be one of stdio, http, streamable-http, sse", label, cfg.Transport)
		}

		// Per-transport required fields.
		switch cfg.Transport {
		case "stdio":
			if strings.TrimSpace(cfg.Command) == "" {
				return fmt.Errorf("mcp_servers[%s]: transport=stdio requires non-empty command", label)
			}
		case "http", "streamable-http", "sse":
			if strings.TrimSpace(cfg.URL) == "" {
				return fmt.Errorf("mcp_servers[%s]: transport=%s requires non-empty url", label, cfg.Transport)
			}
			if err := validateMCPURLScheme(cfg.URL); err != nil {
				return fmt.Errorf("mcp_servers[%s]: %w", label, err)
			}
		}

		// Timeout must parse (after default already applied above).
		if _, err := time.ParseDuration(cfg.Timeout); err != nil {
			return fmt.Errorf("mcp_servers[%s]: invalid timeout %q: %w", label, cfg.Timeout, err)
		}
	}
	return nil
}

// serverLabel picks the most useful identifier for error messages:
// the configured Name when present, otherwise the zero-based index.
// Used exclusively to make ValidateMCPServers errors self-explanatory.
func serverLabel(name string, idx int) string {
	if strings.TrimSpace(name) != "" {
		return name
	}
	return fmt.Sprintf("#%d", idx)
}

// validateMCPURLScheme enforces the https-in-prod rule with a
// localhost dev exception. Accepts https:// always; accepts http://
// only when the host (the segment between `://` and the next `/`
// or `:`) is exactly `localhost` or `127.0.0.1`. Every other http://
// URL is rejected to prevent plaintext auth leakage to remote MCP
// servers. See specs/mcp-client.md §Security Threat Matrix (TLS
// downgrade).
func validateMCPURLScheme(url string) error {
	switch {
	case strings.HasPrefix(url, "https://"):
		return nil
	case strings.HasPrefix(url, "http://"):
		rest := strings.TrimPrefix(url, "http://")
		// Host is the span before the first `/`, `:`, or end.
		host := rest
		for i, r := range rest {
			if r == '/' || r == ':' {
				host = rest[:i]
				break
			}
		}
		if host == "localhost" || host == "127.0.0.1" {
			return nil
		}
		return fmt.Errorf("insecure url %q: http:// only allowed for localhost / 127.0.0.1 (use https:// for remote MCP servers)", url)
	default:
		return fmt.Errorf("invalid url %q: must start with https:// (or http://localhost for dev)", url)
	}
}
