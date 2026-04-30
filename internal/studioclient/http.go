package studioclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/config"
)

// toolSpec describes how an R1 skill name maps onto the Studio HTTP
// API. Each entry lists the method, a path template (with `{param}`
// tokens substituted from the input JSON object), and the per-call
// timeout.
//
// The spec is intentionally explicit per tool rather than inferred
// from the skill name because the zod → HTTP-verb mapping on the
// Studio side is not 1-to-1 (scaffold is POST sites:scaffold, status
// is GET sites/{id}, etc.). Per work order §1.1 / §R1S-2.
type toolSpec struct {
	Method  string
	// Path is a template; {field} tokens are replaced from input.
	// Fields listed in PathFields are consumed from the input JSON
	// object and NOT forwarded as part of the request body / query.
	Path       string
	PathFields []string

	// When Method is GET and this spec lists QueryFields, those input
	// fields are pulled out of the body and placed in the query string
	// instead. Unknown fields stay in the body (no-op for GETs since
	// GETs never send a body — they become query parameters below).
	QueryFields []string

	// Timeout for this specific tool. 0 uses DefaultTimeout.
	Timeout time.Duration
}

// Canonical tool-to-endpoint map. Source of truth: the 53-entry table
// in work-r1-actium-studio-skills.md §1.1 combined with the shapes in
// actium-studio/services/mcp-server/src/tools/*.ts.
//
// NOTE: hero skills (scaffold_site, update_content, publish,
// diff_versions, list_templates, site_status) sometimes compose 2+
// underlying HTTP calls. This map lists the *primary* call; composite
// hero logic lives in Invoke where needed.
var toolSpecs = map[string]toolSpec{
	// Studio hero / scaffold
	"studio.scaffold_site":        {Method: http.MethodPost, Path: "/api/studio/sites:scaffold", Timeout: 60 * time.Second},
	"studio.get_scaffold_status":  {Method: http.MethodGet, Path: "/api/studio/sites/{siteId}", PathFields: []string{"siteId"}},
	"studio.publish":              {Method: http.MethodPost, Path: "/api/pages/{pageId}:publish", PathFields: []string{"pageId"}},
	"studio.update_content":       {Method: http.MethodPost, Path: "/api/pages/{pageId}:update_content", PathFields: []string{"pageId"}, Timeout: 45 * time.Second},
	"studio.diff_versions":        {Method: http.MethodGet, Path: "/api/sites/{siteId}/snapshots:diff", PathFields: []string{"siteId"}, QueryFields: []string{"from", "to"}},
	"studio.site_status":          {Method: http.MethodGet, Path: "/api/sites/{siteId}:status", PathFields: []string{"siteId"}},

	// Sites
	"studio.list_sites":  {Method: http.MethodGet, Path: "/api/sites"},
	"studio.get_site":    {Method: http.MethodGet, Path: "/api/sites/{siteId}", PathFields: []string{"siteId"}},
	"studio.create_site": {Method: http.MethodPost, Path: "/api/sites"},
	"studio.update_site": {Method: http.MethodPatch, Path: "/api/sites/{siteId}", PathFields: []string{"siteId"}},

	// Pages
	"studio.list_pages":     {Method: http.MethodGet, Path: "/api/sites/{siteId}/pages", PathFields: []string{"siteId"}},
	"studio.get_page":       {Method: http.MethodGet, Path: "/api/pages/{pageId}", PathFields: []string{"pageId"}},
	"studio.create_page":    {Method: http.MethodPost, Path: "/api/sites/{siteId}/pages", PathFields: []string{"siteId"}},
	"studio.update_page":    {Method: http.MethodPatch, Path: "/api/pages/{pageId}", PathFields: []string{"pageId"}},
	"studio.publish_page":   {Method: http.MethodPost, Path: "/api/pages/{pageId}:publish", PathFields: []string{"pageId"}},
	"studio.unpublish_page": {Method: http.MethodPost, Path: "/api/pages/{pageId}:unpublish", PathFields: []string{"pageId"}},

	// Blog posts
	"studio.list_posts":   {Method: http.MethodGet, Path: "/api/sites/{siteId}/blog/posts", PathFields: []string{"siteId"}},
	"studio.get_post":     {Method: http.MethodGet, Path: "/api/blog/posts/{postId}", PathFields: []string{"postId"}},
	"studio.create_post":  {Method: http.MethodPost, Path: "/api/sites/{siteId}/blog/posts", PathFields: []string{"siteId"}},
	"studio.update_post":  {Method: http.MethodPatch, Path: "/api/blog/posts/{postId}", PathFields: []string{"postId"}},
	"studio.publish_post": {Method: http.MethodPost, Path: "/api/blog/posts/{postId}:publish", PathFields: []string{"postId"}},

	// Blog taxonomy
	"studio.list_blog_categories":  {Method: http.MethodGet, Path: "/api/sites/{siteId}/blog/categories", PathFields: []string{"siteId"}},
	"studio.create_blog_category":  {Method: http.MethodPost, Path: "/api/sites/{siteId}/blog/categories", PathFields: []string{"siteId"}},
	"studio.delete_blog_category":  {Method: http.MethodDelete, Path: "/api/blog/categories/{categoryId}", PathFields: []string{"categoryId"}},
	"studio.list_blog_tags":        {Method: http.MethodGet, Path: "/api/sites/{siteId}/blog/tags", PathFields: []string{"siteId"}},
	"studio.create_blog_tag":       {Method: http.MethodPost, Path: "/api/sites/{siteId}/blog/tags", PathFields: []string{"siteId"}},
	"studio.delete_blog_tag":       {Method: http.MethodDelete, Path: "/api/blog/tags/{tagId}", PathFields: []string{"tagId"}},

	// SEO
	"studio.get_seo_report":     {Method: http.MethodGet, Path: "/api/sites/{siteId}/seo:report", PathFields: []string{"siteId"}},
	"studio.trigger_seo_audit":  {Method: http.MethodPost, Path: "/api/sites/{siteId}/seo:audit", PathFields: []string{"siteId"}, Timeout: 60 * time.Second},
	"studio.list_keywords":      {Method: http.MethodGet, Path: "/api/sites/{siteId}/seo/keywords", PathFields: []string{"siteId"}},
	"studio.add_keyword":        {Method: http.MethodPost, Path: "/api/sites/{siteId}/seo/keywords", PathFields: []string{"siteId"}},

	// Media
	"studio.list_media": {Method: http.MethodGet, Path: "/api/sites/{siteId}/media", PathFields: []string{"siteId"}},
	"studio.get_media":  {Method: http.MethodGet, Path: "/api/media/{mediaId}", PathFields: []string{"mediaId"}},

	// Settings
	"studio.get_settings":    {Method: http.MethodGet, Path: "/api/sites/{siteId}/settings", PathFields: []string{"siteId"}},
	"studio.update_settings": {Method: http.MethodPatch, Path: "/api/sites/{siteId}/settings", PathFields: []string{"siteId"}},

	// Publish / snapshots
	"studio.list_snapshots":    {Method: http.MethodGet, Path: "/api/sites/{siteId}/snapshots", PathFields: []string{"siteId"}},
	"studio.create_snapshot":   {Method: http.MethodPost, Path: "/api/sites/{siteId}/snapshots", PathFields: []string{"siteId"}},
	"studio.restore_snapshot":  {Method: http.MethodPost, Path: "/api/snapshots/{snapshotId}:restore", PathFields: []string{"snapshotId"}, Timeout: 60 * time.Second},

	// Forms
	"studio.list_forms":            {Method: http.MethodGet, Path: "/api/sites/{siteId}/forms", PathFields: []string{"siteId"}},
	"studio.get_form":              {Method: http.MethodGet, Path: "/api/forms/{formId}", PathFields: []string{"formId"}},
	"studio.list_form_submissions": {Method: http.MethodGet, Path: "/api/forms/{formId}/submissions", PathFields: []string{"formId"}},

	// Navigation
	"studio.get_navigation":    {Method: http.MethodGet, Path: "/api/sites/{siteId}/navigation", PathFields: []string{"siteId"}},
	"studio.update_navigation": {Method: http.MethodPut, Path: "/api/sites/{siteId}/navigation", PathFields: []string{"siteId"}},

	// Redirects
	"studio.list_redirects":   {Method: http.MethodGet, Path: "/api/sites/{siteId}/redirects", PathFields: []string{"siteId"}},
	"studio.create_redirect":  {Method: http.MethodPost, Path: "/api/sites/{siteId}/redirects", PathFields: []string{"siteId"}},
	"studio.update_redirect":  {Method: http.MethodPatch, Path: "/api/redirects/{redirectId}", PathFields: []string{"redirectId"}},
	"studio.delete_redirect":  {Method: http.MethodDelete, Path: "/api/redirects/{redirectId}", PathFields: []string{"redirectId"}},

	// Analytics
	"studio.get_analytics_overview": {Method: http.MethodGet, Path: "/api/sites/{siteId}/analytics:overview", PathFields: []string{"siteId"}},
	"studio.get_page_analytics":     {Method: http.MethodGet, Path: "/api/pages/{pageId}/analytics", PathFields: []string{"pageId"}},

	// Theme
	"studio.get_theme":             {Method: http.MethodGet, Path: "/api/sites/{siteId}/theme", PathFields: []string{"siteId"}},
	"studio.update_theme_tokens":   {Method: http.MethodPatch, Path: "/api/sites/{siteId}/theme/tokens", PathFields: []string{"siteId"}},

	// Staging
	"studio.get_staging_info":  {Method: http.MethodGet, Path: "/api/sites/{siteId}/staging", PathFields: []string{"siteId"}},
	"studio.promote_staging":   {Method: http.MethodPost, Path: "/api/sites/{siteId}/staging:promote", PathFields: []string{"siteId"}, Timeout: 60 * time.Second},

	// Roles / members (read-only list; invite/update/remove are NOT
	// bundled per work order §1.1).
	"studio.list_members": {Method: http.MethodGet, Path: "/api/sites/{siteId}/members", PathFields: []string{"siteId"}},

	// Billing
	"studio.get_credit_balance":   {Method: http.MethodGet, Path: "/api/billing/credits"},
	"studio.get_billing_overview": {Method: http.MethodGet, Path: "/api/billing/overview"},
}

