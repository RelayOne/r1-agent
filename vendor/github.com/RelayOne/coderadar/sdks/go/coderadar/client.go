// Package coderadar is the Go SDK for the CodeRadar error monitoring service.
//
// Basic usage:
//
//	client := coderadar.NewClient(os.Getenv("CODERADAR_API_KEY"), "https://ingest.coderadar.app/v1")
//	err := client.CaptureError(ctx, someError, coderadar.ErrorOpts{
//	    Tags: map[string]string{"feature": "checkout"},
//	    User: "user-123",
//	})
//
// The schema sent on the wire matches the canonical Python SDK and the
// /v1/errors endpoint defined by apps/ingest-api.
package coderadar

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime/debug"
	"strings"
	"time"
)

// DefaultEndpoint is the production CodeRadar ingest base URL.
const DefaultEndpoint = "https://ingest.coderadar.app/v1"

// ErrorOpts is per-call enrichment data attached to a captured error.
type ErrorOpts struct {
	// Tags are flat string key/value pairs (e.g. {"feature": "checkout"}).
	Tags map[string]string
	// User identifies the affected end user (id, email, etc.).
	User string
	// Extra is free-form structured context (typed values are split server-side).
	Extra map[string]any
	// Stack overrides the auto-captured stack trace (one line per frame).
	Stack []string
}

// Client is a thread-safe CodeRadar ingest client.
type Client struct {
	apiKey      string
	baseURL     string
	httpClient  *http.Client
	serviceName string
	environment string
	gitSHA      string

	// retry policy
	maxRetries     int
	retryBaseDelay time.Duration
}

// Option mutates a Client during construction.
type Option func(*Client)

// WithHTTPClient overrides the default HTTP client (useful for tests).
func WithHTTPClient(c *http.Client) Option {
	return func(client *Client) { client.httpClient = c }
}

// WithServiceName tags every event with the given service identifier.
func WithServiceName(s string) Option {
	return func(client *Client) { client.serviceName = s }
}

// WithEnvironment tags every event with the given environment (prod/staging/etc.).
func WithEnvironment(e string) Option {
	return func(client *Client) { client.environment = e }
}

// WithGitSHA tags every event with the given commit SHA.
func WithGitSHA(sha string) Option {
	return func(client *Client) { client.gitSHA = sha }
}

// WithRetry overrides the default retry policy.
func WithRetry(maxRetries int, baseDelay time.Duration) Option {
	return func(client *Client) {
		client.maxRetries = maxRetries
		client.retryBaseDelay = baseDelay
	}
}

// NewClient constructs a Client. baseURL should not include a trailing slash.
func NewClient(apiKey, baseURL string, opts ...Option) *Client {
	if baseURL == "" {
		baseURL = DefaultEndpoint
	}
	baseURL = strings.TrimRight(baseURL, "/")

	c := &Client{
		apiKey:         apiKey,
		baseURL:        baseURL,
		httpClient:     &http.Client{Timeout: 10 * time.Second},
		environment:    "production",
		maxRetries:     3,
		retryBaseDelay: 200 * time.Millisecond,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// errorEvent matches the Zod schema in apps/ingest-api/src/schemas/error-event.ts.
type errorEvent struct {
	Timestamp        string         `json:"timestamp"`
	ErrorType        string         `json:"error_type"`
	ErrorMessage     string         `json:"error_message"`
	StackTrace       string         `json:"stack_trace,omitempty"`
	ServiceName      string         `json:"service_name,omitempty"`
	Environment      string         `json:"environment,omitempty"`
	GitSHA           string         `json:"git_sha,omitempty"`
	DeployVersion    string         `json:"deploy_version,omitempty"`
	Framework        string         `json:"framework,omitempty"`
	FrameworkVersion string         `json:"framework_version,omitempty"`
	HTTPMethod       string         `json:"http_method,omitempty"`
	HTTPPath         string         `json:"http_path,omitempty"`
	HTTPStatus       int            `json:"http_status,omitempty"`
	UserID           string         `json:"user_id,omitempty"`
	Attributes       map[string]any `json:"attributes,omitempty"`
	Frames           []eventFrame   `json:"frames,omitempty"`
}

type eventFrame struct {
	File     string `json:"file"`
	Function string `json:"function,omitempty"`
	Line     int    `json:"line"`
	Column   int    `json:"column,omitempty"`
}

// CaptureError sends err to CodeRadar. Honors ctx cancellation and retries on 5xx.
func (c *Client) CaptureError(ctx context.Context, err error, opts ErrorOpts) error {
	if err == nil {
		return errors.New("coderadar: nil error")
	}

	stack := opts.Stack
	if stack == nil {
		stack = autoStack()
	}

	attrs := mergeAttributes(opts.Tags, opts.Extra)

	event := errorEvent{
		Timestamp:    time.Now().UTC().Format(time.RFC3339Nano),
		ErrorType:    typeOf(err),
		ErrorMessage: err.Error(),
		StackTrace:   strings.Join(stack, "\n"),
		ServiceName:  c.serviceName,
		Environment:  c.environment,
		GitSHA:       c.gitSHA,
		Framework:    "go",
		UserID:       opts.User,
		Attributes:   attrs,
		Frames:       parseFrames(stack),
	}

	body, marshalErr := json.Marshal(event)
	if marshalErr != nil {
		return fmt.Errorf("coderadar: marshal event: %w", marshalErr)
	}

	return c.send(ctx, body)
}

func (c *Client) send(ctx context.Context, body []byte) error {
	url := c.baseURL + "/errors"

	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if reqErr != nil {
			return fmt.Errorf("coderadar: build request: %w", reqErr)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-coderadar-key", c.apiKey)
		req.Header.Set("User-Agent", "coderadar-go/0.1")

		resp, sendErr := c.httpClient.Do(req)
		if sendErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			lastErr = sendErr
			if !c.sleepBackoff(ctx, attempt) {
				return ctx.Err()
			}
			continue
		}

		// Drain & close to allow connection reuse.
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		// 4xx (except 429): client error, don't retry.
		if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
			return fmt.Errorf("coderadar: ingest rejected (status %d)", resp.StatusCode)
		}
		lastErr = fmt.Errorf("coderadar: ingest failed (status %d)", resp.StatusCode)
		if !c.sleepBackoff(ctx, attempt) {
			return ctx.Err()
		}
	}
	return lastErr
}

