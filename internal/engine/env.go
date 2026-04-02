package engine

import (
	"os"
	"slices"
	"strings"
)

// SafeEnvForClaudeMode1 builds a scrubbed environment for Mode 1 Claude execution.
// Strips API keys, auth tokens, and cloud provider credentials.
func SafeEnvForClaudeMode1(configDir string) []string {
	return safeEnvForClaudeMode1(configDir)
}

func safeEnvForClaudeMode1(configDir string) []string {
	passthrough := []string{
		"PATH", "HOME", "TERM", "LANG", "SHELL", "TMPDIR", "USER",
		"NODE_PATH", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "PWD",
	}
	passthroughPrefixes := []string{"npm_config_", "GIT_"}
	stripExact := map[string]bool{
		"ANTHROPIC_API_KEY":       true,
		"ANTHROPIC_AUTH_TOKEN":    true,
		"OPENAI_API_KEY":          true,
		"CODEX_API_KEY":           true,
		"CLAUDE_CODE_USE_BEDROCK": true,
		"CLAUDE_CODE_USE_VERTEX":  true,
		"CLAUDE_CODE_USE_FOUNDRY": true,
	}
	stripPrefixes := []string{"AWS_", "GOOGLE_", "AZURE_", "BEDROCK_", "VERTEX_"}

	env := []string{"CLAUDE_CONFIG_DIR=" + configDir}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if stripExact[key] || hasAnyPrefix(key, stripPrefixes) {
			continue
		}
		if slices.Contains(passthrough, key) || hasAnyPrefix(key, passthroughPrefixes) {
			env = append(env, entry)
		}
	}
	return env
}

func safeEnvForCodexMode1(home string) []string {
	passthrough := []string{
		"PATH", "HOME", "TERM", "LANG", "SHELL", "TMPDIR", "USER", "PWD",
		"XDG_CONFIG_HOME", "XDG_DATA_HOME",
	}
	stripExact := map[string]bool{
		"OPENAI_API_KEY":       true,
		"CODEX_API_KEY":        true,
		"ANTHROPIC_API_KEY":    true,
		"ANTHROPIC_AUTH_TOKEN": true,
	}
	stripPrefixes := []string{"AWS_", "GOOGLE_", "AZURE_", "BEDROCK_", "VERTEX_"}

	env := []string{"CODEX_HOME=" + home}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if stripExact[key] || hasAnyPrefix(key, stripPrefixes) {
			continue
		}
		if slices.Contains(passthrough, key) {
			env = append(env, entry)
		}
	}
	return env
}

func safeEnvMode2(extra map[string]string) []string {
	env := append([]string(nil), os.Environ()...)
	for key, value := range extra {
		env = append(env, key+"="+value)
	}
	return env
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}
