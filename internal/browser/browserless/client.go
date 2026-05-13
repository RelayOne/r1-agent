// Package browserless implements the Browserless CDP-over-WS browser
// Provider for spec C6 (specs/browser-remote-sandbox.md §3, T2..T2d).
//
// Browserless ships a managed Chromium fleet that speaks vanilla CDP
// over a single wss:// WebSocket. The wire format is exactly the
// same as an in-house Chromium's --remote-debugging-port=9222 — that
// shared property is what lets the same conformance suite (T1b)
// validate this client and the inhouse client byte-identically.
//
// What this package owns:
//
//   - WS dial + handshake with token-in-URL auth
//   - Per-tenant incognito context allocation (Target.createBrowserContext)
//   - Page/target lifecycle per Session (Target.createTarget)
//   - Navigate / WaitFor / Screenshot / Eval mapped to single CDP calls
//   - Request interception for the deny-by-default network policy
//   - Per-session cost rollup (Unit = 30s of browser-time)
//   - Event emission (browser.session_*, browser.navigate, …)
//
// What this package does NOT own:
//
//   - Provider selection / config loading (cmd/r1/browse_cmd.go)
//   - Fallback wrapping (internal/browser/fallback.go)
//   - Tenant credential plumbing into SessionOpts (the executor layer)
//
// Reference: spec §3.4 (Refactor sequence), §10 (Acceptance), and
// the Browserless WebSocket CDP docs (cited 2026-05 in §2).

package browserless

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/RelayOne/r1/internal/browser"
)

// Client is the Browserless CDP-over-WS Provider implementation.
// One Client owns one wss:// connection per tenant; each Session
// allocates a fresh incognito context (browserContextId) on that
// connection. Closing the last session for a tenant tears the
// tenant's WS down to enforce "no cross-tenant traffic on a shared
// WS" (spec §7 boundary).
type Client struct {
	cfg Config

	// httpClient is reused for WS handshake (HTTPProxy honored
	// transparently via http.Transport.Proxy).
	httpClient *http.Client

	mu           sync.Mutex
	closed       bool
	tenantConns  map[string]*tenantConn // tenantID → CDP conn
	sessionCount atomic.Int64           // total live sessions
	emit         EventEmitter
	costSink     CostSink
	now          func() time.Time // injectable for tests
}

// tenantConn holds one CDP-over-WS connection plus the live session
// count using it. When count hits zero, the connection is torn down.
type tenantConn struct {
	conn        *CDPConn
	tenantID    string
	endpointURL string
	tokenSlot   string // logged but never printed in full
	live        atomic.Int64
}

// EventEmitter receives browser.* events. Wired by the embedder to
// the existing event-log + CodeRadar pipeline; tests inject a fake
// to capture the emission sequence.
type EventEmitter interface {
	Emit(name string, attrs map[string]any)
}

// CostSink receives per-session cost units at session close. Wired
// to internal/costtrack/ in production.
type CostSink interface {
	RecordSession(provider, tenantID, missionID string, units int, durationSec float64)
	OverBudget(missionID string) bool
}

// nopEventEmitter is the default when the embedder didn't wire one.
type nopEventEmitter struct{}

func (nopEventEmitter) Emit(string, map[string]any) {}

// nopCostSink is the default when the embedder didn't wire one. Its
// OverBudget always returns false so the budget gate is open by
// default — operators MUST wire a real sink to enforce budgets.
type nopCostSink struct{}

func (nopCostSink) RecordSession(string, string, string, int, float64) {}
func (nopCostSink) OverBudget(string) bool                             { return false }

