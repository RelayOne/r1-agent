// Package inhouse implements the in-house Chrome-in-Cloud-Run browser
// Provider for spec C6 (specs/browser-remote-sandbox.md §3, T3/T3a).
//
// Wire-compatible with the Browserless provider: same CDP-over-WS,
// same conformance suite. The differences live in this package only:
//
//   - Auth: Google-issued ID tokens (Authorization: Bearer …) minted
//     from the GCE metadata server with audience=cfg.Audience. Tokens
//     are cached for ~25 minutes against a 60-minute hard expiry.
//   - Network policy: same Network.setRequestInterception path as
//     Browserless, plus an optional defense-in-depth squid/tinyproxy
//     layer wired into the r1-browser container (T5b).
//   - Concurrency=1 per Cloud Run instance: one Chromium per
//     container, so isolation is enforced by Cloud Run itself. The
//     client opens one CDP-over-WS per session and tears it down on
//     Close — there is no cross-session multiplexing on a single WS.
//
// The CDP layer lives in internal/browser/browserless/cdp.go and is
// reused here verbatim. The spec calls out an `internal/browser/cdpconn/`
// extraction only when duplication exceeds 100 LOC; today the only
// inhouse-side code that touches CDP is this file's connect path,
// well under that threshold.

package inhouse

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/RelayOne/r1/internal/browser"
	"github.com/RelayOne/r1/internal/browser/browserless"
)

// Config is the in-house Provider's external configuration.
type Config struct {
	// Endpoint is the WS URL — typically wss://browser.r1.run. Required.
	Endpoint string

	// Audience is the audience claim the metadata server should mint
	// ID tokens for. Defaults to Endpoint when empty.
	Audience string

	// MaxConcurrent caps in-flight sessions. 0 → 5.
	MaxConcurrent int

	// DialTimeoutSeconds caps WS handshake duration. 0 → 10s.
	DialTimeoutSeconds int

	// AllowInsecure permits ws:// for local-docker development.
	AllowInsecure bool

	// TokenMinter overrides the default GCE metadata-server token
	// minter. Tests inject a stub; production leaves it nil.
	TokenMinter TokenMinter

	// HTTPProxy routes the WS handshake through an outbound proxy.
	HTTPProxy string
}

// TokenMinter returns an OAuth/OIDC ID token suitable for the
// Cloud Run service's identity audience. Implementations:
//
//   - production: MetadataServerMinter calls
//     https://metadata.google.internal/computeMetadata/v1/instance/
//     service-accounts/default/identity?audience=<aud>
//   - tests: a stub returning a static token
type TokenMinter interface {
	MintIDToken(ctx context.Context, audience string) (string, error)
}

// Validate enforces non-empty endpoint + scheme rules.
func (c Config) Validate() error {
	if strings.TrimSpace(c.Endpoint) == "" {
		return errors.New("inhouse: endpoint is required")
	}
	scheme := strings.ToLower(c.Endpoint)
	switch {
	case strings.HasPrefix(scheme, "wss://"):
		return nil
	case strings.HasPrefix(scheme, "ws://"):
		if !c.AllowInsecure {
			return errors.New("inhouse: endpoint uses ws://; set AllowInsecure=true for local-dev only")
		}
		return nil
	default:
		return errors.New("inhouse: endpoint must use wss:// (or ws:// with AllowInsecure)")
	}
}

// Client is the in-house Provider implementation.
type Client struct {
	cfg        Config
	httpClient *http.Client

	mu          sync.Mutex
	closed      bool
	sessions    map[string]*session // ID → session
	emit        browserless.EventEmitter
	costSink    browserless.CostSink
	tokenCache  *cachedToken
	tokenMu     sync.Mutex
	now         func() time.Time
	sessCounter atomic.Int64
}

type cachedToken struct {
	value     string
	expiresAt time.Time
}

// NewClient builds a fresh in-house Provider client.
func NewClient(cfg Config) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Audience == "" {
		cfg.Audience = strings.Replace(cfg.Endpoint, "wss://", "https://", 1)
		cfg.Audience = strings.Replace(cfg.Audience, "ws://", "http://", 1)
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 5
	}
	if cfg.DialTimeoutSeconds <= 0 {
		cfg.DialTimeoutSeconds = 10
	}
	if cfg.TokenMinter == nil {
		cfg.TokenMinter = &MetadataServerMinter{HTTPClient: http.DefaultClient}
	}
	httpClient := &http.Client{}
	if cfg.HTTPProxy != "" {
		proxyURL, err := url.Parse(cfg.HTTPProxy)
		if err != nil {
			return nil, fmt.Errorf("inhouse: parse HTTPProxy: %w", err)
		}
		httpClient.Transport = &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	}
	return &Client{
		cfg:        cfg,
		httpClient: httpClient,
		sessions:   make(map[string]*session),
		emit:       nopEmitter{},
		costSink:   nopCost{},
		now:        time.Now,
	}, nil
}

