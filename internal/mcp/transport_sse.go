// transport_sse.go — SSE (legacy) transport for Stoke's MCP client.
//
// This is a thin wrapper around github.com/mark3labs/mcp-go's SSE client
// that adapts it to Stoke's internal Client interface (see types.go).
// SSE is kept only for back-compat with servers that have not yet
// migrated to Streamable-HTTP (see specs/mcp-client.md §Transport
// Details -- SSE).
//
// Scope (MCP-5):
//   * Construct / validate a ServerConfig with Transport == "sse" (or
//     "http" when the HTTP transport falls through in MCP-4).
//   * Perform the MCP Initialize handshake with a 5s timeout.
//   * Proxy ListTools / CallTool / Close to mcp-go.
//   * On Initialize success, log a deprecation notice. Actual
//     bus/streamjson event emission lands in MCP-10.
//   * Expose SetReconnectHandler so the circuit breaker (MCP-7/MCP-8)
//     can observe disconnects later. For now the handler is invoked
//     with an increasing attempt counter on every reconnect.
//
// Not in scope (deliberately left for later MCP-* tasks):
//   * Circuit breaker wiring (MCP-7 / MCP-8).
//   * Structured bus + streamjson events (MCP-10).
//   * Auth-env redaction registration (MCP-9).
//   * Per-server concurrency semaphore (MCP-6).

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcptransport "github.com/mark3labs/mcp-go/client/transport"
	mcpproto "github.com/mark3labs/mcp-go/mcp"
)

// initializeTimeout is shared with transport_stdio.go — the per-Initialize
// upper bound (5s). The spec (specs/mcp-client.md §Transport Details) ties
// SSE connect latency to the same budget so a dead server does not hang
// worker spawn.

// SSETransport is a thin adapter around mcp-go's SSE client that
// satisfies Stoke's Client interface. It is safe for concurrent use
// from multiple goroutines; the underlying mcp-go client serializes
// JSON-RPC writes internally.
type SSETransport struct {
	cfg ServerConfig
	url string

	// mu guards cli + reconnectHandler so Initialize / Close /
	// SetReconnectHandler can race without corrupting state.
	mu   sync.Mutex
	cli  *mcpclient.Client
	sse  *mcptransport.SSE
	open atomic.Bool // true between successful Initialize and Close

	// reconnectAttempts is incremented every time the underlying SSE
	// connection drops. It is exposed only to the reconnect handler.
	reconnectAttempts atomic.Int64

	reconnectHandler func(attempt int, err error)
}

// NewSSETransport validates cfg and returns an un-initialized
// transport. It does NOT dial the server; Initialize performs the
// handshake. Validation rules match specs/mcp-client.md §Data Models:
//
//   - cfg.Transport must be "sse". "http" is accepted as a fall-through
//     hook from the Streamable-HTTP transport (MCP-4) which demotes to
//     SSE when a server advertises SSE-only.
//   - cfg.URL must be non-empty and begin with http:// or https://.
//
// Every other ServerConfig field is left for later MCP-* tasks to
// interpret; we do not reject unknown/unsupported values here so the
// same config object can flow through multiple transports.
func NewSSETransport(cfg ServerConfig) (*SSETransport, error) {
	switch cfg.Transport {
	case "sse", "http":
		// ok — "http" is the MCP-4 fall-through path (see file header).
	default:
		return nil, fmt.Errorf("mcp/sse: unsupported transport %q (want \"sse\" or \"http\")", cfg.Transport)
	}
	if strings.TrimSpace(cfg.URL) == "" {
		return nil, errors.New("mcp/sse: ServerConfig.URL is required for SSE transport")
	}
	if !strings.HasPrefix(cfg.URL, "http://") && !strings.HasPrefix(cfg.URL, "https://") {
		return nil, fmt.Errorf("mcp/sse: URL must start with http:// or https:// (got %q)", cfg.URL)
	}
	if _, err := url.Parse(cfg.URL); err != nil {
		return nil, fmt.Errorf("mcp/sse: invalid URL %q: %w", cfg.URL, err)
	}

	return &SSETransport{cfg: cfg, url: cfg.URL}, nil
}