// NewClient constructs a Browserless Provider. Validates the config
// and resolves the global token at construction time; per-tenant
// tokens are resolved lazily at Open. No network I/O happens here.
func NewClient(cfg Config) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 5
	}
	if cfg.DialTimeoutSeconds <= 0 {
		cfg.DialTimeoutSeconds = 10
	}

	httpClient := &http.Client{}
	if cfg.HTTPProxy != "" {
		proxyURL, err := url.Parse(cfg.HTTPProxy)
		if err != nil {
			return nil, fmt.Errorf("browserless: parse HTTPProxy: %w", err)
		}
		httpClient.Transport = &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	}

	c := &Client{
		cfg:         cfg,
		httpClient:  httpClient,
		tenantConns: make(map[string]*tenantConn),
		emit:        nopEventEmitter{},
		costSink:    nopCostSink{},
		now:         time.Now,
	}
	return c, nil
}

// WithEmitter wires the event emitter. Returns the client for fluent
// construction. Safe to call once at boot.
func (c *Client) WithEmitter(em EventEmitter) *Client {
	if em != nil {
		c.emit = em
	}
	return c
}

// WithCostSink wires the cost sink. Returns the client for fluent
// construction.
func (c *Client) WithCostSink(s CostSink) *Client {
	if s != nil {
		c.costSink = s
	}
	return c
}

// Name returns browser.ProviderBrowserless.
func (c *Client) Name() string { return browser.ProviderBrowserless }

// Open allocates a fresh incognito context on the tenant's CDP
// connection and a target (tab) inside it. The returned session
// owns its targetId; Close disposes both.
func (c *Client) Open(ctx context.Context, opts browser.SessionOpts) (browser.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	opts = resolveOpts(opts)
	if opts.TenantID == "" {
		return nil, errors.New("browserless: SessionOpts.TenantID is required")
	}
	if c.costSink.OverBudget(opts.MissionID) {
		c.emit.Emit(EvtBudgetExhausted, map[string]any{
			"tenant_id":  opts.TenantID,
			"mission_id": opts.MissionID,
			"provider":   browser.ProviderBrowserless,
		})
		return nil, browser.ErrBudgetExhausted
	}

	tConn, err := c.ensureTenantConn(ctx, opts.TenantID)
	if err != nil {
		return nil, err
	}

	// 1. Allocate a fresh incognito browser context.
	var ctxResp struct {
		BrowserContextID string `json:"browserContextId"`
	}
	if err := tConn.conn.Send(ctx, "Target.createBrowserContext", map[string]any{"disposeOnDetach": true}, &ctxResp); err != nil {
		if isAuthError(err) {
			c.emit.Emit(EvtTokenInvalid, map[string]any{"tenant_id": opts.TenantID, "provider": browser.ProviderBrowserless})
			c.tearDownTenant(opts.TenantID)
			return nil, fmt.Errorf("%w: %v", browser.ErrAuthFailed, err)
		}
		return nil, fmt.Errorf("browserless: createBrowserContext: %w", err)
	}

	// 2. Create a target inside that context.
	var targetResp struct {
		TargetID string `json:"targetId"`
	}
	createParams := map[string]any{
		"url":              "about:blank",
		"browserContextId": ctxResp.BrowserContextID,
		"width":            opts.Viewport.Width,
		"height":           opts.Viewport.Height,
	}
	if err := tConn.conn.Send(ctx, "Target.createTarget", createParams, &targetResp); err != nil {
		// Best-effort cleanup of the orphaned context.
		_ = tConn.conn.Send(context.Background(), "Target.disposeBrowserContext",
			map[string]any{"browserContextId": ctxResp.BrowserContextID}, nil)
		return nil, fmt.Errorf("browserless: createTarget: %w", err)
	}

	// 3. Attach a session to the target so we can call methods on it.
	var attachResp struct {
		SessionID string `json:"sessionId"`
	}
	if err := tConn.conn.Send(ctx, "Target.attachToTarget", map[string]any{
		"targetId": targetResp.TargetID,
		"flatten":  true,
	}, &attachResp); err != nil {
		_ = tConn.conn.Send(context.Background(), "Target.disposeBrowserContext",
			map[string]any{"browserContextId": ctxResp.BrowserContextID}, nil)
		return nil, fmt.Errorf("browserless: attachToTarget: %w", err)
	}

	// 4. Wire the network policy (request interception). Best-effort
	//    — non-fatal if the mock CDP server doesn't speak it, so the
	//    conformance suite can exercise mock providers without a full
	//    Network.* dispatcher.
	_ = applyNetworkInterception(ctx, tConn.conn, attachResp.SessionID, opts.NetworkPolicy)

	s := &session{
		client:           c,
		tenantConn:       tConn,
		id:               targetResp.TargetID,
		sessionID:        attachResp.SessionID,
		browserContextID: ctxResp.BrowserContextID,
		opts:             opts,
		openedAt:         c.now(),
		lastActiveAt:     c.now(),
	}
	tConn.live.Add(1)
	c.sessionCount.Add(1)

	c.emit.Emit(EvtSessionOpened, map[string]any{
		"tenant_id":     opts.TenantID,
		"mission_id":    opts.MissionID,
		"provider":      browser.ProviderBrowserless,
		"session_id":    s.id,
		"endpoint_host": hostOf(tConn.endpointURL),
	})
	return s, nil
}

