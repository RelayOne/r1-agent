// fallback_provider.go — FallbackProvider wraps two provider.Provider
// instances (primary, secondary) behind the same interface and
// transparently swaps between them when the active one emits a rate-
// limit / transient-failure signature. Mirrors FallbackPair's semantics
// for the CLI-call path (cmd/r1/fallback.go), but at the
// provider.Provider seam used by the sow-native harness
// (cmd/r1/sow_native.go: cfg.ReviewProvider.Chat).
//
// Why a separate type? FallbackPair operates on a (ctx, prompt) → (text,
// err) contract — fine for simple-loop's direct CLI wrappers, but the
// sow-native reviewer call goes through provider.Provider.Chat, which
// takes a full ChatRequest (system prompt, cache-control, messages,
// max_tokens, tools). Adapting one to the other at every sow call site
// would be lossy. Wrapping at the provider layer keeps the richer
// request shape intact and is a drop-in substitution anywhere a
// provider.Provider is expected.
//
// Health check restores primary after healthCheckEvery (default 5 min):
// when we are currently serving from secondary, the next Chat call
// issues a lightweight ping to the inactive primary. If the ping
// returns cleanly the pair swaps back. This is the same restore logic
// FallbackPair uses, refactored out so both wrappers benefit.

package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/stream"
)

// FallbackProvider implements provider.Provider by delegating to a
// primary provider and falling back to a secondary on rate-limit /
// transient-error signatures. Safe for concurrent use.
//
// The typical wiring (H-13) is:
//
//	primary   = codex reviewer (NewCodexProvider)
//	secondary = Claude Code reviewer (NewClaudeCodeProvider)
//
// When codex emits "no last agent message" or "wrote empty content"
// (its known transient-failure modes), the FallbackProvider swaps to
// CC-sonnet so the sow reviewer call still produces a verdict. On the
// next call, after healthCheckEvery, the pair pings codex; if codex
// is healthy again it swaps back.
type FallbackProvider struct {
	primary   provider.Provider
	secondary provider.Provider

	// currentPrimary: 0 = primary active, 1 = secondary active.
	currentPrimary atomic.Int32

	lastSwap        atomic.Value // time.Time
	lastHealthCheck atomic.Value // time.Time

	healthCheckEvery time.Duration
	// healthPingPrompt is the message we send to the inactive
	// provider to confirm it's responsive. Short and deterministic.
	healthPingPrompt string
	// healthPingModel is forwarded to the provider; empty means
	// "use the provider's default model".
	healthPingModel string

	// role is a human-readable tag that goes into log lines so
	// operators can grep swaps per-role ("harness-reviewer", etc.).
	role string

	// now is the mockable clock. time.Now in production.
	now func() time.Time

	// healthPing, when non-nil, bypasses the real provider.Chat
	// during a health check. Tests use this to avoid spawning real
	// CLI subprocesses.
	healthPing func(p provider.Provider) (string, error)

	mu sync.Mutex
}

// NewFallbackProvider builds a FallbackProvider with production
// defaults: 5-minute health-check interval, wall clock, real Chat as
// the health ping.
func NewFallbackProvider(role string, primary, secondary provider.Provider) *FallbackProvider {
	fp := &FallbackProvider{
		primary:          primary,
		secondary:        secondary,
		healthCheckEvery: 5 * time.Minute,
		healthPingPrompt: "Reply with just: pong",
		role:             role,
		now:              time.Now,
	}
	fp.lastSwap.Store(time.Time{})
	fp.lastHealthCheck.Store(fp.now())
	return fp
}

// Name is the provider.Provider name. We surface the ACTIVE provider's
// name so logs downstream that key off Name() reflect the current
// routing decision. Operators who want the fixed logical role read
// fp.role via ActiveRoleName.
func (fp *FallbackProvider) Name() string {
	return fp.active().Name()
}

// ActiveRoleName returns the logical role label (e.g. "harness-reviewer")
// plus the currently-active provider name. Intended for log lines.
func (fp *FallbackProvider) ActiveRoleName() string {
	return fp.role + "=" + fp.active().Name()
}

// OnSecondary reports whether the pair is currently serving from the
// secondary (fallback) provider. Used by tests and ops.
func (fp *FallbackProvider) OnSecondary() bool {
	return fp.currentPrimary.Load() == 1
}

func (fp *FallbackProvider) active() provider.Provider {
	if fp.currentPrimary.Load() == 0 {
		return fp.primary
	}
	return fp.secondary
}

func (fp *FallbackProvider) inactive() provider.Provider {
	if fp.currentPrimary.Load() == 0 {
		return fp.secondary
	}
	return fp.primary
}

