package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ModelRole is anything that can execute a text prompt and return
// text. Both the claude-CLI worker and the codex-CLI reviewer are
// wrapped as ModelRole for use inside FallbackPair.
//
// Call should return (output, err). A rate-limit signature is
// signalled by returning an error OR by returning output that
// contains the provider-specific rate-limit marker — FallbackPair
// inspects both.
type ModelRole interface {
	Call(ctx context.Context, prompt string) (output string, err error)
	Name() string // short tag for logs: "claude" | "codex"
}

// FallbackPair swaps between a primary and secondary ModelRole when
// the active one hits a rate-limit signature. A periodic health
// check (default every 5 minutes) pings the INACTIVE role and, if
// it responds cleanly, swaps back to the original primary. This
// keeps long-running loops making forward progress even when one
// provider is rate-limited: the secondary carries the work until
// the primary recovers, then we go back to the primary.
//
// FallbackPair is safe for concurrent use from multiple goroutines.
// The active role is swapped under the internal mutex so a swap-
// during-call race produces at most one redundant swap.
//
// The clock is mockable via now; real code uses time.Now. The
// healthPing is mockable too so tests don't spawn CLI subprocesses.
type FallbackPair struct {
	primary   ModelRole
	secondary ModelRole

	// currentPrimary is 0 when primary is active, 1 when secondary
	// is active. Updated atomically but mutated only under mu so
	// concurrent swap() calls don't thrash.
	currentPrimary atomic.Int32

	// Last-swap and last-health-check timestamps, stored as
	// time.Time via atomic.Value for cheap unlocked reads.
	lastSwap        atomic.Value // time.Time
	lastHealthCheck atomic.Value // time.Time

	healthCheckEvery time.Duration
	// healthPingPrompt is the prompt sent to the inactive role for
	// liveness. Short and deterministic so the ping is cheap.
	healthPingPrompt string

	mu sync.Mutex

	// now is the mockable clock. time.Now in production.
	now func() time.Time

	// healthPing is the optional hook tests use to bypass the real
	// ModelRole.Call during a health check. When nil, FallbackPair
	// invokes the inactive role's Call directly with a short timeout
	// context.
	healthPing func(role ModelRole) (string, error)

	// role is the logical role this pair fills ("writer", "reviewer",
	// "harness-reviewer"). Used only in log lines so operators can
	// grep for swaps per-role.
	role string
}

// NewFallbackPair builds a FallbackPair with production defaults:
// 5-minute health-check interval, wall clock, real Call() as the
// health ping. The first argument is the logical role name used in
// log lines.
func NewFallbackPair(role string, primary, secondary ModelRole) *FallbackPair {
	fp := &FallbackPair{
		primary:          primary,
		secondary:        secondary,
		healthCheckEvery: 5 * time.Minute,
		healthPingPrompt: "Reply with just: pong",
		now:              time.Now,
		role:             role,
	}
	// Initialize the atomic timestamps so the first maybeHealthCheck
	// has a sensible "last" value. We set lastHealthCheck to now —
	// the first check fires healthCheckEvery after construction.
	fp.lastSwap.Store(time.Time{})
	fp.lastHealthCheck.Store(fp.now())
	return fp
}

// StartBackgroundPinger spawns a goroutine that runs maybeHealthCheck
// at the configured interval regardless of caller traffic. Without
// this, a run that fell back to secondary and is completing jobs
// happily will never re-probe the primary — the restoration path
// only fires when Call() is invoked, and a long builder call may
// keep the loop pinned to secondary for 30+ min at a time.
//
// Safe to call multiple times; subsequent calls replace the previous
// ticker. Pass a cancelled context to stop.
func (fp *FallbackPair) StartBackgroundPinger(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(fp.healthCheckEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// maybeHealthCheck is guarded by its own interval
				// check, so a separate caller-driven invocation
				// + this background tick won't double-fire.
				fp.maybeHealthCheck(ctx)
			}
		}
	}()
}

