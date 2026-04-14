package modelsource

import (
	"os"
	"testing"
)

func withEnv(t *testing.T, kv map[string]string, fn func()) {
	t.Helper()
	prev := map[string]string{}
	for k := range kv {
		prev[k] = os.Getenv(k)
	}
	for k, v := range kv {
		os.Setenv(k, v)
	}
	defer func() {
		for k, v := range prev {
			if v == "" {
				os.Unsetenv(k)
			} else {
				os.Setenv(k, v)
			}
		}
	}()
	fn()
}

func TestExpandModelAlias(t *testing.T) {
	cases := map[string]string{
		"sonnet":     "claude-sonnet-4-6",
		"opus":       "claude-opus-4-6",
		"gemini":     "gemini-2.5-pro",
		"flash":      "gemini-2.5-flash",
		"codex":      "gpt-5-codex",
		"something-else-verbatim": "something-else-verbatim",
	}
	for in, want := range cases {
		if got := expandModelAlias(in); got != want {
			t.Fatalf("expandModelAlias(%q)=%q want %q", in, got, want)
		}
	}
}

func TestFamilyFor(t *testing.T) {
	cases := []struct {
		model, alias string
		want         modelFamily
	}{
		{"claude-sonnet-4-6", "sonnet", familyAnthropic},
		{"gpt-5-codex", "codex", familyOpenAI},
		{"gemini-2.5-pro", "gemini", familyGemini},
		{"gpt-5", "", familyOpenAI},
		{"something-unknown", "", familyUnknown},
	}
	for _, tc := range cases {
		if got := familyFor(tc.model, tc.alias); got != tc.want {
			t.Fatalf("familyFor(%q, %q)=%d want %d", tc.model, tc.alias, got, tc.want)
		}
	}
}

func TestBuildOpenRouter(t *testing.T) {
	r, err := Config{Role: RoleReviewer, Source: SourceOpenRouter, Model: "sonnet", APIKey: "sk-test"}.Build()
	if err != nil {
		t.Fatal(err)
	}
	if r.Provider == nil {
		t.Fatal("expected a provider")
	}
	if r.Model != "claude-sonnet-4-6" {
		t.Fatalf("alias not expanded; got %q", r.Model)
	}
	if r.Endpoint != "https://openrouter.ai/api" {
		t.Fatalf("wrong endpoint %q", r.Endpoint)
	}
}

func TestBuildDirectGemini(t *testing.T) {
	r, err := Config{Role: RoleReviewer, Source: SourceDirect, Model: "gemini", APIKey: "AIza-test"}.Build()
	if err != nil {
		t.Fatal(err)
	}
	if r.Model != "gemini-2.5-pro" {
		t.Fatalf("alias not expanded; got %q", r.Model)
	}
	// Gemini's chat completions live at /v1beta/openai/chat/completions —
	// ensure the resolved endpoint actually includes that full path.
	want := "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions"
	if r.Endpoint != want {
		t.Fatalf("Gemini endpoint wrong: got %q want %q", r.Endpoint, want)
	}
}

func TestBuildDirectAnthropic(t *testing.T) {
	r, err := Config{Role: RoleBuilder, Source: SourceDirect, Model: "sonnet", APIKey: "sk-test"}.Build()
	if err != nil {
		t.Fatal(err)
	}
	if r.Endpoint != "https://api.anthropic.com" {
		t.Fatalf("wrong endpoint %q", r.Endpoint)
	}
	if r.Provider.Name() != "anthropic" {
		t.Fatalf("expected anthropic provider, got %q", r.Provider.Name())
	}
}

func TestResolveRoleImplicitGeminiReviewer(t *testing.T) {
	// When GEMINI_KEY is set and the operator has not specified any
	// reviewer inputs, the reviewer role auto-routes to Gemini direct.
	withEnv(t, map[string]string{
		"GEMINI_KEY":        "AIza-from-env",
		"BUILDER_MODEL":     "",
		"BUILDER_SOURCE":    "",
		"REVIEWER_MODEL":    "",
		"REVIEWER_SOURCE":   "",
		"REVIEWER_URL":      "",
		"REVIEWER_API_KEY":  "",
	}, func() {
		r, changed, err := ResolveRole(RoleReviewer, "", "", "", "", "", "", "")
		if err != nil {
			t.Fatal(err)
		}
		if !changed || r == nil {
			t.Fatal("expected implicit override when GEMINI_KEY is set")
		}
		if r.Source != SourceDirect || r.Model != "gemini-2.5-pro" {
			t.Fatalf("expected direct gemini-2.5-pro; got %+v", r)
		}
	})
}

func TestResolveRoleUnspecifiedPreservesLegacy(t *testing.T) {
	withEnv(t, map[string]string{
		"GEMINI_KEY":        "",
		"GEMINI_API_KEY":    "",
		"BUILDER_MODEL":     "",
		"BUILDER_SOURCE":    "",
		"BUILDER_URL":       "",
		"BUILDER_API_KEY":   "",
	}, func() {
		r, changed, err := ResolveRole(RoleBuilder, "", "", "", "", "claude-sonnet-4-6", "http://localhost:4001", "sk-legacy")
		if err != nil {
			t.Fatal(err)
		}
		if changed || r != nil {
			t.Fatalf("no inputs should mean no override; got changed=%v r=%+v", changed, r)
		}
	})
}

func TestResolveRoleFlagOverridesEnv(t *testing.T) {
	withEnv(t, map[string]string{
		"REVIEWER_MODEL":  "opus",
		"REVIEWER_SOURCE": "litellm",
	}, func() {
		r, changed, err := ResolveRole(RoleReviewer, "gemini", "direct", "", "AIza-from-flag", "", "", "")
		if err != nil {
			t.Fatal(err)
		}
		if !changed || r == nil {
			t.Fatal("expected override")
		}
		if r.Source != SourceDirect || r.Model != "gemini-2.5-pro" {
			t.Fatalf("flags should beat env; got %+v", r)
		}
	})
}