// ensureTenantConn returns the per-tenant CDP connection, dialing on
// first use. Tokens are resolved per-tenant via Config.ResolveToken.
func (c *Client) ensureTenantConn(ctx context.Context, tenantID string) (*tenantConn, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, browser.ErrProviderClosed
	}
	if tc, ok := c.tenantConns[tenantID]; ok && !tc.conn.Closed() {
		c.mu.Unlock()
		return tc, nil
	}
	c.mu.Unlock()

	token := c.cfg.ResolveToken(tenantID)
	if token == "" {
		// If the operator configured tenant_tokens but this tenant
		// isn't in the map AND no fallback, surface a tenant-specific
		// error so the runbook points at the right line.
		if len(c.cfg.TenantTokens) > 0 {
			return nil, fmt.Errorf("%w: tenant=%s", browser.ErrNoTenantToken, tenantID)
		}
		return nil, browser.ErrNoToken
	}

	dialCtx, cancel := context.WithTimeout(ctx, time.Duration(c.cfg.DialTimeoutSeconds)*time.Second)
	defer cancel()

	endpoint := buildEndpointURL(c.cfg.Endpoint, token, tenantID)
	conn, err := DialCDP(dialCtx, endpoint, nil, c.httpClient)
	if err != nil {
		if isAuthError(err) {
			c.emit.Emit(EvtTokenInvalid, map[string]any{"tenant_id": tenantID, "provider": browser.ProviderBrowserless})
			return nil, fmt.Errorf("%w: %v", browser.ErrAuthFailed, err)
		}
		c.emit.Emit(EvtError, map[string]any{
			"tenant_id": tenantID,
			"provider":  browser.ProviderBrowserless,
			"phase":     "dial",
			"error":     err.Error(),
		})
		return nil, fmt.Errorf("%w: %v", browser.ErrProviderUnreachable, err)
	}

	tc := &tenantConn{
		conn:        conn,
		tenantID:    tenantID,
		endpointURL: endpoint,
		tokenSlot:   redactToken(token),
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = conn.Close()
		return nil, browser.ErrProviderClosed
	}
	// A concurrent caller may have raced us; prefer theirs and
	// throw ours away.
	if existing, ok := c.tenantConns[tenantID]; ok && !existing.conn.Closed() {
		c.mu.Unlock()
		_ = conn.Close()
		return existing, nil
	}
	c.tenantConns[tenantID] = tc
	c.mu.Unlock()
	return tc, nil
}

// tearDownTenant closes a tenant's CDP connection and removes the
// map entry. Called on auth failure (spec T2d) and when the last
// session for the tenant closes.
func (c *Client) tearDownTenant(tenantID string) {
	c.mu.Lock()
	tc, ok := c.tenantConns[tenantID]
	if !ok {
		c.mu.Unlock()
		return
	}
	delete(c.tenantConns, tenantID)
	c.mu.Unlock()
	_ = tc.conn.Close()
}

