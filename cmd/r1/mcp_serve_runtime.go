// mcp_serve_runtime.go — production runtime for `r1 mcp serve` (no flags).
//
// Spec: specs/r1-mcp-serve.md.
//
// runMCPServe constructs an in-process StokeServer with the full r1.*
// catalog (38 tools), optionally attaches a *cortex.Cortex backend via
// WithCortex so r1.cortex.* tools route to a live Workspace, and runs
// the stdio MCP JSON-RPC loop until stdin EOF or SIGINT/SIGTERM.
//
// Stdout is reserved for JSON-RPC frames. Logs go to stderr only —
// any log.* call to stdout breaks framing.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/mcp"
	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/stream"
)

// stubProvider is a Provider that errors on every call. Used by the
// workspace-only cortex configuration where no LLM Lobes are
// registered, so the provider is never invoked. cortex.New requires a
// non-nil Provider; this satisfies the constraint without bringing in
// API credentials.
type stubProvider struct{}

func (stubProvider) Name() string { return "stub" }
func (stubProvider) Chat(_ provider.ChatRequest) (*provider.ChatResponse, error) {
	return nil, errors.New("r1 mcp serve: stub provider; pass --lobes=all and configure ANTHROPIC_API_KEY for live LLM Lobes")
}
func (stubProvider) ChatStream(_ provider.ChatRequest, _ func(stream.Event)) (*provider.ChatResponse, error) {
	return nil, errors.New("r1 mcp serve: stub provider; pass --lobes=all and configure ANTHROPIC_API_KEY for live LLM Lobes")
}

// buildCortexBackend constructs a *cortex.Cortex with no Lobes
// registered (workspace-only mode). Returns the cortex which
// satisfies mcp.CortexBackend via its existing methods (SessionID,
// Workspace, LobeStatus, PauseLobe, ResumeLobe).
//
// Workspace-only is the default for `r1 mcp serve` because:
//   - It requires no API credentials.
//   - r1.cortex.publish + r1.cortex.notes still round-trip via the
//     Workspace.
//   - r1.cortex.lobes_list returns the empty registered set, which
//     external clients can detect and use as the signal that they
//     are in workspace-only mode.
//
// Future: a `--lobes=all` flag will register the 7 v1 Lobes with a
// real Anthropic provider. Out of scope here per spec
// open-question 1.
func buildCortexBackend(sessionID, busDir string) (mcp.CortexBackend, error) {
	eventBus := hub.New()
	durableBus, err := bus.New(busDir)
	if err != nil {
		return nil, fmt.Errorf("durable bus: %w", err)
	}
	c, err := cortex.New(cortex.Config{
		SessionID: sessionID,
		EventBus:  eventBus,
		Durable:   durableBus,
		Provider:  stubProvider{},
		Lobes:     nil, // workspace-only
	})
	if err != nil {
		return nil, fmt.Errorf("cortex.New: %w", err)
	}
	return c, nil
}

// startMCPServer wires the StokeServer + (optional) cortex backend +
// (optional) auth key, then runs s.ServeStdio() until stdin EOF.
//
// stdout: bound to MCP JSON-RPC frames (caller must not write to it).
// stderr: free for diagnostics.
//
// Exposed for test injection: tests pass an alternate stdin/stdout
// pair via testServeStdio rather than going through this function.
func startMCPServer(opts mcpServeOptions, stderr io.Writer) error {
	s := mcp.NewStokeServer("")
	if !opts.NoCortex {
		busDir, err := os.MkdirTemp("", "r1-mcp-serve-bus-")
		if err != nil {
			return fmt.Errorf("bus tmpdir: %w", err)
		}
		c, err := buildCortexBackend(opts.SessionID, busDir)
		if err != nil {
			return fmt.Errorf("build cortex backend: %w", err)
		}
		s.WithCortex(c)
	}
	if opts.AuthKey != "" {
		s.WithAuthKey(opts.AuthKey)
	}
	if opts.SessionID != "" {
		fmt.Fprintf(stderr, "r1 mcp serve: session_id=%s\n", opts.SessionID)
	}
	if opts.NoCortex {
		fmt.Fprintln(stderr, "r1 mcp serve: --no-cortex set; r1.cortex.* tools will return 'cortex backend not wired'")
	}
	if opts.AuthKey != "" {
		fmt.Fprintln(stderr, "r1 mcp serve: R1_MCP_KEY auth gate active")
	}
	return s.ServeStdio()
}

// mcpServeOptions bundles the runtime knobs derived from CLI flags +
// env vars.
type mcpServeOptions struct {
	NoCortex  bool   // --no-cortex
	SessionID string // --session-id (or generated UUID surface'd to stderr)
	AuthKey   string // R1_MCP_KEY env var
}

// envOrEmpty returns os.Getenv(key) for live process; tests override
// this via the package-level envFunc to avoid touching the real env.
var envFunc = os.Getenv
