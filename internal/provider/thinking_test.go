package provider

import "testing"

func TestAnthropicThinkingParam(t *testing.T) {
	cases := []struct {
		name       string
		model      string
		spec       *ThinkingSpec
		maxTokens  int
		wantOK     bool
		wantType   string
		wantBudget int // asserted only when wantType == "enabled"
	}{
		{
			name: "adaptive opus 4.8", model: "claude-opus-4-8",
			spec: &ThinkingSpec{BudgetTokens: 4096}, maxTokens: 16000,
			wantOK: true, wantType: "adaptive",
		},
		{
			name: "adaptive opus 4.6", model: "claude-opus-4-6",
			spec: &ThinkingSpec{BudgetTokens: 4096}, maxTokens: 16000,
			wantOK: true, wantType: "adaptive",
		},
		{
			name: "adaptive sonnet 4.6", model: "claude-sonnet-4-6",
			spec: &ThinkingSpec{BudgetTokens: 4096}, maxTokens: 16000,
			wantOK: true, wantType: "adaptive",
		},
		{
			name: "adaptive sonnet 5", model: "claude-sonnet-5",
			spec: &ThinkingSpec{BudgetTokens: 4096}, maxTokens: 16000,
			wantOK: true, wantType: "adaptive",
		},
		{
			name: "adaptive fable", model: "claude-fable-5",
			spec: &ThinkingSpec{BudgetTokens: 4096}, maxTokens: 16000,
			wantOK: true, wantType: "adaptive",
		},
		{
			name: "legacy sonnet 4.5 dated snapshot", model: "claude-sonnet-4-5-20250929",
			spec: &ThinkingSpec{BudgetTokens: 4096}, maxTokens: 16000,
			wantOK: true, wantType: "enabled", wantBudget: 4096,
		},
		{
			name: "legacy haiku 4.5", model: "claude-haiku-4-5",
			spec: &ThinkingSpec{BudgetTokens: 2048}, maxTokens: 8000,
			wantOK: true, wantType: "enabled", wantBudget: 2048,
		},
		{
			name: "legacy clamps budget up to API floor", model: "claude-sonnet-4-5",
			spec: &ThinkingSpec{BudgetTokens: 512}, maxTokens: 16000,
			wantOK: true, wantType: "enabled", wantBudget: 1024,
		},
		{
			name: "legacy clamps budget below max_tokens", model: "claude-sonnet-4-5",
			spec: &ThinkingSpec{BudgetTokens: 20000}, maxTokens: 16000,
			wantOK: true, wantType: "enabled", wantBudget: 15999,
		},
		{
			name: "legacy fails closed when max_tokens leaves no room", model: "claude-sonnet-4-5",
			spec: &ThinkingSpec{BudgetTokens: 4096}, maxTokens: 1024,
			wantOK: false,
		},
		{
			name: "zero budget disables", model: "claude-sonnet-4-5",
			spec: &ThinkingSpec{BudgetTokens: 0}, maxTokens: 16000,
			wantOK: false,
		},
		{
			name: "nil spec disables", model: "claude-opus-4-8",
			spec: nil, maxTokens: 16000,
			wantOK: false,
		},
		{
			name: "unknown non-claude model fails closed", model: "gpt-4.1",
			spec: &ThinkingSpec{BudgetTokens: 4096}, maxTokens: 16000,
			wantOK: false,
		},
		{
			name: "gateway tier alias fails closed", model: "tier:reasoning",
			spec: &ThinkingSpec{BudgetTokens: 4096}, maxTokens: 16000,
			wantOK: false,
		},
		{
			name: "unknown open-weights model fails closed", model: "glm-4",
			spec: &ThinkingSpec{BudgetTokens: 4096}, maxTokens: 16000,
			wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := anthropicThinkingParam(tc.model, tc.spec, tc.maxTokens)
			if ok != tc.wantOK {
				t.Fatalf("ok=%v, want %v (param=%v)", ok, tc.wantOK, got)
			}
			if !tc.wantOK {
				if got != nil {
					t.Fatalf("param=%v, want nil", got)
				}
				return
			}
			if got["type"] != tc.wantType {
				t.Errorf("type=%v, want %s", got["type"], tc.wantType)
			}
			if tc.wantType == "adaptive" {
				if _, has := got["budget_tokens"]; has {
					t.Error("adaptive param must not carry budget_tokens (400 on 4.7+)")
				}
			}
			if tc.wantType == "enabled" {
				if got["budget_tokens"] != tc.wantBudget {
					t.Errorf("budget_tokens=%v, want %d", got["budget_tokens"], tc.wantBudget)
				}
			}
		})
	}
}
