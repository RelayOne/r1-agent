package pools

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFirstNonEmptyStr locks the short-circuit behavior of the helper:
// returns the first non-empty entry, or "" when every arg is empty.
func TestFirstNonEmptyStr(t *testing.T) {
	cases := []struct {
		name string
		vals []string
		want string
	}{
		{"all empty", []string{"", "", ""}, ""},
		{"first wins", []string{"first", "second", "third"}, "first"},
		{"skip empties", []string{"", "", "third"}, "third"},
		{"single value", []string{"only"}, "only"},
		{"no args", []string{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstNonEmptyStr(tc.vals...); got != tc.want {
				t.Errorf("firstNonEmptyStr(%v) = %q, want %q", tc.vals, got, tc.want)
			}
		})
	}
}

// TestReadAccountID_EmailPreferred verifies readAccountID prefers the
// email field over accountId when both are present (the shipping
// precedence order).
func TestReadAccountID_EmailPreferred(t *testing.T) {
	dir := t.TempDir()
	creds := `{"claudeAiOauth":{"email":"alice@example.com","accountId":"u-123","accessToken":"tok-abcdefghijklmnop"}}`
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(creds), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readAccountID(dir); got != "alice@example.com" {
		t.Errorf("readAccountID = %q, want %q (email should win)", got, "alice@example.com")
	}
}

// TestReadAccountID_AccountIDFallback verifies that when email is
// missing, the accountId field is used.
func TestReadAccountID_AccountIDFallback(t *testing.T) {
	dir := t.TempDir()
	creds := `{"claudeAiOauth":{"accountId":"u-xyz"}}`
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(creds), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readAccountID(dir); got != "u-xyz" {
		t.Errorf("readAccountID = %q, want %q (accountId fallback)", got, "u-xyz")
	}
}

// TestReadAccountID_TokenFallback verifies the last-ditch path: when
// only an access token is present, readAccountID returns the "tok-"
// prefix plus the first 16 chars.
func TestReadAccountID_TokenFallback(t *testing.T) {
	dir := t.TempDir()
	creds := `{"claudeAiOauth":{"accessToken":"abcdefghijklmnopqrstuv"}}`
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(creds), 0o600); err != nil {
		t.Fatal(err)
	}
	got := readAccountID(dir)
	want := "tok-abcdefghijklmnop" // "tok-" + first 16 chars
	if got != want {
		t.Errorf("readAccountID = %q, want %q", got, want)
	}
}

// TestReadAccountID_MissingFileReturnsEmpty verifies readAccountID
// silently returns "" on missing file (caller differentiates by value).
func TestReadAccountID_MissingFileReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	if got := readAccountID(dir); got != "" {
		t.Errorf("readAccountID(empty dir) = %q, want empty", got)
	}
}

// TestReadAccountID_MalformedJSONReturnsEmpty covers the unmarshal
// error branch — a file that exists but isn't valid JSON should not
// panic and must return "".
func TestReadAccountID_MalformedJSONReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readAccountID(dir); got != "" {
		t.Errorf("readAccountID(bad JSON) = %q, want empty", got)
	}
}

// TestReadToken_ReturnsAccessToken verifies readToken extracts the raw
// accessToken (no prefixing, unlike readAccountID).
func TestReadToken_ReturnsAccessToken(t *testing.T) {
	dir := t.TempDir()
	creds := `{"claudeAiOauth":{"accessToken":"oa1-secretvalue"}}`
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(creds), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readToken(dir); got != "oa1-secretvalue" {
		t.Errorf("readToken = %q, want %q", got, "oa1-secretvalue")
	}
}

// TestReadToken_MissingReturnsEmpty covers the "file missing" branch.
func TestReadToken_MissingReturnsEmpty(t *testing.T) {
	if got := readToken(t.TempDir()); got != "" {
		t.Errorf("readToken(empty dir) = %q, want empty", got)
	}
}

