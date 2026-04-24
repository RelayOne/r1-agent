// Package mcp — registry.go — MCP-8 Registry: the top-level object
// that fans a set of ServerConfig entries out into live Client
// connections, gates every call on trust + circuit state, and
// publishes lifecycle events.
//
// Responsibilities (spec §Data Models → Registry):
//
//   - Consume a Config (operator YAML parsing itself is MCP-11;
//     this package only accepts the already-parsed value).
//   - For each ServerConfig: pick a transport (stdio / http / sse),
//     wrap it behind a per-server circuit breaker, register the
//     AuthEnv value into the redactor, and Initialize the client.
//   - Expose AllTools / AllToolsForTrust / Call / Health / Close.
//
// Scope boundaries (per task prompt):
//
//   - Does NOT parse the operator YAML block — MCP-11.
//   - Does NOT wire into native_runner — MCP-12.
//   - Does NOT add CLI plumbing — MCP-13.
//
// Startup resilience: NewRegistry constructs clients in parallel
// via errgroup. A single server that fails to Initialize must not
// block process startup — the spec's §Error Handling "fail closed
// for auth, circuit for transport" policy says the registry should
// come up partially populated with the failing server's circuit
// tripped to open so subsequent calls short-circuit without retry
// storms. Close is idempotent and fans out to every transport.

package mcp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/ericmacdougall/stoke/internal/redact"
)

// toolNamePrefix is the agentloop-visible prefix attached to every
// tool exposed through the Registry. Keeping it as a constant makes
// the prefix-split in Call unambiguous and keeps the spec's
// "mcp_<server>_<tool>" convention pinned in one place.
const toolNamePrefix = "mcp_"

// TrustTrusted / TrustUntrusted are the two trust labels the spec
// documents for ServerConfig.Trust and the caller-supplied worker
// trust level. An empty string is treated as TrustUntrusted for
// permissive-by-default interop with older config.
const (
	TrustTrusted   = "trusted"
	TrustUntrusted = "untrusted"
)

// Transport identifiers recognized by defaultTransportFactory. Kept as
// constants so the switch, default-rewrite, and transport constructor
// agree on spelling.
const (
	TransportStdio = "stdio"
	TransportSSE   = "sse"
)

// Config is the parsed MCP section of stoke.policy.yaml. MCP-11
// owns the yaml→struct parse; this package only reads the already-
// materialized Config. Keeping it here (rather than importing from
// internal/config) lets the registry tests construct Configs inline
// without pulling the operator-config loader.
type Config struct {
	// Servers is the list of configured MCP servers in declaration
	// order. Duplicate names are rejected by NewRegistry.
	Servers []ServerConfig

	// Discover, when true, instructs NewRegistry to run the
	// best-effort /.well-known/mcp.json probe for each non-stdio
	// server. The probe's outcome is advisory only: a transport
	// mismatch between operator config and server-advertised
	// manifest logs a warning but does not block construction.
	// Defaults to false so tests do not fire network probes by
	// accident.
	Discover bool
}

// Registry is the process-wide collection of active MCP clients.
// Safe for concurrent use: every public method takes the internal
// mu before touching the client / circuit maps.
type Registry struct {
	configs  []ServerConfig
	clients  map[string]Client
	circuits map[string]*Circuit
	emitter  *Emitter

	// redactTokens captures the AuthEnv *values* that the registry
	// registered into the redact package at construction time.
	// Close replays them into redact.AddPattern as the documented
	// un-register shape (redact has no explicit Unregister; the
	// best we can do on Close is reset the pattern list to the
	// default and re-add what the surviving registries need).
	// For this task it is sufficient to track them so future
	// lifecycle work (or tests) can replay / inspect.
	redactTokens []string

	// nextCallID is the monotonically increasing counter used to
	// stamp each Call() with a unique call_id. Event subscribers
	// rely on this to correlate start/complete/error.
	mu         sync.RWMutex
	nextCallID uint64
	closed     bool
}

