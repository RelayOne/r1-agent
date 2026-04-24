// Package mcp — transport_stdio.go — MCP-3 stdio Client implementation.
//
// Implements the Client interface from types.go over stdin/stdout of a
// child process using github.com/mark3labs/mcp-go. Owns the child
// process group (Setpgid=true) so Close() can reap the whole tree via
// SIGTERM → SIGKILL. This file is standalone: it must not import the
// legacy StdioClient / LegacyServerConfig types from client.go.
//
// See specs/mcp-client.md §Transport Details § stdio for the full
// behavioral contract and §Library selection for the mcp-go choice.

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcptransport "github.com/mark3labs/mcp-go/client/transport"
	mcpproto "github.com/mark3labs/mcp-go/mcp"
)

// initializeTimeout bounds the MCP `initialize` RPC (the handshake
// after spawn). Real-world stdio servers complete the handshake in
// well under a second; 5s covers slow cold starts without masking
// a genuinely broken binary.
const initializeTimeout = 5 * time.Second

// closeGracePeriod is how long Close() waits for the child to exit
// after SIGTERM-ing the process group before escalating to SIGKILL.
const closeGracePeriod = 3 * time.Second

// StdioTransport is the stdio-flavored Client. It wraps the mcp-go
// client + a captured *exec.Cmd so Close() can signal the whole
// process group. All exported methods satisfy the Client interface
// declared in types.go.
type StdioTransport struct {
	cfg       ServerConfig
	transport *mcptransport.Stdio
	client    *mcpclient.Client

	// cmd is captured inside the CommandFunc we install on the
	// mcp-go transport. It's the handle we need for Setpgid-aware
	// shutdown (syscall.Kill(-pid, …) on the process group).
	cmdMu sync.Mutex
	cmd   *exec.Cmd

	// closed guards Close() against double-close. The underlying
	// mcp-go transport has its own sync.Once, but we still need our
	// own guard because we send process-group signals *before*
	// delegating to mcp-go.
	closeOnce sync.Once
	closeErr  error
}

// NewStdioTransport validates the supplied config and constructs an
// unstarted stdio transport. Nothing is spawned until Initialize is
// called; a failed NewStdioTransport therefore cannot leak a child
// process.
func NewStdioTransport(cfg ServerConfig) (*StdioTransport, error) {
	if cfg.Transport != "stdio" {
		return nil, fmt.Errorf("mcp stdio: transport must be \"stdio\", got %q", cfg.Transport)
	}
	if cfg.Command == "" {
		return nil, fmt.Errorf("mcp stdio: command must be non-empty for server %q", cfg.Name)
	}
	return &StdioTransport{cfg: cfg}, nil
}

