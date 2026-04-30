package config

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/r1env"
)

func TestDefaultStudioConfig(t *testing.T) {
	c := DefaultStudioConfig()
	if c.Enabled {
		t.Errorf("default Enabled = true, want false (pack is opt-in)")
	}
	if c.Transport != StudioTransportHTTP {
		t.Errorf("default Transport = %q, want %q", c.Transport, StudioTransportHTTP)
	}
}

func TestStudioConfig_Validate(t *testing.T) {
	cases := []struct {
		name    string
		c       StudioConfig
		wantErr string
	}{
		{
			name:    "disabled is always valid",
			c:       StudioConfig{Enabled: false, Transport: "garbage"},
			wantErr: "",
		},
		{
			name:    "http ok",
			c:       StudioConfig{Enabled: true, Transport: "http", HTTP: StudioHTTPConfig{BaseURL: "https://studio.actium.dev"}},
			wantErr: "",
		},
		{
			name:    "http default transport ok",
			c:       StudioConfig{Enabled: true, HTTP: StudioHTTPConfig{BaseURL: "http://localhost:4000"}},
			wantErr: "",
		},
		{
			name:    "http missing base url",
			c:       StudioConfig{Enabled: true, Transport: "http"},
			wantErr: "base_url required",
		},
		{
			name:    "http non-http scheme",
			c:       StudioConfig{Enabled: true, Transport: "http", HTTP: StudioHTTPConfig{BaseURL: "gopher://nope"}},
			wantErr: "http:// or https://",
		},
		{
			name:    "stdio ok",
			c:       StudioConfig{Enabled: true, Transport: "stdio-mcp", StdioMCP: StudioStdioMCPConfig{Command: []string{"npx", "actium-studio-mcp"}}},
			wantErr: "",
		},
		{
			name:    "stdio missing cmd",
			c:       StudioConfig{Enabled: true, Transport: "stdio-mcp"},
			wantErr: "command required",
		},
		{
			name:    "unknown transport",
			c:       StudioConfig{Enabled: true, Transport: "grpc"},
			wantErr: "not recognized",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.c.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate() = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}

func TestStudioConfig_ResolvedDefaults(t *testing.T) {
	c := StudioConfig{}
	if got := c.ResolvedTransport(); got != StudioTransportHTTP {
		t.Errorf("ResolvedTransport empty = %q, want %q", got, StudioTransportHTTP)
	}
	if got := c.ResolvedScopes(); got != DefaultStudioScopes {
		t.Errorf("ResolvedScopes empty = %q, want %q", got, DefaultStudioScopes)
	}
	c2 := StudioConfig{Transport: "stdio-mcp", HTTP: StudioHTTPConfig{ScopesHeader: "custom:scope"}}
	if got := c2.ResolvedTransport(); got != "stdio-mcp" {
		t.Errorf("ResolvedTransport set = %q, want stdio-mcp", got)
	}
	if got := c2.ResolvedScopes(); got != "custom:scope" {
		t.Errorf("ResolvedScopes set = %q, want custom:scope", got)
	}
}

func TestStudioConfig_ApplyEnv_Canonical(t *testing.T) {
	r1env.ResetWarnOnceForTests()
	t.Setenv("R1_ACTIUM_STUDIO_ENABLED", "true")
	t.Setenv("R1_ACTIUM_STUDIO_TRANSPORT", "stdio-mcp")
	t.Setenv("R1_ACTIUM_STUDIO_BASE_URL", "https://canonical.test")
	t.Setenv("R1_ACTIUM_STUDIO_SCOPES", "studio:custom")
	t.Setenv("R1_ACTIUM_STUDIO_TOKEN_ENV", "STUDIO_TOKEN")

	c := DefaultStudioConfig()
	c.ApplyEnv()

	if !c.Enabled {
		t.Errorf("Enabled = false, want true")
	}
	if c.Transport != "stdio-mcp" {
		t.Errorf("Transport = %q, want stdio-mcp", c.Transport)
	}
	if c.HTTP.BaseURL != "https://canonical.test" {
		t.Errorf("BaseURL = %q", c.HTTP.BaseURL)
	}
	if c.HTTP.ScopesHeader != "studio:custom" {
		t.Errorf("ScopesHeader = %q", c.HTTP.ScopesHeader)
	}
	if c.HTTP.TokenEnv != "STUDIO_TOKEN" {
		t.Errorf("TokenEnv = %q", c.HTTP.TokenEnv)
	}
}

func TestStudioConfig_ApplyEnv_LegacyFallback(t *testing.T) {
	// Rename-window test: only STOKE_* set; R1 should still resolve.
	r1env.ResetWarnOnceForTests()
	t.Setenv("R1_ACTIUM_STUDIO_BASE_URL", "")
	t.Setenv("STOKE_ACTIUM_STUDIO_BASE_URL", "https://legacy.test")

	c := DefaultStudioConfig()
	c.ApplyEnv()
	if c.HTTP.BaseURL != "https://legacy.test" {
		t.Errorf("BaseURL = %q, want legacy fallback", c.HTTP.BaseURL)
	}
}

func TestStudioConfig_ApplyEnv_CanonicalWinsOverLegacy(t *testing.T) {
	r1env.ResetWarnOnceForTests()
	t.Setenv("R1_ACTIUM_STUDIO_BASE_URL", "https://canonical.test")
	t.Setenv("STOKE_ACTIUM_STUDIO_BASE_URL", "https://legacy.test")

	c := DefaultStudioConfig()
	c.ApplyEnv()
	if c.HTTP.BaseURL != "https://canonical.test" {
		t.Errorf("BaseURL = %q, want canonical to win", c.HTTP.BaseURL)
	}
}

func TestStudioConfig_ApplyEnv_UnsetPreservesBase(t *testing.T) {
	// When no env vars are set, ApplyEnv must leave the decoded values
	// alone (config-file overrides env absence, not env absence overriding).
	r1env.ResetWarnOnceForTests()
	t.Setenv("R1_ACTIUM_STUDIO_BASE_URL", "")
	t.Setenv("STOKE_ACTIUM_STUDIO_BASE_URL", "")

	c := StudioConfig{Enabled: true, HTTP: StudioHTTPConfig{BaseURL: "https://from-file"}}
	c.ApplyEnv()
	if c.HTTP.BaseURL != "https://from-file" {
		t.Errorf("BaseURL = %q, want preserved", c.HTTP.BaseURL)
	}
	if !c.Enabled {
		t.Errorf("Enabled lost")
	}
}

func TestParseStudioBool(t *testing.T) {
	cases := []struct {
		in      string
		current bool
		want    bool
	}{
		{"1", false, true},
		{"true", false, true},
		{"TRUE", false, true},
		{"yes", false, true},
		{"on", false, true},
		{"0", true, false},
		{"false", true, false},
		{"no", true, false},
		{"off", true, false},
		{"garbage", true, true},   // preserves current
		{"garbage", false, false}, // preserves current
		{"  yes  ", false, true},  // trims
	}
	for _, tc := range cases {
		got := parseStudioBool(tc.in, tc.current)
		if got != tc.want {
			t.Errorf("parseStudioBool(%q, current=%v) = %v, want %v", tc.in, tc.current, got, tc.want)
		}
	}
}

func TestUnmarshalStudioConfig(t *testing.T) {
	r1env.ResetWarnOnceForTests()
	t.Setenv("R1_ACTIUM_STUDIO_ENABLED", "")
	t.Setenv("STOKE_ACTIUM_STUDIO_ENABLED", "")
	raw := []byte(`{
		"enabled": true,
		"transport": "http",
		"http": { "base_url": "https://studio.local", "token_env": "STUDIO_TOKEN" },
		"llm":  { "openrouter_base_url": "https://relaygate.dev/openrouter/v1", "default_model": "claude-sonnet-4" }
	}`)
	c, err := UnmarshalStudioConfig(raw)
	if err != nil {
		t.Fatalf("UnmarshalStudioConfig: %v", err)
	}
	if !c.Enabled {
		t.Errorf("Enabled lost")
	}
	if c.HTTP.BaseURL != "https://studio.local" {
		t.Errorf("BaseURL = %q", c.HTTP.BaseURL)
	}
	if c.LLM.DefaultModel != "claude-sonnet-4" {
		t.Errorf("DefaultModel = %q", c.LLM.DefaultModel)
	}
	if err := c.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestUnmarshalStudioConfig_EmptyReturnsDefault(t *testing.T) {
	r1env.ResetWarnOnceForTests()
	t.Setenv("R1_ACTIUM_STUDIO_ENABLED", "")
	t.Setenv("STOKE_ACTIUM_STUDIO_ENABLED", "")
	c, err := UnmarshalStudioConfig(nil)
	if err != nil {
		t.Fatalf("nil input: %v", err)
	}
	if c.Enabled {
		t.Errorf("empty → Enabled true; want default false")
	}
	if c.Transport != StudioTransportHTTP {
		t.Errorf("empty → Transport %q", c.Transport)
	}
}

func TestStudioConfig_JSONRoundTrip(t *testing.T) {
	c := StudioConfig{
		Enabled:   true,
		Transport: "stdio-mcp",
		HTTP:      StudioHTTPConfig{BaseURL: "https://a.b", ScopesHeader: "x:y", TokenEnv: "T"},
		StdioMCP:  StudioStdioMCPConfig{Command: []string{"npx", "pkg"}, Env: map[string]string{"K": "v"}},
		LLM:       StudioLLMConfig{OpenRouterBaseURL: "https://or", DefaultModel: "m"},
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back StudioConfig
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Transport != "stdio-mcp" || back.HTTP.BaseURL != "https://a.b" || back.StdioMCP.Command[1] != "pkg" || back.LLM.DefaultModel != "m" {
		t.Errorf("round trip lost fields: %+v", back)
	}
}
