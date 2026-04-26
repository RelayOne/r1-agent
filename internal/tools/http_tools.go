// http_tools.go — http_request tool handler.
//
// T-R1P-017: Declarative HTTP request tool. Supports GET/POST/PUT/PATCH/DELETE/HEAD
// with custom headers, request body, and configurable timeout.
//
// Rationale: web_fetch is read-only (GET + HTML stripping). http_request exposes
// the full HTTP surface: method choice, headers, body, raw response (no HTML
// stripping), and response header inspection. Required for interacting with JSON
// APIs, webhooks, and authenticated endpoints.
//
// Security: URL scheme must be http or https. No redirect to file:// or data://.
// Body is capped at 1MB. Timeout is bounded at 120s.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// maxHTTPResponseBody caps the response body at 1MB.
	maxHTTPResponseBody = 1 * 1024 * 1024
	// defaultHTTPTimeout is the default http_request timeout.
	defaultHTTPTimeout = 30 * time.Second
	// maxHTTPTimeout is the maximum http_request timeout.
	maxHTTPTimeout = 120 * time.Second
)

// httpClient is a package-level client reused across calls.
// Tests may substitute it via httpClientOverride.
var httpClientOverride *http.Client

// handleHTTPRequest implements the http_request tool (T-R1P-017).
func (r *Registry) handleHTTPRequest(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Method  string            `json:"method"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
		Timeout int               `json:"timeout"` // milliseconds
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if strings.TrimSpace(args.URL) == "" {
		return "", fmt.Errorf("url is required")
	}

	method := strings.ToUpper(strings.TrimSpace(args.Method))
	if method == "" {
		method = http.MethodGet
	}
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodHead:
	default:
		return "", fmt.Errorf("unsupported HTTP method %q: must be GET|POST|PUT|PATCH|DELETE|HEAD", method)
	}

	// Validate scheme.
	urlLower := strings.ToLower(args.URL)
	if !strings.HasPrefix(urlLower, "http://") && !strings.HasPrefix(urlLower, "https://") {
		return "", fmt.Errorf("url must use http or https scheme")
	}

	timeout := defaultHTTPTimeout
	if args.Timeout > 0 {
		timeout = time.Duration(args.Timeout) * time.Millisecond
		if timeout > maxHTTPTimeout {
			timeout = maxHTTPTimeout
		}
	}

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var bodyReader io.Reader
	if args.Body != "" {
		bodyReader = bytes.NewBufferString(args.Body)
	}

	req, err := http.NewRequestWithContext(reqCtx, method, args.URL, bodyReader) //nolint:noctx
	if err != nil {
		return "", fmt.Errorf("http_request: build request: %w", err)
	}

	for k, v := range args.Headers {
		req.Header.Set(k, v)
	}
	// Default Content-Type for bodies without an explicit header.
	if args.Body != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	client := httpClientOverride
	if client == nil {
		client = &http.Client{
			Timeout: timeout,
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		if reqCtx.Err() != nil {
			return fmt.Sprintf("http_request error: timed out after %v", timeout), nil
		}
		return fmt.Sprintf("http_request error: %v", err), nil
	}
	defer resp.Body.Close()

	rawBody, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPResponseBody+1))
	if err != nil {
		return fmt.Sprintf("http_request error reading body: %v", err), nil
	}
	truncated := false
	if len(rawBody) > maxHTTPResponseBody {
		rawBody = rawBody[:maxHTTPResponseBody]
		truncated = true
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "HTTP %d %s\n", resp.StatusCode, resp.Status)
	// Selected response headers.
	for _, key := range []string{"Content-Type", "Content-Length", "Location", "X-Request-Id"} {
		if v := resp.Header.Get(key); v != "" {
			fmt.Fprintf(&sb, "%s: %s\n", key, v)
		}
	}
	sb.WriteString("\n")
	sb.Write(rawBody)
	if truncated {
		sb.WriteString("\n... [body truncated at 1MB]")
	}
	return sb.String(), nil
}
