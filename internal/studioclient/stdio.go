package studioclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/RelayOne/r1/internal/config"
	"github.com/RelayOne/r1/internal/mcp"
)

// StdioMCPTransport spawns Actium Studio's bundled MCP server as a
// child process once per R1 session and routes skill invocations
// over the stdio JSON-RPC transport provided by internal/mcp. The
// lifecycle model follows work order §R1S-3:
//
//   - Lazy spawn on the first Invoke call.
//   - Reuse for session duration.
//   - Close() signals the process group for clean shutdown.
//   - Up to 2 respawns per session on mid-flight crashes; a third
//     failure disables the transport and surfaces ErrStudioUnavailable.
type StdioMCPTransport struct {
	cfg config.StudioStdioMCPConfig

	// clientFactory builds a fresh mcp.Client on each spawn. Injected
	// so tests can substitute a fake; production uses newStdioClient.
	clientFactory func(cfg config.StudioStdioMCPConfig) (mcp.Client, error)

	// Publisher is the optional observability sink. nil is safe.
	publisher EventPublisher

	mu       sync.Mutex
	client   mcp.Client // nil until first call
	closed   bool
	crashCnt int // number of mid-flight spawn cycles that ended in failure
}

// NewStdioMCPTransport constructs an unstarted transport from the
// StudioConfig. Returns ErrStudioDisabled when the cfg is not enabled
// or not set to stdio-mcp.
func NewStdioMCPTransport(cfg config.StudioConfig, pub EventPublisher) (*StdioMCPTransport, error) {
	if !cfg.Enabled {
		return nil, ErrStudioDisabled
	}
	if cfg.ResolvedTransport() != config.StudioTransportStdioMCP {
		return nil, fmt.Errorf("studioclient: NewStdioMCPTransport called with transport=%q", cfg.ResolvedTransport())
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &StdioMCPTransport{
		cfg:           cfg.StdioMCP,
		clientFactory: newStdioClient,
		publisher:     pub,
	}, nil
}

// Name — Transport interface.
func (s *StdioMCPTransport) Name() string { return "stdio-mcp" }

// Close — Transport interface. Idempotent; subsequent calls no-op.
func (s *StdioMCPTransport) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.client == nil {
		return nil
	}
	err := s.client.Close()
	s.client = nil
	return err
}

// Invoke sends the skill call over stdio JSON-RPC. See Transport.Invoke
// for contract.
func (s *StdioMCPTransport) Invoke(ctx context.Context, tool string, input any) (json.RawMessage, error) {
	start := time.Now()

	studioTool, ok := skillToStudioToolName(tool)
	if !ok {
		return nil, s.fail(tool, ErrStudioValidation, fmt.Errorf("unknown tool %q", tool), start)
	}

	// Marshal input to raw JSON once; mcp.Client.CallTool wants that.
	inputBytes, err := json.Marshal(inputOrEmpty(input))
	if err != nil {
		return nil, s.fail(tool, ErrStudioValidation, err, start)
	}

	client, err := s.ensureClient(ctx)
	if err != nil {
		return nil, s.fail(tool, classifyStdioErr(err), err, start)
	}

	result, err := client.CallTool(ctx, studioTool, inputBytes)
	if err != nil {
		// Record the failure and consider it a crash-style event: the
		// subprocess MAY have died. The next Invoke will respawn.
		s.markPossibleCrash()
		return nil, s.fail(tool, classifyStdioErr(err), err, start)
	}
	if result.IsError {
		// The server answered but reported a tool-side error. Extract
		// the first text block as the error body and classify as a
		// validation error — the caller can retry with different input.
		excerpt := firstTextContent(result)
		se := &StudioError{
			Tool:        tool,
			BodyExcerpt: sanitizeBody([]byte(excerpt)),
			Cause:       ErrStudioValidation,
		}
		s.publish(InvocationEvent{
			Transport: "stdio-mcp",
			Tool:      tool,
			Duration:  time.Since(start),
			OK:        false,
			ErrorKind: errorKind(se),
		})
		return nil, se
	}

	// Success. Studio's MCP tools return a single `text` content block
	// whose body is the JSON-encoded response DTO (matches the shape
	// scaffoldPost/scaffoldGet returns in tools/studio.ts). Concatenate
	// text blocks defensively in case Studio splits them.
	body := firstTextContent(result)
	if body == "" {
		// No text blocks → return an empty JSON object so the caller's
		// output-schema validator sees something parseable.
		body = "{}"
	}
	s.publish(InvocationEvent{
		Transport: "stdio-mcp",
		Tool:      tool,
		Duration:  time.Since(start),
		OK:        true,
	})
	return json.RawMessage(body), nil
}

