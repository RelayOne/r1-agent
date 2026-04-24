// Package cloud implements the consumer side of Contract
// Group H (CloudSwarm Stoke Cloud API) from the 2026-04-16
// Good Ventures Cross-Product Contract Bible.
//
// Stoke CONSUMES Stoke Cloud for:
//   - H1 POST /v1/sessions (submit a session)
//   - H2 GET  /v1/sessions/:id (poll status)
//   - H3 GET  /v1/sessions/:id/events?since=<ISO8601> (stream events)
//   - H4 POST /v1/auth/register (one-time opt-in)
//
// Registration is strictly OPT-IN. Stoke never requires a
// cloud linkage; every feature in the self-hosted binary
// remains available without `stoke cloud register`.
//
// After successful registration the client credential +
// endpoint are persisted at ~/.stoke/cloud.json so later
// commands (including the `--cloud` flag on `stoke sow`)
// can pick them up automatically.
package cloud

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/RelayOne/r1/internal/r1env"
)

// ConfigFile is the on-disk record of a successful cloud
// registration. Stored at ~/.stoke/cloud.json.
type ConfigFile struct {
	// Endpoint is the base URL of the Stoke Cloud API
	// (e.g., "https://cloud.stoke.dev"). No trailing slash.
	Endpoint string `json:"endpoint"`

	// APIKey is the persistent device credential returned by
	// /v1/auth/register (or accepted as the registration
	// token itself — the API echoes it back in the register
	// response). Transmitted in every H1/H2/H3 call as a
	// Bearer token.
	APIKey string `json:"api_key"`

	// UserID and OrgID are what /v1/auth/register returned.
	// Stoke uses them for local attribution (e.g., which
	// org submitted a session).
	UserID string `json:"user_id"`
	OrgID  string `json:"org_id"`

	// Status is the registration status from the API
	// (typically "active"). Stored so `stoke cloud status`
	// can show it without hitting the network.
	Status string `json:"status"`
}

// ConfigPath returns the path where the cloud config lives.
// Resolves ~/.stoke/cloud.json, creating the parent dir if
// missing. Honors STOKE_CLOUD_CONFIG env override for tests.
func ConfigPath() (string, error) {
	if override := strings.TrimSpace(r1env.Get("R1_CLOUD_CONFIG", "STOKE_CLOUD_CONFIG")); override != "" {
		if err := os.MkdirAll(filepath.Dir(override), 0o755); err != nil {
			return "", fmt.Errorf("cloud: mkdir parent of %s: %w", override, err)
		}
		return override, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cloud: resolve home dir: %w", err)
	}
	dir := filepath.Join(home, ".stoke")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("cloud: mkdir %s: %w", dir, err)
	}
	return filepath.Join(dir, "cloud.json"), nil
}

// Load reads the cloud config. Returns (nil, nil) when the
// file doesn't exist (the common case for users who never
// opted in). Returns (nil, err) for permission / parse
// errors so callers can distinguish "missing" from "broken".
func Load() (*ConfigFile, error) {
	p, err := ConfigPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("cloud: read %s: %w", p, err)
	}
	var cfg ConfigFile
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("cloud: parse %s: %w", p, err)
	}
	return &cfg, nil
}

// Save persists the config atomically: write to
// cloud.json.tmp, fsync, rename. Prevents a partial file
// if the process dies mid-write. Mode 0600 — the API key is
// a long-lived credential and must not be world-readable.
func Save(cfg *ConfigFile) error {
	if cfg == nil {
		return errors.New("cloud: Save: nil config")
	}
	p, err := ConfigPath()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("cloud: marshal config: %w", err)
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("cloud: write tmp config: %w", err)
	}
	if err := os.Rename(tmp, p); err != nil {
		return fmt.Errorf("cloud: rename tmp config: %w", err)
	}
	return nil
}

// IsLinked reports whether a usable cloud config exists on
// disk. Used by `stoke cloud status` and by `--cloud` flag
// handlers to short-circuit when the user hasn't opted in.
func IsLinked() bool {
	cfg, err := Load()
	if err != nil {
		return false
	}
	return cfg != nil && strings.TrimSpace(cfg.APIKey) != "" && strings.TrimSpace(cfg.Endpoint) != ""
}