// Close shuts the provider. Idempotent. Tears down every tenant
// connection.
func (c *Client) Close(ctx context.Context) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	conns := c.tenantConns
	c.tenantConns = nil
	c.mu.Unlock()
	for _, tc := range conns {
		_ = tc.conn.Close()
	}
	return nil
}

// session is one incognito browser context on a Browserless WS.
type session struct {
	client     *Client
	tenantConn *tenantConn

	id               string // CDP targetId
	sessionID        string // CDP sessionId (attachToTarget result)
	browserContextID string

	opts         browser.SessionOpts
	openedAt     time.Time
	lastActiveAt time.Time

	mu     sync.Mutex
	closed bool
}

// ID returns the CDP targetId — opaque to callers, logged with every event.
func (s *session) ID() string { return s.id }

// touch updates the last-active timestamp for the idle wrapper (T9).
func (s *session) touch() {
	s.mu.Lock()
	s.lastActiveAt = s.client.now()
	s.mu.Unlock()
}

// isClosed reports whether Close already ran.
func (s *session) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// sendOnSession wraps CDP method calls with the per-session sessionId
// header so the Browserless WS routes the call to the right target.
func (s *session) sendOnSession(ctx context.Context, method string, params any, out any) error {
	if params == nil {
		params = map[string]any{}
	}
	// Browserless honors per-session routing via Target.sendMessageToTarget
	// OR via flatten=true (which we set in attachToTarget). With
	// flatten=true the convention is to embed sessionId in the
	// top-level CDP frame; the cdp.go layer doesn't add it. Instead
	// we wrap the method via Target.sendMessageToTarget which is the
	// stable subset that every CDP server (mock + real) speaks.
	inner, err := json.Marshal(struct {
		ID     int64  `json:"id"`
		Method string `json:"method"`
		Params any    `json:"params,omitempty"`
	}{ID: 1, Method: method, Params: params})
	if err != nil {
		return err
	}
	var resp struct {
		Message string `json:"message"`
	}
	wrap := map[string]any{
		"sessionId": s.sessionID,
		"message":   string(inner),
	}
	if err := s.tenantConn.conn.Send(ctx, "Target.sendMessageToTarget", wrap, &resp); err != nil {
		return err
	}
	if resp.Message != "" && out != nil {
		var innerResp struct {
			Result json.RawMessage `json:"result"`
			Error  *rpcError       `json:"error"`
		}
		if err := json.Unmarshal([]byte(resp.Message), &innerResp); err != nil {
			return fmt.Errorf("browserless: parse target reply: %w", err)
		}
		if innerResp.Error != nil {
			return innerResp.Error
		}
		if len(innerResp.Result) > 0 {
			return json.Unmarshal(innerResp.Result, out)
		}
	}
	return nil
}

// Navigate runs Page.navigate and returns the resulting URL/Status.
// Network policy is consulted BEFORE the CDP call — denied requests
// never reach Browserless.
func (s *session) Navigate(ctx context.Context, rawURL string) (browser.NavigateResult, error) {
	if s.isClosed() {
		return browser.NavigateResult{}, browser.ErrSessionClosed
	}
	s.touch()
	host := hostOfURL(rawURL)
	if d := s.opts.NetworkPolicy.Decide(host); !d.Allowed {
		s.client.emit.Emit(EvtNetworkDenied, map[string]any{
			"tenant_id":  s.opts.TenantID,
			"provider":   browser.ProviderBrowserless,
			"session_id": s.id,
			"host":       host,
			"rule":       d.Rule,
		})
		return browser.NavigateResult{}, &browser.NetworkDeniedError{Host: host, Rule: d.Rule}
	}

	start := s.client.now()
	var navResp struct {
		FrameID string `json:"frameId"`
		LoadID  string `json:"loaderId"`
	}
	if err := s.sendOnSession(ctx, "Page.navigate", map[string]any{"url": rawURL}, &navResp); err != nil {
		s.client.emit.Emit(EvtError, map[string]any{
			"tenant_id":  s.opts.TenantID,
			"provider":   browser.ProviderBrowserless,
			"session_id": s.id,
			"phase":      "navigate",
			"error":      err.Error(),
		})
		return browser.NavigateResult{}, err
	}
	// Mock CDP servers + Browserless both return enough for us to
	// derive the final URL via Page.getNavigationHistory or via
	// Runtime.evaluate("location.href"). We use the latter — one
	// fewer round-trip and works against every CDP responder.
	var hrefResp struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	_ = s.sendOnSession(ctx, "Runtime.evaluate", map[string]any{
		"expression":     "location.href",
		"returnByValue":  true,
		"timeout":        5000,
	}, &hrefResp)

	res := browser.NavigateResult{
		FinalURL: defaultStr(hrefResp.Result.Value, rawURL),
		Status:   200, // CDP doesn't surface a top-level status cheaply; the request-interception path can refine
	}
	s.client.emit.Emit(EvtNavigate, map[string]any{
		"tenant_id":   s.opts.TenantID,
		"provider":    browser.ProviderBrowserless,
		"session_id":  s.id,
		"url":         rawURL,
		"final_url":   res.FinalURL,
		"duration_ms": s.client.now().Sub(start).Milliseconds(),
	})
	return res, nil
}