// swap flips active ↔ inactive under the mutex. Returns the newly-
// active provider.
func (fp *FallbackProvider) swap() provider.Provider {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	if fp.currentPrimary.Load() == 0 {
		fp.currentPrimary.Store(1)
	} else {
		fp.currentPrimary.Store(0)
	}
	fp.lastSwap.Store(fp.now())
	return fp.active()
}

// restorePrimary forces the pair back to primary. Called by the health
// check when the primary proves responsive again. Returns true when
// this call actually changed state.
func (fp *FallbackProvider) restorePrimary() bool {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	if fp.currentPrimary.Load() == 0 {
		return false
	}
	fp.currentPrimary.Store(0)
	fp.lastSwap.Store(fp.now())
	return true
}

// isRateLimit classifies a provider.Chat outcome as "the active
// provider is dead/throttled, swap" vs "normal result or normal
// error". Signatures are intentionally generous — a false positive
// triggers one redundant swap (cheap), a false negative wedges the
// whole reviewer loop on a dead provider (expensive).
//
// The error-text signatures mirror cmd/r1/fallback.go isRateLimit
// so both abstractions react to the same failure modes:
//
//   Codex CLI (known flakes):
//     - "no last agent message"
//     - "wrote empty content"
//
//   Claude Code CLI:
//     - "claude-code: ... exit status 1" with no content
//     - hung-process errors ("process hung (no output for")
//
//   Generic provider:
//     - nil error but zero-length content (silent empty)
//     - any error AND empty content
func (fp *FallbackProvider) isRateLimit(p provider.Provider, resp *provider.ChatResponse, err error) bool {
	contentLen := 0
	if resp != nil {
		for _, c := range resp.Content {
			contentLen += len(c.Text)
		}
	}
	if err == nil {
		// Clean success must have SOME content to count as real.
		// Silent-empty responses are almost always a broken provider,
		// so treat them as a swap signal.
		return contentLen == 0
	}
	emsg := strings.ToLower(err.Error())

	// Codex transient failure modes.
	if strings.Contains(emsg, "no last agent message") ||
		strings.Contains(emsg, "wrote empty content") {
		return true
	}
	// Process hung.
	if strings.Contains(emsg, "process hung") {
		return true
	}
	// Claude / Codex CLI exit-status-1 with no usable output.
	if strings.Contains(emsg, "exit status 1") && contentLen < 200 {
		return true
	}
	// Generic rate-limit keyword — errors like "429", "rate limit",
	// "overloaded" should also swap.
	if strings.Contains(emsg, "rate limit") ||
		strings.Contains(emsg, "rate_limit") ||
		strings.Contains(emsg, "overloaded") ||
		strings.Contains(emsg, " 429 ") ||
		strings.HasSuffix(emsg, " 429") ||
		strings.Contains(emsg, "status 429") {
		return true
	}
	// Anthropic quota-exceeded signatures vary; check Name() so we
	// don't swap on an unrelated provider error that happens to
	// contain the string "quota".
	if p != nil {
		pname := strings.ToLower(p.Name())
		if (pname == "claude-code" || pname == "anthropic") &&
			(strings.Contains(emsg, "quota") || strings.Contains(emsg, "usage_limit")) {
			return true
		}
	}
	return false
}

// maybeHealthCheck runs a ping against the INACTIVE provider at most
// once per healthCheckEvery. When we're currently on the secondary and
// the primary answers cleanly, swap back to primary. When we're
// already on primary, the ping confirms the fallback is warm but we
// don't take any swap action. Errors from the ping are never fatal.
func (fp *FallbackProvider) maybeHealthCheck() {
	fp.mu.Lock()
	lastAny := fp.lastHealthCheck.Load()
	var last time.Time
	if t, ok := lastAny.(time.Time); ok {
		last = t
	}
	if fp.now().Sub(last) < fp.healthCheckEvery {
		fp.mu.Unlock()
		return
	}
	fp.lastHealthCheck.Store(fp.now())
	inactive := fp.inactive()
	onSecondary := fp.currentPrimary.Load() == 1
	fp.mu.Unlock()

	out, err := fp.pingProvider(inactive)
	if err != nil || strings.TrimSpace(out) == "" {
		return
	}
	if onSecondary {
		if fp.restorePrimary() {
			fmt.Fprintf(os.Stderr,
				"▶ %s primary %s restored (secondary %s still healthy)\n",
				fp.role, fp.primary.Name(), fp.secondary.Name())
		}
	}
}

