package analytics

import (
	"context"
	"errors"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	posthog "github.com/posthog/posthog-go"
)

// Client is a DSN-aware thin wrapper around the official posthog-go SDK
// that mirrors internal/coderadar/coderadar.go in shape: FromEnv()
// constructor, no-op when POSTHOG_API_KEY is empty or analytics is
// policy-disabled, an Enabled() predicate, panic-safe capture wrappers
// that never propagate SDK failures to the caller.
//
// Methods on a nil *Client OR a *Client with no sdk are safe no-ops; the
// caller never needs to branch on "is analytics on". This is the same
// shape internal/coderadar/coderadar.go offers and lets the bus
// subscriber treat the client as a fire-and-forget destination.
type Client struct {
	sdk         posthog.Client
	environment string
	serviceName string

	// optedOut is the tenant opt-out set (specs/posthog-analytics.md §11).
	// Reads are lock-free via sync.Map; writes happen once at boot from
	// the resolved r1.policy.yaml.
	optedOut sync.Map

	// redaction is the optional redaction checker. nil = no ledger
	// integration, allowlist-only redaction. See redact.go.
	redaction RedactionChecker
}

// Config carries all knobs available to the analytics client. Most
// callers should use FromEnv; Config is exported for tests and for the
// httptest-driven integration suite.
type Config struct {
	APIKey         string
	Host           string
	Environment    string
	ServiceName    string
	Disabled       bool
	FlushAt        int
	FlushInterval  time.Duration
	ShutdownWait   time.Duration
	TenantOptOuts  []string
	Redaction      RedactionChecker
}

// FromEnv reads POSTHOG_API_KEY (plus POSTHOG_HOST, R1_ENV, and the
// analytics knobs documented in specs/posthog-analytics.md §5) and
// constructs the corresponding client. An empty key OR
// ANALYTICS_DISABLED=1 returns a no-op client whose Enabled() reports
// false.
func FromEnv(serviceName string) *Client {
	return New(Config{
		APIKey:        strings.TrimSpace(os.Getenv("POSTHOG_API_KEY")),
		Host:          strings.TrimSpace(os.Getenv("POSTHOG_HOST")),
		Environment:   detectEnvironment(),
		ServiceName:   serviceName,
		Disabled:      envBool("ANALYTICS_DISABLED"),
		FlushAt:       envInt("POSTHOG_FLUSH_AT", 100),
		FlushInterval: envDuration("POSTHOG_FLUSH_INTERVAL", 5*time.Second),
		ShutdownWait:  envDuration("POSTHOG_SHUTDOWN_WAIT", 2*time.Second),
	})
}

// New constructs a Client from an explicit Config. Empty APIKey OR
// Disabled=true returns a no-op client.
func New(cfg Config) *Client {
	if cfg.Disabled || cfg.APIKey == "" {
		c := &Client{environment: cfg.Environment, serviceName: cfg.ServiceName, redaction: cfg.Redaction}
		for _, t := range cfg.TenantOptOuts {
			if t != "" {
				c.optedOut.Store(t, struct{}{})
			}
		}
		return c
	}
	pcfg := posthog.Config{
		Endpoint:        defaultedHost(cfg.Host),
		Interval:        cfg.FlushInterval,
		BatchSize:       cfg.FlushAt,
		ShutdownTimeout: cfg.ShutdownWait,
	}
	sdk, err := posthog.NewWithConfig(cfg.APIKey, pcfg)
	if err != nil {
		log.Printf("analytics: NewWithConfig failed (degrading to no-op): %v", err)
		c := &Client{environment: cfg.Environment, serviceName: cfg.ServiceName, redaction: cfg.Redaction}
		for _, t := range cfg.TenantOptOuts {
			if t != "" {
				c.optedOut.Store(t, struct{}{})
			}
		}
		return c
	}
	c := &Client{sdk: sdk, environment: cfg.Environment, serviceName: cfg.ServiceName, redaction: cfg.Redaction}
	for _, t := range cfg.TenantOptOuts {
		if t != "" {
			c.optedOut.Store(t, struct{}{})
		}
	}
	return c
}

// Enabled reports whether the client will forward events.
func (c *Client) Enabled() bool {
	return c != nil && c.sdk != nil
}

// Environment returns the resolved environment label (development,
// staging, production, ...) — populated from R1_ENV / POSTHOG_ENV.
func (c *Client) Environment() string {
	if c == nil {
		return ""
	}
	return c.environment
}

// ServiceName returns the service name passed at construction.
func (c *Client) ServiceName() string {
	if c == nil {
		return ""
	}
	return c.serviceName
}

// IsTenantOptedOut reports whether a tenant has opted out via the
// resolved policy. Safe to call on a nil receiver.
func (c *Client) IsTenantOptedOut(tenantID string) bool {
	if c == nil || tenantID == "" {
		return false
	}
	_, ok := c.optedOut.Load(tenantID)
	return ok
}

