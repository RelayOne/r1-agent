package main

import (
	"reflect"
	"testing"
)

func TestSpecRunnerModels(t *testing.T) {
	tests := []struct {
		runnerMode string
		want       []string
	}{
		{"", []string{"claude", "codex"}},
		{"claude", []string{"claude", "codex"}},
		{"codex", []string{"claude", "codex"}},
		{"hybrid", []string{"claude", "codex"}},
		// Native mode pins rollouts to the only credentialed engine.
		{"native", []string{"native"}},
	}
	for _, tt := range tests {
		if got := specRunnerModels(tt.runnerMode); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("specRunnerModels(%q) = %v, want %v", tt.runnerMode, got, tt.want)
		}
	}
}

func TestBuildSpecExecSelectorOfflineIsNil(t *testing.T) {
	// No native runner, no key, no base URL: the selector must be nil
	// so winner selection stays fully deterministic offline.
	if sel := buildSpecExecSelector(BuildConfig{}); sel != nil {
		t.Error("selector constructed without any provider config; offline runs must stay deterministic")
	}

	// Kill-switch wins even when a provider could be built.
	t.Setenv("R1_DISABLE_SPECEXEC_LLM_SELECT", "1")
	cfg := BuildConfig{RunnerMode: "native", NativeAPIKey: "sk-test", NativeBaseURL: "http://localhost:4000"}
	if sel := buildSpecExecSelector(cfg); sel != nil {
		t.Error("R1_DISABLE_SPECEXEC_LLM_SELECT=1 must disable the LLM selector")
	}

	t.Setenv("R1_DISABLE_SPECEXEC_LLM_SELECT", "")
	if sel := buildSpecExecSelector(cfg); sel == nil {
		t.Error("expected a selector when a native provider is configured")
	}
}