// pingProvider dispatches a minimal Chat call to confirm liveness.
// Tests substitute fp.healthPing so they don't spawn real CLIs.
func (fp *FallbackProvider) pingProvider(p provider.Provider) (string, error) {
	if fp.healthPing != nil {
		return fp.healthPing(p)
	}
	userContent, err := encodeTextMessage(fp.healthPingPrompt)
	if err != nil {
		return "", err
	}
	req := provider.ChatRequest{
		Model:     fp.healthPingModel,
		MaxTokens: 64,
		Messages:  []provider.ChatMessage{{Role: "user", Content: userContent}},
	}
	// The ping is bounded in practice by the provider's own timeout;
	// we don't pass ctx here because provider.Chat doesn't accept one.
	// The real providers (ClaudeCode / Codex) self-impose 20-min
	// timeouts which is fine for a background health check.
	resp, err := p.Chat(req)
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", fmt.Errorf("nil response")
	}
	for _, c := range resp.Content {
		if c.Type == "text" && strings.TrimSpace(c.Text) != "" {
			return c.Text, nil
		}
	}
	return "", fmt.Errorf("no text in ping response")
}

// Chat implements provider.Provider. Executes the request on the
// active provider; on a rate-limit signature, swaps once and retries
// on the other provider. If both fail, returns the secondary's error
// so the caller sees a real signal.
func (fp *FallbackProvider) Chat(req provider.ChatRequest) (*provider.ChatResponse, error) {
	fp.maybeHealthCheck()

	active := fp.active()
	resp, err := active.Chat(req)
	if !fp.isRateLimit(active, resp, err) {
		return resp, err
	}

	other := fp.swap()
	fmt.Fprintf(os.Stderr,
		"⚠ %s rate-limit on %s → fallback to %s (role: %s, err: %v)\n",
		fp.role, active.Name(), other.Name(), fp.role, err)

	resp2, err2 := other.Chat(req)
	if fp.isRateLimit(other, resp2, err2) {
		if err2 != nil {
			return resp2, err2
		}
		return resp2, fmt.Errorf("both %s providers rate-limited (primary=%s secondary=%s)",
			fp.role, fp.primary.Name(), fp.secondary.Name())
	}
	return resp2, err2
}

// ChatStream implements provider.Provider for the streaming path. On
// the primary's error we fall back to the secondary and re-issue the
// stream there; the onEvent callback is forwarded to whichever
// provider eventually serves the request. If the primary streams
// partial events before failing, those events ARE forwarded — the
// caller is responsible for treating a subsequent error as a signal
// that the stream was restarted from the secondary.
func (fp *FallbackProvider) ChatStream(req provider.ChatRequest, onEvent func(stream.Event)) (*provider.ChatResponse, error) {
	fp.maybeHealthCheck()

	active := fp.active()
	resp, err := active.ChatStream(req, onEvent)
	if !fp.isRateLimit(active, resp, err) {
		return resp, err
	}

	other := fp.swap()
	fmt.Fprintf(os.Stderr,
		"⚠ %s rate-limit on %s → fallback to %s (role: %s, err: %v)\n",
		fp.role, active.Name(), other.Name(), fp.role, err)

	resp2, err2 := other.ChatStream(req, onEvent)
	if fp.isRateLimit(other, resp2, err2) {
		if err2 != nil {
			return resp2, err2
		}
		return resp2, fmt.Errorf("both %s providers rate-limited (primary=%s secondary=%s)",
			fp.role, fp.primary.Name(), fp.secondary.Name())
	}
	return resp2, err2
}

// fallbackReviewProviderFromFlags builds a FallbackProvider for the
// SOW reviewer role when the operator has BOTH a primary reviewer
// (usually LiteLLM-fronted codex, or direct codex via codex://) AND a
// viable CC fallback available. When either is missing, returns the
// primary as-is — no wrapping, zero behavioral change from pre-H-13.
//
// The decision is deliberately simple: if primary.Name() == "codex"
// and we can construct a ClaudeCodeProvider against the configured
// claude binary, wrap. Otherwise return primary unchanged. This keeps
// the default-off posture requested by the task: only when the
// operator is clearly running the codex-primary setup does fallback
// engage.
//
// repoRoot is the working directory the CC fallback will use as its
// --cwd. claudeBin is the resolved Claude Code binary path.
func fallbackReviewProviderFromFlags(primary provider.Provider, claudeBin, repoRoot string) provider.Provider {
	if primary == nil {
		return nil
	}
	// Wrap only if primary is the codex CLI. Other primaries (litellm-
	// fronted anthropic, openrouter, direct anthropic) get their own
	// retry/backoff at the HTTP layer, so the CLI-level fallback
	// signatures we detect don't apply cleanly and double-wrapping
	// would just add latency.
	if primary.Name() != "codex" {
		return primary
	}
	if strings.TrimSpace(claudeBin) == "" {
		claudeBin = "claude"
	}
	secondary := provider.NewClaudeCodeProvider(claudeBin, repoRoot, "")
	return NewFallbackProvider("harness-reviewer", primary, secondary)
}
