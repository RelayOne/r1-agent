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
	antitrunclobe "github.com/RelayOne/r1/internal/cortex/lobes/antitrunc"
	"github.com/RelayOne/r1/internal/cortex/lobes/memoryrecall"
	"github.com/RelayOne/r1/internal/cortex/lobes/rulecheck"
	"github.com/RelayOne/r1/internal/cortex/lobes/walkeeper"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/mcp"
	"github.com/RelayOne/r1/internal/memory"
	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/stream"
	"github.com/RelayOne/r1/internal/wisdom"
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

// buildCortexBackend constructs a *cortex.Cortex backing for the
// r1.cortex.* MCP tools. Returns the live cortex which satisfies
// mcp.CortexBackend (SessionID, Workspace, LobeStatus, PauseLobe,
// ResumeLobe).
//
// lobeMode controls Lobe registration:
//   - "none"           — no Lobes registered (workspace-only). r1.cortex.notes
//                        and .publish still round-trip via the Workspace,
//                        but lobes_list returns []. Cheapest, no creds.
//   - "deterministic"  — register the 4 deterministic Lobes
//                        (memoryrecall, walkeeper, rulecheck, antitrunc).
//                        No API creds needed; the LLM Lobes (clarifyq,
//                        memorycurator, planupdate) are skipped.
//   - "all"            — RESERVED. Today behaves identically to
//                        "deterministic" because LLM Lobe construction
//                        requires an Anthropic provider; once
//                        ANTHROPIC_API_KEY plumbing lands here, this mode
//                        will register the full v1 set of 7 Lobes.
//
// The function uses the documented "shell + live" pattern (see
// internal/cortex/lobes/all_integration_test.go): a shell Cortex
// produces a stable Workspace pointer that every Lobe captures at
// construction time, then a second cortex.New holds the LobeRunners.
// Returning the SHELL cortex makes Workspace + LobeStatus consistent
// with where Lobes write — even when the cortex is not Started (Lobe
// runners are inert until Start, but lobes_list reflects registration
// regardless).
func buildCortexBackend(sessionID, busDir, lobeMode string) (mcp.CortexBackend, error) {
	eventBus := hub.New()
	durableBus, err := bus.New(busDir)
	if err != nil {
		return nil, fmt.Errorf("durable bus: %w", err)
	}
	if lobeMode == "" || lobeMode == "none" {
		c, err := cortex.New(cortex.Config{
			SessionID: sessionID,
			EventBus:  eventBus,
			Durable:   durableBus,
			Provider:  stubProvider{},
		})
		if err != nil {
			return nil, fmt.Errorf("cortex.New: %w", err)
		}
		return c, nil
	}

	// Shell cortex: provides a stable Workspace pointer for the Lobes
	// to capture at construction time.
	shell, err := cortex.New(cortex.Config{
		SessionID: sessionID,
		EventBus:  eventBus,
		Durable:   durableBus,
		Provider:  stubProvider{},
	})
	if err != nil {
		return nil, fmt.Errorf("cortex.New shell: %w", err)
	}
	ws := shell.Workspace()

	// Construct the deterministic Lobes against the shell's Workspace.
	// In-memory memory.Store + wisdom.Store are credential-free; their
	// Path is empty so nothing is persisted.
	memStore, err := memory.NewStore(memory.Config{})
	if err != nil {
		return nil, fmt.Errorf("memory.NewStore: %w", err)
	}
	wisStore := wisdom.NewStore()

	lobeList := []cortex.Lobe{
		memoryrecall.NewMemoryRecallLobe(ws, memStore, wisStore, eventBus),
		walkeeper.NewWALKeeperLobe(eventBus, durableBus, ws, walkeeper.WALFraming{}),
		rulecheck.NewRuleCheckLobe(durableBus, ws),
		antitrunclobe.NewAntiTruncLobe(ws, "", ""),
	}

	// Live cortex: holds the LobeRunners. Returned to callers because
	// LobeStatus / PauseLobe / ResumeLobe need access to the runners.
	// Workspace() is overridden via the wrappedBackend below so it
	// keeps pointing at the SHELL's Workspace (the one Lobes wrote
	// against).
	live, err := cortex.New(cortex.Config{
		SessionID: sessionID,
		EventBus:  eventBus,
		Durable:   durableBus,
		Provider:  stubProvider{},
		Lobes:     lobeList,
	})
	if err != nil {
		return nil, fmt.Errorf("cortex.New live: %w", err)
	}
	return &wrappedBackend{live: live, ws: ws, sessionID: sessionID}, nil
}

// wrappedBackend implements mcp.CortexBackend by composing the live
// cortex (for LobeStatus / PauseLobe / ResumeLobe) with the shell's
// Workspace pointer (so Notes published via r1.cortex.publish land in
// the same Workspace the Lobes write against).
type wrappedBackend struct {
	live      *cortex.Cortex
	ws        *cortex.Workspace
	sessionID string
}

func (b *wrappedBackend) SessionID() string             { return b.sessionID }
func (b *wrappedBackend) Workspace() *cortex.Workspace  { return b.ws }
func (b *wrappedBackend) LobeStatus() []cortex.LobeInfo { return b.live.LobeStatus() }
func (b *wrappedBackend) PauseLobe(id string) error     { return b.live.PauseLobe(id) }
func (b *wrappedBackend) ResumeLobe(id string) error    { return b.live.ResumeLobe(id) }

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
		c, err := buildCortexBackend(opts.SessionID, busDir, opts.LobeMode)
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
	LobeMode  string // --lobes: "none" | "deterministic" | "all"
}

// envOrEmpty returns os.Getenv(key) for live process; tests override
// this via the package-level envFunc to avoid touching the real env.
var envFunc = os.Getenv
