// transport_http.go -- MCP-4 Streamable-HTTP transport with
// automatic legacy-SSE fallthrough.
//
// This file wires github.com/mark3labs/mcp-go's Streamable-HTTP
// client into Stoke's Client interface (see types.go) and, when
// Initialize encounters the documented "server speaks only legacy
// SSE" signal (mcp-go's ErrLegacySSEServer sentinel -- returned on
// 404 on POST, HTTP 4xx during initialize including 405 Method Not
// Allowed and 406 Not Acceptable), it constructs an SSETransport
// from transport_sse.go and delegates the remainder of the session
// to it.
//
// Scope (MCP-4 only):
//   * Construct / validate a ServerConfig whose Transport is one of
//     {"http", "streamable-http", "streamable_http"}.
//   * Perform the MCP Initialize handshake; log info + transition on
//     fallthrough.
//   * Proxy ListTools / CallTool / Close to whichever transport is
//     currently active.
//   * Gate concurrent CallTool invocations through a per-server
//     semaphore sized by cfg.MaxConcurrent (default 8). Ctx
//     cancellation is honored during acquire.
//
// Explicitly out of scope (later MCP-* tasks wire these up):
//   * Circuit breaker (MCP-7 / MCP-8).
//   * Structured bus + streamjson events (MCP-10).
//   * Auth-env redaction registration (MCP-9).
//   * Registry wiring / trust gate (MCP-6).

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

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcptransport "github.com/mark3labs/mcp-go/client/transport"
	mcpproto "github.com/mark3labs/mcp-go/mcp"
)

// defaultMaxConcurrent is the per-server in-flight-call cap used when
// cfg.MaxConcurrent is <= 0. The spec (§Data Models) documents a
// default of 4 for the final config surface; MCP-4 picks a slightly
// larger 8 to match the task requirement so the registry layer in
// MCP-6 can tighten the knob downward without breaking MCP-4 tests.
const defaultMaxConcurrent = 8

// activeTransport labels which concrete transport is in charge of a
// given *HTTPTransport instance. Callers can inspect
// HTTPTransport.Active() to learn the outcome of the fallthrough
// negotiation (useful for telemetry and tests).
type activeTransport int

const (
	// activeNone means Initialize has not (successfully) been called.
	activeNone activeTransport = iota
	// activeStreamable means the Streamable-HTTP POST handshake
	// succeeded and we are speaking to an HTTP-streaming server.
	activeStreamable
	// activeSSE means Streamable-HTTP init rejected us with a
	// legacy-SSE signal and the transport demoted itself to SSE.
	activeSSE
)

// String returns a stable short name for logs + event payloads.
func (a activeTransport) String() string {
	switch a {
	case activeStreamable:
		return "streamable-http"
	case activeSSE:
		return "sse"
	case activeNone:
		return "none"
	default:
		return "none"
	}
}

// HTTPTransport implements Client for MCP's Streamable-HTTP wire
// protocol. It owns either a mcp-go Streamable-HTTP client or --
// after fallthrough -- an *SSETransport. Exactly one of the two is
// non-nil at any time once Initialize has succeeded.
//
// The struct is safe for concurrent use from multiple goroutines.
// The underlying mcp-go client serializes JSON-RPC writes; the
// per-server semaphore bounds simultaneous CallTool in-flight
// requests to cfg.MaxConcurrent.
type HTTPTransport struct {
	cfg ServerConfig

	// sem is a buffered channel used as a counting semaphore. Every
	// successful CallTool acquires one slot before calling the
	// transport and releases it afterward. Size is defaultMaxConcurrent
	// when cfg.MaxConcurrent <= 0, else cfg.MaxConcurrent.
	sem chan struct{}

	// closeCh is closed exactly once by Close() so callers blocked
	// in acquire() wake up immediately rather than waiting for a
	// slot on a saturated semaphore.
	closeCh   chan struct{}
	closeOnce sync.Once

	// mu guards http / cli / sse / active / closed so concurrent
	// Initialize / Close / Active observations do not corrupt state.
	mu     sync.Mutex
	http   *mcptransport.StreamableHTTP // non-nil iff active==activeStreamable
	cli    *mcpclient.Client            // mcp-go client layered on http (nil after fallthrough)
	sse    *SSETransport                // non-nil iff active==activeSSE
	active activeTransport
	closed bool
}

