package specexec

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/stream"
)

// fakeProvider returns a canned text response (or error) for Chat.
type fakeProvider struct {
	text string
	err  error
}

func (f *fakeProvider) Name() string { return "fake" }
func (f *fakeProvider) Chat(provider.ChatRequest) (*provider.ChatResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &provider.ChatResponse{
		Content: []provider.ResponseContent{{Type: "text", Text: f.text}},
	}, nil
}
func (f *fakeProvider) ChatStream(req provider.ChatRequest, _ func(stream.Event)) (*provider.ChatResponse, error) {
	return f.Chat(req)
}

func TestNewLLMSelectorNilProvider(t *testing.T) {
	if sel := NewLLMSelector(nil, ""); sel != nil {
		t.Fatal("nil provider must yield a nil (disabled) Selector")
	}
}

func TestLLMSelectorParsing(t *testing.T) {
	outcomes := []Outcome{
		{StrategyID: "strategy-1", Success: true, Score: 0.9},
		{StrategyID: "strategy-2", Success: true, Score: 0.5},
	}
	tests := []struct {
		name       string
		prov       *fakeProvider
		wantID     string
		wantErr    bool
		wantReason string
	}{
		{
			name:       "valid verdict",
			prov:       &fakeProvider{text: `{"winner":"strategy-2","rationale":"smaller and correct"}`},
			wantID:     "strategy-2",
			wantReason: "smaller and correct",
		},
		{
			name:   "fenced verdict",
			prov:   &fakeProvider{text: "```json\n{\"winner\":\"strategy-1\",\"rationale\":\"ok\"}\n```"},
			wantID: "strategy-1",
		},
		{
			name:    "garbage output",
			prov:    &fakeProvider{text: "I think the second one is nicer."},
			wantErr: true,
		},
		{
			name:    "empty winner",
			prov:    &fakeProvider{text: `{"winner":"","rationale":"?"}`},
			wantErr: true,
		},
		{
			name:    "provider error",
			prov:    &fakeProvider{err: fmt.Errorf("connection refused")},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sel := NewLLMSelector(tc.prov, "test-model")
			if sel == nil {
				t.Fatal("selector is nil for non-nil provider")
			}
			id, reason, err := sel(context.Background(), outcomes)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got id=%q", id)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if id != tc.wantID {
				t.Errorf("winner = %q, want %q", id, tc.wantID)
			}
			if tc.wantReason != "" && reason != tc.wantReason {
				t.Errorf("rationale = %q, want %q", reason, tc.wantReason)
			}
		})
	}
}

func TestBuildSelectorPromptCapsLargeBlocks(t *testing.T) {
	huge := strings.Repeat("x", 3*selectorDiffCap)
	outcomes := []Outcome{
		{StrategyID: "strategy-1", Success: true, DiffText: huge, TestOutput: huge, PlanText: huge},
		{StrategyID: "strategy-2", Success: true, DiffText: huge},
	}
	prompt := buildSelectorPrompt(outcomes)
	// Bound: instruction + per-outcome header + capped blocks. With the
	// caps applied the prompt must be far below the raw 6x cap input.
	limit := len(selectorInstruction) + 2*(selectorDiffCap+selectorTestCap+selectorPlanCap) + 4096
	if len(prompt) > limit {
		t.Fatalf("prompt length %d exceeds bounded limit %d — truncation caps not applied", len(prompt), limit)
	}
	for _, id := range []string{"strategy-1", "strategy-2"} {
		if !strings.Contains(prompt, "candidate "+id) {
			t.Errorf("prompt missing candidate block for %s", id)
		}
	}
}