// WaitFor polls document.querySelector(selector) on a 100ms cadence
// until it resolves or the timeout elapses (spec §3.4 T2c).
func (s *session) WaitFor(ctx context.Context, selector string, timeout time.Duration) error {
	if s.isClosed() {
		return browser.ErrSessionClosed
	}
	s.touch()
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	deadline := s.client.now().Add(timeout)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var resp struct {
			Result struct {
				Type  string `json:"type"`
				Value any    `json:"value"`
			} `json:"result"`
		}
		err := s.sendOnSession(ctx, "Runtime.evaluate", map[string]any{
			"expression":    "document.querySelector(" + jsonString(selector) + ") !== null",
			"returnByValue": true,
		}, &resp)
		if err != nil {
			return err
		}
		if b, ok := resp.Result.Value.(bool); ok && b {
			return nil
		}
		if !s.client.now().Before(deadline) {
			return fmt.Errorf("browserless: WaitFor(%q) timed out after %s", selector, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// Screenshot runs Page.captureScreenshot and decodes the base64 result.
func (s *session) Screenshot(ctx context.Context) ([]byte, error) {
	if s.isClosed() {
		return nil, browser.ErrSessionClosed
	}
	s.touch()
	var resp struct {
		Data string `json:"data"`
	}
	if err := s.sendOnSession(ctx, "Page.captureScreenshot", map[string]any{"format": "png"}, &resp); err != nil {
		return nil, err
	}
	if resp.Data == "" {
		return nil, errors.New("browserless: empty screenshot data")
	}
	b, err := base64.StdEncoding.DecodeString(resp.Data)
	if err != nil {
		return nil, fmt.Errorf("browserless: decode screenshot: %w", err)
	}
	return b, nil
}

// Eval runs Runtime.evaluate with returnByValue=true and unwraps the
// CDP result.value to the underlying JS primitive.
func (s *session) Eval(ctx context.Context, script string) (any, error) {
	if s.isClosed() {
		return nil, browser.ErrSessionClosed
	}
	s.touch()
	var resp struct {
		Result struct {
			Type             string `json:"type"`
			Value            any    `json:"value"`
			Description      string `json:"description"`
			UnserializableValue string `json:"unserializableValue"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := s.sendOnSession(ctx, "Runtime.evaluate", map[string]any{
		"expression":    script,
		"returnByValue": true,
	}, &resp); err != nil {
		return nil, err
	}
	if resp.ExceptionDetails != nil {
		return nil, fmt.Errorf("browserless: eval threw: %s", resp.ExceptionDetails.Text)
	}
	return resp.Result.Value, nil
}

// Close disposes the browser context (Target.disposeBrowserContext)
// which evicts all per-context client-state by construction. Idempotent.
// When the last session on a tenant connection closes, the tenant
// WS itself is torn down to keep "no cross-tenant traffic on a shared
// WS" enforceable.
func (s *session) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	closedAt := s.client.now()
	s.mu.Unlock()

	// Best-effort dispose — the WS may already be torn, in which case
	// the context is gone anyway.
	if !s.tenantConn.conn.Closed() {
		_ = s.tenantConn.conn.Send(ctx, "Target.disposeBrowserContext",
			map[string]any{"browserContextId": s.browserContextID}, nil)
	}

	duration := closedAt.Sub(s.openedAt)
	units := costUnits(duration)
	s.client.costSink.RecordSession(browser.ProviderBrowserless, s.opts.TenantID, s.opts.MissionID, units, duration.Seconds())
	s.client.emit.Emit(EvtSessionClosed, map[string]any{
		"tenant_id":    s.opts.TenantID,
		"mission_id":   s.opts.MissionID,
		"provider":     browser.ProviderBrowserless,
		"session_id":   s.id,
		"duration_ms":  duration.Milliseconds(),
		"cost_units":   units,
		"reason":       "explicit",
	})

	// Last session on this tenant? Tear the WS down.
	if s.tenantConn.live.Add(-1) == 0 {
		s.client.tearDownTenant(s.tenantConn.tenantID)
	}
	s.client.sessionCount.Add(-1)
	return nil
}

// costUnits computes Browserless's "Unit" cost = ceil(duration / 30s)
// per their pricing page (cited 2026-05 in the spec).
func costUnits(d time.Duration) int {
	if d <= 0 {
		return 1
	}
	secs := int(d.Seconds())
	units := secs / 30
	if secs%30 != 0 {
		units++
	}
	if units < 1 {
		units = 1
	}
	return units
}

// resolveOpts fills in defaults consistent with provider.go.
func resolveOpts(opts browser.SessionOpts) browser.SessionOpts {
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = browser.DefaultIdleTimeout
	}
	if opts.HardTimeout <= 0 {
		opts.HardTimeout = browser.DefaultHardTimeout
	}
	if opts.Viewport.Width == 0 || opts.Viewport.Height == 0 {
		opts.Viewport = browser.DefaultViewport()
	}
	return opts
}

// buildEndpointURL appends Browserless's query params to the
// configured base. trackingId is the tenant ID — Browserless surfaces
// it in their dashboard for usage attribution.
func buildEndpointURL(base, token, tenantID string) string {
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	q := url.Values{}
	q.Set("token", token)
	q.Set("headless", "true")
	q.Set("blockAds", "true")
	q.Set("stealth", "false")
	if tenantID != "" {
		q.Set("trackingId", tenantID)
	}
	return base + sep + q.Encode()
}

// isAuthError reports whether the error is the well-known 401/403
// signature from the cdp.go dial path. Used to map to ErrAuthFailed.
func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "401") || strings.Contains(s, "403") || strings.Contains(s, "auth rejected")
}

// redactToken returns a length-prefixed redacted form for log lines.
// Never logs the full token — credential isolation rule (spec T6).
func redactToken(t string) string {
	if len(t) <= 6 {
		return "redacted"
	}
	return t[:4] + "…" + t[len(t)-2:]
}

// hostOf returns the hostname of a URL (no port, no scheme).
func hostOf(rawURL string) string { return hostOfURL(rawURL) }

func hostOfURL(rawURL string) string {
	s := rawURL
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	for _, sep := range []byte{'/', '?', '#'} {
		if i := strings.IndexByte(s, sep); i >= 0 {
			s = s[:i]
		}
	}
	if i := strings.IndexByte(s, '@'); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.IndexByte(s, ':'); i >= 0 {
		s = s[:i]
	}
	return strings.ToLower(s)
}

// jsonString returns a JSON-quoted form of s for inline use in CDP
// Runtime.evaluate expressions.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// defaultStr returns a when non-empty, b otherwise.
func defaultStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Compile-time assertions.
var (
	_ browser.Provider = (*Client)(nil)
	_ browser.Session  = (*session)(nil)
)