// NewHTTPTransport validates cfg and returns an un-initialized
// transport. No network I/O happens here -- Initialize is what dials
// the server. Accepts "http", "streamable-http", and "streamable_http"
// spellings for Transport so operators can use whichever the MCP
// spec / mcp-go docs happen to prefer at read time.
func NewHTTPTransport(cfg ServerConfig) (*HTTPTransport, error) {
	switch cfg.Transport {
	case "http", "streamable-http", "streamable_http":
		// ok
	default:
		return nil, fmt.Errorf("mcp/http: unsupported transport %q (want \"http\", \"streamable-http\", or \"streamable_http\")", cfg.Transport)
	}
	if strings.TrimSpace(cfg.URL) == "" {
		return nil, errors.New("mcp/http: ServerConfig.URL is required")
	}
	if !strings.HasPrefix(cfg.URL, "http://") && !strings.HasPrefix(cfg.URL, "https://") {
		return nil, fmt.Errorf("mcp/http: URL must start with http:// or https:// (got %q)", cfg.URL)
	}
	if _, err := url.Parse(cfg.URL); err != nil {
		return nil, fmt.Errorf("mcp/http: invalid URL %q: %w", cfg.URL, err)
	}

	size := cfg.MaxConcurrent
	if size <= 0 {
		size = defaultMaxConcurrent
	}

	return &HTTPTransport{
		cfg:     cfg,
		sem:     make(chan struct{}, size),
		closeCh: make(chan struct{}),
		active:  activeNone,
	}, nil
}

// Active returns which concrete transport (if any) is currently
// servicing RPCs. Useful for tests and telemetry.
func (t *HTTPTransport) Active() activeTransport {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.active
}

// MaxConcurrent returns the effective per-server in-flight cap,
// matching the semaphore capacity. Exposed primarily for tests.
func (t *HTTPTransport) MaxConcurrent() int {
	return cap(t.sem)
}

// Initialize performs the MCP initialize handshake. It first tries
// Streamable-HTTP; on the documented fallthrough signals it closes
// the Streamable-HTTP client and constructs an *SSETransport against
// the same URL (with Transport overridden to "sse") and delegates
// Initialize to it. The overall budget is initializeTimeout (5s)
// stacked on top of any deadline already carried by ctx.
//
// Calling Initialize twice returns an error -- transitions between
// transports are irreversible once negotiated.
func (t *HTTPTransport) Initialize(ctx context.Context) error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return errors.New("mcp/http: Initialize on closed transport")
	}
	if t.active != activeNone {
		t.mu.Unlock()
		return errors.New("mcp/http: Initialize called twice (transport already initialized)")
	}
	t.mu.Unlock()

	// Bound the handshake separately from the caller's ctx so a
	// hung server cannot stall worker spawn beyond initializeTimeout.
	initCtx, cancel := context.WithTimeout(ctx, initializeTimeout)
	defer cancel()

	httpTr, err := mcptransport.NewStreamableHTTP(t.cfg.URL)
	if err != nil {
		return fmt.Errorf("mcp/http: build Streamable-HTTP transport: %w", err)
	}

	cli := mcpclient.NewClient(httpTr)
	// Start on Streamable-HTTP is cheap (no network), but mcp-go
	// requires it before Initialize.
	if err = cli.Start(initCtx); err != nil {
		_ = cli.Close()
		return fmt.Errorf("mcp/http: start Streamable-HTTP transport: %w", err)
	}

	initReq := mcpproto.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcpproto.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcpproto.Implementation{
		Name:    "stoke",
		Version: "mcp-client/0.1",
	}

	_, initErr := cli.Initialize(initCtx, initReq)
	if initErr == nil {
		// Streamable-HTTP won.
		t.mu.Lock()
		t.http = httpTr
		t.cli = cli
		t.active = activeStreamable
		t.mu.Unlock()
		return nil
	}

	// Initialize failed -- check whether it is the mcp-go "server is
	// actually legacy SSE" signal. That sentinel fires on:
	//   * 404 Not Found on the POST (session-terminated path, which
	//     for an initialize request is re-classified as legacy-SSE)
	//   * any HTTP 4xx (405 Method Not Allowed, 406 Not Acceptable,
	//     etc.) returned for an initialize POST
	// Either way, the correct action is to tear down the
	// Streamable-HTTP client and retry on SSE.
	if !errors.Is(initErr, mcptransport.ErrLegacySSEServer) {
		_ = cli.Close()
		return fmt.Errorf("mcp/http: initialize handshake: %w", initErr)
	}

	// Fallthrough: server only speaks legacy SSE. Drop the
	// Streamable-HTTP client + open an SSE transport against the same
	// URL. cfg.Transport is overridden to "sse" so NewSSETransport
	// accepts it without the "http" fallthrough quirk taking
	// precedence in telemetry.
	_ = cli.Close()

	log.Printf("mcp/http: server %q at %s rejected Streamable-HTTP initialize (%v); falling through to SSE transport",
		t.cfg.Name, t.cfg.URL, initErr)

	sseCfg := t.cfg
	sseCfg.Transport = "sse"
	sse, err := NewSSETransport(sseCfg)
	if err != nil {
		return fmt.Errorf("mcp/http: build SSE fallthrough transport: %w", err)
	}
	if err := sse.Initialize(ctx); err != nil {
		return fmt.Errorf("mcp/http: SSE fallthrough initialize: %w", err)
	}

	t.mu.Lock()
	t.sse = sse
	t.active = activeSSE
	t.mu.Unlock()
	return nil
}

