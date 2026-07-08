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

type codeRadarTrackBatch struct {
	Events []codeRadarTrackEvent `json:"events"`
}

type codeRadarTrackEvent struct {
	EventName  string         `json:"event_name"`
	DistinctID string         `json:"distinct_id"`
	Properties map[string]any `json:"properties,omitempty"`
	Timestamp  string         `json:"timestamp"`
	SessionID  string         `json:"session_id,omitempty"`
	Device     string         `json:"device,omitempty"`
	UserAgent  string         `json:"user_agent,omitempty"`
	Region     string         `json:"region,omitempty"`
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
	_ = c.postIgnoreErr(ctx, "/errors", body)
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
	_ = c.postIgnoreErr(ctx, "/errors", body)
}

// Track sends a product-analytics event to CodeRadar's CR-1 /v1/track
// ingest route. Reserved envelope fields are lifted out of props so the
// hosted R1 surfaces match the shared browser/backend contract.
func (c *CodeRadar) Track(ctx context.Context, distinctID, eventName string, props map[string]any) error {
	if c == nil || !c.enabled {
		return nil
	}
	event := codeRadarTrackEvent{
		EventName:  eventName,
		DistinctID: distinctID,
		Timestamp:  c.Now().UTC().Format(time.RFC3339Nano),
	}
	if event.DistinctID == "" {
		event.DistinctID = "anonymous"
	}
	event.Properties = copyPropsWithoutReserved(props)
	if value, ok := stringProp(props, "timestamp"); ok {
		event.Timestamp = value
	}
	if value, ok := stringProp(props, "session_id"); ok {
		event.SessionID = value
	}
	if value, ok := stringProp(props, "device"); ok {
		event.Device = value
	}
	if value, ok := stringProp(props, "user_agent"); ok {
		event.UserAgent = value
	}
	if value, ok := stringProp(props, "region"); ok {
		event.Region = value
	}
	return c.postTrack(ctx, codeRadarTrackBatch{Events: []codeRadarTrackEvent{event}})
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

func (c *CodeRadar) postTrack(ctx context.Context, body codeRadarTrackBatch) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal coderadar analytics body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/track", bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("coderadar analytics request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("coderadar analytics do: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		errBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("coderadar analytics %d: %s", resp.StatusCode, errBody)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func copyPropsWithoutReserved(props map[string]any) map[string]any {
	if len(props) == 0 {
		return nil
	}
	out := make(map[string]any, len(props))
	for key, value := range props {
		switch key {
		case "timestamp", "session_id", "device", "user_agent", "region":
			continue
		default:
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func stringProp(props map[string]any, key string) (string, bool) {
	if len(props) == 0 {
		return "", false
	}
	value, ok := props[key].(string)
	if !ok || value == "" {
		return "", false
	}
	return value, true
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
