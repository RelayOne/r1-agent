// Package config — studio.go
//
// R1S-1 of work-r1-actium-studio-skills.md. Defines the `studio_config`
// section R1 uses to drive the Actium Studio skill pack's runtime.
//
// The struct mirrors the shape specified in work order §4.1. Secrets
// live in env vars (indirected via TokenEnv); only references land in
// on-disk config. Env-var precedence follows work-r1-rename.md §S1-1:
// canonical R1_ACTIUM_STUDIO_* read first, legacy STOKE_ACTIUM_STUDIO_*
// fall back with a single-shot deprecation WARN during the 90-day
// window (ending 2026-07-23).
//
// The resolver `LoadStudioConfig` overlays env-var values onto a base
// struct (typically zero-valued or decoded from JSON/YAML), so operators
// can set any of these via env without editing config files:
//
//	R1_ACTIUM_STUDIO_ENABLED        → StudioConfig.Enabled
//	R1_ACTIUM_STUDIO_TRANSPORT      → StudioConfig.Transport
//	R1_ACTIUM_STUDIO_BASE_URL       → StudioConfig.HTTP.BaseURL
//	R1_ACTIUM_STUDIO_SCOPES         → StudioConfig.HTTP.ScopesHeader
//	R1_ACTIUM_STUDIO_TOKEN_ENV      → StudioConfig.HTTP.TokenEnv
//
// The token itself is not read here; the HTTPTransport resolves it
// per-call via TokenEnv indirection (work order §4.3: R1 never stores
// key material).
package config

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/RelayOne/r1/internal/r1env"
)

// DefaultStudioScopes is the scope string forwarded on the X-Studio-Scopes
// header when StudioConfig.HTTP.ScopesHeader is empty. Matches the Studio
// MCP client's default in services/mcp-server/src/tools/studio.ts:55.
const DefaultStudioScopes = "studio:sites:scaffold"

// Studio transport enum values. `Transport` accepts these literals only.
const (
	StudioTransportHTTP     = "http"
	StudioTransportStdioMCP = "stdio-mcp"
)

// StudioConfig is the on-disk config block for the Actium Studio skill
// pack. It is consumed by internal/studioclient to build the correct
// Transport and by the skill pack registration path (R1S-1.2) to decide
// whether the pack's skills are active at all.
type StudioConfig struct {
	// Enabled gates the entire pack. When false, skill resolution for
	// `studio.*` names short-circuits with ErrStudioDisabled. Default
	// false — the pack is opt-in per work order §5.
	Enabled bool `json:"enabled" yaml:"enabled"`

	// Transport is "http" or "stdio-mcp". Default "http" when empty.
	Transport string `json:"transport" yaml:"transport"`

	HTTP     StudioHTTPConfig     `json:"http" yaml:"http"`
	StdioMCP StudioStdioMCPConfig `json:"stdio_mcp" yaml:"stdio_mcp"`
	LLM      StudioLLMConfig      `json:"llm" yaml:"llm"`
}

// StudioHTTPConfig captures the HTTP transport's wiring.
type StudioHTTPConfig struct {
	// BaseURL is the Studio API root, e.g. "https://studio.actium.dev".
	// Trailing slash is stripped by the client.
	BaseURL string `json:"base_url" yaml:"base_url"`

	// ScopesHeader is forwarded as X-Studio-Scopes. Empty → DefaultStudioScopes.
	ScopesHeader string `json:"scopes_header" yaml:"scopes_header"`

	// TokenEnv is the name of the env var holding the bearer token.
	// The token is not stored in this struct. Empty TokenEnv leaves the
	// Authorization header unset (useful only against local dev servers
	// without auth; production will 401).
	TokenEnv string `json:"token_env" yaml:"token_env"`
}

// StudioStdioMCPConfig captures the subprocess invocation for the stdio
// MCP transport. Used by StdioMCPTransport in studioclient.
type StudioStdioMCPConfig struct {
	// Command is the argv to spawn the Studio MCP server, e.g.
	// []string{"npx", "actium-studio-mcp"}. Empty disables the
	// transport (LoadStudioConfig surfaces that as ErrStudioDisabled
	// when Transport == "stdio-mcp").
	Command []string `json:"command" yaml:"command"`

	// Env is extra env vars forwarded to the subprocess. Merged over
	// the inherited environment; keys here win.
	Env map[string]string `json:"env" yaml:"env"`
}

// StudioLLMConfig records the LLM topology Studio itself should use.
// R1 does not forward credentials; Studio resolves them itself per the
// indirection model in work order §4.2. These fields are informational
// for the HTTP transport (logged at startup) and forwarded to the
// subprocess in the stdio-MCP transport.
type StudioLLMConfig struct {
	// OpenRouterBaseURL — "" means "let Studio resolve its own LLM
	// endpoint". "https://relaygate.<tenant>.dev/openrouter/v1" routes
	// through RelayGate (Topology A in work order §4.2). Any other
	// value is Topology B (direct).
	OpenRouterBaseURL string `json:"openrouter_base_url" yaml:"openrouter_base_url"`

	// DefaultModel is the preferred model name, e.g. "claude-sonnet-4".
	// Empty means Studio picks its own default.
	DefaultModel string `json:"default_model" yaml:"default_model"`
}

