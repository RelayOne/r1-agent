// Package mcp — types.go — client-facing interface + value types for Stoke
// as an MCP consumer. Stoke's server-side MCP types (LegacyServerConfig,
// ToolDefinition as used by CodebaseServer / StokeServer) live in
// client.go / codebase_server.go / r1_server.go and are not affected
// by this interface.

package mcp

import (
	"context"
	"encoding/json"
	"errors"
)

// Client is the canonical interface every transport (stdio, HTTP,
// SSE) satisfies. Caller code holds a Client, not a concrete
// transport type. See specs/mcp-client.md §Library selection.
type Client interface {
	Initialize(ctx context.Context) error
	ListTools(ctx context.Context) ([]Tool, error)
	CallTool(ctx context.Context, name string, args json.RawMessage) (ToolResult, error)
	Close() error
}

// ServerConfig is the operator-configured description of ONE MCP
// server in stoke.policy.yaml. See spec §Data Models for every
// field + validation rule (name regex, transport enum, http→https
// rule, required-fields-per-transport). Keep every field + tag the
// spec enumerates; do not re-order or drop any.
type ServerConfig struct {
	Name          string            `yaml:"name"`
	Transport     string            `yaml:"transport"` // "stdio" | "http" | "sse"
	Command       string            `yaml:"command,omitempty"`
	Args          []string          `yaml:"args,omitempty"`
	URL           string            `yaml:"url,omitempty"`
	AuthEnv       string            `yaml:"auth_env,omitempty"`
	Env           map[string]string `yaml:"env,omitempty"`
	Trust         string            `yaml:"trust,omitempty"`          // "trusted" | "untrusted"; default untrusted
	MaxConcurrent int               `yaml:"max_concurrent,omitempty"` // per-server concurrent call cap
	Timeout       string            `yaml:"timeout,omitempty"`        // e.g. "30s"; parsed to time.Duration
	Enabled       bool              `yaml:"enabled,omitempty"`
}

// ToolDefinition is a remote MCP server's tool advertisement. The
// same struct is used internally by Stoke's own MCP servers
// (CodebaseServer, StokeServer, LanesServer) to describe the tools
// they expose, since the on-the-wire shape is identical.
//
// OutputSchema was added for the lanes-protocol §7 contract
// (specs/lanes-protocol.md TASK-18) which mandates that each lane
// tool advertise a JSON Schema draft 2020-12 document for its result
// envelope so MCP clients can do client-side validation. Existing
// tools that don't supply OutputSchema serialize it via omitempty
// so the wire shape is unchanged for them.
type ToolDefinition struct {
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"input_schema"`
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`
}

// Tool is the Stoke-facing value for a remote tool returned from
// Client.ListTools. Wraps ToolDefinition with the server prefix
// used in mcp_* dispatch names.
type Tool struct {
	Definition ToolDefinition
	ServerName string // populated at list time
	Trust      string // inherited from ServerConfig.Trust
}

// ToolResult is the value returned from Client.CallTool.
type ToolResult struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

// Content is one content block from a ToolResult.
type Content struct {
	Type string          `json:"type"`
	Text string          `json:"text,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
	MIME string          `json:"mimeType,omitempty"`
}

// Error sentinels — every package consumer errors.Is-checks these.
var (
	// ErrCircuitOpen is returned by Client.CallTool when the
	// circuit breaker has opened on this server.
	ErrCircuitOpen = errors.New("mcp: circuit open")

	// ErrAuthMissing is returned when the server requires an env
	// var named in ServerConfig.AuthEnv but it is unset or empty.
	ErrAuthMissing = errors.New("mcp: auth env missing")

	// ErrPolicyDenied is returned when the trust gate rejects a
	// tool call (untrusted server + worker not permitted).
	ErrPolicyDenied = errors.New("mcp: policy denied")

	// ErrSchemaInvalid is returned when the remote server sends a
	// tool definition whose input_schema is not valid JSON Schema.
	ErrSchemaInvalid = errors.New("mcp: schema invalid")

	// ErrSizeCap is returned when the result body exceeds the
	// per-call size cap configured on the transport.
	ErrSizeCap = errors.New("mcp: size cap exceeded")
)
