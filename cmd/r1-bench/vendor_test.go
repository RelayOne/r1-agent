package main

import (
	"strings"
	"testing"
)

func TestVendorForAgent_KnownAgents(t *testing.T) {
	cases := []struct {
		agent  string
		want   string
	}{
		{"r1", "anthropic"},
		{"r1-antitrunc", "anthropic"},
		{"claude-code-default", "anthropic"},
		{"claude-code-stop-hook", "anthropic"},
		{"codex-cli", "openai"},
		{"cursor", "anthropic"},
		{"aider", ""},
		{"cline", ""},
		{"tether+codex-cli", "openai"},
		{"tether+claude-code-default", "anthropic"},
		{"unknown-agent", ""},
	}
	for _, tc := range cases {
		got := vendorForAgent(tc.agent)
		if got != tc.want {
			t.Errorf("vendorForAgent(%q) = %q, want %q", tc.agent, got, tc.want)
		}
	}
}

func TestVendorForModel_KnownModels(t *testing.T) {
	cases := []struct {
		model string
		want  string
	}{
		{"claude-opus-4-7", "anthropic"},
		{"claude-sonnet-4-5", "anthropic"},
		{"gpt-5", "openai"},
		{"gpt-4o", "openai"},
		{"o1-preview", "openai"},
		{"o3-mini", "openai"},
		{"o4-mini", "openai"},
		{"gemini-2.5-pro", "google"},
		{"mistral-large", "mistral"},
		{"llama-3.1-70b", "meta"},
		{"meta-llama/llama-3", "meta"},
		{"unknown-model-xyz", ""},
	}
	for _, tc := range cases {
		got := vendorForModel(tc.model)
		if got != tc.want {
			t.Errorf("vendorForModel(%q) = %q, want %q", tc.model, got, tc.want)
		}
	}
}

// TestCrossVendorJudgeRejected asserts the cross-vendor judge
// constraint: when agent + judge vendors match, the runner refuses
// to start.
func TestCrossVendorJudgeRejected(t *testing.T) {
	// Same-vendor pairs that MUST be rejected by the runner.
	rejectPairs := []struct {
		agent, judge string
	}{
		{"r1", "claude-opus-4-7"},
		{"claude-code-default", "claude-sonnet-4-5"},
		{"codex-cli", "gpt-5"},
		{"codex-cli", "o3-mini"},
		{"tether+codex-cli", "gpt-4o"},
	}
	for _, p := range rejectPairs {
		av := vendorForAgent(p.agent)
		jv := vendorForModel(p.judge)
		if av == "" || jv == "" {
			t.Errorf("test fixture broken: agent=%q vendor=%q, judge=%q vendor=%q (need both non-empty)", p.agent, av, p.judge, jv)
			continue
		}
		if av != jv {
			t.Errorf("expected same-vendor pair (agent=%q jud=%q): agent=%q judge=%q", p.agent, p.judge, av, jv)
		}
	}
}

// TestCrossVendorJudgeAccepted asserts legitimately-cross pairs are
// accepted by the constraint check.
func TestCrossVendorJudgeAccepted(t *testing.T) {
	acceptPairs := []struct {
		agent, judge string
	}{
		{"r1", "gpt-5"},
		{"claude-code-default", "gpt-4o"},
		{"codex-cli", "claude-opus-4-7"},
		{"tether+codex-cli", "gemini-2.5-pro"},
	}
	for _, p := range acceptPairs {
		av := vendorForAgent(p.agent)
		jv := vendorForModel(p.judge)
		if av == "" || jv == "" {
			t.Errorf("fixture broken: agent=%q vendor=%q, judge=%q vendor=%q", p.agent, av, p.judge, jv)
			continue
		}
		if av == jv {
			t.Errorf("expected cross-vendor pair (agent=%q judge=%q) but vendors matched: %q", p.agent, p.judge, av)
		}
	}
}

// TestBuildJudge_EmptyModelErrors locks in the precondition.
func TestBuildJudge_EmptyModelErrors(t *testing.T) {
	_, err := buildJudge("")
	if err == nil {
		t.Errorf("buildJudge(\"\") should error")
	}
}

// TestConfigForModel_PickProvider asserts model→provider mapping.
func TestConfigForModel_PickProvider(t *testing.T) {
	cases := []struct {
		model, wantBaseURL, wantKeyEnv string
	}{
		{"claude-opus-4-7", "anthropic.com", "ANTHROPIC_API_KEY"},
		{"gpt-5", "openai.com", "OPENAI_API_KEY"},
		{"gemini-2.5-pro", "openrouter.ai", "OPENROUTER_API_KEY"},
	}
	for _, tc := range cases {
		// Set the env var to a known value so we can verify it's the
		// one the helper read.
		old := getenvFirst
		getenvFirst = func(name string) string {
			if name == tc.wantKeyEnv {
				return "sentinel-" + name
			}
			return ""
		}
		cfg, key, err := configForModel(tc.model)
		getenvFirst = old
		if err != nil {
			t.Fatalf("configForModel(%q): %v", tc.model, err)
		}
		if !strings.Contains(cfg.BaseURL, tc.wantBaseURL) {
			t.Errorf("model %q: BaseURL = %q, want substring %q", tc.model, cfg.BaseURL, tc.wantBaseURL)
		}
		if key != "sentinel-"+tc.wantKeyEnv {
			t.Errorf("model %q: key = %q, want from %s", tc.model, key, tc.wantKeyEnv)
		}
	}
}
