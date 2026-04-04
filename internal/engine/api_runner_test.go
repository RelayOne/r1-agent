package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/ericmacdougall/stoke/internal/apiclient"
)

func TestAPIRunnerPrepare(t *testing.T) {
	runner := NewAPIRunner()
	prepared, err := runner.Prepare(RunSpec{
		Prompt:      "refactor the auth module",
		WorktreeDir: "/tmp/test-worktree",
		PoolAPIKey:  "sk-test-key-123",
		PoolBaseURL: "https://api.anthropic.com",
		Phase:       PhaseSpec{Name: "execute"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Binary should be empty (no subprocess)
	if prepared.Binary != "" {
		t.Errorf("binary = %q, want empty (no subprocess)", prepared.Binary)
	}

	// Dir should be the worktree
	if prepared.Dir != "/tmp/test-worktree" {
		t.Errorf("dir = %q, want /tmp/test-worktree", prepared.Dir)
	}

	// Notes should describe the API call
	if len(prepared.Notes) == 0 {
		t.Fatal("notes should not be empty")
	}
	notesJoined := strings.Join(prepared.Notes, " ")
	if !strings.Contains(notesJoined, "Native API runner") {
		t.Error("notes should mention Native API runner")
	}
	if !strings.Contains(notesJoined, "api.anthropic.com") {
		t.Error("notes should include the endpoint URL")
	}
	if !strings.Contains(notesJoined, "execute") {
		t.Error("notes should mention the phase name")
	}
}

func TestAPIRunnerPrepareDefaultBaseURL(t *testing.T) {
	runner := NewAPIRunner()
	prepared, err := runner.Prepare(RunSpec{
		Prompt:     "test prompt",
		PoolAPIKey: "sk-test-key",
		Phase:      PhaseSpec{Name: "plan"},
	})
	if err != nil {
		t.Fatal(err)
	}

	notesJoined := strings.Join(prepared.Notes, " ")
	if !strings.Contains(notesJoined, "api.anthropic.com/v1/messages") {
		t.Error("should default to Anthropic endpoint when no base URL given")
	}
}

func TestAPIRunnerPrepareCustomBaseURL(t *testing.T) {
	runner := NewAPIRunner()
	prepared, err := runner.Prepare(RunSpec{
		Prompt:      "test prompt",
		PoolAPIKey:  "sk-test-key",
		PoolBaseURL: "http://localhost:4000",
		Phase:       PhaseSpec{Name: "execute"},
	})
	if err != nil {
		t.Fatal(err)
	}

	notesJoined := strings.Join(prepared.Notes, " ")
	if !strings.Contains(notesJoined, "localhost:4000/v1/messages") {
		t.Errorf("notes should show custom endpoint, got: %s", notesJoined)
	}
}

func TestAPIRunnerRequiresAPIKey(t *testing.T) {
	runner := NewAPIRunner()
	_, err := runner.Prepare(RunSpec{
		Prompt:      "test",
		WorktreeDir: "/tmp/test",
		PoolAPIKey:  "", // no key
		Phase:       PhaseSpec{Name: "execute"},
	})
	if err == nil {
		t.Fatal("expected error when PoolAPIKey is empty")
	}
	if !strings.Contains(err.Error(), "API key") {
		t.Errorf("error should mention API key, got: %q", err.Error())
	}
}

func TestAPIRunnerImplementsCommandRunner(t *testing.T) {
	// Compile-time check that APIRunner satisfies CommandRunner
	var _ CommandRunner = (*APIRunner)(nil)
}

func TestDetectProvider(t *testing.T) {
	tests := []struct {
		baseURL  string
		expected apiclient.Provider
	}{
		{"", apiclient.ProviderAnthropic},
		{"https://api.anthropic.com", apiclient.ProviderAnthropic},
		{"https://api.openai.com", apiclient.ProviderOpenAI},
		{"https://openrouter.ai", apiclient.ProviderOpenRouter},
		{"http://localhost:4000", apiclient.ProviderOpenAI},           // LiteLLM proxy
		{"https://my-litellm.example.com", apiclient.ProviderOpenAI}, // custom proxy
	}

	for _, tt := range tests {
		t.Run(tt.baseURL, func(t *testing.T) {
			got := detectProvider(tt.baseURL)
			if got != tt.expected {
				t.Errorf("detectProvider(%q) = %q, want %q", tt.baseURL, got, tt.expected)
			}
		})
	}
}

func TestEstimateCost(t *testing.T) {
	usage := apiclient.Usage{
		InputTokens:  1000,
		OutputTokens: 500,
	}
	cost := estimateCost(usage)
	// 1000 * 3/1M + 500 * 15/1M = 0.003 + 0.0075 = 0.0105
	if cost < 0.010 || cost > 0.011 {
		t.Errorf("estimateCost = %f, want ~0.0105", cost)
	}
}

func TestEstimateCostZero(t *testing.T) {
	usage := apiclient.Usage{}
	cost := estimateCost(usage)
	if cost != 0 {
		t.Errorf("estimateCost for zero usage = %f, want 0", cost)
	}
}

func TestNewAPIRunnerDefaults(t *testing.T) {
	runner := NewAPIRunner()
	if runner.DefaultModel == "" {
		t.Error("DefaultModel should not be empty")
	}
	if runner.DefaultMaxTokens == 0 {
		t.Error("DefaultMaxTokens should not be zero")
	}
	if runner.Timeout == 0 {
		t.Error("Timeout should not be zero")
	}
}

func TestAPIRunnerRunRequiresAPIKey(t *testing.T) {
	runner := NewAPIRunner()
	_, err := runner.Run(context.Background(), RunSpec{
		Prompt:      "test",
		WorktreeDir: "/tmp/test",
		PoolAPIKey:  "",
		Phase:       PhaseSpec{Name: "execute"},
	}, nil)
	if err == nil {
		t.Fatal("expected error when PoolAPIKey is empty")
	}
	if !strings.Contains(err.Error(), "API key") {
		t.Errorf("error should mention API key, got: %q", err.Error())
	}
}
