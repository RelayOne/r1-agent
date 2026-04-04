package notify

import (
	"encoding/json"
	"fmt"
)

// GenericFormat produces plain JSON.
func GenericFormat(e NotifyEvent) ([]byte, error) {
	return json.Marshal(e)
}

// DiscordFormat produces Discord webhook JSON with embeds.
func DiscordFormat(e NotifyEvent) ([]byte, error) {
	color := 3066993 // green
	if e.Type == "task_failed" || e.Type == "build_failed" {
		color = 15158332 // red
	}
	payload := map[string]interface{}{
		"embeds": []map[string]interface{}{{
			"title":       fmt.Sprintf("Stoke: %s", e.Type),
			"description": e.Message,
			"color":       color,
			"timestamp":   e.Timestamp.Format("2006-01-02T15:04:05Z"),
		}},
	}
	return json.Marshal(payload)
}

// SlackFormat produces Slack Block Kit JSON.
func SlackFormat(e NotifyEvent) ([]byte, error) {
	payload := map[string]interface{}{
		"blocks": []map[string]interface{}{
			{"type": "header", "text": map[string]string{"type": "plain_text", "text": fmt.Sprintf("Stoke: %s", e.Type)}},
			{"type": "section", "text": map[string]string{"type": "mrkdwn", "text": e.Message}},
		},
	}
	return json.Marshal(payload)
}

// TelegramFormat produces Telegram sendMessage JSON.
func TelegramFormat(chatID string) func(NotifyEvent) ([]byte, error) {
	return func(e NotifyEvent) ([]byte, error) {
		payload := map[string]interface{}{
			"chat_id":    chatID,
			"text":       fmt.Sprintf("*Stoke: %s*\n%s", e.Type, e.Message),
			"parse_mode": "Markdown",
		}
		return json.Marshal(payload)
	}
}
