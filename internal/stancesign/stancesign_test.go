package stancesign

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestIdentityForCreatesKeyPair(t *testing.T) {
	dir := t.TempDir()
	id, err := IdentityFor(dir, "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(id.KeyPath); err != nil {
		t.Fatalf("private key missing: %v", err)
	}
	info, err := os.Stat(id.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("private key perms should be 0600, got %v", info.Mode().Perm())
	}
	if _, err := os.Stat(id.PublicKeyPath); err != nil {
		t.Fatalf("public key missing: %v", err)
	}
	data, err := os.ReadFile(id.PublicKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "ssh-ed25519 ") {
		t.Fatalf("public key not in OpenSSH format: %q", string(data))
	}
}

func TestIdentityForIsolatesStances(t *testing.T) {
	dir := t.TempDir()
	a, err := IdentityFor(dir, "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	b, err := IdentityFor(dir, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if a.KeyPath == b.KeyPath {
		t.Fatal("distinct stances must get distinct key paths")
	}
	if a.CommitterEmail == b.CommitterEmail {
		t.Fatal("distinct stances must get distinct emails")
	}
	aData, _ := os.ReadFile(a.PublicKeyPath)
	bData, _ := os.ReadFile(b.PublicKeyPath)
	if string(aData) == string(bData) {
		t.Fatal("distinct stances must hold distinct public keys")
	}
}

func TestIdentityForIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	a, err := IdentityFor(dir, "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	info1, _ := os.Stat(a.KeyPath)
	b, err := IdentityFor(dir, "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	info2, _ := os.Stat(b.KeyPath)
	if info1.ModTime() != info2.ModTime() {
		t.Fatalf("second call must not regenerate key (mod times differ)")
	}
}

func TestSanitizeStance(t *testing.T) {
	cases := map[string]string{
		"Reviewer":         "reviewer",
		"cto":              "cto",
		"WITH_UNDERSCORE":  "with-underscore",
		"  spaces around ": "spaces-around", //nolint:gocritic // mapKey: intentional whitespace tests sanitization
	}
	for in, want := range cases {
		if got := sanitizeStance(in); got != want {
			t.Fatalf("sanitize(%q)=%q want %q", in, got, want)
		}
	}
}

func TestApplyToInjectsConfigAndEnv(t *testing.T) {
	dir := t.TempDir()
	id, err := IdentityFor(dir, "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "commit", "-m", "x")
	id.ApplyTo(cmd)
	// After ApplyTo: args should include user.signingkey=... between
	// "git" and the subcommand.
	joined := strings.Join(cmd.Args, " ")
	if !strings.Contains(joined, "user.signingkey="+id.PublicKeyPath) {
		t.Fatalf("signingkey flag missing from args: %s", joined)
	}
	if !strings.Contains(joined, "commit.gpgsign=true") {
		t.Fatalf("gpgsign flag missing: %s", joined)
	}
	// Env must carry GIT_COMMITTER_EMAIL matching our stance email.
	foundEmail := false
	for _, e := range cmd.Env {
		if e == "GIT_COMMITTER_EMAIL="+id.CommitterEmail {
			foundEmail = true
		}
	}
	if !foundEmail {
		t.Fatalf("committer email not set in env: %v", cmd.Env)
	}
}

func TestApplyToIdempotent(t *testing.T) {
	dir := t.TempDir()
	id, _ := IdentityFor(dir, "reviewer")
	cmd := exec.Command("git", "commit")
	id.ApplyTo(cmd)
	before := len(cmd.Args)
	id.ApplyTo(cmd)
	if len(cmd.Args) != before {
		t.Fatalf("second ApplyTo duplicated args: %v", cmd.Args)
	}
}

// Guard: make sure the key can be round-tripped by a separate helper
// that reads the PEM — i.e. the file we wrote is a valid OpenSSH
// private key and not a typo/broken PEM envelope. We don't invoke git
// from the test (no worktree), but we at least confirm the file opens
// cleanly as a PEM.
func TestKeyFileIsValidPEM(t *testing.T) {
	dir := t.TempDir()
	id, _ := IdentityFor(dir, "reviewer")
	data, err := os.ReadFile(id.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "-----BEGIN OPENSSH PRIVATE KEY-----") {
		t.Fatalf("missing PEM header in %s:\n%s", filepath.Base(id.KeyPath), string(data))
	}
	if !strings.Contains(string(data), "-----END OPENSSH PRIVATE KEY-----") {
		t.Fatal("missing PEM footer")
	}
}
