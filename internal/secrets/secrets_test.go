package secrets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withEnv sets envVar=value for the test and restores on cleanup. Unset when
// value is "".
func withEnv(t *testing.T, key, value string) {
	t.Helper()
	prev, had := os.LookupEnv(key)
	if value == "" {
		os.Unsetenv(key)
	} else {
		os.Setenv(key, value)
	}
	t.Cleanup(func() {
		if had {
			os.Setenv(key, prev)
		} else {
			os.Unsetenv(key)
		}
	})
}

// writeFile creates a file and registers cleanup.
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResolve_InlineWins(t *testing.T) {
	withEnv(t, "TEST_VAR", "from-env")
	got, err := Resolve("from-inline", "TEST_VAR")
	if err != nil {
		t.Fatal(err)
	}
	if got != "from-inline" {
		t.Errorf("expected inline to win, got %q", got)
	}
}

func TestResolve_EnvWinsOverFile(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "secret", "from-file")
	withEnv(t, "TEST_VAR", "from-env")
	withEnv(t, "TEST_VAR_FILE", path)
	got, err := Resolve("", "TEST_VAR")
	if err != nil {
		t.Fatal(err)
	}
	if got != "from-env" {
		t.Errorf("expected env to win, got %q", got)
	}
}

func TestResolve_FileUsedWhenEnvEmpty(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "secret", "from-file")
	withEnv(t, "TEST_VAR", "")
	withEnv(t, "TEST_VAR_FILE", path)
	got, err := Resolve("", "TEST_VAR")
	if err != nil {
		t.Fatal(err)
	}
	if got != "from-file" {
		t.Errorf("expected file value, got %q", got)
	}
}

func TestResolve_FileTrimsWhitespace(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "secret", "  from-file-with-newline\n\n")
	withEnv(t, "TEST_VAR_FILE", path)
	got, err := Resolve("", "TEST_VAR")
	if err != nil {
		t.Fatal(err)
	}
	if got != "from-file-with-newline" {
		t.Errorf("expected whitespace-trimmed, got %q", got)
	}
}

func TestResolve_EnvTrimsWhitespace(t *testing.T) {
	withEnv(t, "TEST_VAR", "  spaced-env  ")
	got, err := Resolve("", "TEST_VAR")
	if err != nil {
		t.Fatal(err)
	}
	if got != "spaced-env" {
		t.Errorf("expected trimmed, got %q", got)
	}
}

func TestResolve_EmptyFileFallsThrough(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "empty", "")
	withEnv(t, "TEST_VAR_FILE", path)
	got, err := Resolve("", "TEST_VAR")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("empty file should return empty, got %q", got)
	}
}

func TestResolve_AllEmpty(t *testing.T) {
	withEnv(t, "TEST_VAR", "")
	withEnv(t, "TEST_VAR_FILE", "")
	got, err := Resolve("", "TEST_VAR")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("all-empty should return empty, got %q", got)
	}
}

func TestResolve_UnreadableFileIsError(t *testing.T) {
	withEnv(t, "TEST_VAR_FILE", "/nonexistent/path/nowhere")
	_, err := Resolve("", "TEST_VAR")
	if err == nil {
		t.Fatal("expected error on unreadable file")
	}
	if !strings.Contains(err.Error(), "secrets:") {
		t.Errorf("expected 'secrets:' in error, got %v", err)
	}
}

func TestResolve_InlineWhitespaceFallsThrough(t *testing.T) {
	withEnv(t, "TEST_VAR", "from-env")
	got, err := Resolve("   ", "TEST_VAR")
	if err != nil {
		t.Fatal(err)
	}
	if got != "from-env" {
		t.Errorf("whitespace inline should fall through, got %q", got)
	}
}

func TestResolve_EmptyEnvVarName(t *testing.T) {
	got, err := Resolve("from-inline", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "from-inline" {
		t.Errorf("inline should still work with empty envVar, got %q", got)
	}
}

func TestResolveRequired_EmptyInlineAndEnvVarRejected(t *testing.T) {
	_, err := ResolveRequired("", "")
	if err == nil {
		t.Fatal("expected error when both inline and envVar are empty")
	}
	if !strings.Contains(err.Error(), "no resolvable source") {
		t.Errorf("expected 'no resolvable source' in error, got %v", err)
	}
}

func TestResolveRequired_SucceedsWhenPresent(t *testing.T) {
	withEnv(t, "TEST_VAR", "present")
	got, err := ResolveRequired("", "TEST_VAR")
	if err != nil {
		t.Fatal(err)
	}
	if got != "present" {
		t.Errorf("expected 'present', got %q", got)
	}
}

func TestResolveRequired_FailsWhenAllEmpty(t *testing.T) {
	withEnv(t, "MISSING_VAR", "")
	withEnv(t, "MISSING_VAR_FILE", "")
	_, err := ResolveRequired("", "MISSING_VAR")
	if err == nil {
		t.Fatal("expected error when all sources empty")
	}
	if !strings.Contains(err.Error(), "MISSING_VAR") {
		t.Errorf("error should name the env var, got %v", err)
	}
	if !strings.Contains(err.Error(), "MISSING_VAR_FILE") {
		t.Errorf("error should mention _FILE variant, got %v", err)
	}
}

func TestReloadFromFile_ReadsCurrentContents(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "rotated", "v1")
	withEnv(t, "TEST_VAR_FILE", path)

	got, err := ReloadFromFile("TEST_VAR")
	if err != nil {
		t.Fatal(err)
	}
	if got != "v1" {
		t.Errorf("expected v1, got %q", got)
	}

	// Rotate the file.
	if err := os.WriteFile(path, []byte("v2-rotated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = ReloadFromFile("TEST_VAR")
	if err != nil {
		t.Fatal(err)
	}
	if got != "v2-rotated" {
		t.Errorf("expected v2-rotated after rotation, got %q", got)
	}
}

func TestReloadFromFile_ErrorsWhenEnvVarEmpty(t *testing.T) {
	_, err := ReloadFromFile("")
	if err == nil {
		t.Fatal("expected error for empty envVar")
	}
}

func TestReloadFromFile_ErrorsWhenFileVarUnset(t *testing.T) {
	withEnv(t, "UNSET_VAR_FILE", "")
	_, err := ReloadFromFile("UNSET_VAR")
	if err == nil {
		t.Fatal("expected error when _FILE var unset")
	}
	if !strings.Contains(err.Error(), "UNSET_VAR_FILE") {
		t.Errorf("error should name the _FILE var, got %v", err)
	}
}

func TestReloadFromFile_ErrorsWhenFileMissing(t *testing.T) {
	withEnv(t, "TEST_VAR_FILE", "/nonexistent/nowhere/secret")
	_, err := ReloadFromFile("TEST_VAR")
	if err == nil {
		t.Fatal("expected error when file unreadable")
	}
}