// DefaultTimeout — per work order §R1S-2.6, non-scaffold calls get 30s.
const DefaultTimeout = 30 * time.Second

// DefaultRetries — 3× on 5xx with exponential backoff.
const DefaultRetries = 3

// HTTPTransport is the REST-over-HTTPS implementation.
type HTTPTransport struct {
	// BaseURL is the Studio API root (no trailing slash — normalized).
	BaseURL string

	// Scopes is forwarded as X-Studio-Scopes.
	Scopes string

	// TokenEnv is the env var name holding the bearer token. Empty
	// means do-not-authenticate (localhost dev only).
	TokenEnv string

	// Client is the underlying http.Client. Injected so tests can swap
	// it for an httptest.Server loopback; in production this is
	// http.DefaultClient with a sane Timeout: 0 (per-call context
	// governs timing).
	Client *http.Client

	// Publisher is the optional observability sink. nil is safe.
	Publisher EventPublisher

	// Retries is the max number of attempts on 5xx. 0 → DefaultRetries.
	Retries int

	// Now is overridable for tests so retry backoff doesn't stall the
	// suite. Production callers leave it nil (defaults to time.Now).
	Now func() time.Time

	// Sleep is overridable for tests. Production callers leave it nil.
	Sleep func(time.Duration)
}

