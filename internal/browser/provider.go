// provider.go: Provider / Session interfaces — the new canonical
// executor-facing contract for the browser package, introduced by
// spec C6 (specs/browser-remote-sandbox.md §3.3).
//
// The Backend interface from spec 17 (browser-interactive.md) stays
// in place for callers that already speak it; Provider is the
// forward-looking shape that ALL backends — local rod, Browserless
// CDP-over-WS, in-house Cloud Run — satisfy identically. Selection
// happens at construction time from `browser.provider` config:
//
//   local       → wraps *Client / *RodClient                (provider_local.go)
//   browserless → CDP-over-WS to Browserless cloud or self-host (browserless/)
//   inhouse     → CDP-over-WS to r1-browser Cloud Run service (inhouse/)
//
// Each Provider.Open() returns a Session that represents one
// incognito-isolated browser context — single tenant, single task.
// Multiple Open calls allocate independent contexts so two tasks
// never share client-side state. Close on the Provider is
// idempotent and final; existing Sessions become invalid afterwards.

package browser

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Provider is the executor-facing browser contract. Local rod,
// Browserless, and the in-house Cloud Run service all satisfy it.
// Selection happens at construction time from config; see
// PickProvider in cmd/r1/browse_cmd.go for the dispatch.
type Provider interface {
	// Open allocates a new incognito Session. The returned Session
	// is single-tenant — per-session client-state is invisible to
	// every other Session from the same Provider.
	Open(ctx context.Context, opts SessionOpts) (Session, error)

	// Close shuts the provider; existing Sessions become invalid.
	// Idempotent — repeated calls return nil after the first.
	Close(ctx context.Context) error

	// Name returns "local" / "browserless" / "inhouse" so callers,
	// the event log, and the cost tracker can attribute by provider.
	Name() string
}

// Session is one browser context — incognito-isolated, single-tenant,
// single-task. Created by Provider.Open. Caller MUST Close to release
// resources; the session-bound wrapper (T9) does this automatically
// when the R1 session ends. Per-call client-state (cookies and
// browser storage) NEVER crosses sessions — that property is the
// load-bearing security claim of the Provider abstraction.
type Session interface {
	// Navigate loads the given URL. Returns the post-redirect URL and
	// the top-level HTTP status. Non-2xx is reported in the result,
	// not as a Go error.
	Navigate(ctx context.Context, url string) (NavigateResult, error)

	// WaitFor blocks until the CSS selector resolves in the DOM, or
	// timeout elapses. Returns a deadline error on timeout.
	WaitFor(ctx context.Context, selector string, timeout time.Duration) error

	// Screenshot captures the current viewport as PNG bytes.
	Screenshot(ctx context.Context) ([]byte, error)

	// Eval runs the given JS expression. Returns the value as the JS
	// primitive type — number / bool / string / nil / map[string]any.
	// Callers cast. See spec §3.3 note 1 for the rationale.
	Eval(ctx context.Context, script string) (any, error)

	// Close releases the session. Idempotent.
	Close(ctx context.Context) error

	// ID returns the opaque session identifier (CDP targetId for the
	// remote providers; a counter for local). Logged with every event.
	ID() string
}

// SessionOpts configures a single Session at Open time.
type SessionOpts struct {
	// TenantID is required when Provider != local. Used for credential
	// resolution (per-tenant Browserless tokens) and cost attribution.
	TenantID string

	// MissionID is the cost-budgeting unit. Per-mission budgets
	// (spec T12a) are enforced by the cost tracker.
	MissionID string

	// UserAgent overrides the default UA string. Empty → provider default.
	UserAgent string

	// Viewport sets the browser viewport. Zero value → 1280x720.
	Viewport Viewport

	// NetworkPolicy is the egress allowlist/denylist enforced at the
	// provider boundary. Resolved by the caller from operator config.
	NetworkPolicy NetworkPolicy

	// IdleTimeout closes the session after no activity. 0 → 5m default.
	IdleTimeout time.Duration

	// HardTimeout is the absolute lifetime cap. 0 → 30m default.
	HardTimeout time.Duration
}

