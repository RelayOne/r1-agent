// Package secrets resolves sensitive config values from multiple sources.
//
// Resolution order: inline value → env var → file pointed at by env var + "_FILE".
//
// The file source (3) is conventional for Kubernetes Secret projection and
// GitHub Actions `with: { value_file: ... }` patterns. Empty strings at each
// layer fall through to the next layer so operators can leave a variable unset
// without explicit defaults.
package secrets

import (
	"fmt"
	"os"
	"strings"
)

// Resolve returns the first non-empty value among:
//
//  1. inline
//  2. os.Getenv(envVar)
//  3. trimmed contents of the file at os.Getenv(envVar + "_FILE")
//
// Whitespace is trimmed from every source (inline, env, file) so operators
// can paste values with incidental trailing newlines from `echo token >
// file` or copy-paste whitespace in inline config without corrupting tokens.
// A source that is purely whitespace is treated as absent and falls through
// to the next source.
//
// Returns ("", nil) when all three sources are empty.
// Returns ("", err) only when (3)'s file path is set AND the file cannot be
// read. An empty file at (3) is treated as an absent source (falls through to
// "").
func Resolve(inline, envVar string) (string, error) {
	if v := strings.TrimSpace(inline); v != "" {
		return v, nil
	}
	if envVar == "" {
		return "", nil
	}
	if v := strings.TrimSpace(os.Getenv(envVar)); v != "" {
		return v, nil
	}
	path := strings.TrimSpace(os.Getenv(envVar + "_FILE"))
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("secrets: read %s (from %s_FILE): %w", path, envVar, err)
	}
	return strings.TrimSpace(string(data)), nil
}

// ResolveRequired is Resolve but returns a non-nil error when the result is
// empty. The error names the env var so operators can diagnose which variable
// is missing without reading source.
func ResolveRequired(inline, envVar string) (string, error) {
	if envVar == "" && strings.TrimSpace(inline) == "" {
		return "", fmt.Errorf("secrets: ResolveRequired called with empty inline AND empty envVar — no resolvable source")
	}
	v, err := Resolve(inline, envVar)
	if err != nil {
		return "", err
	}
	if v == "" {
		return "", fmt.Errorf("secrets: %s required (set %s or %s_FILE)", envVar, envVar, envVar)
	}
	return v, nil
}

// ReloadFromFile re-reads the file at os.Getenv(envVar + "_FILE") and returns
// the trimmed contents. Intended for SIGHUP-triggered secret rotation where
// the env var and file path stay stable but the file's bytes change.
//
// Returns ("", err) when:
//   - envVar + "_FILE" is unset
//   - the file is unreadable
func ReloadFromFile(envVar string) (string, error) {
	if envVar == "" {
		return "", fmt.Errorf("secrets: envVar required for ReloadFromFile")
	}
	path := strings.TrimSpace(os.Getenv(envVar + "_FILE"))
	if path == "" {
		return "", fmt.Errorf("secrets: %s_FILE not set", envVar)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("secrets: read %s (from %s_FILE): %w", path, envVar, err)
	}
	return strings.TrimSpace(string(data)), nil
}