// SetReconnectHandler installs a callback invoked once per SSE
// disconnect. The first argument is the 1-based attempt count (1 on
// the first drop, 2 on the second, ...); the second is the error that
// tore the stream down (may be io.EOF for a clean remote close).
//
// Safe to call before or after Initialize. Calling it with a nil
// handler clears the current one. The circuit breaker wired up in
// MCP-7 will register here.
func (t *SSETransport) SetReconnectHandler(h func(attempt int, err error)) {
	t.mu.Lock()
	t.reconnectHandler = h
	// Propagate immediately if we are already dialed so a late
	// registration still sees future drops.
	sse := t.sse
	t.mu.Unlock()
	if sse != nil {
		t.wireReconnect(sse)
	}
}

// wireReconnect attaches our increment-then-dispatch handler onto the
// mcp-go SSE transport. It is safe to call multiple times; the
// underlying transport uses a single-slot handler under a mutex.
func (t *SSETransport) wireReconnect(sse *mcptransport.SSE) {
	sse.SetConnectionLostHandler(func(err error) {
		attempt := int(t.reconnectAttempts.Add(1))
		t.mu.Lock()
		h := t.reconnectHandler
		t.mu.Unlock()
		if h != nil {
			h(attempt, err)
		}
	})
}

// Initialize creates the mcp-go SSE client, opens the SSE stream, and
// performs the MCP initialize handshake. Subject to a 5s upper-bound
// (initializeTimeout) layered on top of any deadline already on ctx —
// whichever fires first wins.
func (t *SSETransport) Initialize(ctx context.Context) error {
	t.mu.Lock()
	if t.cli != nil {
		t.mu.Unlock()
		return errors.New("mcp/sse: Initialize called twice (transport already initialized)")
	}
	t.mu.Unlock()

	// Build the mcp-go SSE transport.
	sse, err := mcptransport.NewSSE(t.url)
	if err != nil {
		return fmt.Errorf("mcp/sse: build SSE transport: %w", err)
	}

	// The Start context must outlive Initialize — mcp-go's SSE transport
	// hangs its long-lived GET stream on the ctx passed to Start
	// (transport/sse.go derives a cancellable child from it and wires
	// cancellation of that child into the stream teardown). Passing a
	// short-lived initCtx here would tear down the stream as soon as
	// Initialize returns, breaking every subsequent RPC. Use a
	// background-derived context for Start and rely on Close() +
	// mcp-go's own cancelSSEStream to shut the stream down cleanly.
	//
	// The 5s initializeTimeout budget is enforced on initCtx below,
	// which bounds only the handshake RPCs — not the stream itself.
	startCtx := context.Background()
	initCtx, initCancel := context.WithTimeout(ctx, initializeTimeout)
	defer initCancel()

	cli := mcpclient.NewClient(sse)
	if err := cli.Start(startCtx); err != nil {
		_ = cli.Close()
		return fmt.Errorf("mcp/sse: start transport: %w", err)
	}

	initReq := mcpproto.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcpproto.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcpproto.Implementation{
		Name:    "stoke",
		Version: "mcp-client/0.1",
	}
	if _, err := cli.Initialize(initCtx, initReq); err != nil {
		_ = cli.Close()
		return fmt.Errorf("mcp/sse: initialize handshake: %w", err)
	}

	t.mu.Lock()
	t.cli = cli
	t.sse = sse
	t.mu.Unlock()
	t.open.Store(true)

	// Wire reconnect handler now that the transport exists. Any
	// handler installed before Initialize still fires; any handler
	// installed after re-wires itself via SetReconnectHandler.
	t.wireReconnect(sse)

	// MCP-10 will replace this with a structured bus event.
	log.Printf("mcp: SSE transport is deprecated; prefer Streamable-HTTP (server=%q url=%q)", t.cfg.Name, t.url)

	return nil
}

