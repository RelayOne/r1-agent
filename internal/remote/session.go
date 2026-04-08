// Package remote reports build session progress to the Ember dashboard for live monitoring.
package remote

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// SessionReporter pushes session lifecycle events (register, update, complete) to the Ember API.
type SessionReporter struct {
	endpoint  string
	apiKey    string
	sessionID string
	client    *http.Client
}

// TaskProgress captures the current phase, worker, cost, and duration for a single task in a session update.
type TaskProgress struct {
	TaskID      string  `json:"task_id"`
	Description string  `json:"description"`
	Phase       string  `json:"phase"`
	Worker      string  `json:"worker"`
	CostUSD     float64 `json:"cost_usd"`
	DurationMs  int64   `json:"duration_ms"`
}

// SessionUpdate is a progress snapshot sent to the Ember API containing all active task statuses.
type SessionUpdate struct {
	SessionID    string         `json:"session_id"`
	PlanID       string         `json:"plan_id"`
	Tasks        []TaskProgress `json:"tasks"`
	TotalCostUSD float64        `json:"total_cost_usd"`
	BurstWorkers int            `json:"burst_workers"`
	UpdatedAt    time.Time      `json:"updated_at"`
}

// New creates a session reporter. Returns nil if no Ember key is configured.
func New() *SessionReporter {
	key := os.Getenv("EMBER_API_KEY")
	if key == "" {
		return nil
	}
	endpoint := os.Getenv("EMBER_API_URL")
	if endpoint == "" {
		endpoint = "https://api.ember.dev"
	}
	return &SessionReporter{
		endpoint: endpoint,
		apiKey:   key,
		client:   &http.Client{Timeout: 10 * time.Second},
	}
}

func (r *SessionReporter) doReq(method, path string, body any) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, r.endpoint+path, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+r.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return r.client.Do(req)
}

// RegisterSession creates a session on Ember and returns the shareable web URL.
func (r *SessionReporter) RegisterSession(planID string) (string, error) {
	if r == nil {
		return "", nil
	}

	resp, err := r.doReq("POST", "/v1/sessions", map[string]string{"plan_id": planID})
	if err != nil {
		return "", fmt.Errorf("register session: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 && resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("register session: HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		SessionID string `json:"session_id"`
		URL       string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode session response: %w", err)
	}
	r.sessionID = result.SessionID
	return result.URL, nil
}

// Update pushes a progress snapshot to Ember.
func (r *SessionReporter) Update(update SessionUpdate) error {
	if r == nil || r.sessionID == "" {
		return nil
	}
	update.SessionID = r.sessionID
	update.UpdatedAt = time.Now()

	resp, err := r.doReq("PUT", "/v1/sessions/"+r.sessionID, update)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 204 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("update session: HTTP %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// completeRequest is the typed request body for session completion.
type completeRequest struct {
	Status  string `json:"status"`
	Success bool   `json:"success"`
	Summary string `json:"summary"`
}

// Complete marks the session as finished on Ember.
func (r *SessionReporter) Complete(success bool, summary string) error {
	if r == nil || r.sessionID == "" {
		return nil
	}

	resp, err := r.doReq("PUT", "/v1/sessions/"+r.sessionID, completeRequest{
		Status:  "completed",
		Success: success,
		Summary: summary,
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 204 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("complete session: HTTP %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// SessionID returns the registered session ID (empty if not registered).
func (r *SessionReporter) SessionID() string {
	if r == nil {
		return ""
	}
	return r.sessionID
}

// WebURL returns the shareable dashboard URL for this session.
func (r *SessionReporter) WebURL() string {
	if r == nil || r.sessionID == "" {
		return ""
	}
	return fmt.Sprintf("%s/s/%s", r.endpoint, r.sessionID)
}
