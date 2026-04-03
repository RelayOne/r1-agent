package model

import (
	"strings"
	"testing"
)

func TestDefaultPipelineConfig(t *testing.T) {
	cfg := DefaultPipelineConfig()
	if !cfg.Enabled {
		t.Error("default should be enabled")
	}
	if cfg.ArchitectProvider != ProviderClaude {
		t.Errorf("expected Claude as architect, got %s", cfg.ArchitectProvider)
	}
	if cfg.EditorProvider != ProviderCodex {
		t.Errorf("expected Codex as editor, got %s", cfg.EditorProvider)
	}
}

func TestSingleModelConfig(t *testing.T) {
	cfg := SingleModelConfig(ProviderClaude)
	if cfg.Enabled {
		t.Error("single model should not be enabled")
	}
	if cfg.ArchitectProvider != ProviderClaude {
		t.Error("all roles should be Claude")
	}
}

func TestResolveRolePipeline(t *testing.T) {
	cfg := DefaultPipelineConfig()
	allAvail := func(Provider) bool { return true }

	arch := cfg.ResolveRole(RoleArchitect, allAvail)
	if arch != ProviderClaude {
		t.Errorf("architect should be Claude, got %s", arch)
	}

	edit := cfg.ResolveRole(RoleEditor, allAvail)
	if edit != ProviderCodex {
		t.Errorf("editor should be Codex, got %s", edit)
	}

	rev := cfg.ResolveRole(RoleReviewer, allAvail)
	if rev != ProviderCodex {
		t.Errorf("reviewer should be Codex, got %s", rev)
	}
}

func TestResolveRoleSingleModel(t *testing.T) {
	cfg := SingleModelConfig(ProviderClaude)
	allAvail := func(Provider) bool { return true }

	for _, role := range []PipelineRole{RoleArchitect, RoleEditor, RoleReviewer} {
		p := cfg.ResolveRole(role, allAvail)
		if p != ProviderClaude {
			t.Errorf("single model: %s should resolve to Claude, got %s", role, p)
		}
	}
}

func TestResolveRoleFallback(t *testing.T) {
	cfg := DefaultPipelineConfig()

	// Claude unavailable — editor should still work
	noClaude := func(p Provider) bool { return p != ProviderClaude }
	arch := cfg.ResolveRole(RoleArchitect, noClaude)
	if arch == ProviderClaude {
		t.Error("should not return unavailable provider")
	}
	if arch == ProviderLintOnly {
		t.Error("should fall back to Codex, not lint-only")
	}
}

func TestResolveRoleNoneAvailable(t *testing.T) {
	cfg := DefaultPipelineConfig()
	none := func(Provider) bool { return false }

	p := cfg.ResolveRole(RoleArchitect, none)
	if p != ProviderLintOnly {
		t.Errorf("expected lint-only when nothing available, got %s", p)
	}
}

func TestArchitectPrompt(t *testing.T) {
	prompt := ArchitectPrompt("add caching to the API")
	if !strings.Contains(prompt, "ARCHITECT") {
		t.Error("expected ARCHITECT in prompt")
	}
	if !strings.Contains(prompt, "add caching") {
		t.Error("expected task in prompt")
	}
	if !strings.Contains(prompt, "Do NOT write code") {
		t.Error("expected code prohibition")
	}
}

func TestEditorPrompt(t *testing.T) {
	prompt := EditorPrompt("modify handler.go to add cache check", "add caching")
	if !strings.Contains(prompt, "EDITOR") {
		t.Error("expected EDITOR in prompt")
	}
	if !strings.Contains(prompt, "modify handler.go") {
		t.Error("expected architect output")
	}
	if !strings.Contains(prompt, "add caching") {
		t.Error("expected original task")
	}
}

func TestShouldUsePipeline(t *testing.T) {
	tests := []struct {
		taskType TaskType
		want     bool
	}{
		{TaskTypeArchitecture, true},
		{TaskTypeSecurity, true},
		{TaskTypeConcurrency, true},
		{TaskTypeDocs, false},
		{TaskTypeRefactor, true},
	}
	for _, tc := range tests {
		got := ShouldUsePipeline(tc.taskType)
		if got != tc.want {
			t.Errorf("ShouldUsePipeline(%s) = %v, want %v", tc.taskType, got, tc.want)
		}
	}
}