// active returns the currently-active ModelRole. Cheap, lock-free.
func (fp *FallbackPair) active() ModelRole {
	if fp.currentPrimary.Load() == 0 {
		return fp.primary
	}
	return fp.secondary
}

// inactive returns the NON-active role — the one we health-check.
func (fp *FallbackPair) inactive() ModelRole {
	if fp.currentPrimary.Load() == 0 {
		return fp.secondary
	}
	return fp.primary
}

// OnSecondary reports whether we are currently running on the
// fallback (secondary) rather than the configured primary. Used by
// health checks to decide whether restoring primary is appropriate.
func (fp *FallbackPair) OnSecondary() bool {
	return fp.currentPrimary.Load() == 1
}

// swap flips active ↔ inactive under the mutex. Intended for the
// rate-limit path. Returns the role we just swapped TO.
func (fp *FallbackPair) swap() ModelRole {
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

// restorePrimary forces the pair back to the configured primary.
// Called by the health check when the primary is responsive again.
// Returns true if this call actually changed state (i.e. we were on
// secondary and are now back on primary).
func (fp *FallbackPair) restorePrimary() bool {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	if fp.currentPrimary.Load() == 0 {
		return false
	}
	fp.currentPrimary.Store(0)
	fp.lastSwap.Store(fp.now())
	return true
}

// isRateLimit classifies a Call outcome as "provider is rate-
// limited or hard-failed, swap immediately" vs "normal result /
// normal error". Signatures are intentionally generous — a false
// positive triggers one unnecessary swap (cheap), a false negative
// burns the whole loop on a dead provider (expensive).
//
// CC (claude):
//   - err != nil AND output length < 200 chars (short + errored),
//     OR output contains "claude error: exit status 1"
//
// Codex:
//   - err contains "no last agent message" (the codex CLI failure),
//   - err contains "wrote empty content",
//   - err != nil AND output is empty (codex had nothing to say),
//   - err contains "exit status 1" from codex path.
func (fp *FallbackPair) isRateLimit(role ModelRole, output string, err error) bool {
	if err == nil && output == "" {
		// Caller got nothing and no error — treat as rate-limit-like
		// signal so we try the other role. Avoids infinite silent
		// empty loops.
		return true
	}
	name := role.Name()
	low := strings.ToLower(output)
	if strings.Contains(low, "claude error: exit status 1") {
		return true
	}
	if err != nil {
		emsg := strings.ToLower(err.Error())
		if name == "codex" {
			if strings.Contains(emsg, "no last agent message") ||
				strings.Contains(emsg, "wrote empty content") {
				return true
			}
		}
		// Claude-specific rate-limit / empty-output signals. The
		// ccRole.Call adapter synthesizes "claude returned empty
		// output" when claudeCall bubbled up a rate-limit that
		// WaitIfPaused already consumed — match that explicitly
		// so the pair swaps to codex instead of waiting out another
		// 15-min exponential backoff. Without this branch the swap
		// never fires on a saturated Claude account.
		if name == "claude" {
			if strings.Contains(emsg, "returned empty output") ||
				strings.Contains(emsg, "rate-limit") ||
				strings.Contains(emsg, "rate limit") {
				return true
			}
		}
		if name == "claude" && len(output) < 200 {
			return true
		}
		if strings.Contains(emsg, "exit status 1") && len(output) < 200 {
			return true
		}
	}
	return false
}

// maybeHealthCheck runs a liveness ping against the INACTIVE role
// at most once per healthCheckEvery. When we are currently on the
// secondary and the primary responds cleanly, swap back to the
// primary; log the restoration. When both roles are currently
// healthy (i.e. we never swapped) this is still cheap — we just
// verify the secondary can answer so operators know the fallback
// is warm for when they need it.
//
// Errors from the health ping are NEVER fatal — a failed ping just
// means we stay on the current active role.
func (fp *FallbackPair) maybeHealthCheck(ctx context.Context) {
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

	// Fire the ping outside the mutex — Call may block on I/O.
	out, err := fp.pingRole(ctx, inactive)
	if err != nil || strings.TrimSpace(out) == "" {
		// Inactive still failing — leave state alone.
		return
	}
	if onSecondary {
		// Inactive == primary, and it answered cleanly. Restore.
		if fp.restorePrimary() {
			fmt.Fprintf(os.Stderr,
				"▶ %s primary %s restored (secondary %s still healthy)\n",
				fp.role, fp.primary.Name(), fp.secondary.Name())
		}
	}
	// If we're already on primary and the secondary is healthy, do
	// nothing — we only use secondary when primary rate-limits.
}

// pingRole sends the health-ping prompt to a role. If the test
// harness set healthPing, use that; otherwise call the real role
// with a 30-second timeout context so a wedged provider can't
// block the health check forever.
func (fp *FallbackPair) pingRole(parent context.Context, role ModelRole) (string, error) {
	if fp.healthPing != nil {
		return fp.healthPing(role)
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	// Health check must bypass the backoff short-circuits in
	// ccRole.Call / codexRole.Call — otherwise when a provider is
	// paused (which is exactly why health checks exist), the ping
	// always "fails" without actually probing, and the primary is
	// never restored. Both ccRole and codexRole are bypassed
	// symmetrically here.
	if _, ok := role.(ccRole); ok {
		out := claudeCall(globalClaudeBin, globalRepoRoot, fp.healthPingPrompt)
		if strings.TrimSpace(out) == "" {
			return "", fmt.Errorf("claude ping returned empty")
		}
		return out, nil
	}
	if _, ok := role.(codexRole); ok {
		out := codexCall(globalCodexBin, globalRepoRoot, fp.healthPingPrompt)
		if strings.TrimSpace(out) == "" {
			return "", fmt.Errorf("codex ping returned empty")
		}
		return out, nil
	}
	return role.Call(ctx, fp.healthPingPrompt)
}

// Call executes the prompt on the active role, swapping and
// retrying on a rate-limit signature. When BOTH roles are rate-
// limited simultaneously, keep pinging both on a 30-second cycle
// until one responds, then retry the prompt on the recovered role.
// Bounded by ctx timeout so a truly-dead provider pair eventually
// gives up instead of looping forever.
//
// Thread-safe: concurrent Call() invocations may race on the swap,
// but each invocation sees a consistent active role via the lock-
// free .active() read.
func (fp *FallbackPair) Call(ctx context.Context, prompt string) (string, error) {
	fp.maybeHealthCheck(ctx)

	active := fp.active()
	out, err := active.Call(ctx, prompt)
	if !fp.isRateLimit(active, out, err) {
		return out, err
	}
	// Swap and retry on the other role.
	other := fp.swap()
	fmt.Fprintf(os.Stderr,
		"⚠ %s rate-limit on %s → fallback to %s (role: %s)\n",
		fp.role, active.Name(), other.Name(), fp.role)

	out2, err2 := other.Call(ctx, prompt)
	if !fp.isRateLimit(other, out2, err2) {
		return out2, err2
	}

	// Both roles failed. Keep pinging both on a 30s cycle until one
	// recovers — then retry the prompt on the recovered role. Exits
	// on ctx cancellation / deadline.
	fmt.Fprintf(os.Stderr,
		"⚠ both %s roles rate-limited (primary=%s secondary=%s) — pinging until recovery\n",
		fp.role, fp.primary.Name(), fp.secondary.Name())
	recovered, rerr := fp.waitForRecovery(ctx)
	if rerr != nil {
		// Context cancelled or deadline; return the last call's result.
		return out2, rerr
	}
	fmt.Fprintf(os.Stderr,
		"▶ %s %s recovered, retrying the original prompt\n",
		fp.role, recovered.Name())
	return recovered.Call(ctx, prompt)
}

// waitForRecovery pings both roles on a 30-second cycle until one
// responds to a lightweight health ping. Returns the first recovered
// role, or (nil, ctx.Err()) when the context is cancelled / deadlined.
//
// Alternates primary/secondary ping order per tick so neither role
// gets preferentially probed. The same pingRole helper is used, so
// cc/codex short-circuits are bypassed — the ping is the liveness
// probe, not a backoff-gated call.
func (fp *FallbackPair) waitForRecovery(ctx context.Context) (ModelRole, error) {
	const pingEvery = 30 * time.Second
	ticker := time.NewTicker(pingEvery)
	defer ticker.Stop()
	// Probe immediately once so callers don't wait for the first tick.
	if role, ok := fp.probeForRecovery(ctx); ok {
		return role, nil
	}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			if role, ok := fp.probeForRecovery(ctx); ok {
				return role, nil
			}
		}
	}
}

