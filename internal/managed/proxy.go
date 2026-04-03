// Package managed provides a proxy client for the Ember managed AI endpoint, handling streaming chat and usage metering.
package managed

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Config holds connection settings and pricing markup for the Ember managed AI endpoint.
type Config struct {
	Enabled     bool
	APIEndpoint string
	APIKey      string
	Markup      float64
}

// LoadConfig reads managed pool config from env or config file.
func LoadConfig() Config {
	key := os.Getenv("EMBER_API_KEY")
	if key == "" {
		return Config{Enabled: false}
	}
	endpoint := os.Getenv("EMBER_API_URL")
	if endpoint == "" {
		endpoint = "https://api.ember.dev"
	}
	return Config{Enabled: true, APIEndpoint: endpoint, APIKey: key, Markup: 0.20}
}

// Message represents a single chat message with a role (system, user, assistant) and text content.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// UsageEvent records token counts, cost, and markup for a single managed AI completion request.
type UsageEvent struct {
	TaskID       string    `json:"task_id"`
	Model        string    `json:"model"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	CostUSD      float64   `json:"cost_usd"`
	MarkupUSD    float64   `json:"markup_usd"`
	Timestamp    time.Time `json:"timestamp"`
}

// Proxy routes AI chat requests through the Ember endpoint and accumulates usage events for billing.
type Proxy struct {
	config Config
	client *http.Client
	mu     sync.Mutex
	usage  []UsageEvent
}

func NewProxy(cfg Config) *Proxy {
	return &Proxy{
		config: cfg,
		client: &http.Client{Timeout: 5 * time.Minute},
	}
}

func (p *Proxy) Enabled() bool { return p.config.Enabled }

// Chat sends a completion request through the Ember managed AI endpoint.
// Ember proxies to OpenRouter, handles model routing, and meters usage.
func (p *Proxy) Chat(model string, messages []Message) (string, UsageEvent, error) {
	if !p.config.Enabled {
		return "", UsageEvent{}, fmt.Errorf("managed AI not enabled (set EMBER_API_KEY)")
	}

	body, err := json.Marshal(map[string]any{
		"model":    model,
		"messages": messages,
		"stream":   true,
	})
	if err != nil {
		return "", UsageEvent{}, fmt.Errorf("marshal chat request: %w", err)
	}

	req, err := http.NewRequest("POST", p.config.APIEndpoint+"/v1/ai/chat", bytes.NewReader(body))
	if err != nil {
		return "", UsageEvent{}, err
	}
	req.Header.Set("Authorization", "Bearer "+p.config.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := p.client.Do(req)
	if err != nil {
		return "", UsageEvent{}, fmt.Errorf("managed AI request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		errBody, _ := io.ReadAll(resp.Body)
		return "", UsageEvent{}, fmt.Errorf("managed AI: HTTP %d: %s", resp.StatusCode, string(errBody))
	}

	// Parse SSE stream
	var fullText strings.Builder
	var usage UsageEvent
	usage.Model = model
	usage.Timestamp = time.Now()

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int     `json:"prompt_tokens"`
				CompletionTokens int     `json:"completion_tokens"`
				TotalCost        float64 `json:"total_cost"`
			} `json:"usage,omitempty"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		for _, c := range chunk.Choices {
			fullText.WriteString(c.Delta.Content)
		}
		if chunk.Usage != nil {
			usage.InputTokens = chunk.Usage.PromptTokens
			usage.OutputTokens = chunk.Usage.CompletionTokens
			usage.CostUSD = chunk.Usage.TotalCost
			usage.MarkupUSD = chunk.Usage.TotalCost * p.config.Markup
		}
	}

	p.mu.Lock()
	p.usage = append(p.usage, usage)
	p.mu.Unlock()
	return fullText.String(), usage, nil
}

// ChatSync sends a non-streaming completion request. Simpler for short tasks.
func (p *Proxy) ChatSync(model string, messages []Message) (string, UsageEvent, error) {
	if !p.config.Enabled {
		return "", UsageEvent{}, fmt.Errorf("managed AI not enabled")
	}

	body, err := json.Marshal(map[string]any{
		"model":    model,
		"messages": messages,
		"stream":   false,
	})
	if err != nil {
		return "", UsageEvent{}, fmt.Errorf("marshal chat request: %w", err)
	}

	req, err := http.NewRequest("POST", p.config.APIEndpoint+"/v1/ai/chat", bytes.NewReader(body))
	if err != nil {
		return "", UsageEvent{}, err
	}
	req.Header.Set("Authorization", "Bearer "+p.config.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return "", UsageEvent{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		errBody, _ := io.ReadAll(resp.Body)
		return "", UsageEvent{}, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(errBody))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int     `json:"prompt_tokens"`
			CompletionTokens int     `json:"completion_tokens"`
			TotalCost        float64 `json:"total_cost"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", UsageEvent{}, fmt.Errorf("decode chat response: %w", err)
	}

	text := ""
	if len(result.Choices) > 0 {
		text = result.Choices[0].Message.Content
	}

	usage := UsageEvent{
		Model: model, InputTokens: result.Usage.PromptTokens,
		OutputTokens: result.Usage.CompletionTokens,
		CostUSD:      result.Usage.TotalCost,
		MarkupUSD:    result.Usage.TotalCost * p.config.Markup,
		Timestamp:    time.Now(),
	}
	p.mu.Lock()
	p.usage = append(p.usage, usage)
	p.mu.Unlock()
	return text, usage, nil
}

// TotalCost returns cumulative cost and markup for this session.
func (p *Proxy) TotalCost() (cost, markup float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, u := range p.usage {
		cost += u.CostUSD
		markup += u.MarkupUSD
	}
	return
}

// FlushUsage returns all usage events and clears the buffer.
func (p *Proxy) FlushUsage() []UsageEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	events := p.usage
	p.usage = nil
	return events
}

// ModelForTask returns the recommended managed model for a task type.
func ModelForTask(taskType string) string {
	switch taskType {
	case "security", "plan":
		return "anthropic/claude-sonnet-4"
	case "architecture":
		return "anthropic/claude-sonnet-4"
	case "review":
		return "openai/gpt-4.1"
	default:
		return "anthropic/claude-sonnet-4"
	}
}