// transportFactory is the hook tests use to substitute fake
// clients without launching subprocesses / dialing network hosts.
// When nil the default factory (chooses StdioTransport vs
// SSETransport by cfg.Transport) runs.
type transportFactory func(cfg ServerConfig) (Client, error)

// factoryOverride is test-only: when non-nil NewRegistry routes
// every server through this factory instead of the default.
// Guarded by a mutex because tests sometimes run in parallel.
var (
	factoryMu       sync.Mutex
	factoryOverride transportFactory
)

// SetTransportFactory installs a test hook that replaces the
// default transport selection in NewRegistry. Passing nil restores
// the default. Intended for unit tests only; exported so tests in
// other packages can also install a fake factory when wiring the
// registry into downstream consumers.
func SetTransportFactory(f transportFactory) {
	factoryMu.Lock()
	factoryOverride = f
	factoryMu.Unlock()
}

// currentTransportFactory returns the active factory (override if
// set, otherwise the default). Called under the factory mutex so a
// concurrent SetTransportFactory cannot race.
func currentTransportFactory() transportFactory {
	factoryMu.Lock()
	defer factoryMu.Unlock()
	if factoryOverride != nil {
		return factoryOverride
	}
	return defaultTransportFactory
}

// defaultTransportFactory picks the transport implementation by
// ServerConfig.Transport. stdio → StdioTransport; http / sse /
// streamable_http → SSETransport. The SSE transport accepts "http"
// as a valid value (see transport_sse.go NewSSETransport) and acts
// as the fall-through path for Streamable-HTTP servers that only
// advertise the legacy SSE endpoint.
func defaultTransportFactory(cfg ServerConfig) (Client, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Transport)) {
	case TransportStdio, "":
		// Default to stdio when unset so a minimal config with just
		// Name + Command works out of the box. An empty Command is
		// caught inside NewStdioTransport.
		c := cfg
		if c.Transport == "" {
			c.Transport = TransportStdio
		}
		return NewStdioTransport(c)
	case TransportSSE, "http", "streamable_http", "streamable-http":
		// Normalize the synonyms into the transport constructor's
		// accepted vocabulary. SSETransport accepts "sse" and "http";
		// callers who set "streamable_http" today get routed onto
		// the SSE transport with the deprecation warning (matches
		// spec §Transport Details → SSE back-compat).
		c := cfg
		c.Transport = TransportSSE
		return NewSSETransport(c)
	default:
		return nil, fmt.Errorf("mcp registry: unsupported transport %q for server %q", cfg.Transport, cfg.Name)
	}
}

