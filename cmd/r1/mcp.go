package main

// mcp.go — `r1 mcp …` subcommand group (spec: specs/mcp-client.md §CLI Surface).
//
// Four subcommands:
//
//   r1 mcp list-servers               list every configured server
//   r1 mcp list-tools [--server X]    list tools (optionally filtered)
//   r1 mcp test <server>              connect + list + trivial call
//   r1 mcp call <server> <tool> [--args-json JSON]
//
// All commands share:
//   --policy PATH     override stoke.policy.yaml discovery
//   --timeout DUR     per-operation timeout (default 30s; spec §Data Models)
//   --json            machine-readable output on list-tools
//
// Construction path:
//   1. AutoLoadPolicy → Policy.MCPServers ([]mcp.ServerConfig)
//   2. mcp.NewRegistry(Config{Servers: …}, nil)   (nil emitter: CLI has no SOW bus)
//   3. dispatch
//   4. Close
//
// Exit codes:
//   0 = success
//   1 = usage / config error
//   2 = test-command: transport/connect failure
//   3 = test-command: tool-call failure

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/config"
	"github.com/RelayOne/r1/internal/mcp"
)

// defaultMCPTimeout mirrors specs/mcp-client.md §Data Models: 30s cap on
// per-tool-call operations. Applied across every subcommand.
const defaultMCPTimeout = 30 * time.Second

// mcpCmd is the top-level dispatch for `r1 mcp …`. Thin shim that
// reads the first positional, then hands off to a subcommand-specific
// runner. Unknown / missing subcommands print usage and exit 1.
func mcpCmd(args []string) {
	code := runMCPCmd(args, os.Stdout, os.Stderr, loadMCPRegistry)
	if code != 0 {
		os.Exit(code)
	}
}

// registryLoader abstracts the policy-load + registry-construct path so
// tests can inject fake registries without touching the filesystem.
type registryLoader func(policyPath string) (*mcp.Registry, []mcp.ServerConfig, func(), error)

// runMCPCmd is the testable core of mcpCmd — exit code is returned
// rather than triggering os.Exit so the unit tests can assert on every
// branch. stdout / stderr are injectable for the same reason.
func runMCPCmd(args []string, stdout, stderr io.Writer, loader registryLoader) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, mcpUsage)
		return 1
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "list-servers":
		return runMCPListServers(rest, stdout, stderr, loader)
	case "list-tools":
		return runMCPListTools(rest, stdout, stderr, loader)
	case "test":
		return runMCPTest(rest, stdout, stderr, loader)
	case "call":
		return runMCPCall(rest, stdout, stderr, loader)
	case "-h", "--help", "help":
		fmt.Fprintln(stdout, mcpUsage)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown mcp subcommand: %s\n\n%s\n", sub, mcpUsage)
		return 1
	}
}

const mcpUsage = `r1 mcp — MCP client management

USAGE:
  r1 mcp list-servers [--policy PATH]
  r1 mcp list-tools   [--server NAME] [--json] [--policy PATH] [--timeout DUR]
  r1 mcp test <server>            [--policy PATH] [--timeout DUR]
  r1 mcp call <server> <tool> [--args-json JSON] [--policy PATH] [--timeout DUR]

Reads stoke.policy.yaml's mcp_servers block. Exit codes:
  0 success   1 usage/config   2 transport/connect   3 tool call`

// loadMCPRegistry is the production registryLoader: discovers the
// policy file via config.AutoLoadPolicy, validates the mcp_servers
// block, constructs a Registry with a nil emitter (CLI is outside the
// SOW event bus), and returns a Close closure for deferred cleanup.
func loadMCPRegistry(policyPath string) (*mcp.Registry, []mcp.ServerConfig, func(), error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("getwd: %w", err)
	}
	p, err := config.AutoLoadPolicy(cwd, policyPath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load policy: %w", err)
	}
	servers := append([]mcp.ServerConfig(nil), p.MCPServers...)
	reg, err := mcp.NewRegistry(mcp.Config{Servers: servers}, nil)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("build registry: %w", err)
	}
	closer := func() { _ = reg.Close() }
	return reg, servers, closer, nil
}

// --- list-servers -------------------------------------------------