// NewHTTPTransport builds the transport from a StudioConfig. Returns
// ErrStudioDisabled when the config is not enabled or is misconfigured
// for HTTP.
func NewHTTPTransport(cfg config.StudioConfig, client *http.Client, pub EventPublisher) (*HTTPTransport, error) {
	if !cfg.Enabled {
		return nil, ErrStudioDisabled
	}
	if cfg.ResolvedTransport() != config.StudioTransportHTTP {
		return nil, fmt.Errorf("studioclient: NewHTTPTransport called with transport=%q", cfg.ResolvedTransport())
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if client == nil {
		client = &http.Client{}
	}
	return &HTTPTransport{
		BaseURL:   strings.TrimRight(cfg.HTTP.BaseURL, "/"),
		Scopes:    cfg.ResolvedScopes(),
		TokenEnv:  cfg.HTTP.TokenEnv,
		Client:    client,
		Publisher: pub,
		Retries:   DefaultRetries,
	}, nil
}

// Name — Transport interface.
func (h *HTTPTransport) Name() string { return "http" }

// Close — Transport interface. HTTPTransport has no persistent state.
func (h *HTTPTransport) Close() error { return nil }

// Invoke sends the skill call over HTTP and returns the raw JSON body.
// See Transport.Invoke for contract.
func (h *HTTPTransport) Invoke(ctx context.Context, tool string, input any) (json.RawMessage, error) {
	start := h.now()
	spec, ok := toolSpecs[tool]
	if !ok {
		return nil, h.fail(tool, 0, nil, ErrStudioValidation, fmt.Errorf("unknown tool %q", tool), start)
	}

	inputMap, err := asMap(input)
	if err != nil {
		return nil, h.fail(tool, 0, nil, ErrStudioValidation, err, start)
	}

	// Materialize path + query from the input map. Path fields and
	// (for GETs) query fields are removed from the body; everything
	// else stays.
	path, remaining, err := renderPath(spec.Path, spec.PathFields, inputMap)
	if err != nil {
		return nil, h.fail(tool, 0, nil, ErrStudioValidation, err, start)
	}

	fullURL := h.BaseURL + path
	var reqBody []byte
	if spec.Method == http.MethodGet || spec.Method == http.MethodDelete {
		// Body-less verbs: push remaining fields (plus explicit query
		// fields) into the query string.
		q := url.Values{}
		for _, k := range spec.QueryFields {
			if v, ok := inputMap[k]; ok {
				q.Set(k, toQueryString(v))
				delete(remaining, k)
			}
		}
		for k, v := range remaining {
			q.Set(k, toQueryString(v))
		}
		if enc := q.Encode(); enc != "" {
			fullURL += "?" + enc
		}
	} else {
		// Body-carrying verbs: JSON-encode `remaining`.
		b, err := json.Marshal(remaining)
		if err != nil {
			return nil, h.fail(tool, 0, nil, ErrStudioValidation, err, start)
		}
		reqBody = b
	}

	timeout := spec.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}

	// The retry loop lives here rather than via http.Client hooks so
	// we can observe the 5xx body on each attempt.
	var lastBody []byte
	var lastStatus int
	retries := h.Retries
	if retries <= 0 {
		retries = DefaultRetries
	}

	for attempt := 0; attempt < retries; attempt++ {
		body, status, attemptErr := h.doOnce(ctx, spec.Method, fullURL, reqBody, timeout)
		if attemptErr != nil {
			return nil, h.fail(tool, status, body, classifyTransportErr(ctx, attemptErr), attemptErr, start)
		}
		if status >= 500 {
			lastBody = body
			lastStatus = status
			// Exponential backoff, capped at 4s. Skip on last attempt.
			if attempt < retries-1 {
				h.sleep(time.Duration(200*(1<<attempt)) * time.Millisecond)
				continue
			}
			break
		}
		if cause := classifyHTTPStatus(status); cause != nil {
			return nil, h.fail(tool, status, body, cause, nil, start)
		}
		// Success path.
		h.publish(InvocationEvent{
			Transport: "http",
			Tool:      tool,
			Status:    status,
			Duration:  h.now().Sub(start),
			OK:        true,
		})
		return json.RawMessage(body), nil
	}

	// Fell out of loop = all retries exhausted on 5xx.
	return nil, h.fail(tool, lastStatus, lastBody, ErrStudioServer, nil, start)
}