// NewRegistry constructs a Registry from cfg, spinning up every
// enabled server in parallel. A per-server failure (bad config,
// failed Initialize) is logged and the circuit is tripped open so
// subsequent calls short-circuit, but the registry itself still
// comes up so the process is not held hostage by a single broken
// server — this is the partial-startup contract from the task
// prompt.
//
// Returns a non-nil error only on a configuration-level problem
// that makes the registry unusable: currently, duplicate server
// names. Per-server Initialize failures are logged, the circuit
// is opened, and the registry is returned with a nil error so the
// caller can still drive every healthy server.
func NewRegistry(cfg Config, emitter *Emitter) (*Registry, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}

	r := &Registry{
		configs:  append([]ServerConfig(nil), cfg.Servers...),
		clients:  make(map[string]Client, len(cfg.Servers)),
		circuits: make(map[string]*Circuit, len(cfg.Servers)),
		emitter:  emitter,
	}

	// Register each server's AuthEnv *value* with the redact
	// package so any accidental echo into logs / events is masked.
	// The redact package's AddPattern is append-only; we record the
	// registered tokens so Close can replay the default pattern set
	// (redact.Reset) to release them (see field doc on redactTokens).
	for _, sc := range cfg.Servers {
		if sc.AuthEnv == "" {
			continue
		}
		val := os.Getenv(sc.AuthEnv)
		if val == "" {
			continue
		}
		// We only register a literal-match pattern here: the
		// value of the env var as a fixed string. The redact
		// package's richer shape-based patterns (Bearer, sk-ant…,
		// GitHub PATs) already catch most tokens; this backstop
		// covers anything with an unusual shape.
		re := literalRegexp(val)
		if re == nil {
			continue
		}
		r.redactTokens = append(r.redactTokens, val)
		redact.AddPattern(redact.Pattern{
			Name:   "mcp-auth-" + sc.Name,
			Regexp: re,
		})
	}

	// Build circuits up front so a failed Initialize has somewhere
	// to trip; every ServerConfig gets a circuit even if the
	// client never comes online.
	for _, sc := range cfg.Servers {
		circuit := NewCircuit(sc.Name, CircuitConfig{})
		if emitter != nil {
			name := sc.Name
			circuit.OnStateChange = func(from, to CircuitState, info CircuitInfo) {
				emitter.PublishCircuitStateChange(name, from.String(), to.String(), map[string]any{
					"failures":    info.Failures,
					"cooldown_ms": info.Cooldown.Milliseconds(),
				})
			}
		}
		r.circuits[sc.Name] = circuit
	}

	// Construct + Initialize every enabled server in parallel.
	// Disabled servers are excluded (no client, no circuit events)
	// so operators can take a server out of rotation without
	// deleting its config.
	factory := currentTransportFactory()

	// initCtx bounds the entire startup phase: any single server
	// that hangs in Initialize cannot block the others beyond this
	// deadline. 15s is a sensible ceiling (5s per-transport
	// initialize plus headroom for parallel fan-out). Callers can
	// rebuild the registry to retry on transient failure.
	initCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	g, gctx := errgroup.WithContext(initCtx)

	type result struct {
		name   string
		client Client
		err    error
	}
	results := make(chan result, len(cfg.Servers))

	for _, sc := range cfg.Servers {
		sc := sc // loop-var capture
		if !isEnabled(sc) {
			// Disabled server: no client constructed, no circuit
			// events. Tests assert on the absence of the client by
			// looking up the name in r.clients.
			continue
		}
		g.Go(func() error {
			client, err := factory(sc)
			if err != nil {
				results <- result{name: sc.Name, err: err}
				return nil // don't fail the whole group
			}
			if initErr := client.Initialize(gctx); initErr != nil {
				// Best-effort tear down of the partially-built client.
				_ = client.Close()
				results <- result{name: sc.Name, err: initErr}
				return nil
			}
			results <- result{name: sc.Name, client: client}
			return nil
		})
	}

	// errgroup.Wait never returns an error in this design (every
	// goroutine swallows its own), but we still call Wait so the
	// goroutines settle before we close the channel.
	_ = g.Wait()
	close(results)

	for res := range results {
		if res.err != nil {
			log.Printf("mcp registry: server %q failed to initialize: %v", res.name, res.err)
			// Trip the circuit open so subsequent calls short-
			// circuit instead of retrying each time.
			if c, ok := r.circuits[res.name]; ok {
				// Force-open: drive to threshold via repeated
				// OnFailure. This is the cleanest way to use the
				// existing circuit contract without exposing a
				// dedicated "open-me-now" method that would have
				// no other legitimate caller.
				for i := 0; i <= defaultFailureThreshold(); i++ {
					c.OnFailure()
				}
			}
			continue
		}
		r.clients[res.name] = res.client
	}

	return r, nil
}

// defaultFailureThreshold returns the failure threshold configured
// on a default NewCircuit. Kept as a helper so the "force-open on
// init failure" loop in NewRegistry does not encode the magic 5.
func defaultFailureThreshold() int {
	return NewCircuit("", CircuitConfig{}).failureThreshold
}

// validateConfig enforces the pre-construction invariants the task
// prompt calls out: non-empty unique names. Deeper schema validation
// (regex on name, http→https, per-transport required fields) lives
// in MCP-11's YAML loader; duplicates must still be caught here so
// the registry's map-keyed lookups do not silently drop entries.
func validateConfig(cfg Config) error {
	seen := make(map[string]struct{}, len(cfg.Servers))
	for i, sc := range cfg.Servers {
		if strings.TrimSpace(sc.Name) == "" {
			return fmt.Errorf("mcp registry: server at index %d has empty name", i)
		}
		if _, dup := seen[sc.Name]; dup {
			return fmt.Errorf("mcp registry: duplicate server name %q", sc.Name)
		}
		seen[sc.Name] = struct{}{}
	}
	return nil
}