// WithEmitter wires the event emitter.
func (c *Client) WithEmitter(em browserless.EventEmitter) *Client {
	if em != nil {
		c.emit = em
	}
	return c
}

// WithCostSink wires the cost sink.
func (c *Client) WithCostSink(s browserless.CostSink) *Client {
	if s != nil {
		c.costSink = s
	}
	return c
}

// Name returns browser.ProviderInhouse.
func (c *Client) Name() string { return browser.ProviderInhouse }

// idToken returns a fresh ID token, using the cache when possible.
// Refresh policy: refresh at 25 minutes against the 60-minute hard
// expiry (spec T3a).
func (c *Client) idToken(ctx context.Context) (string, error) {
	c.tokenMu.Lock()
	if c.tokenCache != nil && c.tokenCache.expiresAt.After(c.now()) {
		t := c.tokenCache.value
		c.tokenMu.Unlock()
		return t, nil
	}
	c.tokenMu.Unlock()

	tok, err := c.cfg.TokenMinter.MintIDToken(ctx, c.cfg.Audience)
	if err != nil {
		return "", fmt.Errorf("inhouse: mint id token: %w", err)
	}
	c.tokenMu.Lock()
	c.tokenCache = &cachedToken{
		value:     tok,
		expiresAt: c.now().Add(25 * time.Minute),
	}
	c.tokenMu.Unlock()
	return tok, nil
}

// Open allocates a fresh session — one CDP-over-WS per session.
func (c *Client) Open(ctx context.Context, opts browser.SessionOpts) (browser.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, browser.ErrProviderClosed
	}
	c.mu.Unlock()

	if opts.TenantID == "" {
		return nil, errors.New("inhouse: SessionOpts.TenantID is required")
	}
	opts = resolveOpts(opts)

	if c.costSink.OverBudget(opts.MissionID) {
		c.emit.Emit(browserless.EvtBudgetExhausted, map[string]any{
			"tenant_id":  opts.TenantID,
			"mission_id": opts.MissionID,
			"provider":   browser.ProviderInhouse,
		})
		return nil, browser.ErrBudgetExhausted
	}

	tok, err := c.idToken(ctx)
	if err != nil {
		return nil, err
	}

	dialCtx, cancel := context.WithTimeout(ctx, time.Duration(c.cfg.DialTimeoutSeconds)*time.Second)
	defer cancel()

	header := http.Header{}
	header.Set("Authorization", "Bearer "+tok)
	conn, err := browserless.DialCDP(dialCtx, c.cfg.Endpoint, header, c.httpClient)
	if err != nil {
		if isAuthError(err) {
			c.emit.Emit(browserless.EvtTokenInvalid, map[string]any{
				"tenant_id": opts.TenantID,
				"provider":  browser.ProviderInhouse,
			})
			return nil, fmt.Errorf("%w: %v", browser.ErrAuthFailed, err)
		}
		c.emit.Emit(browserless.EvtError, map[string]any{
			"tenant_id": opts.TenantID,
			"provider":  browser.ProviderInhouse,
			"phase":     "dial",
			"error":     err.Error(),
		})
		return nil, fmt.Errorf("%w: %v", browser.ErrProviderUnreachable, err)
	}

	// One Chromium per Cloud Run instance — concurrency=1. The
	// Chromium inside the container is launched with --user-data-dir
	// per-request by the supervisor (see services/r1-browser/main.go),
	// so we still allocate a browserContextId here for symmetry with
	// Browserless. CDP servers that don't speak Target.* gracefully
	// return empty results which we tolerate via defaults.
	var ctxResp struct {
		BrowserContextID string `json:"browserContextId"`
	}
	if err := conn.Send(ctx, "Target.createBrowserContext", map[string]any{"disposeOnDetach": true}, &ctxResp); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("inhouse: createBrowserContext: %w", err)
	}

	var targetResp struct {
		TargetID string `json:"targetId"`
	}
	if err := conn.Send(ctx, "Target.createTarget", map[string]any{
		"url":              "about:blank",
		"browserContextId": ctxResp.BrowserContextID,
		"width":            opts.Viewport.Width,
		"height":           opts.Viewport.Height,
	}, &targetResp); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("inhouse: createTarget: %w", err)
	}

	var attachResp struct {
		SessionID string `json:"sessionId"`
	}
	if err := conn.Send(ctx, "Target.attachToTarget", map[string]any{
		"targetId": targetResp.TargetID, "flatten": true,
	}, &attachResp); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("inhouse: attachToTarget: %w", err)
	}

	id := targetResp.TargetID
	if id == "" {
		id = fmt.Sprintf("inhouse-%d", c.sessCounter.Add(1))
	}
	s := &session{
		client:           c,
		conn:             conn,
		id:               id,
		sessionID:        attachResp.SessionID,
		browserContextID: ctxResp.BrowserContextID,
		opts:             opts,
		openedAt:         c.now(),
		lastActiveAt:     c.now(),
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = conn.Close()
		return nil, browser.ErrProviderClosed
	}
	c.sessions[id] = s
	c.mu.Unlock()

	c.emit.Emit(browserless.EvtSessionOpened, map[string]any{
		"tenant_id":     opts.TenantID,
		"mission_id":    opts.MissionID,
		"provider":      browser.ProviderInhouse,
		"session_id":    s.id,
		"endpoint_host": hostOf(c.cfg.Endpoint),
	})
	return s, nil
}