// ListTools delegates to whichever transport is currently active.
func (t *HTTPTransport) ListTools(ctx context.Context) ([]Tool, error) {
	active, cli, sse, err := t.snapshot()
	if err != nil {
		return nil, err
	}
	switch active {
	case activeSSE:
		return sse.ListTools(ctx)
	case activeStreamable:
		return t.listToolsStreamable(ctx, cli)
	case activeNone:
		return nil, errors.New("mcp/http: transport not initialized (call Initialize first)")
	default:
		return nil, errors.New("mcp/http: transport not initialized (call Initialize first)")
	}
}

// CallTool delegates to whichever transport is currently active.
// It first acquires a slot on the per-server semaphore (honoring ctx
// cancellation) so no more than cfg.MaxConcurrent in-flight calls
// hit the server simultaneously. Close() drops a nil onto the
// semaphore to wake any caller blocked on acquire.
func (t *HTTPTransport) CallTool(ctx context.Context, name string, args json.RawMessage) (ToolResult, error) {
	if err := t.acquire(ctx); err != nil {
		return ToolResult{}, err
	}
	defer t.release()

	active, cli, sse, err := t.snapshot()
	if err != nil {
		return ToolResult{}, err
	}
	switch active {
	case activeSSE:
		return sse.CallTool(ctx, name, args)
	case activeStreamable:
		return t.callToolStreamable(ctx, cli, name, args)
	case activeNone:
		return ToolResult{}, errors.New("mcp/http: transport not initialized (call Initialize first)")
	default:
		return ToolResult{}, errors.New("mcp/http: transport not initialized (call Initialize first)")
	}
}

// Close tears down the active transport. Idempotent -- calling
// Close multiple times returns nil after the first successful
// shutdown. After Close, any caller blocked on the semaphore wakes
// with context.Canceled (if their ctx was cancelled) or with the
// error from CallTool's next guard check (transport not
// initialized / closed).
func (t *HTTPTransport) Close() error {
	already := true
	t.closeOnce.Do(func() {
		already = false
		close(t.closeCh)
	})
	if already {
		return nil
	}

	t.mu.Lock()
	t.closed = true
	cli := t.cli
	httpTr := t.http
	sse := t.sse
	t.cli = nil
	t.http = nil
	t.sse = nil
	active := t.active
	t.active = activeNone
	t.mu.Unlock()

	var errs []string
	switch active {
	case activeStreamable:
		if cli != nil {
			if err := cli.Close(); err != nil {
				errs = append(errs, fmt.Sprintf("streamable-http cli close: %v", err))
			}
		} else if httpTr != nil {
			if err := httpTr.Close(); err != nil {
				errs = append(errs, fmt.Sprintf("streamable-http transport close: %v", err))
			}
		}
	case activeSSE:
		if sse != nil {
			if err := sse.Close(); err != nil {
				errs = append(errs, fmt.Sprintf("sse close: %v", err))
			}
		}
	case activeNone:
		// Nothing was ever initialized. No-op.
	}

	if len(errs) > 0 {
		return fmt.Errorf("mcp/http: close: %s", strings.Join(errs, "; "))
	}
	return nil
}