// probeForRecovery tries both primary and secondary via pingRole.
// Returns the first role that answers with a non-empty body; if both
// fail, returns (nil, false). Picks primary first, since restoring
// to primary is the preferred steady state.
func (fp *FallbackPair) probeForRecovery(ctx context.Context) (ModelRole, bool) {
	if out, err := fp.pingRole(ctx, fp.primary); err == nil && strings.TrimSpace(out) != "" {
		// Primary recovered — restore it as active if we were on
		// secondary, so subsequent calls don't need another swap.
		if fp.currentPrimary.Load() == 1 {
			fp.restorePrimary()
		}
		return fp.primary, true
	}
	if out, err := fp.pingRole(ctx, fp.secondary); err == nil && strings.TrimSpace(out) != "" {
		// Secondary came back first — leave it as active; the
		// background pinger will restore primary later.
		if fp.currentPrimary.Load() == 0 {
			// We were on primary but primary's failing. Swap so the
			// caller's retry lands on the working role.
			fp.swap()
		}
		return fp.secondary, true
	}
	return nil, false
}

// ActiveName returns the name of the currently-active role. For
// logging and tests.
func (fp *FallbackPair) ActiveName() string {
	return fp.active().Name()
}

// ccRole wraps the package-level claudeCall into the ModelRole
// interface. Used as the primary role in writerPair and the
// secondary role in reviewerPair.
type ccRole struct{}