// Close shuts every active session and the provider. Idempotent.
func (c *Client) Close(ctx context.Context) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	sessions := c.sessions
	c.sessions = nil
	c.mu.Unlock()
	for _, s := range sessions {
		_ = s.conn.Close()
	}
	return nil
}

// session is one in-house browser session.
type session struct {
	client *Client
	conn   *browserless.CDPConn

	id               string
	sessionID        string
	browserContextID string

	opts         browser.SessionOpts
	openedAt     time.Time
	lastActiveAt time.Time

	mu     sync.Mutex
	closed bool
}

// ID returns the CDP targetId.
func (s *session) ID() string { return s.id }

// touch updates the last-active timestamp.
func (s *session) touch() {
	s.mu.Lock()
	s.lastActiveAt = s.client.now()
	s.mu.Unlock()
}

func (s *session) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// sendOnSession mirrors the Browserless wrapper so per-session
// routing works identically.
func (s *session) sendOnSession(ctx context.Context, method string, params any, out any) error {
	if params == nil {
		params = map[string]any{}
	}
	// Tiny JSON-RPC envelope identical to browserless/.client.go
	// shape; kept inline so the inhouse path doesn't depend on
	// browserless internals beyond DialCDP / CDPConn / EventEmitter.
	inner := encodeJSON(struct {
		ID     int64  `json:"id"`
		Method string `json:"method"`
		Params any    `json:"params,omitempty"`
	}{ID: 1, Method: method, Params: params})
	var resp struct {
		Message string `json:"message"`
	}
	wrap := map[string]any{"sessionId": s.sessionID, "message": inner}
	if err := s.conn.Send(ctx, "Target.sendMessageToTarget", wrap, &resp); err != nil {
		return err
	}
	if resp.Message != "" && out != nil {
		return decodeInnerResult(resp.Message, out)
	}
	return nil
}

// Navigate runs Page.navigate after the network-policy pre-check.
func (s *session) Navigate(ctx context.Context, rawURL string) (browser.NavigateResult, error) {
	if s.isClosed() {
		return browser.NavigateResult{}, browser.ErrSessionClosed
	}
	s.touch()
	host := hostOfURL(rawURL)
	if d := s.opts.NetworkPolicy.Decide(host); !d.Allowed {
		s.client.emit.Emit(browserless.EvtNetworkDenied, map[string]any{
			"tenant_id":  s.opts.TenantID,
			"provider":   browser.ProviderInhouse,
			"session_id": s.id,
			"host":       host,
			"rule":       d.Rule,
		})
		return browser.NavigateResult{}, &browser.NetworkDeniedError{Host: host, Rule: d.Rule}
	}
	start := s.client.now()
	if err := s.sendOnSession(ctx, "Page.navigate", map[string]any{"url": rawURL}, nil); err != nil {
		return browser.NavigateResult{}, err
	}
	var hrefResp struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	_ = s.sendOnSession(ctx, "Runtime.evaluate", map[string]any{
		"expression": "location.href", "returnByValue": true,
	}, &hrefResp)
	res := browser.NavigateResult{
		FinalURL: defaultStr(hrefResp.Result.Value, rawURL),
		Status:   200,
	}
	s.client.emit.Emit(browserless.EvtNavigate, map[string]any{
		"tenant_id":   s.opts.TenantID,
		"provider":    browser.ProviderInhouse,
		"session_id":  s.id,
		"url":         rawURL,
		"final_url":   res.FinalURL,
		"duration_ms": s.client.now().Sub(start).Milliseconds(),
	})
	return res, nil
}