// DefaultStudioConfig returns the zero-value that every operator starts
// from: pack disabled, HTTP transport, no endpoint. Enabling the pack
// is an affirmative config act.
func DefaultStudioConfig() StudioConfig {
	return StudioConfig{
		Enabled:   false,
		Transport: StudioTransportHTTP,
	}
}

// ApplyEnv overlays env-var values onto the receiver using the
// R1_ACTIUM_STUDIO_* → STOKE_ACTIUM_STUDIO_* dual-accept pattern from
// work-r1-rename.md §S1-1. Returns the mutated config for chaining.
//
// Any env var whose canonical AND legacy forms are both unset leaves
// the existing field value intact, so callers can decode a JSON/YAML
// block and then ApplyEnv to layer operator overrides on top.
//
// Fields controlled by env:
//
//	R1_ACTIUM_STUDIO_ENABLED    → Enabled (truthy: "1", "true", "yes", "on")
//	R1_ACTIUM_STUDIO_TRANSPORT  → Transport ("http" | "stdio-mcp")
//	R1_ACTIUM_STUDIO_BASE_URL   → HTTP.BaseURL
//	R1_ACTIUM_STUDIO_SCOPES     → HTTP.ScopesHeader
//	R1_ACTIUM_STUDIO_TOKEN_ENV  → HTTP.TokenEnv
//
// The token itself is never read here — LoadStudioConfig does not
// touch secret material. The HTTP transport reads `os.Getenv(TokenEnv)`
// per-call.
func (c *StudioConfig) ApplyEnv() *StudioConfig {
	if v := r1env.Get("R1_ACTIUM_STUDIO_ENABLED", "STOKE_ACTIUM_STUDIO_ENABLED"); v != "" {
		c.Enabled = parseStudioBool(v, c.Enabled)
	}
	if v := r1env.Get("R1_ACTIUM_STUDIO_TRANSPORT", "STOKE_ACTIUM_STUDIO_TRANSPORT"); v != "" {
		c.Transport = v
	}
	if v := r1env.Get("R1_ACTIUM_STUDIO_BASE_URL", "STOKE_ACTIUM_STUDIO_BASE_URL"); v != "" {
		c.HTTP.BaseURL = v
	}
	if v := r1env.Get("R1_ACTIUM_STUDIO_SCOPES", "STOKE_ACTIUM_STUDIO_SCOPES"); v != "" {
		c.HTTP.ScopesHeader = v
	}
	if v := r1env.Get("R1_ACTIUM_STUDIO_TOKEN_ENV", "STOKE_ACTIUM_STUDIO_TOKEN_ENV"); v != "" {
		c.HTTP.TokenEnv = v
	}
	return c
}

// Validate returns non-nil when the config is internally inconsistent.
// A disabled config is always valid regardless of other fields, so
// operators can keep stale values in their file without breaking
// startup.
func (c StudioConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	switch c.Transport {
	case "", StudioTransportHTTP:
		if c.HTTP.BaseURL == "" {
			return fmt.Errorf("studio_config.http.base_url required when transport=http")
		}
		if !strings.HasPrefix(c.HTTP.BaseURL, "http://") && !strings.HasPrefix(c.HTTP.BaseURL, "https://") {
			return fmt.Errorf("studio_config.http.base_url must start with http:// or https://")
		}
	case StudioTransportStdioMCP:
		if len(c.StdioMCP.Command) == 0 {
			return fmt.Errorf("studio_config.stdio_mcp.command required when transport=stdio-mcp")
		}
	default:
		return fmt.Errorf("studio_config.transport %q not recognized (want %q or %q)",
			c.Transport, StudioTransportHTTP, StudioTransportStdioMCP)
	}
	return nil
}

// ResolvedTransport returns the transport in use after defaulting.
// Empty Transport coerces to "http"; any other value passes through.
func (c StudioConfig) ResolvedTransport() string {
	if c.Transport == "" {
		return StudioTransportHTTP
	}
	return c.Transport
}

// ResolvedScopes returns the effective X-Studio-Scopes header value.
func (c StudioConfig) ResolvedScopes() string {
	if s := strings.TrimSpace(c.HTTP.ScopesHeader); s != "" {
		return s
	}
	return DefaultStudioScopes
}

// UnmarshalStudioConfig decodes a JSON-encoded studio_config block and
// applies ApplyEnv afterward. Exposed as a helper so the top-level
// config loader (claude_settings.go etc.) can defer to a single entry
// point when it adds the new section.
func UnmarshalStudioConfig(raw []byte) (StudioConfig, error) {
	c := DefaultStudioConfig()
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &c); err != nil {
			return StudioConfig{}, fmt.Errorf("decode studio_config: %w", err)
		}
	}
	c.ApplyEnv()
	return c, nil
}

// parseStudioBool accepts the canonical shell-truthy strings. Falls
// back to the provided current value on any unrecognized token so
// stale env vars don't silently flip the switch.
func parseStudioBool(s string, current bool) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return current
	}
}
