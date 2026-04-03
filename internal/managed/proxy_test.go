package managed

import (
	"sync"
	"testing"
)

func TestLoadConfig_NoEnv(t *testing.T) {
	// Without EMBER_API_KEY set, LoadConfig should return Enabled=false.
	// We rely on the test environment not having EMBER_API_KEY set.
	t.Setenv("EMBER_API_KEY", "")
	cfg := LoadConfig()
	if cfg.Enabled {
		t.Error("LoadConfig() Enabled = true, want false when EMBER_API_KEY is empty")
	}
}

func TestLoadConfig_WithEnv(t *testing.T) {
	t.Setenv("EMBER_API_KEY", "test-key-123")
	t.Setenv("EMBER_API_URL", "")
	cfg := LoadConfig()
	if !cfg.Enabled {
		t.Fatal("LoadConfig() Enabled = false, want true")
	}
	if cfg.APIKey != "test-key-123" {
		t.Errorf("APIKey = %q, want %q", cfg.APIKey, "test-key-123")
	}
	if cfg.APIEndpoint != "https://api.ember.dev" {
		t.Errorf("APIEndpoint = %q, want default", cfg.APIEndpoint)
	}
	if cfg.Markup != 0.20 {
		t.Errorf("Markup = %f, want 0.20", cfg.Markup)
	}
}

func TestModelForTask(t *testing.T) {
	tests := []struct {
		taskType string
		want     string
	}{
		{"security", "anthropic/claude-sonnet-4"},
		{"plan", "anthropic/claude-sonnet-4"},
		{"architecture", "anthropic/claude-sonnet-4"},
		{"review", "openai/gpt-4.1"},
		{"unknown", "anthropic/claude-sonnet-4"},
		{"", "anthropic/claude-sonnet-4"},
	}
	for _, tt := range tests {
		t.Run(tt.taskType, func(t *testing.T) {
			got := ModelForTask(tt.taskType)
			if got != tt.want {
				t.Errorf("ModelForTask(%q) = %q, want %q", tt.taskType, got, tt.want)
			}
		})
	}
}

func TestProxy_NotEnabled(t *testing.T) {
	p := NewProxy(Config{Enabled: false})
	if p.Enabled() {
		t.Error("Enabled() = true, want false")
	}

	msgs := []Message{{Role: "user", Content: "hello"}}

	_, _, err := p.Chat("model", msgs)
	if err == nil {
		t.Error("Chat() expected error when not enabled")
	}

	_, _, err = p.ChatSync("model", msgs)
	if err == nil {
		t.Error("ChatSync() expected error when not enabled")
	}
}

func TestProxy_TotalCost(t *testing.T) {
	p := NewProxy(Config{Enabled: true, Markup: 0.20})

	// Manually inject usage events (the mutex-protected field)
	p.mu.Lock()
	p.usage = append(p.usage,
		UsageEvent{CostUSD: 0.10, MarkupUSD: 0.02},
		UsageEvent{CostUSD: 0.30, MarkupUSD: 0.06},
	)
	p.mu.Unlock()

	cost, markup := p.TotalCost()
	if cost != 0.40 {
		t.Errorf("TotalCost() cost = %f, want 0.40", cost)
	}
	if markup != 0.08 {
		t.Errorf("TotalCost() markup = %f, want 0.08", markup)
	}
}

func TestProxy_FlushUsage(t *testing.T) {
	p := NewProxy(Config{Enabled: true})

	p.mu.Lock()
	p.usage = append(p.usage,
		UsageEvent{TaskID: "t1", CostUSD: 0.05},
		UsageEvent{TaskID: "t2", CostUSD: 0.15},
	)
	p.mu.Unlock()

	events := p.FlushUsage()
	if len(events) != 2 {
		t.Fatalf("FlushUsage() returned %d events, want 2", len(events))
	}
	if events[0].TaskID != "t1" || events[1].TaskID != "t2" {
		t.Errorf("FlushUsage() returned wrong events: %+v", events)
	}

	// After flush, usage should be empty
	events2 := p.FlushUsage()
	if len(events2) != 0 {
		t.Errorf("FlushUsage() after flush returned %d events, want 0", len(events2))
	}

	cost, markup := p.TotalCost()
	if cost != 0 || markup != 0 {
		t.Errorf("TotalCost() after flush = (%f, %f), want (0, 0)", cost, markup)
	}
}

func TestProxy_FlushUsage_Concurrent(t *testing.T) {
	p := NewProxy(Config{Enabled: true})

	// Pre-load some usage
	p.mu.Lock()
	for i := 0; i < 100; i++ {
		p.usage = append(p.usage, UsageEvent{CostUSD: 0.01})
	}
	p.mu.Unlock()

	var wg sync.WaitGroup
	totalEvents := 0
	var mu sync.Mutex

	// Concurrent flushes and cost reads
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			events := p.FlushUsage()
			mu.Lock()
			totalEvents += len(events)
			mu.Unlock()
		}()
	}
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.TotalCost() // should not race
		}()
	}
	wg.Wait()

	if totalEvents != 100 {
		t.Errorf("concurrent FlushUsage() collected %d total events, want 100", totalEvents)
	}
}
