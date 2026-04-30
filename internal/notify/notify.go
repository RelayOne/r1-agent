package notify

import (
	"bytes"
	"fmt"
	"net/http"
	"time"
)

// NotifyEvent is an event that triggers a notification.
type NotifyEvent struct {
	Type        string            `json:"type"` // task_complete, task_failed, build_complete, build_failed, rate_limited
	TaskID      string            `json:"task_id,omitempty"`
	BeaconID    string            `json:"beacon_id,omitempty"`
	SessionID   string            `json:"session_id,omitempty"`
	ArtifactRef string            `json:"artifact_ref,omitempty"`
	Message     string            `json:"message"`
	Timestamp   time.Time         `json:"timestamp"`
	Details     map[string]string `json:"details,omitempty"`
}

// Notifier sends notifications.
type Notifier interface {
	Notify(event NotifyEvent) error
}

// WebhookNotifier sends notifications via HTTP POST.
type WebhookNotifier struct {
	URL     string
	Headers map[string]string
	Format  func(NotifyEvent) ([]byte, error)
	client  *http.Client
}

// NewWebhookNotifier creates a webhook notifier.
func NewWebhookNotifier(url string, headers map[string]string, format func(NotifyEvent) ([]byte, error)) *WebhookNotifier {
	if format == nil {
		format = GenericFormat
	}
	return &WebhookNotifier{
		URL: url, Headers: headers, Format: format,
		client: &http.Client{Timeout: 5 * time.Second},
	}
}

// Notify sends the event via HTTP POST. Retries once on failure. Never blocks execution.
func (w *WebhookNotifier) Notify(event NotifyEvent) error {
	body, err := w.Format(event)
	if err != nil {
		return fmt.Errorf("format: %w", err)
	}

	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequest("POST", w.URL, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		for k, v := range w.Headers {
			req.Header.Set(k, v)
		}

		resp, err := w.client.Do(req)
		if err != nil {
			if attempt == 0 {
				continue
			}
			return err
		}
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		if attempt == 0 {
			continue
		}
		return fmt.Errorf("webhook returned %d", resp.StatusCode)
	}
	return nil
}

// NopNotifier does nothing (used when notifications are not configured).
type NopNotifier struct{}

// Notify is a no-op.
func (NopNotifier) Notify(NotifyEvent) error { return nil }