// sleepBackoff blocks for the next retry delay, returning false if ctx is cancelled.
func (c *Client) sleepBackoff(ctx context.Context, attempt int) bool {
	delay := c.retryBaseDelay * (1 << attempt)
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// mergeAttributes flattens tags + extra into a single attributes map. Tags
// always win over Extra on collision.
func mergeAttributes(tags map[string]string, extra map[string]any) map[string]any {
	if len(tags) == 0 && len(extra) == 0 {
		return nil
	}
	out := make(map[string]any, len(tags)+len(extra))
	for k, v := range extra {
		out[k] = v
	}
	for k, v := range tags {
		out[k] = v
	}
	return out
}

// typeOf returns a string suitable for the error_type field.
func typeOf(err error) string {
	if err == nil {
		return ""
	}
	t := fmt.Sprintf("%T", err)
	// Drop pointer asterisks for readability ("*errors.errorString" -> "errors.errorString").
	t = strings.TrimPrefix(t, "*")
	return t
}

// autoStack returns a one-line-per-frame stack starting at the caller of the SDK.
func autoStack() []string {
	raw := string(debug.Stack())
	lines := strings.Split(raw, "\n")
	// Drop the goroutine header line and the runtime/debug frame.
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if strings.HasPrefix(l, "goroutine ") {
			continue
		}
		out = append(out, l)
	}
	return out
}

// parseFrames extracts (file, line) tuples from a stack-trace string slice.
// Best-effort: tolerates non-standard input by skipping unparseable lines.
func parseFrames(stack []string) []eventFrame {
	var frames []eventFrame
	// debug.Stack() emits paired lines: function name, then "\tfile.go:NN +0xAB".
	for i := 0; i+1 < len(stack); i++ {
		fn := stack[i]
		loc := stack[i+1]
		if !strings.Contains(loc, ".go:") {
			continue
		}
		// loc looks like "/path/file.go:42 +0x1f" — peel off offset, then split.
		locOnly := loc
		if sp := strings.Index(locOnly, " "); sp > 0 {
			locOnly = locOnly[:sp]
		}
		colon := strings.LastIndex(locOnly, ":")
		if colon < 0 {
			continue
		}
		file := locOnly[:colon]
		var line int
		if _, scanErr := fmt.Sscanf(locOnly[colon+1:], "%d", &line); scanErr != nil {
			continue
		}
		frames = append(frames, eventFrame{
			File:     file,
			Function: stripFnArgs(fn),
			Line:     line,
		})
		i++ // consume the location line
	}
	return frames
}

func stripFnArgs(fn string) string {
	if idx := strings.LastIndex(fn, "("); idx > 0 {
		return fn[:idx]
	}
	return fn
}
