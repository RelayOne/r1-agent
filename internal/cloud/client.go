// Package cloud — client.go
//
// HTTP client for Contract Group H (Stoke Cloud API).
// Implements H1/H2/H3/H4 request/response shapes verbatim
// from the 2026-04-16 Contract Bible.
package cloud

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

// DefaultEndpoint is the public Stoke Cloud endpoint. Can
// be overridden per Client for self-hosted deployments.
const DefaultEndpoint = "https://cloud.stoke.dev"

// Client is a thin HTTP wrapper around the cloud API.
// Threadsafe via immutable fields + stdlib http.Client
// internal synchronization.
type Client struct {
	Endpoint   string        // base URL, no trailing slash
	APIKey     string        // Bearer token for H1/H2/H3
	HTTPClient *http.Client  // optional; defaults to 30s timeout
	UserAgent  string        // optional; defaults to "stoke-cloud-client/1"
}

// New returns a Client pointed at endpoint with the given
// API key. Callers can also load from disk via Load() and
// build a Client via FromConfig.
func New(endpoint, apiKey string) *Client {
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	endpoint = strings.TrimRight(endpoint, "/")
	return &Client{
		Endpoint: endpoint,
		APIKey:   apiKey,
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		UserAgent: "stoke-cloud-client/1",
	}
}

// FromConfig builds a Client from a persisted ConfigFile.
// Returns an error if the config is missing either Endpoint
// or APIKey — both are required to make authenticated calls.
func FromConfig(cfg *ConfigFile) (*Client, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cloud: FromConfig: nil config (run 'stoke cloud register')")
	}
	if strings.TrimSpace(cfg.Endpoint) == "" || strings.TrimSpace(cfg.APIKey) == "" {
		return nil, fmt.Errorf("cloud: config missing endpoint or api_key")
	}
	return New(cfg.Endpoint, cfg.APIKey), nil
}

// --- H4 POST /v1/auth/register ---

// RegisterRequest matches Contract H4 request body:
//   { "api_key": "string" }
// Codex bible shape.
type RegisterRequest struct {
	APIKey string `json:"api_key"`
}

// RegisterResponse matches Contract H4 response body:
//   { "user_id": "string", "org_id": "string", "status": "active" }
type RegisterResponse struct {
	UserID string `json:"user_id"`
	OrgID  string `json:"org_id"`
	Status string `json:"status"`
}

// Register performs the H4 handshake. The APIKey is sent in
// the body (NOT as Bearer) since this is the exchange step.
// Returns the registration record on success. Callers
// persist via Save(&ConfigFile{...}) so subsequent commands
// can load via Load().
func (c *Client) Register(ctx context.Context, apiKey string) (*RegisterResponse, error) {
	body, err := json.Marshal(RegisterRequest{APIKey: apiKey})
	if err != nil {
		return nil, fmt.Errorf("cloud: marshal register: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.Endpoint+"/v1/auth/register", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.UserAgent)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cloud: register call: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, readHTTPError(resp, "register")
	}
	var out RegisterResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("cloud: decode register response: %w", err)
	}
	return &out, nil
}

// --- H1 POST /v1/sessions ---

// SessionConfig mirrors Contract H1's `config` object.
type SessionConfig struct {
	Model    string `json:"model,omitempty"`
	PoolID   string `json:"pool_id,omitempty"`
	Verify   bool   `json:"verify,omitempty"`
	MaxTurns int    `json:"max_turns,omitempty"`
}

// SubmitSessionRequest mirrors Contract H1's request body.
type SubmitSessionRequest struct {
	SessionID       string        `json:"session_id"`
	RepoURL         string        `json:"repo_url"`
	Branch          string        `json:"branch"`
	TaskSpec        string        `json:"task_spec"`
	GovernanceTier  string        `json:"governance_tier"`
	Config          SessionConfig `json:"config"`
}

// SubmitSessionResponse mirrors Contract H1's response.
type SubmitSessionResponse struct {
	SessionID    string `json:"session_id"`
	Status       string `json:"status"`
	DashboardURL string `json:"dashboard_url"`
}

// SubmitSession sends an H1 request. Caller builds the
// ULID/ID, branch, repo_url, task spec — the client just
// wires the HTTP call.
func (c *Client) SubmitSession(ctx context.Context, r SubmitSessionRequest) (*SubmitSessionResponse, error) {
	body, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("cloud: marshal submit: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.Endpoint+"/v1/sessions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.setCommonHeaders(req)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cloud: submit call: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, readHTTPError(resp, "submit session")
	}
	var out SubmitSessionResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("cloud: decode submit response: %w", err)
	}
	return &out, nil
}

// --- H2 GET /v1/sessions/:id ---

// SessionStatus mirrors Contract H2's response.
type SessionStatus struct {
	SessionID       string                 `json:"session_id"`
	Status          string                 `json:"status"`
	CurrentPhase    string                 `json:"current_phase"`
	TurnsCompleted  int                    `json:"turns_completed"`
	TurnsRemaining  int                    `json:"turns_remaining"`
	LastEvent       map[string]any         `json:"last_event"`
	DashboardURL    string                 `json:"dashboard_url"`
}

// GetSession fetches H2 status.
func (c *Client) GetSession(ctx context.Context, sessionID string) (*SessionStatus, error) {
	u := c.Endpoint + "/v1/sessions/" + url.PathEscape(sessionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	c.setCommonHeaders(req)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cloud: get session: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readHTTPError(resp, "get session")
	}
	var out SessionStatus
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("cloud: decode status: %w", err)
	}
	return &out, nil
}

// --- H3 GET /v1/sessions/:id/events?since=<ISO8601> ---

// SessionEvent mirrors Contract H3's event shape.
type SessionEvent struct {
	EventType string         `json:"event_type"`
	Timestamp string         `json:"timestamp"` // ISO 8601
	Data      map[string]any `json:"data"`
}

// SessionEventsResponse mirrors Contract H3's response.
type SessionEventsResponse struct {
	Events []SessionEvent `json:"events"`
}

// GetSessionEvents fetches H3 events. `since` is optional
// (zero time → fetch all events the server still has).
func (c *Client) GetSessionEvents(ctx context.Context, sessionID string, since time.Time) (*SessionEventsResponse, error) {
	u := c.Endpoint + "/v1/sessions/" + url.PathEscape(sessionID) + "/events"
	if !since.IsZero() {
		u += "?since=" + url.QueryEscape(since.UTC().Format(time.RFC3339Nano))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	c.setCommonHeaders(req)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cloud: get events: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readHTTPError(resp, "get events")
	}
	var out SessionEventsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("cloud: decode events: %w", err)
	}
	return &out, nil
}

// --- helpers ---

func (c *Client) setCommonHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.UserAgent)
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
}

// readHTTPError converts a non-2xx response into a Go
// error with the body included (truncated so we don't dump
// an HTML error page into the CLI output).
func readHTTPError(resp *http.Response, op string) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	snippet := strings.TrimSpace(string(body))
	if len(snippet) > 500 {
		snippet = snippet[:497] + "..."
	}
	if snippet == "" {
		return fmt.Errorf("cloud: %s: http %d", op, resp.StatusCode)
	}
	return fmt.Errorf("cloud: %s: http %d: %s", op, resp.StatusCode, snippet)
}