// ensureClient returns the lazily-spawned stdio client. On first call
// (or after a crash) it calls clientFactory + Initialize. Respawns up
// to 2 times per session; on the third failure it permanently disables
// the transport.
func (s *StdioMCPTransport) ensureClient(ctx context.Context) (mcp.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrStudioDisabled
	}
	if s.client != nil {
		return s.client, nil
	}
	// Respawn budget — work order §R1S-3.6: third failure disables.
	if s.crashCnt >= 3 {
		return nil, fmt.Errorf("%w: stdio transport disabled after %d crashes this session",
			ErrStudioUnavailable, s.crashCnt)
	}
	cli, err := s.clientFactory(s.cfg)
	if err != nil {
		s.crashCnt++
		return nil, err
	}
	if err := cli.Initialize(ctx); err != nil {
		_ = cli.Close()
		s.crashCnt++
		return nil, err
	}
	s.client = cli
	return cli, nil
}

// markPossibleCrash tears down the (possibly dead) client so the next
// Invoke respawns. Also bumps the crash counter.
func (s *StdioMCPTransport) markPossibleCrash() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client != nil {
		_ = s.client.Close()
		s.client = nil
	}
	s.crashCnt++
}

func (s *StdioMCPTransport) fail(tool string, cause error, underlying error, start time.Time) error {
	se := &StudioError{
		Tool:       tool,
		Cause:      cause,
		Underlying: underlying,
	}
	s.publish(InvocationEvent{
		Transport: "stdio-mcp",
		Tool:      tool,
		Duration:  time.Since(start),
		OK:        false,
		ErrorKind: errorKind(se),
	})
	return se
}

func (s *StdioMCPTransport) publish(ev InvocationEvent) {
	if s.publisher != nil {
		s.publisher.Publish(ev)
	}
}

// --- helpers ---

// skillToStudioToolName translates an R1 skill name (`studio.X`) to
// the Studio-side MCP tool name. Two scaffold-era tools get a
// `studio_` prefix on the Studio side (ts: studio_scaffold_site,
// studio_get_scaffold_status); the rest drop the leading `studio.`.
func skillToStudioToolName(skill string) (string, bool) {
	if !strings.HasPrefix(skill, "studio.") {
		return "", false
	}
	local := strings.TrimPrefix(skill, "studio.")
	if local == "" {
		return "", false
	}
	switch local {
	case "scaffold_site":
		return "studio_scaffold_site", true
	case "get_scaffold_status":
		return "studio_get_scaffold_status", true
	default:
		// Hero skills that don't have a direct Studio-side tool
		// (update_content, publish, diff_versions, list_templates,
		// site_status) are composite — they MUST use the HTTP
		// transport today because stdio-MCP only exposes the 53 raw
		// tools. Reject with Validation so the operator swaps
		// transport or waits on the HTTP fallback path.
		switch local {
		case "update_content", "publish", "diff_versions", "list_templates", "site_status":
			return "", false
		}
		return local, true
	}
}

func inputOrEmpty(input any) any {
	if input == nil {
		return map[string]any{}
	}
	return input
}

// firstTextContent concatenates every `text` content block in the
// result into one string. Handles the common case where Studio
// returns a single text block carrying the JSON DTO.
func firstTextContent(r mcp.ToolResult) string {
	var sb strings.Builder
	for _, c := range r.Content {
		if c.Type == "text" && c.Text != "" {
			sb.WriteString(c.Text)
		}
	}
	return sb.String()
}

// classifyStdioErr maps an mcp.Client error to a sentinel cause.
// Subprocess crashes / dial failures become ErrStudioUnavailable;
// ErrAuthMissing becomes ErrStudioAuth; context cancellation becomes
// ErrStudioTimeout when deadline-exceeded, otherwise unavailable.
func classifyStdioErr(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, mcp.ErrAuthMissing):
		return ErrStudioAuth
	case errors.Is(err, mcp.ErrPolicyDenied):
		return ErrStudioScope
	case errors.Is(err, mcp.ErrCircuitOpen):
		return ErrStudioUnavailable
	case errors.Is(err, context.DeadlineExceeded):
		return ErrStudioTimeout
	case errors.Is(err, context.Canceled):
		return ErrStudioUnavailable
	default:
		return ErrStudioUnavailable
	}
}

// newStdioClient is the production factory — constructs an mcp.Client
// from the StudioStdioMCPConfig. Extracted into a package-level var
// so tests can stub it without mutating global state of the mcp
// package.
var newStdioClient = func(cfg config.StudioStdioMCPConfig) (mcp.Client, error) {
	if len(cfg.Command) == 0 {
		return nil, fmt.Errorf("stdio_mcp.command empty")
	}
	srvCfg := mcp.ServerConfig{
		Name:      "actium-studio",
		Transport: "stdio",
		Command:   cfg.Command[0],
		Args:      cfg.Command[1:],
		Env:       cfg.Env,
		Enabled:   true,
		Trust:     "trusted", // pack is opt-in by the operator already
	}
	return mcp.NewStdioTransport(srvCfg)
}