// Capture queues a product event. Required fields:
//   - distinctID identifies the user. Pass the SSO user UUID once
//     available; otherwise pass "anon_<session_id>".
//   - event is the snake_case taxonomy name (see taxonomy.go).
//   - props is the allowlisted property map. Nil is OK.
//
// The function pulls a TenantID off ctx via TenantIDFromContext and
// attaches it both as a top-level tenant_id property AND as a
// PostHog Groups{"tenant": ...} binding. Empty TenantID skips the
// group binding entirely (matches the no-empty-header convention in
// internal/correlation/correlation.go).
//
// Capture never returns an error; SDK enqueue failures are logged at
// most once per second and silently dropped — analytics must never
// block or fail R1's hot path.
func (c *Client) Capture(ctx context.Context, distinctID, event string, props map[string]any) {
	if !c.Enabled() {
		return
	}
	if event == "" || distinctID == "" {
		return
	}
	tenantID := TenantIDFromContext(ctx)
	if tenantID != "" && c.IsTenantOptedOut(tenantID) {
		return
	}
	merged := mergeProps(props)
	if tenantID != "" {
		merged["tenant_id"] = tenantID
	}
	merged["environment"] = c.environment
	merged["service"] = c.serviceName
	merged = c.applyRedaction(merged)

	msg := posthog.Capture{
		DistinctId: distinctID,
		Event:      event,
		Properties: merged,
		Timestamp:  time.Now().UTC(),
	}
	if tenantID != "" {
		msg.Groups = posthog.Groups{"tenant": tenantID}
	}
	if err := c.sdk.Enqueue(msg); err != nil {
		logOnce("analytics: enqueue Capture failed: %v", err)
	}
}

// Identify binds a distinct_id to a set of person-level properties. The
// caller MUST NOT pass PII (email, name, raw prompts); the property
// allowlist enforces this in production via redact.go.
func (c *Client) Identify(ctx context.Context, distinctID string, props map[string]any) {
	if !c.Enabled() || distinctID == "" {
		return
	}
	merged := mergeProps(props)
	merged["environment"] = c.environment
	merged = c.applyRedaction(merged)
	if err := c.sdk.Enqueue(posthog.Identify{
		DistinctId: distinctID,
		Properties: merged,
		Timestamp:  time.Now().UTC(),
	}); err != nil {
		logOnce("analytics: enqueue Identify failed: %v", err)
	}
}

// Group binds group-level properties to a (groupType, groupKey) pair.
// Called once per session on sso_login_succeeded to populate the tenant
// group dimensions (plan_tier, signup_date, region) that PostHog Group
// Analytics dashboards slice on.
func (c *Client) Group(ctx context.Context, groupType, groupKey string, props map[string]any) {
	if !c.Enabled() || groupType == "" || groupKey == "" {
		return
	}
	if c.IsTenantOptedOut(groupKey) {
		return
	}
	merged := mergeProps(props)
	merged["environment"] = c.environment
	merged = c.applyRedaction(merged)
	// posthog.GroupIdentify requires DistinctId for routing.
	if err := c.sdk.Enqueue(posthog.GroupIdentify{
		Type:       groupType,
		Key:        groupKey,
		DistinctId: "$groupidentify_" + groupType + "_" + groupKey,
		Properties: merged,
		Timestamp:  time.Now().UTC(),
	}); err != nil {
		logOnce("analytics: enqueue GroupIdentify failed: %v", err)
	}
}

// Shutdown flushes any pending captures and closes the SDK client. It
// honours the deadline carried by ctx — if the SDK takes longer than
// the configured ShutdownTimeout to drain, Close returns and the
// remaining events are dropped (with the SDK logging the drop count).
//
// Shutdown is idempotent and safe to call on a no-op client.
func (c *Client) Shutdown(ctx context.Context) error {
	if !c.Enabled() {
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- c.sdk.Close() }()
	if ctx == nil {
		return <-done
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return errors.New("analytics: shutdown timed out — remaining events dropped")
	}
}

// detectEnvironment mirrors internal/coderadar/coderadar.go's helper,
// preferring R1-specific env vars over generic NODE_ENV.
func detectEnvironment() string {
	for _, key := range []string{"POSTHOG_ENV", "R1_ENV", "NODE_ENV"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return "development"
}

func defaultedHost(h string) string {
	h = strings.TrimSpace(h)
	if h == "" {
		return posthog.DefaultEndpoint
	}
	return strings.TrimRight(h, "/")
}

func envBool(key string) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return false
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func envInt(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

func envDuration(key string, fallback time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}

// mergeProps shallow-copies props so the caller cannot mutate the map
// after enqueue. A nil props is normalised to an empty map.
func mergeProps(props map[string]any) map[string]any {
	out := make(map[string]any, len(props)+3)
	for k, v := range props {
		out[k] = v
	}
	return out
}

var (
	logOnceMu sync.Mutex
	logOnceTs time.Time
)

func logOnce(format string, args ...any) {
	logOnceMu.Lock()
	defer logOnceMu.Unlock()
	now := time.Now()
	if now.Sub(logOnceTs) < time.Second {
		return
	}
	logOnceTs = now
	log.Printf(format, args...)
}