// Viewport is the browser viewport in pixels.
type Viewport struct {
	Width  int
	Height int
}

// DefaultViewport returns 1280x720 — the conventional headless default.
func DefaultViewport() Viewport { return Viewport{Width: 1280, Height: 720} }

// NavigateResult captures the load-bearing fields from a top-level
// navigation. Callers that need the rendered text body use the
// existing FetchResult path; this is the CDP-aligned shape.
type NavigateResult struct {
	FinalURL string // post-redirect URL
	Status   int    // HTTP status of the top-level navigation
	Title    string // populated when the provider exposes it cheaply
}

// PolicyMode selects the default disposition for the network policy.
type PolicyMode int

// PolicyMode values: DenyByDefault drops requests that don't match an
// allow rule; AllowByDefault permits everything not on the deny list.
const (
	PolicyDenyByDefault PolicyMode = iota
	PolicyAllowByDefault
)

// String returns the operator-facing rendering of the mode.
func (m PolicyMode) String() string {
	switch m {
	case PolicyDenyByDefault:
		return "deny_by_default"
	case PolicyAllowByDefault:
		return "allow_by_default"
	default:
		return "unknown"
	}
}

// ParsePolicyMode decodes the YAML/config string form into a PolicyMode.
// Empty string → PolicyDenyByDefault (the sec-arch default).
func ParsePolicyMode(s string) (PolicyMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "deny_by_default":
		return PolicyDenyByDefault, nil
	case "allow_by_default":
		return PolicyAllowByDefault, nil
	default:
		return PolicyDenyByDefault, errors.New("browser: invalid policy mode (want deny_by_default or allow_by_default): " + s)
	}
}

// NetworkPolicy is the deny-by-default egress allowlist enforced at
// the provider boundary. Glob matching is via the per-provider
// implementations (see browserless/network_policy.go, inhouse/client.go).
// Deny always takes precedence over Allow regardless of Mode.
type NetworkPolicy struct {
	Mode  PolicyMode
	Allow []string // hostname globs: "docs.anthropic.com", "*.relayone.com"
	Deny  []string // hostname globs: "169.254.169.254", "metadata.*"
}

// Validate enforces the operator-footgun guard from spec T5: a policy
// in DenyByDefault mode with an empty allow list cannot navigate
// anywhere, which is almost certainly a configuration bug.
func (p NetworkPolicy) Validate() error {
	if p.Mode == PolicyDenyByDefault && len(p.Allow) == 0 {
		return errors.New("browser: network policy is deny_by_default but has no allow entries; " +
			"this configuration cannot navigate to any host. Set browser.network_policy.allow " +
			"or switch to allow_by_default.")
	}
	return nil
}

// Decide returns whether to permit a request to the given host under
// this policy. Match priority: Deny > Allow > Mode default.
// Glob rules: leading "*." matches any subdomain at one or more levels;
// "*" alone matches anything; otherwise exact host match.
func (p NetworkPolicy) Decide(host string) PolicyDecision {
	host = strings.ToLower(strings.TrimSpace(host))
	for _, d := range p.Deny {
		if matchHostGlob(d, host) {
			return PolicyDecision{Allowed: false, Rule: d, Reason: "deny"}
		}
	}
	for _, a := range p.Allow {
		if matchHostGlob(a, host) {
			return PolicyDecision{Allowed: true, Rule: a, Reason: "allow"}
		}
	}
	switch p.Mode {
	case PolicyAllowByDefault:
		return PolicyDecision{Allowed: true, Rule: "*", Reason: "default-allow"}
	default:
		return PolicyDecision{Allowed: false, Rule: "*", Reason: "default-deny"}
	}
}

