package coderadar

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

	sdk "github.com/RelayOne/coderadar/sdks/go/coderadar"
)

// Client is a thin DSN-aware wrapper around the shared CodeRadar Go SDK.
type Client struct {
	sdk         *sdk.Client
	apiKey      string
	baseURL     string
	serviceName string
	environment string
	httpClient  *http.Client
}

// FromEnv configures a client from CODERADAR_DSN. Missing DSNs return a no-op client.
func FromEnv(serviceName string) *Client {
	return New(os.Getenv("CODERADAR_DSN"), serviceName, detectEnvironment())
}

// New constructs a DSN-aware client. Empty DSNs return a no-op client.
func New(dsn, serviceName, environment string) *Client {
	apiKey, baseURL, ok := parseDSN(dsn)
	if !ok {
		return &Client{}
	}
	return &Client{
		sdk: sdk.NewClient(
			apiKey,
			baseURL,
			sdk.WithServiceName(serviceName),
			sdk.WithEnvironment(environment),
		),
		apiKey:      apiKey,
		baseURL:     baseURL,
		serviceName: serviceName,
		environment: environment,
		// 5s timeout — observability must not queue behind slow ingest.
		// Separate from the SDK's 10s default for error capture.
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}
}

// ServiceName returns the configured service tag (for subscriber use).
func (c *Client) ServiceName() string {
	if c == nil {
		return ""
	}
	return c.serviceName
}

// Environment returns the configured environment tag.
func (c *Client) Environment() string {
	if c == nil {
		return ""
	}
	return c.environment
}

// Enabled reports whether the client will forward events.
func (c *Client) Enabled() bool {
	return c != nil && c.sdk != nil
}

// CaptureError reports err with optional structured attributes.
func (c *Client) CaptureError(ctx context.Context, err error, extra map[string]any) error {
	if !c.Enabled() || err == nil {
		return nil
	}
	return c.sdk.CaptureError(ctx, err, sdk.ErrorOpts{Extra: extra})
}

// CaptureRecovered converts a recovered panic payload into an error event.
func (c *Client) CaptureRecovered(ctx context.Context, recovered any, extra map[string]any) error {
	if !c.Enabled() || recovered == nil {
		return nil
	}
	return c.CaptureError(ctx, panicToError(recovered), extra)
}

func detectEnvironment() string {
	for _, key := range []string{"CODERADAR_ENV", "R1_ENV", "NODE_ENV"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return "development"
}

func parseDSN(dsn string) (apiKey string, baseURL string, ok bool) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return "", "", false
	}
	if !strings.Contains(dsn, "://") {
		return dsn, sdk.DefaultEndpoint, true
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

func panicToError(recovered any) error {
	switch value := recovered.(type) {
	case error:
		return value
	case string:
		return errors.New(value)
	default:
		return fmt.Errorf("panic: %v", value)
	}
}

// eventPayload is the POST body shape for /v1/events. Mirrors the
// upstream apps/ingest-api/src/routes/events.ts contract; kept private
// so the subscriber's typed Event struct is the public surface.
type eventPayload struct {
	Timestamp     string         `json:"timestamp"`
	Level         string         `json:"level"`
	Message       string         `json:"message"`
	ServiceName   string         `json:"service_name,omitempty"`
	Environment   string         `json:"environment,omitempty"`
	EventID       string         `json:"event_id,omitempty"`
	SchemaVersion string         `json:"schema_version,omitempty"`
	CorrelationID string         `json:"correlation_id,omitempty"`
	TenantID      string         `json:"tenant_id,omitempty"`
	LatencyMs     int64          `json:"latency_ms,omitempty"`
	Tags          map[string]any `json:"tags,omitempty"`
	RuntimeCtx    map[string]any `json:"runtime_ctx,omitempty"`
}

// eventsBatch is the wire shape POSTed to /v1/events.
type eventsBatch struct {
	Events []eventPayload `json:"events"`
}

// Emit POSTs a single canonical Event to the configured CodeRadar
// ingest endpoint (/v1/events). Returns nil on success, an error on
// transport / non-2xx / context cancellation. No-op (returns nil)
// when the client is disabled (empty DSN).
//
// Emit is the dogfood event path. The error-capture path (CaptureError)
// stays on the upstream SDK and continues to POST to /v1/errors.
func (c *Client) Emit(ctx context.Context, ev Event) error {
	if !c.Enabled() {
		return nil
	}
	return c.EmitBatch(ctx, []Event{ev})
}

// EmitBatch POSTs a batch of canonical Events to /v1/events in a single
// request. Used by the bus subscriber's drain loop to amortize HTTP
// overhead.
func (c *Client) EmitBatch(ctx context.Context, events []Event) error {
	if !c.Enabled() || len(events) == 0 {
		return nil
	}
	batch := eventsBatch{Events: make([]eventPayload, 0, len(events))}
	for _, ev := range events {
		batch.Events = append(batch.Events, eventToPayload(ev, c.serviceName, c.environment))
	}
	body, err := json.Marshal(batch)
	if err != nil {
		return fmt.Errorf("coderadar: marshal events batch: %w", err)
	}
	endpoint := c.baseURL + "/events"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("coderadar: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("User-Agent", "r1-coderadar/1.0")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("coderadar: post events: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("coderadar: events ingest rejected (status %d)", resp.StatusCode)
	}
	return nil
}

// eventToPayload converts the typed envelope into the wire shape.
// service/env are the client-level defaults that get filled in when
// the per-event values are empty.
func eventToPayload(ev Event, svc, env string) eventPayload {
	ts := ev.Timestamp
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	sv := ev.SchemaVersion
	if sv == "" {
		if v, ok := SchemaVersions[ev.Name]; ok {
			sv = v
		}
	}
	service := ev.Service
	if service == "" {
		service = svc
	}
	environment := ev.Env
	if environment == "" {
		environment = env
	}
	return eventPayload{
		Timestamp:     ts.UTC().Format(time.RFC3339Nano),
		Level:         "info",
		Message:       ev.Name,
		ServiceName:   service,
		Environment:   environment,
		EventID:       ev.EventID,
		SchemaVersion: sv,
		CorrelationID: ev.CorrelationID,
		TenantID:      ev.TenantID,
		LatencyMs:     ev.LatencyMs,
		Tags:          stringifyProps(ev.Props),
		RuntimeCtx: map[string]any{
			"event_name": ev.Name,
		},
	}
}

func stringifyProps(props map[string]any) map[string]any {
	if len(props) == 0 {
		return nil
	}
	out := make(map[string]any, len(props))
	for key, value := range props {
		switch typed := value.(type) {
		case string:
			out[key] = typed
		case fmt.Stringer:
			out[key] = typed.String()
		case nil:
			out[key] = ""
		default:
			out[key] = fmt.Sprint(value)
		}
	}
	return out
}