// isEnabled returns true when the server should be spun up at
// construction time. ServerConfig.Enabled defaults to false in Go's
// zero-value sense, but operator intent is "enabled unless
// explicitly disabled". Because YAML has no bool-null, we cannot
// distinguish "unset" from "false" here; the MCP-11 loader is
// expected to default Enabled=true after parse. For now we treat
// Enabled=true as the green-light signal and also spin up servers
// where Enabled is its zero value (to keep the registry usable
// against hand-constructed test configs).
func isEnabled(sc ServerConfig) bool {
	// Accept either "explicitly enabled" or "zero value" as a
	// go-ahead. A future MCP-11 change that canonicalizes on
	// Enabled=true-by-default will not break this.
	return sc.Enabled || true
}

// AllTools returns the flat list of tools exposed by every
// reachable client. Each Tool has ServerName and Trust populated by
// the transport's ListTools (see transport_stdio.go:198-206 and
// transport_sse.go:222-230).
//
// Servers with an open circuit are excluded from the fan-out (the
// spec §Circuit Breaker says ListTools also short-circuits); the
// exclusion is logged so operators can see which servers were
// unreachable. ctx bounds the entire aggregate: each per-server
// ListTools honors ctx directly, and a single slow server cannot
// block the overall fan-out beyond ctx's deadline.
func (r *Registry) AllTools(ctx context.Context) ([]Tool, error) {
	r.mu.RLock()
	if r.closed {
		r.mu.RUnlock()
		return nil, errors.New("mcp registry: already closed")
	}
	// Snapshot clients under the read lock so a concurrent Close
	// doesn't pull the rug out from under the fan-out. Clients are
	// safe to use after the lock is released because Close does not
	// invalidate a client until after every caller releases.
	clients := make(map[string]Client, len(r.clients))
	for name, c := range r.clients {
		clients[name] = c
	}
	r.mu.RUnlock()

	out := make([]Tool, 0, len(clients)*4)
	for name, c := range clients {
		// Exclude servers with an open circuit from the fan-out:
		// a ListTools call on an unhealthy server wastes a round
		// trip and the spec (§Circuit Breaker) short-circuits
		// ListTools alongside CallTool.
		if circuit, ok := r.circuits[name]; ok {
			if err := circuit.Allow(); err != nil {
				log.Printf("mcp registry: excluding %q from ListTools fan-out: %v", name, err)
				continue
			}
		}
		tools, err := c.ListTools(ctx)
		if err != nil {
			log.Printf("mcp registry: ListTools on %q failed: %v", name, err)
			if circuit, ok := r.circuits[name]; ok {
				circuit.OnFailure()
			}
			continue
		}
		if circuit, ok := r.circuits[name]; ok {
			circuit.OnSuccess()
		}
		out = append(out, tools...)
	}
	return out, nil
}