// acquire blocks until a slot is available on the per-server
// semaphore, ctx is cancelled, or Close is called. On success the
// caller MUST pair this with exactly one release() via defer.
func (t *HTTPTransport) acquire(ctx context.Context) error {
	// Fast path: if Close already happened, fail immediately so we
	// don't let in-flight callers hang the shutdown.
	select {
	case <-t.closeCh:
		return errors.New("mcp/http: transport closed")
	default:
	}

	select {
	case t.sem <- struct{}{}:
		// Re-check closed: a concurrent Close between the check
		// above and the send would otherwise let a post-close call
		// through. Release the slot immediately in that case.
		select {
		case <-t.closeCh:
			<-t.sem
			return errors.New("mcp/http: transport closed")
		default:
		}
		return nil
	case <-t.closeCh:
		return errors.New("mcp/http: transport closed")
	case <-ctx.Done():
		return ctx.Err()
	}
}

// release returns a slot to the semaphore. Paired 1:1 with acquire
// via defer in CallTool.
func (t *HTTPTransport) release() {
	select {
	case <-t.sem:
	default:
	}
}

// snapshot grabs the current active+handles under the lock so
// concurrent Close cannot null out a pointer mid-call.
func (t *HTTPTransport) snapshot() (activeTransport, *mcpclient.Client, *SSETransport, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return activeNone, nil, nil, errors.New("mcp/http: transport closed")
	}
	return t.active, t.cli, t.sse, nil
}

// listToolsStreamable calls tools/list on the Streamable-HTTP client
// and adapts the result into Stoke's []Tool shape. Keeps the
// translation inline (rather than sharing with transport_sse.go)
// because the SSE transport already projects through its own
// ListTools path.
func (t *HTTPTransport) listToolsStreamable(ctx context.Context, cli *mcpclient.Client) ([]Tool, error) {
	if cli == nil {
		return nil, errors.New("mcp/http: streamable-http client is nil")
	}
	resp, err := cli.ListTools(ctx, mcpproto.ListToolsRequest{})
	if err != nil {
		return nil, fmt.Errorf("mcp/http: tools/list: %w", err)
	}
	out := make([]Tool, 0, len(resp.Tools))
	for _, remote := range resp.Tools {
		schema, serr := json.Marshal(remote.InputSchema)
		if serr != nil {
			return nil, fmt.Errorf("mcp/http: marshal input_schema for %q: %w", remote.Name, serr)
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

// callToolStreamable invokes a remote tool and translates the
// mcp-go result into Stoke's ToolResult. Mirrors transport_sse.go's
// CallTool content projection.
func (t *HTTPTransport) callToolStreamable(ctx context.Context, cli *mcpclient.Client, name string, args json.RawMessage) (ToolResult, error) {
	if cli == nil {
		return ToolResult{}, errors.New("mcp/http: streamable-http client is nil")
	}

	var argsAny any
	if len(args) > 0 {
		var decoded any
		if err := json.Unmarshal(args, &decoded); err != nil {
			return ToolResult{}, fmt.Errorf("mcp/http: decode args for %q: %w", name, err)
		}
		argsAny = decoded
	}

	req := mcpproto.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = argsAny

	resp, err := cli.CallTool(ctx, req)
	if err != nil {
		return ToolResult{}, fmt.Errorf("mcp/http: tools/call %q: %w", name, err)
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
			out.Content = append(out.Content, Content{Type: "unknown"})
		}
	}
	return out, nil
}

// Compile-time assertion: *HTTPTransport satisfies Client.
var _ Client = (*HTTPTransport)(nil)