// PolicyDecision is the outcome of NetworkPolicy.Decide.
type PolicyDecision struct {
	Allowed bool
	Rule    string // the matching glob, or "*" for the default branch
	Reason  string // "allow" | "deny" | "default-allow" | "default-deny"
}

// matchHostGlob implements the small subset of glob semantics the
// policy needs: bare "*", leading "*." subdomain wildcard, exact host.
// Anything else is treated as exact-match for predictability.
func matchHostGlob(pattern, host string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if pattern == "" || host == "" {
		return false
	}
	if pattern == "*" {
		return true
	}
	if strings.HasPrefix(pattern, "*.") {
		suffix := pattern[1:] // ".example.com"
		// Match either ".example.com" suffix OR exact "example.com".
		bare := pattern[2:]
		return host == bare || strings.HasSuffix(host, suffix)
	}
	return host == pattern
}

// ProviderName values for the Name() return — kept as constants so
// the cost tracker, event log, and tests share one source of truth.
const (
	ProviderLocal       = "local"
	ProviderBrowserless = "browserless"
	ProviderInhouse     = "inhouse"
)

// Default timeouts used by the session-bound wrapper (T9) when
// SessionOpts left them zero.
const (
	DefaultIdleTimeout = 5 * time.Minute
	DefaultHardTimeout = 30 * time.Minute
)

// resolveTimeouts fills in defaults for IdleTimeout / HardTimeout.
func resolveTimeouts(opts SessionOpts) SessionOpts {
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = DefaultIdleTimeout
	}
	if opts.HardTimeout <= 0 {
		opts.HardTimeout = DefaultHardTimeout
	}
	if opts.Viewport.Width == 0 || opts.Viewport.Height == 0 {
		opts.Viewport = DefaultViewport()
	}
	return opts
}

// ErrProviderUnreachable is returned when the WS dial / HTTP probe
// for a remote provider fails for transient reasons (DNS, refused,
// timeout). Caller may retry; the fallback wrapper (T8) routes around it.
var ErrProviderUnreachable = errors.New("browser: provider unreachable")

// ErrAuthFailed is the permanent-failure form: token rejected. Fallback
// MUST NOT retry on this — the token is wrong or revoked.
var ErrAuthFailed = errors.New("browser: provider auth failed")

// ErrNoToken signals that the operator config did not supply a token
// for a provider that requires one (Browserless without a token).
var ErrNoToken = errors.New("browser: no token configured for provider")

// ErrNoTenantToken signals that the operator config configured a
// `tenant_tokens` map but the requested TenantID is not present and
// no `default_token` fallback is set.
var ErrNoTenantToken = errors.New("browser: no token configured for tenant")

// ErrBudgetExhausted fires from Provider.Open when the mission's
// cumulative browser cost has crossed the per-mission unit cap.
// The planner skips browser-requiring steps when it sees this.
var ErrBudgetExhausted = errors.New("browser: per-mission budget exhausted")

// ErrSessionClosed fires from Session methods called after Close.
var ErrSessionClosed = errors.New("browser: session is closed")

// ErrProviderClosed fires from Open / Close on a provider whose
// Close already ran.
var ErrProviderClosed = errors.New("browser: provider is closed")

// NetworkDeniedError fires when a navigation hits a deny-by-policy
// destination. Carries the failing host + the matching rule so
// operators can identify which line of the allowlist needs fixing.
type NetworkDeniedError struct {
	Host string
	Rule string
}

func (e *NetworkDeniedError) Error() string {
	if e.Rule != "" {
		return "browser: network denied: host=" + e.Host + " rule=" + e.Rule
	}
	return "browser: network denied: host=" + e.Host
}

// IsNetworkDenied reports whether err is a *NetworkDeniedError.
func IsNetworkDenied(err error) bool {
	var nd *NetworkDeniedError
	return err != nil && errors.As(err, &nd)
}
