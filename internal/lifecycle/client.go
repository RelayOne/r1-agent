// Package lifecycle ships R1's retention + lifecycle email integration
// with Customer.io. It is a sibling of the PostHog analytics package
// (internal/analytics/) and follows the same DSN-aware client shape used
// by internal/coderadar/coderadar.go: FromEnv constructor, no-op when
// CUSTOMERIO_SITE_ID / CUSTOMERIO_API_KEY are empty, an Enabled()
// predicate, and panic-safe wrappers that never propagate SDK failures
// to the caller.
//
// Methods on a no-op client are safe and return nil. Callers never need
// to branch on "is lifecycle on". This matches the shape of
// internal/coderadar/coderadar.go and lets the bus subscriber treat
// the client as fire-and-forget.
//
// Scope is triggers and identity sync only. R1 does not author email
// copy in code, does not template, does not deliver. Customer.io owns
// delivery; R1 owns the event vocabulary and the first-time debounce
// that makes the milestone events meaningful.
//
// See specs/customerio-lifecycle.md for the full design.
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	customerio "github.com/customerio/go-customerio/v3"
)

// envSiteID and envAPIKey are the two required environment variables
// the FromEnv constructor reads. Both must be non-empty for the client
// to be Enabled; otherwise FromEnv returns the no-op shape that records
// nothing and never opens a network connection.
const (
	envSiteID = "CUSTOMERIO_SITE_ID"
	envAPIKey = "CUSTOMERIO_API_KEY" // #nosec G101 -- env var name, not a credential.
	envRegion = "CUSTOMERIO_REGION"
)

// Client is the interface every caller in the codebase uses to talk to
// Customer.io. Two implementations are provided in this file:
//
//   - httpClient wraps the upstream go-customerio SDK and issues real
//     PUT / POST / DELETE requests against the Customer.io Track API.
//   - noopClient is the zero-cost stub returned when credentials are
//     not configured. Every method returns nil; Enabled() returns false.
//
// All methods are safe to call on a nil interface value (no-op
// receivers do that work). A misconfigured runtime never panics here.
type Client interface {
	// Enabled reports whether the client will forward events to the
	// Customer.io Track API. A false return means the client is in
	// no-op mode and every method below is a silent return.
	Enabled() bool

	// Identify upserts the customer profile at /customers/{id}. The
	// traits map MUST be produced by traits.Build — see traits.go for
	// the PII allowlist enforcement.
	Identify(ctx context.Context, userID, email string, traits map[string]any) error

	// Track records an event at /customers/{id}/events. The data map
	// may carry mission_id / session_id / task_id / cost_usd — never
	// raw content (see §6.6 of the spec).
	Track(ctx context.Context, userID, event string, data map[string]any) error

	// MergeIdentities POSTs /merge_customers, used when an anonymous
	// CLI run is later linked to a logged-in SSO user. The spec maps
	// this onto the A4 SSO post-login hand-off.
	MergeIdentities(ctx context.Context, oldID, newID string) error

	// Delete issues DELETE /customers/{id}, the Customer.io
	// suppress-and-delete primitive used by the DSAR flow in §9.
	Delete(ctx context.Context, userID string) error
}

// Config carries every knob the lifecycle client honors. Most callers
// should use FromEnv; Config is exported for tests and for the
// httptest-driven integration suite that needs to point the client at
// a local server.
type Config struct {
	// SiteID is the Customer.io workspace Site-ID (basic-auth user).
	SiteID string

	// APIKey is the Customer.io Track API key (basic-auth password).
	APIKey string

	// Region selects the Track API endpoint. Recognized values are
	// "us" (default) and "eu". Unknown values log a warning and fall
	// back to "us" — see region.go.
	Region string

	// BaseURL, when non-empty, overrides the region-derived URL. Used
	// by tests to point the client at a httptest.NewServer.
	BaseURL string

	// ServiceName labels outbound calls in Customer.io's User-Agent.
	// Mirrors internal/coderadar/coderadar.go's serviceName.
	ServiceName string

	// Environment is the deploy environment label (development /
	// staging / production). Read from R1_ENV / CUSTOMERIO_ENV /
	// NODE_ENV in that order.
	Environment string

	// Disabled, when true, forces the no-op client regardless of
	// credentials. Used by the policy-overlay path.
	Disabled bool

	// Timeout caps every Customer.io HTTP roundtrip. Default 5s
	// (spec §6.4). Used as the per-call context timeout in the bus
	// subscriber and the DSAR CLI.
	Timeout time.Duration
}

// DefaultTimeout is the per-call ceiling on Customer.io SDK roundtrips.
// 5 seconds matches the spec §6.4 ceiling and the production SDK
// default; longer than 5s and the worker goroutine ought to drop the
// event rather than back-pressure the bus.
const DefaultTimeout = 5 * time.Second

// FromEnv reads CUSTOMERIO_SITE_ID + CUSTOMERIO_API_KEY + the optional
// CUSTOMERIO_REGION and returns the matching client. Empty credentials
// (either of the two required env vars missing) return the no-op
// client whose Enabled() reports false.
func FromEnv(serviceName string) Client {
	return New(Config{
		SiteID:      strings.TrimSpace(os.Getenv(envSiteID)),
		APIKey:      strings.TrimSpace(os.Getenv(envAPIKey)),
		Region:      strings.TrimSpace(os.Getenv(envRegion)),
		ServiceName: serviceName,
		Environment: detectEnvironment(),
		Timeout:     DefaultTimeout,
	})
}