// TestReadCodexAccountID_EmailInPrimaryFile verifies the codex reader
// tries .codex-credentials.json first and extracts the email field.
func TestReadCodexAccountID_EmailInPrimaryFile(t *testing.T) {
	dir := t.TempDir()
	creds := `{"email":"bob@example.com"}`
	if err := os.WriteFile(filepath.Join(dir, ".codex-credentials.json"), []byte(creds), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readCodexAccountID(dir); got != "bob@example.com" {
		t.Errorf("readCodexAccountID = %q, want %q", got, "bob@example.com")
	}
}

// TestReadCodexAccountID_FallbackFileNames exercises the fallback
// search order — if .codex-credentials.json is missing, the function
// checks credentials.json, then auth.json.
func TestReadCodexAccountID_FallbackFileNames(t *testing.T) {
	dir := t.TempDir()
	// Only auth.json exists with user_id — none of the earlier names.
	creds := `{"user_id":"u-777"}`
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(creds), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readCodexAccountID(dir); got != "u-777" {
		t.Errorf("readCodexAccountID = %q, want %q (auth.json fallback)", got, "u-777")
	}
}

// TestReadCodexAccountID_TokenPrefix covers the "only a token" branch:
// without id/email, a >16-char token becomes "codex-tok-<first 16>".
func TestReadCodexAccountID_TokenPrefix(t *testing.T) {
	dir := t.TempDir()
	creds := `{"access_token":"abcdefghijklmnopqrstuv"}`
	if err := os.WriteFile(filepath.Join(dir, ".codex-credentials.json"), []byte(creds), 0o600); err != nil {
		t.Fatal(err)
	}
	got := readCodexAccountID(dir)
	want := "codex-tok-abcdefghijklmnop"
	if got != want {
		t.Errorf("readCodexAccountID = %q, want %q", got, want)
	}
}

// TestReadCodexAccountID_AllMissingReturnsEmpty verifies no credentials
// file means empty string (not error, not panic).
func TestReadCodexAccountID_AllMissingReturnsEmpty(t *testing.T) {
	if got := readCodexAccountID(t.TempDir()); got != "" {
		t.Errorf("readCodexAccountID(empty dir) = %q, want empty", got)
	}
}

// TestCopyCredentials_CopiesRegularFilesOnly verifies copyCredentials
// copies regular files to the destination with 0600 perms but skips
// subdirectories and symlinks (for credential-safety reasons).
func TestCopyCredentials_CopiesRegularFilesOnly(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "dst")

	// Regular files to copy
	if err := os.WriteFile(filepath.Join(src, ".credentials.json"), []byte(`{"x":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "config.toml"), []byte("key=val"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Subdirectory that should be skipped
	if err := os.MkdirAll(filepath.Join(src, "nested", "deep"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "nested", "inside.txt"), []byte("skip"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Symlink that should be skipped
	if err := os.Symlink("/etc/passwd", filepath.Join(src, "malicious-link")); err != nil {
		t.Fatal(err)
	}

	if err := copyCredentials(src, dst); err != nil {
		t.Fatalf("copyCredentials: %v", err)
	}

	// Regular files present
	if _, err := os.Stat(filepath.Join(dst, ".credentials.json")); err != nil {
		t.Errorf(".credentials.json missing in dst: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "config.toml")); err != nil {
		t.Errorf("config.toml missing in dst: %v", err)
	}

	// Nested dir NOT copied
	if _, err := os.Stat(filepath.Join(dst, "nested")); err == nil {
		t.Error("nested/ should have been skipped")
	}

	// Symlink NOT followed/copied
	if _, err := os.Lstat(filepath.Join(dst, "malicious-link")); err == nil {
		t.Error("symlink should have been skipped to prevent pointing at sensitive files")
	}

	// Permissions tightened to 0600 on dst even though src was 0644
	info, err := os.Stat(filepath.Join(dst, ".credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("dst perms = %v, want 0600 (credentials must be restrictive)", info.Mode().Perm())
	}

	// Content round-trip
	data, err := os.ReadFile(filepath.Join(dst, ".credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"x":1}` {
		t.Errorf("content = %q, want %q", data, `{"x":1}`)
	}
}

// TestCopyCredentials_BadSrcDirReturnsError exercises the negative path:
// when the source directory does not exist, copyCredentials surfaces
// an error with the "read source dir" prefix.
func TestCopyCredentials_BadSrcDirReturnsError(t *testing.T) {
	src := filepath.Join(t.TempDir(), "does-not-exist")
	dst := filepath.Join(t.TempDir(), "dst")
	err := copyCredentials(src, dst)
	if err == nil {
		t.Fatal("expected error on nonexistent src, got nil")
	}
}

// TestDockerExecArgs_NonContainerPool ensures the helper returns nil
// when called on a non-container pool — callers branch on nil.
func TestDockerExecArgs_NonContainerPool(t *testing.T) {
	p := Pool{ID: "claude-1", Runtime: RuntimeHost}
	if args := DockerExecArgs(p, "img", "/repo"); args != nil {
		t.Errorf("DockerExecArgs(host pool) = %v, want nil", args)
	}
}

// TestDockerExecArgs_ContainerPoolBuildsMounts exercises the container
// branch and checks that the built arg list carries the credential
// volume mount, workdir bind, and CLAUDE_CONFIG_DIR env injection.
func TestDockerExecArgs_ContainerPoolBuildsMounts(t *testing.T) {
	p := Pool{
		ID:           "claude-7",
		Runtime:      RuntimeContainer,
		ContainerVol: "stoke-pool-claude-7",
		ConfigDir:    "/config",
		Provider:     "claude",
	}
	args := DockerExecArgs(p, "my-image:tag", "/work/repo")
	if len(args) == 0 {
		t.Fatal("DockerExecArgs(container) returned no args")
	}
	if args[0] != "docker" || args[1] != "run" {
		t.Errorf("args prefix = %v, want to start with docker run", args[:2])
	}
	// Container name uses pool ID
	mustContain(t, args, "stoke-worker-claude-7")
	// Credential volume bind
	mustContain(t, args, "stoke-pool-claude-7:/config")
	// Workdir bind + -w
	mustContain(t, args, "/work/repo:/work/repo")
	// Env wiring for Claude and Codex (both set so either binary works)
	mustContain(t, args, "CLAUDE_CONFIG_DIR=/config")
	mustContain(t, args, "CODEX_HOME=/config")
	// Image placed last in the prefix
	if args[len(args)-1] != "my-image:tag" {
		t.Errorf("last arg = %q, want image %q", args[len(args)-1], "my-image:tag")
	}
}

func mustContain(t *testing.T, args []string, want string) {
	t.Helper()
	for _, a := range args {
		if a == want {
			return
		}
	}
	t.Errorf("args missing %q: %v", want, args)
}