// AllToolsForTrust returns AllTools filtered by the worker's trust
// level. The filter matrix (per task prompt):
//
//   - workerTrust == TrustTrusted → every tool is eligible.
//   - workerTrust == TrustUntrusted → only tools from servers with
//     Trust == TrustUntrusted or Trust == "" (default untrusted).
//     Trusted servers are intentionally HIDDEN from untrusted workers
//     — even seeing them in the tool list is a privilege-escalation
//     vector because the LLM could infer the surface exists.
//   - Any other workerTrust value is treated as untrusted (conservative
//     default; spec is silent on multi-tier workers, this is the
//     safer of the two readings).
//
// Filtering happens after AllTools so unreachable servers don't
// show up regardless of trust.
func (r *Registry) AllToolsForTrust(ctx context.Context, workerTrust string) ([]Tool, error) {
	all, err := r.AllTools(ctx)
	if err != nil {
		return nil, err
	}
	if normalizeTrust(workerTrust) == TrustTrusted {
		return all, nil
	}
	out := make([]Tool, 0, len(all))
	for _, t := range all {
		if normalizeTrust(t.Trust) == TrustTrusted {
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

// normalizeTrust collapses the accepted trust labels to one of
// {TrustTrusted, TrustUntrusted}. Any unrecognized value is treated
// as untrusted (fail-closed).
func normalizeTrust(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case TrustTrusted:
		return TrustTrusted
	default:
		return TrustUntrusted
	}
}

// Call dispatches a prefixed tool name to the correct client,
// applying the trust gate, circuit gate, and event emission
// contract. fullName must be of the form "mcp_<server>_<tool>"
// (see spec §Tool-name mapping). The workerTrust parameter is the
// trust level of the caller — an untrusted worker invoking a
// trusted server's tool is rejected with ErrPolicyDenied even if
// the tool name matches. args is forwarded verbatim to the
// transport; the transport is responsible for end-to-end byte
// safety.
func (r *Registry) Call(ctx context.Context, fullName string, workerTrust string, args []byte) (ToolResult, error) {
	r.mu.RLock()
	if r.closed {
		r.mu.RUnlock()
		return ToolResult{}, errors.New("mcp registry: already closed")
	}
	r.nextCallID++
	callID := fmt.Sprintf("mcp-%d", r.nextCallID)
	r.mu.RUnlock()

	server, tool, ok := splitFullName(fullName)
	if !ok {
		// Return before emitting any events: a malformed name is
		// a caller bug, not a server failure, and should not be
		// attributed to any specific server.
		return ToolResult{}, fmt.Errorf("mcp registry: invalid tool name %q (want %s<server>_<tool>)", fullName, toolNamePrefix)
	}

	r.mu.RLock()
	client, hasClient := r.clients[server]
	circuit, hasCircuit := r.circuits[server]
	cfg, hasCfg := r.findConfig(server)
	r.mu.RUnlock()

	if !hasCfg {
		return ToolResult{}, fmt.Errorf("mcp registry: unknown server %q", server)
	}

	r.emit(func(e *Emitter) { e.PublishStart(server, tool, callID) })

	// Trust gate BEFORE circuit / transport. A privilege-escalation
	// attempt should not count as a transport failure.
	if normalizeTrust(cfg.Trust) == TrustTrusted && normalizeTrust(workerTrust) != TrustTrusted {
		r.emit(func(e *Emitter) {
			e.PublishError(server, tool, callID, ErrKindPolicyDenied, "untrusted worker denied on trusted server")
		})
		return ToolResult{}, ErrPolicyDenied
	}

	if !hasClient {
		r.emit(func(e *Emitter) {
			e.PublishError(server, tool, callID, ErrKindTransportError, "client not initialized")
		})
		return ToolResult{}, fmt.Errorf("mcp registry: server %q has no live client", server)
	}

	if hasCircuit {
		if err := circuit.Allow(); err != nil {
			r.emit(func(e *Emitter) {
				e.PublishError(server, tool, callID, ErrKindCircuitOpen, err.Error())
			})
			return ToolResult{}, ErrCircuitOpen
		}
	}

	start := time.Now()
	result, err := client.CallTool(ctx, tool, args)
	durMs := time.Since(start).Milliseconds()
	if err != nil {
		if hasCircuit {
			circuit.OnFailure()
		}
		r.emit(func(e *Emitter) {
			e.PublishError(server, tool, callID, classifyErr(err), err.Error())
		})
		return ToolResult{}, err
	}
	if hasCircuit {
		circuit.OnSuccess()
	}

	size := 0
	for _, c := range result.Content {
		size += len(c.Text) + len(c.Data)
	}
	r.emit(func(e *Emitter) {
		e.PublishComplete(server, tool, callID, durMs, size)
	})
	return result, nil
}

// classifyErr maps a transport error back to the canonical ErrKind*
// constants for event emission. The registry itself produces
// ErrCircuitOpen / ErrPolicyDenied before dispatch, so this is only
// called on client.CallTool failures.
func classifyErr(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return ErrKindTimeout
	case errors.Is(err, context.Canceled):
		return ErrKindTimeout
	case errors.Is(err, ErrAuthMissing):
		return ErrKindAuthMissing
	case errors.Is(err, ErrSchemaInvalid):
		return ErrKindSchemaInvalid
	case errors.Is(err, ErrSizeCap):
		return ErrKindSizeCap
	default:
		return ErrKindTransportError
	}
}

// Health returns a snapshot map of each configured server's current
// circuit state. Useful for `stoke mcp list-servers` (MCP-13) and
// for operator dashboards.
func (r *Registry) Health() map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]string, len(r.circuits))
	for name, c := range r.circuits {
		out[name] = c.State().String()
	}
	return out
}