// New constructs a Client from an explicit Config. Empty SiteID OR
// empty APIKey OR Disabled=true returns the no-op client. The
// fallback path never opens a network connection or allocates a
// background goroutine — it is genuinely free.
func New(cfg Config) Client {
	if cfg.Disabled || cfg.SiteID == "" || cfg.APIKey == "" {
		return noopClient{}
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	region := normalizeRegion(cfg.Region)
	baseURL := strings.TrimSpace(cfg.BaseURL)
	if baseURL == "" {
		baseURL = RegionURL(region)
	} else {
		baseURL = strings.TrimRight(baseURL, "/")
	}
	sdk := customerio.NewTrackClient(cfg.SiteID, cfg.APIKey)
	// The SDK exposes URL as a writable field; override after
	// construction so tests can route through httptest.NewServer and
	// EU customers route to track-eu.customer.io.
	sdk.URL = baseURL
	if cfg.ServiceName != "" {
		sdk.UserAgent = fmt.Sprintf("r1-lifecycle/%s (%s)", cfg.ServiceName, cfg.Environment)
	}
	return &httpClient{
		sdk:         sdk,
		timeout:     cfg.Timeout,
		environment: cfg.Environment,
		serviceName: cfg.ServiceName,
		region:      region,
		baseURL:     baseURL,
	}
}

// httpClient is the production Client. It wraps the upstream
// github.com/customerio/go-customerio/v3 SDK and adds (a) a per-call
// context timeout, (b) a once-per-second dropped-credential log, and
// (c) a panic recovery so a regression in the SDK never takes down
// the lifecycle subscriber's drain goroutine.
type httpClient struct {
	sdk         *customerio.CustomerIO
	timeout     time.Duration
	environment string
	serviceName string
	region      string
	baseURL     string

	mu sync.Mutex
}

func (c *httpClient) Enabled() bool { return c != nil && c.sdk != nil }

func (c *httpClient) Identify(ctx context.Context, userID, email string, traits map[string]any) error {
	if !c.Enabled() {
		return nil
	}
	if userID == "" {
		return errMissingUserID
	}
	merged := make(map[string]any, len(traits)+1)
	for k, v := range traits {
		merged[k] = v
	}
	if email != "" {
		merged["email"] = email
	}
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
	return c.sdk.IdentifyCtx(ctx, userID, merged)
}

func (c *httpClient) Track(ctx context.Context, userID, event string, data map[string]any) error {
	if !c.Enabled() {
		return nil
	}
	if userID == "" {
		return errMissingUserID
	}
	if event == "" {
		return errMissingEvent
	}
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
	return c.sdk.TrackCtx(ctx, userID, event, data)
}

func (c *httpClient) MergeIdentities(ctx context.Context, oldID, newID string) error {
	if !c.Enabled() {
		return nil
	}
	if oldID == "" || newID == "" {
		return errMissingUserID
	}
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
	return c.sdk.MergeCustomersCtx(ctx,
		customerio.Identifier{Type: customerio.IdentifierTypeID, Value: newID},
		customerio.Identifier{Type: customerio.IdentifierTypeID, Value: oldID},
	)
}

func (c *httpClient) Delete(ctx context.Context, userID string) error {
	if !c.Enabled() {
		return nil
	}
	if userID == "" {
		return errMissingUserID
	}
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
	return c.sdk.DeleteCtx(ctx, userID)
}

// withTimeout produces a derived context bounded by c.timeout. If ctx
// already has a shorter deadline, the existing deadline wins.
func (c *httpClient) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, c.timeout)
}

// noopClient is the credential-less stub. It records nothing, makes no
// network calls, and reports Enabled()=false. Every method is safe on
// a zero-value receiver — callers never need to branch on "is lifecycle
// configured?".
type noopClient struct{}

func (noopClient) Enabled() bool                                                    { return false }
func (noopClient) Identify(context.Context, string, string, map[string]any) error   { return nil }
func (noopClient) Track(context.Context, string, string, map[string]any) error      { return nil }
func (noopClient) MergeIdentities(context.Context, string, string) error            { return nil }
func (noopClient) Delete(context.Context, string) error                             { return nil }

// detectEnvironment mirrors the helper in internal/coderadar/coderadar.go,
// preferring R1-specific env vars over the generic NODE_ENV.
func detectEnvironment() string {
	for _, key := range []string{"CUSTOMERIO_ENV", "R1_ENV", "NODE_ENV"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return "development"
}

// errMissingUserID and errMissingEvent are surfaced when the bus
// subscriber attempts to enqueue an event with an empty subject — a
// defensive check; the subscriber's top-of-handler anonymous-user
// filter normally drops those events before reaching this layer.
var (
	errMissingUserID = errors.New("lifecycle: missing user_id")
	errMissingEvent  = errors.New("lifecycle: missing event name")
)

// ParseEndpoint validates that the configured base URL is a valid
// https:// scheme with a non-empty host. Exported so the DSAR CLI can
// fail fast on operator typos.
func ParseEndpoint(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errors.New("lifecycle: empty endpoint")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("lifecycle: parse endpoint %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("lifecycle: endpoint %q must be http(s)", raw)
	}
	if u.Host == "" {
		return fmt.Errorf("lifecycle: endpoint %q has empty host", raw)
	}
	return nil
}
