package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// fakeEngineBin writes an executable stub into dir and returns its path,
// so specRunnerModels' exec.LookPath check sees an "installed" engine
// without needing the real claude/codex CLIs.
func fakeEngineBin(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write stub %s: %v", name, err)
	}
	return p
}

func TestSpecRunnerModels(t *testing.T) {
	dir := t.TempDir()
	claude := fakeEngineBin(t, dir, "claude-stub")
	codex := fakeEngineBin(t, dir, "codex-stub")
	missing := filepath.Join(dir, "definitely-not-installed")

	// Native mode pins rollouts to the only credentialed engine,
	// regardless of which CLIs are on PATH.
	if got := specRunnerModels("native", claude, codex); !reflect.DeepEqual(got, []string{"native"}) {
		t.Errorf("native mode = %v, want [native]", got)
	}

	tests := []struct {
		name       string
		runnerMode string
		claudeBin  string
		codexBin   string
		want       []string
	}{
		// Both engines installed → router-ordered pair (claude first).
		{"both installed", "", claude, codex, []string{"claude", "codex"}},
		{"both installed hybrid", "hybrid", claude, codex, []string{"claude", "codex"}},
		// Only one engine installed → single-model list, no dead runner.
		{"only claude", "", claude, missing, []string{"claude"}},
		{"only codex", "", missing, codex, []string{"codex"}},
		// Neither installed → empty, so rollouts use the build default
		// instead of round-robining binaries that would fail to launch.
		{"neither installed", "", missing, missing, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := specRunnerModels(tt.runnerMode, tt.claudeBin, tt.codexBin); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("specRunnerModels(%q, claude=%v, codex=%v) = %v, want %v",
					tt.runnerMode, tt.claudeBin != missing, tt.codexBin != missing, got, tt.want)
			}
		})
	}

	// Default binary names ("claude"/"codex") when the config leaves them
	// blank must not panic and must return only what is actually on PATH.
	_ = specRunnerModels("", "", "")
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
