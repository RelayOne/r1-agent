package subscriptions

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// UsageData is the response from the undocumented Anthropic OAuth usage endpoint.
// GET https://api.anthropic.com/api/oauth/usage
// Headers: Authorization: Bearer <token>, anthropic-beta: oauth-2025-04-20
type UsageData struct {
	FiveHour     UsageWindow `json:"five_hour"`
	SevenDay     UsageWindow `json:"seven_day"`
	SevenDayOpus UsageWindow `json:"seven_day_opus"`
}

// UsageWindow represents a single rate-limit window with utilization percentage and reset time.
type UsageWindow struct {
	Utilization float64    `json:"utilization"` // 0-100
	ResetsAt    *time.Time `json:"resets_at"`
}

const usageURL = "https://api.anthropic.com/api/oauth/usage"

// PollClaudeUsage queries the OAuth usage endpoint. No quota consumed.
func PollClaudeUsage(ctx context.Context, oauthToken string) (*UsageData, error) {
	if oauthToken == "" {
		return nil, fmt.Errorf("empty oauth token")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", usageURL, nil)
	if err != nil { return nil, err }
	req.Header.Set("Authorization", "Bearer "+oauthToken)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("User-Agent", "stoke/0.1.0")

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil { return nil, err }
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("usage endpoint %d: %s", resp.StatusCode, body)
	}
	var data UsageData
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	return &data, nil
}

// StartPoller polls utilization for all Claude pools at the given interval.
// Uses the thread-safe UpdateUtilization method.
func (m *Manager) StartPoller(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				snapshot := m.Snapshot()
				for _, p := range snapshot {
					if p.Provider != ProviderClaude || p.OAuthToken == "" {
						continue
					}
					data, err := PollClaudeUsage(ctx, p.OAuthToken)
					if err != nil {
						continue
					}
					var fiveReset, sevenReset time.Time
					if data.FiveHour.ResetsAt != nil {
						fiveReset = *data.FiveHour.ResetsAt
					}
					if data.SevenDay.ResetsAt != nil {
						sevenReset = *data.SevenDay.ResetsAt
					}
					m.UpdateUtilization(p.ID, data.FiveHour.Utilization, data.SevenDay.Utilization, fiveReset, sevenReset)
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}