// ListTools proxies to the underlying mcp-go client and adapts the
// result into Stoke's []Tool shape.
func (t *SSETransport) ListTools(ctx context.Context) ([]Tool, error) {
	cli, err := t.client()
	if err != nil {
		return nil, err
	}
	resp, err := cli.ListTools(ctx, mcpproto.ListToolsRequest{})
	if err != nil {
		return nil, fmt.Errorf("mcp/sse: tools/list: %w", err)
	}
	out := make([]Tool, 0, len(resp.Tools))
	for _, remote := range resp.Tools {
		schema, serr := json.Marshal(remote.InputSchema)
		if serr != nil {
			return nil, fmt.Errorf("mcp/sse: marshal input_schema for %q: %w", remote.Name, serr)
		}
		out = append(out, Tool{
			Definition: ToolDefinition{
				Name:        remote.Name,
				Description: remote.Description,
				InputSchema: schema,
			},
			ServerName: t.cfg.Name,
			Trust:      t.cfg.Trust,
		})
	}
	return out, nil
}

// CallTool invokes a remote tool and adapts the result into Stoke's
// ToolResult. Only TextContent is projected into ToolResult.Content
// for now — richer types (image / audio / resource_link) are mirrored
// as empty-typed Content entries so callers can still count parts
// without crashing. MCP-10 will add structured event emission.
func (t *SSETransport) CallTool(ctx context.Context, name string, args json.RawMessage) (ToolResult, error) {
	cli, err := t.client()
	if err != nil {
		return ToolResult{}, err
	}

	var argsAny any
	if len(args) > 0 {
		var decoded any
		if err = json.Unmarshal(args, &decoded); err != nil {
			return ToolResult{}, fmt.Errorf("mcp/sse: decode args for %q: %w", name, err)
		}
		argsAny = decoded
	}

	req := mcpproto.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = argsAny

	resp, err := cli.CallTool(ctx, req)
	if err != nil {
		return ToolResult{}, fmt.Errorf("mcp/sse: tools/call %q: %w", name, err)
	}

	out := ToolResult{IsError: resp.IsError}
	for _, c := range resp.Content {
		switch v := c.(type) {
		case mcpproto.TextContent:
			out.Content = append(out.Content, Content{Type: "text", Text: v.Text})
		case mcpproto.ImageContent:
			out.Content = append(out.Content, Content{Type: "image", MIME: v.MIMEType})
		case mcpproto.AudioContent:
			out.Content = append(out.Content, Content{Type: "audio", MIME: v.MIMEType})
		default:
			// Best-effort: surface whatever came back so callers can log
			// the type tag. Full fidelity lands in MCP-10.
			out.Content = append(out.Content, Content{Type: "unknown"})
		}
	}
	return out, nil
}

// Close terminates the SSE stream and releases the mcp-go client.
// Idempotent — calling Close multiple times returns nil after the
// first successful shutdown.
func (t *SSETransport) Close() error {
	t.mu.Lock()
	cli := t.cli
	t.cli = nil
	t.sse = nil
	t.mu.Unlock()

	if cli == nil {
		return nil
	}
	t.open.Store(false)
	if err := cli.Close(); err != nil {
		return fmt.Errorf("mcp/sse: close: %w", err)
	}
	return nil
}

// client returns the initialized mcp-go client or an error. It is the
// single gate every RPC-issuing method funnels through so the "must
// Initialize first" contract is enforced in one place.
func (t *SSETransport) client() (*mcpclient.Client, error) {
	t.mu.Lock()
	cli := t.cli
	t.mu.Unlock()
	if cli == nil {
		return nil, errors.New("mcp/sse: transport not initialized (call Initialize first)")
	}
	return cli, nil
}

// Compile-time check: *SSETransport satisfies Client.
var _ Client = (*SSETransport)(nil)