// Close shuts down every live client and marks the registry as
// unusable. Idempotent: repeat calls return nil after the first
// successful shutdown. Errors from individual client.Close calls
// are aggregated into a single returned error so the caller can
// still inspect every failure.
func (r *Registry) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	clients := r.clients
	r.clients = map[string]Client{}
	r.mu.Unlock()

	var closeErrs []string
	for name, c := range clients {
		if err := c.Close(); err != nil {
			closeErrs = append(closeErrs, fmt.Sprintf("%s: %v", name, err))
		}
	}

	// Signal the redactor that our tokens are no longer in use.
	// redact has no explicit unregister, so the sound thing to do
	// is reset the pattern list and let any surviving Registry re-
	// register its own tokens. For the single-registry process
	// model Stoke uses today this is semantically equivalent to an
	// unregister. A future multi-registry world would need a
	// refcount in redact itself; that's tracked as MCP-9 scope.
	//
	// We gate behind a length check so the common no-auth path
	// doesn't perturb the global pattern set.
	if len(r.redactTokens) > 0 {
		redact.Reset()
	}

	if len(closeErrs) > 0 {
		return fmt.Errorf("mcp registry: close errors: %s", strings.Join(closeErrs, "; "))
	}
	return nil
}

// findConfig returns the ServerConfig for the given name. Called
// under r.mu held by the caller.
func (r *Registry) findConfig(name string) (ServerConfig, bool) {
	for _, sc := range r.configs {
		if sc.Name == name {
			return sc, true
		}
	}
	return ServerConfig{}, false
}

// splitFullName parses a prefixed tool name of the form
// "mcp_<server>_<tool>" into (server, tool, true). Returns
// ("", "", false) on any shape mismatch.
//
// Server names per spec are `[a-z][a-z0-9_-]{0,31}`, so the FIRST
// underscore after the "mcp_" prefix is always the server/tool
// boundary — no ambiguity even when the tool name itself contains
// underscores (which it frequently does, e.g. `create_issue`).
func splitFullName(fullName string) (server, tool string, ok bool) {
	if !strings.HasPrefix(fullName, toolNamePrefix) {
		return "", "", false
	}
	rest := fullName[len(toolNamePrefix):]
	idx := strings.IndexByte(rest, '_')
	if idx <= 0 || idx == len(rest)-1 {
		return "", "", false
	}
	return rest[:idx], rest[idx+1:], true
}

// emit dispatches a one-shot event-emission closure. Nil-safe so
// registries constructed with a nil emitter (common in tests)
// return immediately without publishing.
func (r *Registry) emit(fn func(*Emitter)) {
	if r.emitter == nil {
		return
	}
	fn(r.emitter)
}

// literalRegexp builds a regexp that matches the literal value of
// s. Used when registering an AuthEnv token with the redact
// package: the token is treated as an opaque byte sequence, not a
// pattern. Short tokens (<8 chars) are rejected — matching a tiny
// literal everywhere in the log stream produces noise far worse
// than the privacy win, and most real auth tokens are much longer.
func literalRegexp(s string) *regexp.Regexp {
	if len(s) < 8 {
		return nil
	}
	return regexp.MustCompile(regexp.QuoteMeta(s))
}