func (ccRole) Name() string { return "claude" }

// Call invokes claudeCall via the package-level globalClaudeBin.
// The existing claudeCall signature is (bin, dir, prompt) string,
// with no error return — it logs errors to stderr and returns "".
// We adapt to ModelRole by synthesizing an error when the output
// is empty (so FallbackPair.isRateLimit can swap).
//
// The ctx is NOT threaded through claudeCall today; that function
// uses its own internal 40-min timeout. If the caller cancels ctx
// we check it post-hoc so a cancelled loop doesn't wait for a full
// claudeCall to return.
func (ccRole) Call(ctx context.Context, prompt string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// If the rate-limit detector has already entered Active pause,
	// fail FAST so FallbackPair swaps to the secondary instead of
	// blocking for a 15-minute backoff. Without this short-circuit
	// writerPair sees claudeCall as "slow but working" and never
	// gets a chance to try codex. This is the observed bug from
	// the live cohort: 3 Claude rate-limit events → 15m pause →
	// writer loop wastes the pause window instead of using codex.
	if claudeBackoff.State() == claudeBackoffActive {
		return "", fmt.Errorf("claude rate-limit pause active (H-10)")
	}
	dir := globalRepoRoot
	if dir == "" {
		dir = "."
	}
	out := claudeCall(globalClaudeBin, dir, prompt)
	if strings.TrimSpace(out) == "" {
		return out, fmt.Errorf("claude returned empty output")
	}
	return out, nil
}

// codexRole wraps the package-level codexCall into ModelRole.
// Used as the primary role in reviewerPair and the secondary role
// in writerPair. Note: codex is read-only by default so it cannot
// safely take over the writer role in every case — but for
// plan/review/audit prompts (the bulk of simple-loop's claude
// usage) the writer's output is prose, which codex CAN produce.
// The orchestrator must only call writerPair on prose-producing
// prompts; for builder prompts where CC needs tools, the pair
// still swaps but codex's "review" mode output is surfaced as
// prose, which the outer loop treats as "CC stalled, keep going
// next round" — better than a permanent rate-limit lockup.
type codexRole struct{}

