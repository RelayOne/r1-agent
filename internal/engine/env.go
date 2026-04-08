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
	// Deny-by-default: only allow known-safe variables through.
	// This is the allowlist approach — everything not listed is stripped.
	passthrough := []string{
		// Core POSIX
		"PATH", "HOME", "TERM", "LANG", "SHELL", "TMPDIR", "USER", "PWD",
		// XDG
		"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_RUNTIME_DIR",
		// Node.js (safe subset — NOT NODE_OPTIONS which allows --require injection)
		"NODE_PATH",
		// Proxy (needed in corporate environments)
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY",
		"http_proxy", "https_proxy", "no_proxy",
		// CI/display
		"NO_COLOR", "FORCE_COLOR",
		// Locale
		"LC_ALL", "LC_CTYPE",
	}
	passthroughPrefixes := []string{"npm_config_", "GIT_"}

	// Explicitly dangerous: these are stripped even if they somehow match
	// a prefix rule. Belt-and-suspenders defense.
	stripExact := map[string]bool{
		"ANTHROPIC_API_KEY":       true,
		"ANTHROPIC_AUTH_TOKEN":    true,
		"OPENAI_API_KEY":          true,
		"CODEX_API_KEY":           true,
		"CLAUDE_CODE_USE_BEDROCK": true,
		"CLAUDE_CODE_USE_VERTEX":  true,
		"CLAUDE_CODE_USE_FOUNDRY": true,
		"GITHUB_TOKEN":            true,
		"GH_TOKEN":                true,
		"NPM_TOKEN":               true,
		"NODE_OPTIONS":            true, // can inject --require /path/to/evil.js
		"DOCKER_HOST":             true,
		"KUBECONFIG":              true,
		"CI":                      true, // affects tool behavior, could leak context
	}
	stripPrefixes := []string{"AWS_", "GOOGLE_", "AZURE_", "BEDROCK_", "VERTEX_", "DOCKER_", "KUBE_"}

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
		"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_RUNTIME_DIR",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY",
		"http_proxy", "https_proxy", "no_proxy",
		"NO_COLOR", "FORCE_COLOR", "LC_ALL", "LC_CTYPE",
	}
	stripExact := map[string]bool{
		"OPENAI_API_KEY":       true,
		"CODEX_API_KEY":        true,
		"ANTHROPIC_API_KEY":    true,
		"ANTHROPIC_AUTH_TOKEN": true,
		"GITHUB_TOKEN":         true,
		"GH_TOKEN":             true,
		"NPM_TOKEN":            true,
		"NODE_OPTIONS":         true,
		"DOCKER_HOST":          true,
		"KUBECONFIG":           true,
	}
	stripPrefixes := []string{"AWS_", "GOOGLE_", "AZURE_", "BEDROCK_", "VERTEX_", "DOCKER_", "KUBE_"}

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