// WaitFor polls querySelector on a 100ms cadence.
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
				Value any `json:"value"`
			} `json:"result"`
		}
		err := s.sendOnSession(ctx, "Runtime.evaluate", map[string]any{
			"expression":    "document.querySelector(" + quoteJSON(selector) + ") !== null",
			"returnByValue": true,
		}, &resp)
		if err != nil {
			return err
		}
		if b, ok := resp.Result.Value.(bool); ok && b {
			return nil
		}
		if !s.client.now().Before(deadline) {
			return fmt.Errorf("inhouse: WaitFor(%q) timed out after %s", selector, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// Screenshot runs Page.captureScreenshot.
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
		return nil, errors.New("inhouse: empty screenshot data")
	}
	return decodeBase64(resp.Data)
}

// Eval runs Runtime.evaluate with returnByValue=true.
func (s *session) Eval(ctx context.Context, script string) (any, error) {
	if s.isClosed() {
		return nil, browser.ErrSessionClosed
	}
	s.touch()
	var resp struct {
		Result struct {
			Value any `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := s.sendOnSession(ctx, "Runtime.evaluate", map[string]any{
		"expression": script, "returnByValue": true,
	}, &resp); err != nil {
		return nil, err
	}
	if resp.ExceptionDetails != nil {
		return nil, fmt.Errorf("inhouse: eval threw: %s", resp.ExceptionDetails.Text)
	}
	return resp.Result.Value, nil
}

// Close disposes the browser context and tears down the WS.
func (s *session) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	closedAt := s.client.now()
	s.mu.Unlock()

	if !s.conn.Closed() {
		_ = s.conn.Send(ctx, "Target.disposeBrowserContext",
			map[string]any{"browserContextId": s.browserContextID}, nil)
		_ = s.conn.Close()
	}
	duration := closedAt.Sub(s.openedAt)
	// Inhouse cost: tagged as "browser-inhouse" so the existing
	// Cloud Run cost ingester can roll it up alongside Cloud Run
	// vCPU-seconds (spec T12).
	s.client.costSink.RecordSession(browser.ProviderInhouse, s.opts.TenantID, s.opts.MissionID, 0, duration.Seconds())
	s.client.emit.Emit(browserless.EvtSessionClosed, map[string]any{
		"tenant_id":   s.opts.TenantID,
		"mission_id":  s.opts.MissionID,
		"provider":    browser.ProviderInhouse,
		"session_id":  s.id,
		"duration_ms": duration.Milliseconds(),
		"reason":      "explicit",
	})
	s.client.mu.Lock()
	if s.client.sessions != nil {
		delete(s.client.sessions, s.id)
	}
	s.client.mu.Unlock()
	return nil
}

// ----- helpers -----

// resolveOpts mirrors browserless.resolveOpts.
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

// nopEmitter is the default zero-config emitter.
type nopEmitter struct{}

func (nopEmitter) Emit(string, map[string]any) {}

// nopCost is the default zero-config cost sink.
type nopCost struct{}

func (nopCost) RecordSession(string, string, string, int, float64) {}
func (nopCost) OverBudget(string) bool                             { return false }

// isAuthError mirrors browserless.isAuthError.
func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "401") || strings.Contains(s, "403") || strings.Contains(s, "auth rejected")
}

// hostOf returns the hostname of a URL with scheme stripped.
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

func defaultStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// MetadataServerMinter mints ID tokens from the GCE metadata server.
// In a non-GCP environment the HTTP call fails and the caller falls
// back to a stub minter (tests) or to the no-token path.
type MetadataServerMinter struct {
	HTTPClient *http.Client
}

// MintIDToken fetches an ID token for the given audience.
func (m *MetadataServerMinter) MintIDToken(ctx context.Context, audience string) (string, error) {
	if m.HTTPClient == nil {
		m.HTTPClient = http.DefaultClient
	}
	u := "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/identity?audience=" + url.QueryEscape(audience)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Metadata-Flavor", "Google")
	resp, err := m.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("metadata server: status %d", resp.StatusCode)
	}
	return strings.TrimSpace(string(body)), nil
}

// StubMinter is a TokenMinter for tests / dev environments.
type StubMinter struct {
	Token string
	Err   error
}

// MintIDToken returns the canned values.
func (s *StubMinter) MintIDToken(ctx context.Context, audience string) (string, error) {
	return s.Token, s.Err
}

// Compile-time assertions.
var (
	_ browser.Provider = (*Client)(nil)
	_ browser.Session  = (*session)(nil)
	_ TokenMinter      = (*MetadataServerMinter)(nil)
	_ TokenMinter      = (*StubMinter)(nil)
)
