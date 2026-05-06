// coderadar.go — minimal CodeRadar SDK port for the SaaS Go services.
// Captures errors + breadcrumbs to the in-house CodeRadar instance.
//
// Mirrors third_party/coderadar-go-sdk's Client surface (small subset).
package tracking

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// CodeRadar captures errors + messages to the in-house CodeRadar
// observability service. Configured by DSN:
//
//	CODERADAR_DSN=https://<api-key>@api.coderadar.app
type CodeRadar struct {
	APIKey      string
	BaseURL     string
	ServiceName string
	Env         string
	Version     string
	HTTP        *http.Client
	Now         func() time.Time
	enabled     bool
}

// NewCodeRadar parses the DSN and returns a client; no-op when DSN is empty.
func NewCodeRadar(dsn, serviceName, env, version string) *CodeRadar {
	apiKey, baseURL, ok := parseDSN(dsn)
	if !ok {
		return &CodeRadar{enabled: false}
	}
	return &CodeRadar{
		APIKey:      apiKey,
		BaseURL:     baseURL,
		ServiceName: serviceName,
		Env:         env,
		Version:     version,
		HTTP:        &http.Client{Timeout: 10 * time.Second},
		Now:         time.Now,
		enabled:     true,
	}
}

// CaptureError sends a Go error to CodeRadar with optional context.
func (c *CodeRadar) CaptureError(ctx context.Context, err error, contextProps map[string]any) {
	if c == nil || !c.enabled || err == nil {
		return
	}
	body := map[string]any{
		"service_name": c.ServiceName,
		"env":          c.Env,
		"version":      c.Version,
		"timestamp":    c.Now().UTC().Format(time.RFC3339Nano),
		"level":        "error",
		"message":      err.Error(),
		"context":      contextProps,
	}
	_ = c.postIgnoreErr(ctx, "/v1/errors", body)
}

// CaptureMessage sends a structured log line at the given level.
func (c *CodeRadar) CaptureMessage(ctx context.Context, level, msg string, contextProps map[string]any) {
	if c == nil || !c.enabled {
		return
	}
	body := map[string]any{
		"service_name": c.ServiceName,
		"env":          c.Env,
		"version":      c.Version,
		"timestamp":    c.Now().UTC().Format(time.RFC3339Nano),
		"level":        level,
		"message":      msg,
		"context":      contextProps,
	}
	_ = c.postIgnoreErr(ctx, "/v1/errors", body)
}

// Enabled reports whether captures are forwarded.
func (c *CodeRadar) Enabled() bool {
	if c == nil {
		return false
	}
	return c.enabled
}

func (c *CodeRadar) postIgnoreErr(ctx context.Context, path string, body any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-coderadar-key", c.APIKey)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("coderadar %d", resp.StatusCode)
	}
	return nil
}

// parseDSN — same logic as internal/coderadar/coderadar.go.
func parseDSN(dsn string) (apiKey string, baseURL string, ok bool) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return "", "", false
	}
	if !strings.Contains(dsn, "://") {
		// Bare key — use the canonical r1 ingest endpoint.
		return dsn, "https://api.coderadar.app/v1", true
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Host == "" {
		return "", "", false
	}
	apiKey = u.User.Username()
	if apiKey == "" {
		return "", "", false
	}
	u.User = nil
	baseURL = strings.TrimRight(u.String(), "/")
	switch {
	case strings.HasSuffix(baseURL, "/v1/errors"):
		baseURL = strings.TrimSuffix(baseURL, "/errors")
	case !strings.HasSuffix(baseURL, "/v1"):
		baseURL += "/v1"
	}
	return apiKey, baseURL, true
}