func (codexRole) Name() string { return "codex" }

func (codexRole) Call(ctx context.Context, prompt string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// Symmetric short-circuit with ccRole: if codex's own backoff
	// (H-7) has piled up multiple-x delays, fail fast so the pair
	// swaps to the Claude secondary rather than waiting out the
	// backoff on every call. Both directions now cover: codex freezes
	// → use claude; claude freezes → use codex. When each recovers
	// the background pinger restores it as primary.
	if codexBackoff.Multiplier() >= 4 {
		return "", fmt.Errorf("codex rate-limit backoff active (%dx, H-7)",
			codexBackoff.Multiplier())
	}
	dir := globalRepoRoot
	if dir == "" {
		dir = "."
	}
	out := codexCall(globalCodexBin, dir, prompt)
	if strings.TrimSpace(out) == "" {
		return out, fmt.Errorf("codex returned empty output")
	}
	return out, nil
}

// Package-level pairs. writerPair routes claude-worker prompts
// through the claude-primary / codex-secondary fallback. The
// reviewerPair routes codex-reviewer prompts through the codex-
// primary / claude-sonnet-secondary fallback.
//
// These are initialized lazily from simpleLoopCmd once the user's
// --claude-bin / --codex-bin / --claude-model flags are parsed.
// Nil-safe use sites fall back to calling claudeCall/codexCall
// directly so tests that don't initialize these still work.
var (
	writerPair    *FallbackPair
	reviewerPair  *FallbackPair
	globalRepoRoot string // set by simpleLoopCmd so {cc,codex}Role can find the worktree
)

// initFallbackPairs wires up the package-level writer and reviewer
// pairs. Called from simpleLoopCmd after the binaries and repo
// root have been resolved. Idempotent.
func initFallbackPairs(repoRoot string) {
	globalRepoRoot = repoRoot
	// Background pingers use a package-level cancel so a second
	// initFallbackPairs call (e.g. tests) doesn't spawn duplicates.
	if fallbackPingerCancel != nil {
		fallbackPingerCancel()
	}
	pingerCtx, cancel := context.WithCancel(context.Background())
	fallbackPingerCancel = cancel

	if writerPair == nil {
		writerPair = NewFallbackPair("writer", ccRole{}, codexRole{})
	}
	writerPair.StartBackgroundPinger(pingerCtx)

	if reviewerPair == nil {
		reviewerPair = NewFallbackPair("reviewer", codexRole{}, ccRole{})
	}
	reviewerPair.StartBackgroundPinger(pingerCtx)
}

// fallbackPingerCancel cancels the pinger goroutines from a previous
// initFallbackPairs call so re-init doesn't leak duplicates.
var fallbackPingerCancel context.CancelFunc

// writerCall routes a writer prompt through the writerPair when it
// is wired, falling back to direct claudeCall for backward compat
// in tests / non-simple-loop code paths. Returns only the output
// text — the error is logged and dropped to match claudeCall's
// existing (string) return signature.
func writerCall(dir, prompt string) string {
	if writerPair == nil {
		return claudeCall(globalClaudeBin, dir, prompt)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	// Pin dir for this call so the wrapped roles use the right cwd.
	prev := globalRepoRoot
	globalRepoRoot = dir
	defer func() { globalRepoRoot = prev }()
	out, err := writerPair.Call(ctx, prompt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  writer fallback pair error: %v\n", err)
	}
	return out
}

// reviewerCallViaPair routes a reviewer prompt through reviewerPair
// when wired. Used by reviewCall's codex branch so codex→CC
// fallback is transparent to the existing reviewer backend switch.
func reviewerCallViaPair(dir, prompt string) string {
	if reviewerPair == nil {
		// Direct codex — preserves existing behavior for tests.
		return codexCall(globalCodexBin, dir, prompt)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	prev := globalRepoRoot
	globalRepoRoot = dir
	defer func() { globalRepoRoot = prev }()
	out, err := reviewerPair.Call(ctx, prompt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  reviewer fallback pair error: %v\n", err)
	}
	return out
}
