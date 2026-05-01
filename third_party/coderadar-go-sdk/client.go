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

const DefaultEndpoint = "https://ingest.coderadar.app/v1"

type ErrorOpts struct {
	Tags  map[string]string
	User  string
	Extra map[string]any
	Stack []string
}

type Client struct {
	apiKey         string
	baseURL        string
	httpClient     *http.Client
	serviceName    string
	environment    string
	gitSHA         string
	maxRetries     int
	retryBaseDelay time.Duration
}

type Option func(*Client)

func WithHTTPClient(c *http.Client) Option {
	return func(client *Client) { client.httpClient = c }
}

func WithServiceName(s string) Option {
	return func(client *Client) { client.serviceName = s }
}

func WithEnvironment(e string) Option {
	return func(client *Client) { client.environment = e }
}

func WithGitSHA(sha string) Option {
	return func(client *Client) { client.gitSHA = sha }
}

func WithRetry(maxRetries int, baseDelay time.Duration) Option {
	return func(client *Client) {
		client.maxRetries = maxRetries
		client.retryBaseDelay = baseDelay
	}
}

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

type errorEvent struct {
	Timestamp    string         `json:"timestamp"`
	ErrorType    string         `json:"error_type"`
	ErrorMessage string         `json:"error_message"`
	StackTrace   string         `json:"stack_trace,omitempty"`
	ServiceName  string         `json:"service_name,omitempty"`
	Environment  string         `json:"environment,omitempty"`
	GitSHA       string         `json:"git_sha,omitempty"`
	Framework    string         `json:"framework,omitempty"`
	UserID       string         `json:"user_id,omitempty"`
	Attributes   map[string]any `json:"attributes,omitempty"`
	Frames       []eventFrame   `json:"frames,omitempty"`
}

type eventFrame struct {
	File     string `json:"file"`
	Function string `json:"function,omitempty"`
	Line     int    `json:"line"`
	Column   int    `json:"column,omitempty"`
}

func (c *Client) CaptureError(ctx context.Context, err error, opts ErrorOpts) error {
	if err == nil {
		return errors.New("coderadar: nil error")
	}

	stack := opts.Stack
	if stack == nil {
		stack = autoStack()
	}

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
		Attributes:   mergeAttributes(opts.Tags, opts.Extra),
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

		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
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

func typeOf(err error) string {
	if err == nil {
		return ""
	}
	return strings.TrimPrefix(fmt.Sprintf("%T", err), "*")
}

func autoStack() []string {
	lines := strings.Split(string(debug.Stack()), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "goroutine ") {
			continue
		}
		out = append(out, line)
	}
	return out
}

func parseFrames(stack []string) []eventFrame {
	var frames []eventFrame
	for i := 0; i+1 < len(stack); i++ {
		fn := stack[i]
		loc := stack[i+1]
		if !strings.Contains(loc, ".go:") {
			continue
		}
		locOnly := loc
		if space := strings.Index(locOnly, " "); space > 0 {
			locOnly = locOnly[:space]
		}
		colon := strings.LastIndex(locOnly, ":")
		if colon < 0 {
			continue
		}
		var line int
		if _, err := fmt.Sscanf(locOnly[colon+1:], "%d", &line); err != nil {
			continue
		}
		frames = append(frames, eventFrame{
			File:     locOnly[:colon],
			Function: stripFnArgs(fn),
			Line:     line,
		})
		i++
	}
	return frames
}

func stripFnArgs(fn string) string {
	if idx := strings.LastIndex(fn, "("); idx > 0 {
		return fn[:idx]
	}
	return fn
}
