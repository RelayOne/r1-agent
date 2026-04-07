// Package flyclient implements a Go client for the Fly.io-compatible Machines API.
// It works with both Fly.io and Flare (which implements the same API surface).
package flyclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client communicates with a Fly/Flare Machines API.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// New creates a Fly/Flare API client.
// baseURL is the control plane URL (e.g. "https://api.machines.dev" or "http://flare:8090").
func New(baseURL, token string) *Client {
	return &Client{
		baseURL: baseURL,
		token:   token,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// --- Types ---

// App represents a Fly/Flare application.
type App struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	OrgID     string    `json:"org_id"`
	CreatedAt time.Time `json:"created_at"`
}

// Machine represents a Fly/Flare machine (VM).
type Machine struct {
	ID                string        `json:"id"`
	Name              string        `json:"name,omitempty"`
	State             string        `json:"state"`
	DesiredState      string        `json:"desired_state"`
	ObservedState     string        `json:"observed_state"`
	Region            string        `json:"region"`
	IPAddress         string        `json:"ip_address"`
	GeneratedHostname string        `json:"generated_hostname"`
	Config            MachineConfig `json:"config"`
	CreatedAt         time.Time     `json:"created_at"`
	UpdatedAt         time.Time     `json:"updated_at"`
}

// MachineConfig describes the machine's configuration.
type MachineConfig struct {
	Image    string            `json:"image"`
	Guest    GuestConfig       `json:"guest,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// GuestConfig sets resource limits.
type GuestConfig struct {
	CPUs     int `json:"cpus,omitempty"`
	MemoryMB int `json:"memory_mb,omitempty"`
}

// CreateAppRequest is the payload for POST /v1/apps.
type CreateAppRequest struct {
	AppName string `json:"app_name"`
	OrgSlug string `json:"org_slug"`
}

// CreateMachineRequest is the payload for POST /v1/apps/{app}/machines.
type CreateMachineRequest struct {
	Name   string        `json:"name,omitempty"`
	Region string        `json:"region"`
	Config MachineConfig `json:"config"`
}

// --- App Operations ---

// CreateApp creates a new application.
func (c *Client) CreateApp(ctx context.Context, req CreateAppRequest) (*App, error) {
	var app App
	if err := c.post(ctx, "/v1/apps", req, &app); err != nil {
		return nil, fmt.Errorf("create app: %w", err)
	}
	return &app, nil
}

// DeleteApp deletes an application.
func (c *Client) DeleteApp(ctx context.Context, appName string) error {
	return c.del(ctx, fmt.Sprintf("/v1/apps/%s", appName))
}

// --- Machine Operations ---

// CreateMachine creates a new machine in an app.
func (c *Client) CreateMachine(ctx context.Context, appName string, req CreateMachineRequest) (*Machine, error) {
	var machine Machine
	if err := c.post(ctx, fmt.Sprintf("/v1/apps/%s/machines", appName), req, &machine); err != nil {
		return nil, fmt.Errorf("create machine: %w", err)
	}
	return &machine, nil
}

// ListMachines lists all machines in an app.
func (c *Client) ListMachines(ctx context.Context, appName string) ([]Machine, error) {
	var machines []Machine
	if err := c.get(ctx, fmt.Sprintf("/v1/apps/%s/machines", appName), &machines); err != nil {
		return nil, fmt.Errorf("list machines: %w", err)
	}
	return machines, nil
}

// GetMachine retrieves a single machine.
func (c *Client) GetMachine(ctx context.Context, appName, machineID string) (*Machine, error) {
	var machine Machine
	if err := c.get(ctx, fmt.Sprintf("/v1/apps/%s/machines/%s", appName, machineID), &machine); err != nil {
		return nil, fmt.Errorf("get machine: %w", err)
	}
	return &machine, nil
}

// StartMachine starts a stopped machine.
func (c *Client) StartMachine(ctx context.Context, appName, machineID string) error {
	return c.postEmpty(ctx, fmt.Sprintf("/v1/apps/%s/machines/%s/start", appName, machineID))
}

// StopMachine stops a running machine.
func (c *Client) StopMachine(ctx context.Context, appName, machineID string) error {
	return c.postEmpty(ctx, fmt.Sprintf("/v1/apps/%s/machines/%s/stop", appName, machineID))
}

// DeleteMachine destroys a machine.
func (c *Client) DeleteMachine(ctx context.Context, appName, machineID string) error {
	return c.del(ctx, fmt.Sprintf("/v1/apps/%s/machines/%s", appName, machineID))
}

// Health checks the API health.
func (c *Client) Health(ctx context.Context) error {
	return c.get(ctx, "/health", nil)
}

// --- HTTP helpers ---

// APIError represents a Fly/Flare API error response.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("fly api: %d %s", e.StatusCode, e.Message)
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.httpClient.Do(req)
}

func (c *Client) get(ctx context.Context, path string, out interface{}) error {
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return parseError(resp)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (c *Client) post(ctx context.Context, path string, body, out interface{}) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, http.MethodPost, path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return parseError(resp)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (c *Client) postEmpty(ctx context.Context, path string) error {
	resp, err := c.do(ctx, http.MethodPost, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return parseError(resp)
	}
	return nil
}

func (c *Client) del(ctx context.Context, path string) error {
	resp, err := c.do(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return parseError(resp)
	}
	return nil
}

func parseError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var errResp struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
		return &APIError{StatusCode: resp.StatusCode, Message: errResp.Error}
	}
	return &APIError{StatusCode: resp.StatusCode, Message: string(body)}
}
