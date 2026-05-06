// customerio.go — Customer.io track API client. Identify / Track / Event.
package tracking

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

// CustomerIO implements retention + lifecycle email triggers. The track
// API base is region-specific:
//
//	us → https://track.customer.io/api
//	eu → https://track-eu.customer.io/api
//
// Auth is HTTP Basic with siteID:apiKey.
type CustomerIO struct {
	SiteID  string
	APIKey  string
	BaseURL string
	HTTP    *http.Client
	Now     func() time.Time
	enabled bool
}

// NewCustomerIO returns a client; no-op when siteID or apiKey is empty.
func NewCustomerIO(siteID, apiKey, region string) *CustomerIO {
	base := "https://track.customer.io/api"
	if strings.EqualFold(region, "eu") {
		base = "https://track-eu.customer.io/api"
	}
	return &CustomerIO{
		SiteID:  siteID,
		APIKey:  apiKey,
		BaseURL: base,
		HTTP:    &http.Client{Timeout: 10 * time.Second},
		Now:     time.Now,
		enabled: siteID != "" && apiKey != "",
	}
}

// Identify upserts a customer. customerID must be stable (we use the
// JWT sub claim as the identity).
func (c *CustomerIO) Identify(ctx context.Context, customerID string, traits map[string]any) error {
	if c == nil || !c.enabled {
		return nil
	}
	if traits == nil {
		traits = map[string]any{}
	}
	if _, ok := traits["created_at"]; !ok {
		traits["created_at"] = c.Now().Unix()
	}
	return c.put(ctx, fmt.Sprintf("/v1/customers/%s", customerID), traits)
}

// Track sends an event for a customer. Used to drive lifecycle email
// (e.g. signup → activation → first-session funnel).
func (c *CustomerIO) Track(ctx context.Context, customerID, eventName string, data map[string]any) error {
	if c == nil || !c.enabled {
		return nil
	}
	body := map[string]any{
		"name":      eventName,
		"data":      data,
		"timestamp": c.Now().Unix(),
	}
	return c.post(ctx, fmt.Sprintf("/v1/customers/%s/events", customerID), body)
}

// Anonymous sends an event for a not-yet-identified visitor. Used for
// pre-signup funnel events.
func (c *CustomerIO) Anonymous(ctx context.Context, eventName string, data map[string]any) error {
	if c == nil || !c.enabled {
		return nil
	}
	body := map[string]any{
		"name":      eventName,
		"data":      data,
		"timestamp": c.Now().Unix(),
	}
	return c.post(ctx, "/v1/events", body)
}

// Enabled reports whether the client will actually send events.
func (c *CustomerIO) Enabled() bool {
	if c == nil {
		return false
	}
	return c.enabled
}

func (c *CustomerIO) post(ctx context.Context, path string, body any) error {
	return c.do(ctx, http.MethodPost, path, body)
}

func (c *CustomerIO) put(ctx context.Context, path string, body any) error {
	return c.do(ctx, http.MethodPut, path, body)
}

func (c *CustomerIO) do(ctx context.Context, method, path string, body any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal customerio body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("customerio request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(c.SiteID, c.APIKey)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("customerio do: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		errBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("customerio %d: %s", resp.StatusCode, errBody)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}