func TestRunSelectorOverride(t *testing.T) {
	exec := func(ctx context.Context, s Strategy) Outcome {
		switch s.ID {
		case "s-high":
			return Outcome{Success: true, TestsPassed: 10, DiffLines: 10}
		case "s-low":
			return Outcome{Success: true, TestsPassed: 1, TestsFailed: 1, DiffLines: 900}
		default:
			return Outcome{Success: false, Error: "boom"}
		}
	}
	strategies := []Strategy{{ID: "s-high"}, {ID: "s-low"}, {ID: "s-fail"}}

	t.Run("selector overrides to lower-scored success", func(t *testing.T) {
		var sawScores bool
		sel := func(ctx context.Context, outcomes []Outcome) (string, string, error) {
			for _, o := range outcomes {
				if o.Success && o.Score > 0 {
					sawScores = true
				}
			}
			return "s-low", "prefers the alternative", nil
		}
		res := Run(context.Background(), Spec{Strategies: strategies, Selector: sel}, exec)
		if res.Winner == nil || res.Winner.StrategyID != "s-low" {
			t.Fatalf("winner = %+v, want s-low", res.Winner)
		}
		if !res.SelectorUsed {
			t.Error("SelectorUsed = false, want true")
		}
		if res.SelectorRationale != "prefers the alternative" {
			t.Errorf("rationale = %q", res.SelectorRationale)
		}
		if !sawScores {
			t.Error("selector must see outcomes AFTER scoring (Score populated)")
		}
	})

	t.Run("selector naming failed outcome keeps deterministic winner", func(t *testing.T) {
		sel := func(ctx context.Context, outcomes []Outcome) (string, string, error) {
			return "s-fail", "bad pick", nil
		}
		res := Run(context.Background(), Spec{Strategies: strategies, Selector: sel}, exec)
		if res.Winner == nil || res.Winner.StrategyID != "s-high" {
			t.Fatalf("winner = %+v, want deterministic s-high", res.Winner)
		}
		if res.SelectorUsed {
			t.Error("SelectorUsed = true for a rejected selector pick")
		}
	})

	t.Run("selector error keeps deterministic winner", func(t *testing.T) {
		sel := func(ctx context.Context, outcomes []Outcome) (string, string, error) {
			return "", "", fmt.Errorf("provider down")
		}
		res := Run(context.Background(), Spec{Strategies: strategies, Selector: sel}, exec)
		if res.Winner == nil || res.Winner.StrategyID != "s-high" {
			t.Fatalf("winner = %+v, want deterministic s-high", res.Winner)
		}
		if res.SelectorUsed {
			t.Error("SelectorUsed = true after selector error")
		}
	})

	t.Run("selector skipped with fewer than two successes", func(t *testing.T) {
		var called bool
		sel := func(ctx context.Context, outcomes []Outcome) (string, string, error) {
			called = true
			return "s-high", "", nil
		}
		soloExec := func(ctx context.Context, s Strategy) Outcome {
			if s.ID == "s-high" {
				return Outcome{Success: true, TestsPassed: 1}
			}
			return Outcome{Success: false, Error: "boom"}
		}
		res := Run(context.Background(), Spec{Strategies: strategies, Selector: sel}, soloExec)
		if called {
			t.Error("selector called with <2 successful outcomes")
		}
		if res.Winner == nil || res.Winner.StrategyID != "s-high" {
			t.Fatalf("winner = %+v, want s-high", res.Winner)
		}
	})
}

func TestGenerateStrategiesWithModels(t *testing.T) {
	approaches := []string{"a", "b", "c", "d"}
	strategies := GenerateStrategiesWithModels("task", approaches, []string{"claude", "codex"})
	want := []string{"claude", "codex", "claude", "codex"}
	for i, s := range strategies {
		if s.Model != want[i] {
			t.Errorf("strategy %d Model = %q, want %q", i, s.Model, want[i])
		}
		if !strings.Contains(s.Prompt, approaches[i]) {
			t.Errorf("strategy %d prompt missing approach %q", i, approaches[i])
		}
	}

	plain := GenerateStrategiesWithModels("task", approaches, nil)
	for i, s := range plain {
		if s.Model != "" {
			t.Errorf("strategy %d Model = %q, want empty with nil models", i, s.Model)
		}
	}

	// GenerateStrategies must stay byte-identical to the nil-models path.
	legacy := GenerateStrategies("task", approaches)
	for i := range legacy {
		if legacy[i].ID != plain[i].ID || legacy[i].Prompt != plain[i].Prompt || legacy[i].Model != plain[i].Model {
			t.Errorf("GenerateStrategies diverged from nil-models path at %d", i)
		}
	}
}

func TestLLMSelectorHonorsContext(t *testing.T) {
	blocking := &slowProvider{delay: 5 * time.Second}
	sel := NewLLMSelector(blocking, "m")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, _, err := sel(ctx, []Outcome{{StrategyID: "s1", Success: true}, {StrategyID: "s2", Success: true}})
	if err == nil {
		t.Fatal("expected context deadline error from a hung provider")
	}
}

type slowProvider struct{ delay time.Duration }

func (s *slowProvider) Name() string { return "slow" }
func (s *slowProvider) Chat(provider.ChatRequest) (*provider.ChatResponse, error) {
	time.Sleep(s.delay)
	return &provider.ChatResponse{}, nil
}
func (s *slowProvider) ChatStream(req provider.ChatRequest, _ func(stream.Event)) (*provider.ChatResponse, error) {
	return s.Chat(req)
}