func runMCPListServers(args []string, stdout, stderr io.Writer, loader registryLoader) int {
	fs := flag.NewFlagSet("mcp list-servers", flag.ContinueOnError)
	fs.SetOutput(stderr)
	policy := fs.String("policy", "", "path to stoke.policy.yaml (default: auto-detect)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	reg, servers, closer, err := loader(*policy)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	defer closer()

	health := reg.Health()

	// Stable output order: policy-declaration order (same as servers
	// slice). Preserves operator-configured grouping.
	for _, sc := range servers {
		endpoint := sc.URL
		if strings.TrimSpace(endpoint) == "" {
			endpoint = sc.Command
			if len(sc.Args) > 0 {
				endpoint = endpoint + " " + strings.Join(sc.Args, " ")
			}
		}
		if endpoint == "" {
			endpoint = "(none)"
		}
		trust := sc.Trust
		if trust == "" {
			trust = mcp.TrustUntrusted
		}
		circuit := health[sc.Name]
		if circuit == "" {
			circuit = "unknown"
		}
		transport := sc.Transport
		if transport == "" {
			transport = "stdio"
		}
		fmt.Fprintf(stdout, "%s | %s | %s | %s | %s\n",
			sc.Name, transport, endpoint, trust, circuit)
	}
	if len(servers) == 0 {
		fmt.Fprintln(stdout, "(no mcp_servers configured)")
	}
	return 0
}

// --- list-tools ---------------------------------------------------

func runMCPListTools(args []string, stdout, stderr io.Writer, loader registryLoader) int {
	fs := flag.NewFlagSet("mcp list-tools", flag.ContinueOnError)
	fs.SetOutput(stderr)
	policy := fs.String("policy", "", "path to stoke.policy.yaml (default: auto-detect)")
	server := fs.String("server", "", "filter by server name (default: all servers)")
	asJSON := fs.Bool("json", false, "emit raw tool definitions as JSON")
	timeout := fs.Duration("timeout", defaultMCPTimeout, "per-operation timeout")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	reg, servers, closer, err := loader(*policy)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	defer closer()

	// Validate --server names up front so a typo fails fast with a
	// useful message instead of silently returning zero tools.
	if *server != "" {
		found := false
		for _, sc := range servers {
			if sc.Name == *server {
				found = true
				break
			}
		}
		if !found {
			fmt.Fprintf(stderr, "error: server %q not configured\n", *server)
			return 1
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	tools, err := reg.AllTools(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "list-tools: %v\n", err)
		return 2
	}
	if *server != "" {
		filtered := make([]mcp.Tool, 0, len(tools))
		for _, t := range tools {
			if t.ServerName == *server {
				filtered = append(filtered, t)
			}
		}
		tools = filtered
	}
	// Deterministic ordering for both text and JSON output — makes
	// shell-script consumers (e.g. the spec's jq assertion) stable
	// across runs regardless of the registry's internal map iteration.
	sort.SliceStable(tools, func(i, j int) bool {
		if tools[i].ServerName != tools[j].ServerName {
			return tools[i].ServerName < tools[j].ServerName
		}
		return tools[i].Definition.Name < tools[j].Definition.Name
	})

	if *asJSON {
		// Emit the array of tool definitions verbatim. Consumers pipe
		// this to jq; do not wrap in an outer object.
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(tools); err != nil {
			fmt.Fprintf(stderr, "encode: %v\n", err)
			return 1
		}
		return 0
	}
	for _, t := range tools {
		fmt.Fprintf(stdout, "mcp_%s_%s\t%s\n",
			t.ServerName, t.Definition.Name,
			singleLine(t.Definition.Description))
	}
	if len(tools) == 0 {
		fmt.Fprintln(stdout, "(no tools)")
	}
	return 0
}

// singleLine collapses whitespace in a tool description so each tool
// occupies exactly one output line — servers occasionally return
// multi-line descriptions and that wrecks the table layout.
func singleLine(s string) string {
	f := strings.Fields(s)
	return strings.Join(f, " ")
}

// partitionFlags splits argv into (flagTokens, positionals) so
// subcommands can accept flags either before or after positionals.
// Go's stdlib flag package stops at the first non-flag arg, which
// forces users to put `--foo bar <positional>`; partitioning lets us
// honor the more familiar `<positional> --foo bar` shape too.
//
// A token starting with `-` (and not bare `-`/`--`) is a flag. The
// next token is consumed as its value unless the flag uses the
// `--name=value` form. Values for bool-typed flags technically
// consume a following non-dash token that they shouldn't, but this
// CLI uses only string/duration flags so the heuristic is safe.
func partitionFlags(args []string) (flags, positionals []string) {
	for i := 0; i < len(args); i++ {
		tok := args[i]
		if tok == "--" {
			positionals = append(positionals, args[i+1:]...)
			return
		}
		if strings.HasPrefix(tok, "-") && tok != "-" {
			flags = append(flags, tok)
			// `--name=value` shape: value is already in tok.
			if strings.Contains(tok, "=") {
				continue
			}
			// Otherwise the next token (if any and not another flag)
			// is the value. Bool flags would mis-consume here, but
			// the MCP CLI has none.
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		positionals = append(positionals, tok)
	}
	return
}

// --- test ---------------------------------------------------------

func runMCPTest(args []string, stdout, stderr io.Writer, loader registryLoader) int {
	fs := flag.NewFlagSet("mcp test", flag.ContinueOnError)
	fs.SetOutput(stderr)
	policy := fs.String("policy", "", "path to stoke.policy.yaml (default: auto-detect)")
	timeout := fs.Duration("timeout", defaultMCPTimeout, "per-operation timeout")
	flagTokens, positionals := partitionFlags(args)
	if err := fs.Parse(flagTokens); err != nil {
		return 1
	}
	if len(positionals) < 1 {
		fmt.Fprintln(stderr, "usage: r1 mcp test <server>")
		return 1
	}
	serverName := positionals[0]

	reg, servers, closer, err := loader(*policy)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	defer closer()

	// Confirm the server name is configured before reporting stages.
	var found bool
	var cfg mcp.ServerConfig
	for _, sc := range servers {
		if sc.Name == serverName {
			found = true
			cfg = sc
			break
		}
	}
	if !found {
		fmt.Fprintf(stderr, "error: server %q not configured\n", serverName)
		return 1
	}

	// Trust level: `test` uses TrustTrusted so the CLI can probe
	// trusted servers the same as untrusted ones. Operators invoking
	// this CLI are already privileged (they read the policy file).
	trust := mcp.TrustTrusted

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	// STAGE 1: Initialize is implicit in NewRegistry; we surface its
	// outcome by checking Health.
	health := reg.Health()
	state := health[serverName]
	if state == "open" {
		fmt.Fprintf(stdout, "initialize: FAIL (circuit=open)\n")
		return 2
	}
	fmt.Fprintf(stdout, "initialize: PASS (circuit=%s)\n", state)

	// STAGE 2: ListTools (filter to this server).
	all, err := reg.AllToolsForTrust(ctx, trust)
	if err != nil {
		fmt.Fprintf(stdout, "list-tools: FAIL (%v)\n", err)
		return 2
	}
	var tools []mcp.Tool
	for _, t := range all {
		if t.ServerName == serverName {
			tools = append(tools, t)
		}
	}
	fmt.Fprintf(stdout, "list-tools: PASS (count=%d)\n", len(tools))
	if len(tools) == 0 {
		fmt.Fprintln(stdout, "call-tool:  SKIP (no tools advertised)")
		return 0
	}

	// STAGE 3: CallTool on the first-advertised tool with `{}` args.
	// The spec says "if schema allows an empty arg" — we try the
	// empty object regardless and report the server's error verbatim
	// when the schema rejects it. That is still a useful signal that
	// transport + auth + dispatch work end-to-end.
	first := tools[0]
	prefixed := fmt.Sprintf("mcp_%s_%s", first.ServerName, first.Definition.Name)
	_ = cfg // cfg reserved for future per-server overrides
	result, err := reg.Call(ctx, prefixed, trust, json.RawMessage("{}"))
	if err != nil {
		fmt.Fprintf(stdout, "call-tool:  FAIL (%s: %v)\n", prefixed, err)
		return 3
	}
	fmt.Fprintf(stdout, "call-tool:  PASS (%s, %d content block(s))\n",
		prefixed, len(result.Content))
	return 0
}

// --- call ---------------------------------------------------------

func runMCPCall(args []string, stdout, stderr io.Writer, loader registryLoader) int {
	fs := flag.NewFlagSet("mcp call", flag.ContinueOnError)
	fs.SetOutput(stderr)
	policy := fs.String("policy", "", "path to stoke.policy.yaml (default: auto-detect)")
	timeout := fs.Duration("timeout", defaultMCPTimeout, "per-operation timeout")
	argsJSON := fs.String("args-json", "{}", "JSON object of tool arguments")
	// Go's flag pkg stops at the first non-flag positional. To let
	// users mix `<server> <tool>` positionals with `--args-json`
	// either before or after, partition args into (flagTokens,
	// positionals) manually and feed only flagTokens to fs.Parse.
	flagTokens, positionals := partitionFlags(args)
	if err := fs.Parse(flagTokens); err != nil {
		return 1
	}
	if len(positionals) < 2 {
		fmt.Fprintln(stderr, "usage: r1 mcp call <server> <tool> [--args-json JSON]")
		return 1
	}
	serverName := positionals[0]
	toolName := positionals[1]

	// Validate the args-json payload up front: a bad blob is a caller
	// bug and should not count as a transport failure.
	if !json.Valid([]byte(*argsJSON)) {
		fmt.Fprintf(stderr, "error: --args-json is not valid JSON\n")
		return 1
	}

	reg, servers, closer, err := loader(*policy)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	defer closer()

	found := false
	for _, sc := range servers {
		if sc.Name == serverName {
			found = true
			break
		}
	}
	if !found {
		fmt.Fprintf(stderr, "error: server %q not configured\n", serverName)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	prefixed := fmt.Sprintf("mcp_%s_%s", serverName, toolName)
	result, err := reg.Call(ctx, prefixed, mcp.TrustTrusted, json.RawMessage(*argsJSON))
	if err != nil {
		fmt.Fprintf(stderr, "call-tool: %v\n", err)
		return 3
	}

	// Pretty-print the result so humans can read the content blocks.
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(result); err != nil {
		fmt.Fprintf(stderr, "encode: %v\n", err)
		return 1
	}
	return 0
}
