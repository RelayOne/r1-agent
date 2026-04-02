package engine

import (
	"strings"
	"testing"
)

func TestSafeEnvForClaudeMode1StripsAuthVariables(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "secret")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "token")
	t.Setenv("OPENAI_API_KEY", "openai")
	t.Setenv("PATH", "/usr/bin")

	env := safeEnvForClaudeMode1("/tmp/pool")
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "ANTHROPIC_API_KEY=") || strings.Contains(joined, "OPENAI_API_KEY=") {
		t.Fatalf("auth env vars not stripped: %q", joined)
	}
	if !strings.Contains(joined, "CLAUDE_CONFIG_DIR=/tmp/pool") {
		t.Fatalf("CLAUDE_CONFIG_DIR not injected")
	}
}

func TestSafeEnvForClaudeMode1StripsCloudProviders(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "aws-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "aws-secret")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/path/creds.json")
	t.Setenv("AZURE_TENANT_ID", "tenant")
	t.Setenv("BEDROCK_ENDPOINT", "https://bedrock")
	t.Setenv("VERTEX_PROJECT", "proj")
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "1")
	t.Setenv("CLAUDE_CODE_USE_VERTEX", "1")
	t.Setenv("PATH", "/usr/bin")

	env := safeEnvForClaudeMode1("/tmp/pool")
	joined := strings.Join(env, "\n")

	forbidden := []string{
		"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY",
		"GOOGLE_APPLICATION_CREDENTIALS", "AZURE_TENANT_ID",
		"BEDROCK_ENDPOINT", "VERTEX_PROJECT",
		"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX",
	}
	for _, key := range forbidden {
		if strings.Contains(joined, key+"=") {
			t.Errorf("cloud provider var %s should be stripped in Mode 1", key)
		}
	}
}

func TestSafeEnvForClaudeMode1PassesAllowlisted(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("HOME", "/home/test")
	t.Setenv("LANG", "en_US.UTF-8")
	t.Setenv("GIT_AUTHOR_NAME", "Test")
	t.Setenv("npm_config_registry", "https://registry.npmjs.org")

	env := safeEnvForClaudeMode1("/tmp/pool")
	joined := strings.Join(env, "\n")

	for _, key := range []string{"PATH=", "HOME=", "LANG=", "GIT_AUTHOR_NAME=", "npm_config_registry="} {
		if !strings.Contains(joined, key) {
			t.Errorf("allowlisted var %s should pass through", key)
		}
	}
}

func TestSafeEnvMode2PassesEverything(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "keep-this")
	t.Setenv("CUSTOM_VAR", "custom-value")

	env := safeEnvMode2(map[string]string{"EXTRA": "val"})
	joined := strings.Join(env, "\n")

	if !strings.Contains(joined, "ANTHROPIC_API_KEY=keep-this") {
		t.Error("Mode 2 should pass ANTHROPIC_API_KEY")
	}
	if !strings.Contains(joined, "EXTRA=val") {
		t.Error("Mode 2 should inject extra vars")
	}
}

func TestSafeEnvForCodexMode1StripsAuth(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "openai-secret")
	t.Setenv("CODEX_API_KEY", "codex-secret")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-secret")
	t.Setenv("AWS_ACCESS_KEY_ID", "aws-key")
	t.Setenv("PATH", "/usr/bin")

	env := safeEnvForCodexMode1("/tmp/codex-pool")
	joined := strings.Join(env, "\n")

	for _, key := range []string{"OPENAI_API_KEY=", "CODEX_API_KEY=", "ANTHROPIC_API_KEY=", "AWS_ACCESS_KEY_ID="} {
		if strings.Contains(joined, key) {
			t.Errorf("auth var %s should be stripped in Codex Mode 1", key)
		}
	}
	if !strings.Contains(joined, "CODEX_HOME=/tmp/codex-pool") {
		t.Error("CODEX_HOME should be injected")
	}
}

func TestMode1ConfigDirOverridesExisting(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/original/path")
	t.Setenv("PATH", "/usr/bin")

	env := safeEnvForClaudeMode1("/override/path")
	configDirCount := 0
	for _, e := range env {
		if strings.HasPrefix(e, "CLAUDE_CONFIG_DIR=") {
			configDirCount++
			if e != "CLAUDE_CONFIG_DIR=/override/path" {
				t.Errorf("CLAUDE_CONFIG_DIR should be overridden, got %s", e)
			}
		}
	}
	if configDirCount != 1 {
		t.Errorf("expected exactly 1 CLAUDE_CONFIG_DIR, got %d", configDirCount)
	}
}