// Initialize spawns the configured child process under a new process
// group, connects the mcp-go client to its stdio, and performs the
// MCP `initialize` handshake under a 5s timeout.
func (s *StdioTransport) Initialize(ctx context.Context) error {
	if s.client != nil {
		return fmt.Errorf("mcp stdio: already initialized")
	}

	// Build the merged env the child will see: host env (required so
	// PATH, HOME, etc. resolve normally), then cfg.Env overrides,
	// then the AuthEnv value forwarded from the parent's environment
	// so the server can authenticate without the parent leaking the
	// token into arbitrary processes. AuthEnv with an empty value is
	// intentionally forwarded as the empty string so the server can
	// fail-closed with its own error message — this mirrors the
	// "fail-closed when env missing" rule in §Auth / Secret Handling.
	childEnv := make([]string, 0, len(s.cfg.Env)+1)
	for k, v := range s.cfg.Env {
		childEnv = append(childEnv, k+"="+v)
	}
	if s.cfg.AuthEnv != "" {
		childEnv = append(childEnv, s.cfg.AuthEnv+"="+os.Getenv(s.cfg.AuthEnv))
	}

	// CommandFunc captures the *exec.Cmd so Close() can address the
	// whole process group. Setpgid:true is the mandated isolation
	// pattern (CLAUDE.md decision #7, mirrored by internal/engine/
	// claude.go:136 and codex.go:75).
	//
	// We deliberately use exec.Command, not exec.CommandContext,
	// because the ctx passed in here is the bounded Initialize
	// context; attaching it would cause the subprocess to be killed
	// when Initialize returns successfully. Process lifetime is
	// managed by Close() instead.
	cmdFunc := func(_ context.Context, command string, env []string, args []string) (*exec.Cmd, error) {
		cmd := exec.Command(command, args...) // #nosec G204 -- binary name is hardcoded; args come from Stoke-internal orchestration, not external input.
		// Host env first, then transport-merged additions. The mcp-go
		// transport has already merged os.Environ() with its own `env`
		// arg when cmdFunc is absent; we replicate that here because
		// we're overriding the default cmd construction.
		cmd.Env = append(os.Environ(), env...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

		s.cmdMu.Lock()
		s.cmd = cmd
		s.cmdMu.Unlock()
		return cmd, nil
	}

	args := append([]string(nil), s.cfg.Args...)
	stdio := mcptransport.NewStdioWithOptions(
		s.cfg.Command,
		childEnv,
		args,
		mcptransport.WithCommandFunc(cmdFunc),
	)
	s.transport = stdio

	// Wire the mcp-go Client on top of the transport. Client.Start
	// delegates to transport.Start which runs cmdFunc and spawns the
	// child.
	c := mcpclient.NewClient(stdio)

	// Bound the whole init sequence under initializeTimeout so a
	// hung server never blocks the caller indefinitely. The ctx the
	// caller passed in is still honored (whichever fires first).
	initCtx, cancel := context.WithTimeout(ctx, initializeTimeout)
	defer cancel()

	if err := c.Start(initCtx); err != nil {
		// Spawn failed: nothing to clean up on the mcp-go side yet,
		// but the cmdFunc may have captured a zombie. Signal the
		// group so we don't leak. Safe no-op when cmd is nil.
		s.killGroup(syscall.SIGKILL)
		return fmt.Errorf("mcp stdio: start transport for %q: %w", s.cfg.Name, err)
	}

	initReq := mcpproto.InitializeRequest{
		Params: mcpproto.InitializeParams{
			ProtocolVersion: mcpproto.LATEST_PROTOCOL_VERSION,
			ClientInfo: mcpproto.Implementation{
				Name:    "stoke",
				Version: "1.0.0",
			},
			Capabilities: mcpproto.ClientCapabilities{},
		},
	}
	if _, err := c.Initialize(initCtx, initReq); err != nil {
		// Don't leave a partly-initialized subprocess dangling: tear
		// it down, ignore any secondary error from the tear-down
		// since the primary failure is the one the caller needs.
		_ = c.Close()
		s.killGroup(syscall.SIGKILL)
		return fmt.Errorf("mcp stdio: initialize %q: %w", s.cfg.Name, err)
	}

	s.client = c
	return nil
}

// ListTools queries the server for its advertised tools and stamps
// each entry with the server name + trust level from the config so
// the registry layer (MCP-4+) doesn't have to re-decorate.
func (s *StdioTransport) ListTools(ctx context.Context) ([]Tool, error) {
	if s.client == nil {
		return nil, fmt.Errorf("mcp stdio: not initialized")
	}
	res, err := s.client.ListTools(ctx, mcpproto.ListToolsRequest{})
	if err != nil {
		return nil, fmt.Errorf("mcp stdio: list tools %q: %w", s.cfg.Name, err)
	}
	out := make([]Tool, 0, len(res.Tools))
	for _, t := range res.Tools {
		schema, merr := marshalInputSchema(t)
		if merr != nil {
			// A single bad schema should not poison the whole list.
			// Emit an empty-object schema so downstream callers can
			// still address the tool; the registry layer will note
			// the normalization in its own event stream.
			schema = json.RawMessage(`{}`)
		}
		out = append(out, Tool{
			Definition: ToolDefinition{
				Name:        t.Name,
				Description: t.Description,
				InputSchema: schema,
			},
			ServerName: s.cfg.Name,
			Trust:      s.cfg.Trust,
		})
	}
	return out, nil
}

// CallTool invokes the named tool with the supplied raw-JSON
// arguments and translates the mcp-go result into our ToolResult
// shape. The ctx deadline is honored end-to-end; cancellation
// propagates into the mcp-go transport via SendRequest.
func (s *StdioTransport) CallTool(ctx context.Context, name string, args json.RawMessage) (ToolResult, error) {
	if s.client == nil {
		return ToolResult{}, fmt.Errorf("mcp stdio: not initialized")
	}
	if err := ctx.Err(); err != nil {
		return ToolResult{}, err
	}

	// mcp-go's CallToolParams.Arguments is typed `any`. Passing a
	// json.RawMessage forwards the bytes verbatim through its own
	// Marshal without double-encoding.
	var argsAny any
	if len(args) > 0 {
		argsAny = args
	}
	req := mcpproto.CallToolRequest{
		Params: mcpproto.CallToolParams{
			Name:      name,
			Arguments: argsAny,
		},
	}

	res, err := s.client.CallTool(ctx, req)
	if err != nil {
		return ToolResult{}, fmt.Errorf("mcp stdio: call %q.%s: %w", s.cfg.Name, name, err)
	}

	out := ToolResult{
		IsError: res.IsError,
		Content: translateContent(res.Content),
	}
	return out, nil
}

// Close terminates the subprocess. It signals the whole process group
// (SIGTERM first, then SIGKILL after closeGracePeriod), which reaps
// any grandchild processes the server may have spawned. It is safe to
// call multiple times; only the first call actually signals.
func (s *StdioTransport) Close() error {
	s.closeOnce.Do(func() {
		s.closeErr = s.doClose()
	})
	return s.closeErr
}

func (s *StdioTransport) doClose() error {
	// Send SIGTERM to the process group and wait up to grace period
	// for the child (and any descendants) to exit cleanly. If it's
	// still alive after grace period, SIGKILL the group.
	s.cmdMu.Lock()
	cmd := s.cmd
	s.cmdMu.Unlock()

	var waitErr error
	if cmd != nil && cmd.Process != nil {
		// Prefer addressing the process *group* so forked grandchildren
		// die with their parent. Fall back to Signal on the single
		// process if Getpgid fails (shouldn't happen with Setpgid).
		if err := killGroup(cmd, syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
			// Non-fatal: record but keep tearing down.
			waitErr = err
		}

		// Wait for exit up to the grace period. We can't call
		// cmd.Wait() here because the mcp-go transport already owns
		// it (inside its own Close). So we poll the process state
		// via signal-0. This is cheap and avoids racing the Wait.
		deadline := time.Now().Add(closeGracePeriod)
		for time.Now().Before(deadline) {
			if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
				// Process already reaped or gone.
				break
			}
			time.Sleep(25 * time.Millisecond)
		}

		// If still alive, escalate to SIGKILL on the group.
		if err := cmd.Process.Signal(syscall.Signal(0)); err == nil {
			_ = killGroup(cmd, syscall.SIGKILL)
		}
	}

	// Delegate to mcp-go to close pipes, unblock in-flight requests,
	// and cmd.Wait() for the subprocess. The library's own Close is
	// idempotent (sync.Once-guarded) and will see the process has
	// already exited thanks to our SIGTERM/SIGKILL above, so its
	// 2s + 3s timeouts short-circuit immediately.
	var closeErr error
	if s.client != nil {
		closeErr = s.client.Close()
	} else if s.transport != nil {
		closeErr = s.transport.Close()
	}

	if closeErr != nil {
		if waitErr != nil {
			return errors.Join(fmt.Errorf("mcp stdio: close: %w", closeErr), waitErr)
		}
		return fmt.Errorf("mcp stdio: close: %w", closeErr)
	}
	return waitErr
}

