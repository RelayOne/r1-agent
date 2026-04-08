package builtin

import (
	"context"
	"regexp"
	"strings"

	"github.com/ericmacdougall/stoke/internal/hub"
)

// SecretScanner is a gate subscriber that denies file writes containing
// hardcoded secrets (AWS keys, private keys, API keys, Stripe keys, etc.).
type SecretScanner struct {
	Patterns []*regexp.Regexp
}

// NewDefaultSecretScanner creates a scanner with patterns for common secret types.
func NewDefaultSecretScanner() *SecretScanner {
	return &SecretScanner{
		Patterns: []*regexp.Regexp{
			regexp.MustCompile(`(?i)aws_access_key_id\s*=\s*['"]?AKIA[0-9A-Z]{16}`),
			regexp.MustCompile(`(?i)aws_secret_access_key\s*=\s*['"]?[A-Za-z0-9/+=]{40}`),
			regexp.MustCompile(`-----BEGIN (RSA |OPENSSH |EC )?PRIVATE KEY-----`),
			regexp.MustCompile(`(?i)api[_-]?key\s*[=:]\s*['"][a-zA-Z0-9]{32,}['"]`),
			regexp.MustCompile(`(?i)stripe.*['"]sk_live_[0-9a-zA-Z]{24,}['"]`),
			regexp.MustCompile(`(?i)stripe.*['"]rk_live_[0-9a-zA-Z]{24,}['"]`),
			regexp.MustCompile(`xox[baprs]-[0-9a-zA-Z]{10,48}`),
			regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{36}`),
		},
	}
}

// Register adds the secret scanner to the bus.
func (s *SecretScanner) Register(bus *hub.Bus) {
	bus.Register(hub.Subscriber{
		ID:       "builtin.secret_scanner",
		Events:   []hub.EventType{hub.EventToolFileWrite},
		Mode:     hub.ModeGateStrict,
		Priority: 50, // before honesty gate
		Handler:  s.handle,
	})
}

func (s *SecretScanner) handle(ctx context.Context, ev *hub.Event) *hub.HookResponse {
	content := ""
	if ev.Tool != nil {
		content = ev.Tool.Output
	}
	if content == "" {
		if c, ok := ev.Custom["content"]; ok {
			if str, ok := c.(string); ok {
				content = str
			}
		}
	}
	if content == "" {
		return &hub.HookResponse{Decision: hub.Allow}
	}

	for _, pat := range s.Patterns {
		if loc := pat.FindStringIndex(content); loc != nil {
			line := lineContaining(content, loc[0])
			return &hub.HookResponse{
				Decision: hub.Deny,
				Reason:   "secret detected in file write: " + strings.TrimSpace(line),
			}
		}
	}
	return &hub.HookResponse{Decision: hub.Allow}
}

func lineContaining(content string, offset int) string {
	if offset < 0 || offset >= len(content) {
		return ""
	}
	start := offset
	for start > 0 && content[start-1] != '\n' {
		start--
	}
	end := offset
	for end < len(content) && content[end] != '\n' {
		end++
	}
	return content[start:end]
}