// doOnce performs exactly one HTTP round-trip with the given timeout.
// Returns (body, status, err). err is non-nil on DNS / dial / timeout /
// body-read failure; status is 0 in those cases.
func (h *HTTPTransport) doOnce(ctx context.Context, method, url string, body []byte, timeout time.Duration) ([]byte, int, error) {
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(callCtx, method, url, reader)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if h.Scopes != "" {
		req.Header.Set("X-Studio-Scopes", h.Scopes)
	}
	if h.TokenEnv != "" {
		if tok := os.Getenv(h.TokenEnv); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		} else {
			// Caller explicitly declared TokenEnv but it's unset: fail
			// pre-flight with ErrStudioAuth so operators see the root
			// cause instead of a confusing 401.
			return nil, 0, fmt.Errorf("env %q unset (StudioConfig.HTTP.TokenEnv)", h.TokenEnv)
		}
	}

	resp, err := h.Client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return b, resp.StatusCode, nil
}

// fail is the single exit point for error paths — builds the
// *StudioError, emits the observability record, and returns.
func (h *HTTPTransport) fail(tool string, status int, body []byte, cause error, underlying error, start time.Time) error {
	// Guard against the pre-flight TokenEnv-unset diagnostic leaking as
	// generic-underlying: the doOnce path synthesizes a fmt.Errorf with
	// the TokenEnv name. Promote it to ErrStudioAuth here.
	if underlying != nil && strings.Contains(underlying.Error(), "StudioConfig.HTTP.TokenEnv") {
		cause = ErrStudioAuth
	}
	se := &StudioError{
		Tool:        tool,
		Status:      status,
		BodyExcerpt: sanitizeBody(body),
		Cause:       cause,
		Underlying:  underlying,
	}
	h.publish(InvocationEvent{
		Transport: "http",
		Tool:      tool,
		Status:    status,
		Duration:  h.now().Sub(start),
		OK:        false,
		ErrorKind: errorKind(se),
	})
	return se
}

