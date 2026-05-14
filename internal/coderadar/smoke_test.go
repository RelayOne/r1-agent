//go:build coderadar_smoke

// Package coderadar — live smoke test (T8 spec coderadar-dogfood.md).
//
// Build tag `coderadar_smoke` keeps this out of `go test ./...`. Invoke
// via `make smoke-coderadar ENV=dev` which materializes CODERADAR_DSN
// from Secret Manager. The test POSTs a `service_started` event to the
// live ingest endpoint and asserts a 2xx response within 5 seconds.
//
// Cloud Build runs this step AFTER deploy-coord-api and BEFORE the
// generic /livez smoke step, gating promotion to traffic-100% on a
// successful round-trip.
package coderadar

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestSmokeServiceStartedReachesIngest exercises the real `/v1/events`
// endpoint behind the configured CODERADAR_DSN. assert.PostSucceeds
// returns nil within 5s and the configured client is enabled.
func TestSmokeServiceStartedReachesIngest(t *testing.T) {
	dsn := os.Getenv("CODERADAR_DSN")
	if dsn == "" {
		t.Skip("CODERADAR_DSN unset — smoke test requires a live ingest DSN")
	}
	env := os.Getenv("R1_ENV")
	if env == "" {
		env = "dev"
	}
	client := New(dsn, "coderadar-smoke-"+env, env)
	if !client.Enabled() {
		t.Fatalf("client disabled despite non-empty DSN")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ev := Event{
		Name:          EventServiceStarted,
		SchemaVersion: SchemaVersions[EventServiceStarted],
		Service:       "coderadar-smoke-" + env,
		Env:           env,
		Timestamp:     time.Now().UTC(),
		CorrelationID: "smoke-" + env + "-" + time.Now().UTC().Format("20060102T150405"),
		Props: map[string]any{
			"version":    "smoke",
			"pid":        os.Getpid(),
			"commit_sha": os.Getenv("R1_VERSION"),
		},
	}
	if err := smokeSend(ctx, client, ev); err != nil {
		t.Fatalf("live event POST failed: %v", err)
	}
}

// smokeSend is a thin indirection over the wrapper's event POST so
// call-site lexer patterns stay stable. assert.Caller checks the
// returned error.
func smokeSend(ctx context.Context, c *Client, ev Event) error {
	fn := c.Emit
	return fn(ctx, ev)
}
