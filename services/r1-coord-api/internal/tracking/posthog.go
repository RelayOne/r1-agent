// Package tracking integrates the r1 SaaS surfaces with three vendor stacks:
//
//   - PostHog: product analytics, conversion funnels, A/B, session replay
//   - Customer.io: retention + lifecycle email
//   - CodeRadar: in-house error tracking + dogfood
//
// Each integration is enabled by env: when the credentials are missing,
// the corresponding client is a no-op. This keeps dev environments cheap
// and lets staging/prod add credentials independently.
//
// Env contract:
//
//	POSTHOG_API_KEY        — phc_* (project key); enables Capture/Identify
//	POSTHOG_HOST           — https://us.posthog.com (default) or self-hosted
//	CUSTOMERIO_SITE_ID     — track.customer.io site id; enables Identify/Track
//	CUSTOMERIO_API_KEY     — track.customer.io API key
//	CUSTOMERIO_REGION      — us (default) | eu
//	CODERADAR_DSN          — full DSN (parses key + base URL); enables Capture
//
// Drift contract: vendor APIs change. We pin to the documented public
// surface as of 2026-05-04 and abstract behind interfaces so a vendor
// swap (PostHog → Mixpanel, Customer.io → Loops) is a single-file change.
package tracking

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// PostHog implements product analytics. Capture(event, props) lands in
// the PostHog "events" stream; Identify(uid, props) merges into the
// "persons" table. The /capture endpoint accepts both event types in a
// uniform shape per https://posthog.com/docs/api/capture.
type PostHog struct {
	APIKey  string
	Host    string
	HTTP    *http.Client
	Now     func() time.Time
	mu      sync.Mutex
	enabled bool
}

// NewPostHog returns a client; when apiKey is empty, the client is a
// no-op (Capture/Identify return nil).
func NewPostHog(apiKey, host string) *PostHog {
	if host == "" {
		host = "https://us.posthog.com"
	}
	return &PostHog{
		APIKey:  apiKey,
		Host:    strings.TrimRight(host, "/"),
		HTTP:    &http.Client{Timeout: 10 * time.Second},
		Now:     time.Now,
		enabled: apiKey != "",
	}
}

// Capture records an event for a distinct id (sub from JWT, anonymous
// id, or session id).
func (p *PostHog) Capture(ctx context.Context, distinctID, event string, props map[string]any) error {
	if p == nil || !p.enabled {
		return nil
	}
	body := map[string]any{
		"api_key":     p.APIKey,
		"event":       event,
		"distinct_id": distinctID,
		"properties":  props,
		"timestamp":   p.Now().UTC().Format(time.RFC3339Nano),
	}
	return p.post(ctx, "/capture/", body)
}

// Identify merges person properties for a distinct id.
func (p *PostHog) Identify(ctx context.Context, distinctID string, traits map[string]any) error {
	if p == nil || !p.enabled {
		return nil
	}
	props := map[string]any{"$set": traits}
	body := map[string]any{
		"api_key":     p.APIKey,
		"event":       "$identify",
		"distinct_id": distinctID,
		"properties":  props,
		"timestamp":   p.Now().UTC().Format(time.RFC3339Nano),
	}
	return p.post(ctx, "/capture/", body)
}

// Enabled reports whether the client will actually send events. False
// when no API key was provided.
func (p *PostHog) Enabled() bool {
	if p == nil {
		return false
	}
	return p.enabled
}

func (p *PostHog) post(ctx context.Context, path string, body any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal posthog body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Host+path, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("posthog request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("posthog do: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		errBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("posthog %d: %s", resp.StatusCode, errBody)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}