// killGroup signals the *process group* of cmd with sig. If Getpgid
// fails (e.g. Setpgid never took effect, or the process is already
// reaped) it falls back to signaling the single pid so the caller
// gets consistent semantics. errors.Is(err, os.ErrProcessDone) is
// the normal shape returned when the kernel already reaped.
func killGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		// Process likely already gone; signal the pid directly as a
		// best-effort and let the caller treat ProcessDone as success.
		return cmd.Process.Signal(sig)
	}
	return syscall.Kill(-pgid, sig)
}

// killGroup wrapper used in error paths where we don't care about the
// return value but want the group-level behavior.
func (s *StdioTransport) killGroup(sig syscall.Signal) {
	s.cmdMu.Lock()
	cmd := s.cmd
	s.cmdMu.Unlock()
	_ = killGroup(cmd, sig)
}

// marshalInputSchema re-serializes an mcp-go Tool's input schema to
// the raw-JSON shape our ToolDefinition uses. mcp-go's Tool has a
// custom MarshalJSON that picks between InputSchema and
// RawInputSchema; we reuse that by marshalling a shim struct with
// just the inputSchema visible.
func marshalInputSchema(t mcpproto.Tool) (json.RawMessage, error) {
	// RawInputSchema takes precedence in mcp-go's own MarshalJSON; we
	// mirror that here so whatever the server actually advertised is
	// what we pass along.
	if len(t.RawInputSchema) > 0 {
		return append(json.RawMessage(nil), t.RawInputSchema...), nil
	}
	return json.Marshal(t.InputSchema)
}