func (h *HTTPTransport) publish(ev InvocationEvent) {
	if h.Publisher != nil {
		h.Publisher.Publish(ev)
	}
}

func (h *HTTPTransport) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *HTTPTransport) sleep(d time.Duration) {
	if h.Sleep != nil {
		h.Sleep(d)
		return
	}
	time.Sleep(d)
}

// --- helpers ---

// asMap coerces the caller-provided input into a JSON object so path /
// query rendering can pull fields out. Accepts map[string]any verbatim;
// any other value round-trips through JSON.
func asMap(input any) (map[string]any, error) {
	if input == nil {
		return map[string]any{}, nil
	}
	if m, ok := input.(map[string]any); ok {
		// Shallow copy so we don't mutate caller's map.
		out := make(map[string]any, len(m))
		for k, v := range m {
			out[k] = v
		}
		return out, nil
	}
	b, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("marshal input: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("input must be a JSON object: %w", err)
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

// renderPath substitutes {field} tokens in path from the input map,
// consuming the matching keys. Returns the rendered path plus the
// remaining input fields (body / query candidates).
func renderPath(tmpl string, fields []string, input map[string]any) (string, map[string]any, error) {
	remaining := make(map[string]any, len(input))
	for k, v := range input {
		remaining[k] = v
	}
	path := tmpl
	for _, f := range fields {
		raw, ok := remaining[f]
		if !ok {
			return "", nil, fmt.Errorf("missing required path field %q", f)
		}
		s := toQueryString(raw)
		if s == "" {
			return "", nil, fmt.Errorf("path field %q is empty", f)
		}
		path = strings.ReplaceAll(path, "{"+f+"}", url.PathEscape(s))
		delete(remaining, f)
	}
	// Sanity check: no unfilled tokens left.
	if strings.Contains(path, "{") {
		return "", nil, fmt.Errorf("unfilled path template %q", path)
	}
	return path, remaining, nil
}

// toQueryString converts scalar values to their string form. Non-
// scalar values (maps, slices) round-trip through JSON.
func toQueryString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		// json.Unmarshal of numbers into any yields float64; trim the
		// trailing .0 when it looks like an integer.
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprintf("%g", t)
	case int, int32, int64:
		return fmt.Sprintf("%d", t)
	case nil:
		return ""
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

// classifyTransportErr maps a low-level net/http error to one of the
// sentinel causes.
func classifyTransportErr(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return ErrStudioTimeout
		}
		// Context cancelled by operator — classify as unavailable so
		// the degradation path fires.
		return ErrStudioUnavailable
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if urlErr.Timeout() {
			return ErrStudioTimeout
		}
	}
	// Missing env-var indirection is pre-flight-only; doOnce tags it.
	if strings.Contains(err.Error(), "StudioConfig.HTTP.TokenEnv") {
		return ErrStudioAuth
	}
	return ErrStudioUnavailable
}
