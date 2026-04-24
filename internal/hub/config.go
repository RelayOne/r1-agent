package hub

import (
	"encoding/json"
	"os"
	"time"

	"github.com/RelayOne/r1/internal/r1dir"
)

// HookConfig represents the hub configuration from .stoke/hooks.json.
type HookConfig struct {
	// Scripts defines external script hooks.
	Scripts []ScriptHookConfig `json:"scripts,omitempty"`

	// Webhooks defines external webhook hooks.
	Webhooks []WebhookHookConfig `json:"webhooks,omitempty"`
}

// ScriptHookConfig defines a script-based hook subscriber from config.
type ScriptHookConfig struct {
	ID       string   `json:"id"`
	Events   []string `json:"events"`
	Mode     string   `json:"mode"` // gate, transform, observe
	Priority int      `json:"priority"`
	Command  string   `json:"command"`
	Timeout  string   `json:"timeout,omitempty"` // e.g. "5s", "30s"
}

// WebhookHookConfig defines a webhook-based hook subscriber from config.
type WebhookHookConfig struct {
	ID       string            `json:"id"`
	Events   []string          `json:"events"`
	Mode     string            `json:"mode"`
	Priority int               `json:"priority"`
	URL      string            `json:"url"`
	Headers  map[string]string `json:"headers,omitempty"`
	Timeout  string            `json:"timeout,omitempty"`
	Retries  int               `json:"retries,omitempty"`
}

// LoadConfig loads hook configuration from .r1/hooks.json (canonical) or
// .stoke/hooks.json (legacy fallback) in the repo root. Returns an empty
// config if the file doesn't exist under either layout.
func LoadConfig(repoRoot string) (HookConfig, error) {
	data, err := r1dir.ReadFileFor(repoRoot, "hooks.json")
	if err != nil {
		if os.IsNotExist(err) {
			return HookConfig{}, nil
		}
		return HookConfig{}, err
	}
	var cfg HookConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return HookConfig{}, err
	}
	return cfg, nil
}

// ApplyConfig registers subscribers from a hook config onto the bus.
func (b *Bus) ApplyConfig(cfg HookConfig) {
	for _, sc := range cfg.Scripts {
		timeout := 10 * time.Second
		if sc.Timeout != "" {
			if d, err := time.ParseDuration(sc.Timeout); err == nil {
				timeout = d
			}
		}
		events := make([]EventType, len(sc.Events))
		for i, e := range sc.Events {
			events[i] = EventType(e)
		}
		b.Register(Subscriber{
			ID:       sc.ID,
			Events:   events,
			Mode:     Mode(sc.Mode),
			Priority: sc.Priority,
			Script: &ScriptConfig{
				Command:    sc.Command,
				Timeout:    timeout,
				InputJSON:  true,
				OutputJSON: true,
			},
		})
	}

	for _, wc := range cfg.Webhooks {
		timeout := 10 * time.Second
		if wc.Timeout != "" {
			if d, err := time.ParseDuration(wc.Timeout); err == nil {
				timeout = d
			}
		}
		events := make([]EventType, len(wc.Events))
		for i, e := range wc.Events {
			events[i] = EventType(e)
		}
		b.Register(Subscriber{
			ID:       wc.ID,
			Events:   events,
			Mode:     Mode(wc.Mode),
			Priority: wc.Priority,
			Webhook: &WebhookConfig{
				URL:     wc.URL,
				Headers: wc.Headers,
				Timeout: timeout,
				Retries: wc.Retries,
			},
		})
	}
}