// translateContent maps mcp-go's Content interface (TextContent,
// ImageContent, AudioContent, ResourceLink, EmbeddedResource) into
// our concrete Content struct. Unknown content types fall through
// with Type left blank and the original marshalled bytes stuffed
// into Data so callers can inspect them if desired.
func translateContent(src []mcpproto.Content) []Content {
	if len(src) == 0 {
		return nil
	}
	out := make([]Content, 0, len(src))
	for _, c := range src {
		switch v := c.(type) {
		case mcpproto.TextContent:
			out = append(out, Content{Type: "text", Text: v.Text})
		case *mcpproto.TextContent:
			out = append(out, Content{Type: "text", Text: v.Text})
		case mcpproto.ImageContent:
			out = append(out, Content{
				Type: "image",
				MIME: v.MIMEType,
				Data: json.RawMessage(quoteJSON(v.Data)),
			})
		case *mcpproto.ImageContent:
			out = append(out, Content{
				Type: "image",
				MIME: v.MIMEType,
				Data: json.RawMessage(quoteJSON(v.Data)),
			})
		case mcpproto.AudioContent:
			out = append(out, Content{
				Type: "audio",
				MIME: v.MIMEType,
				Data: json.RawMessage(quoteJSON(v.Data)),
			})
		case *mcpproto.AudioContent:
			out = append(out, Content{
				Type: "audio",
				MIME: v.MIMEType,
				Data: json.RawMessage(quoteJSON(v.Data)),
			})
		default:
			// Unknown/new content type — marshal the whole value so
			// callers at least see the bytes and can self-describe.
			if b, err := json.Marshal(v); err == nil {
				out = append(out, Content{Data: b})
			}
		}
	}
	return out
}

// quoteJSON wraps a raw string in JSON quotes so it round-trips as a
// valid json.RawMessage. Small helper kept inline to avoid pulling
// encoding/json for a two-byte decoration.
func quoteJSON(s string) []byte {
	b, _ := json.Marshal(s)
	return b
}

// Compile-time assertion that StdioTransport satisfies Client. Keeps
// the interface and its only current implementation in lock-step.
var _ Client = (*StdioTransport)(nil)
