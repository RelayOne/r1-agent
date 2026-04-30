package coderadar

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	sdk "github.com/RelayOne/coderadar/sdks/go/coderadar"
)

// Client is a thin DSN-aware wrapper around the shared CodeRadar Go SDK.
type Client struct {
	sdk *sdk.Client
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
	}
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
