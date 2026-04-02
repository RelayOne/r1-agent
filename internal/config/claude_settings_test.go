package config

import "testing"

func TestBuildClaudeSettingsMode1SuppressesAPIKeyHelper(t *testing.T) {
	settings := BuildClaudeSettings(ClaudeSettingsOptions{
		Mode: "mode1",
		Phase: PhasePolicy{
			AllowedRules: []string{"Read"},
			DeniedRules:  []string{"Bash(curl *)"},
		},
		SandboxEnabled: true,
	})
	if settings.APIKeyHelper != nil {
		t.Fatalf("expected apiKeyHelper to be nil (JSON null) in mode1, got %v", settings.APIKeyHelper)
	}
	if settings.Sandbox == nil || !settings.Sandbox.FailIfUnavailable {
		t.Fatalf("expected fail-closed sandbox settings")
	}
}
