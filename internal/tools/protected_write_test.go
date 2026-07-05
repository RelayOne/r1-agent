package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// SECURITY gap #2 (native path): the native write_file/str_replace tools
// resolve paths with workDir confinement only, so without a protected-write
// deny the model could overwrite CLAUDE.md/.env/.claude/ inside the worktree —
// the exact files the enforcer hook protects on the Claude Code path.
func TestNativeWriteBlocksProtectedFiles(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(dir)

	blocked := []string{
		"CLAUDE.md",
		".env",
		".env.local",
		"settings.json",
		"stoke.policy.yaml",
		".claude/settings.json",
		".stoke/session.json",
		".r1/state.json",
		"nested/.claude/hooks/pre.sh",
	}
	for _, p := range blocked {
		_, err := r.Handle(context.Background(), "write_file",
			toJSON(map[string]string{"path": p, "content": "pwned"}))
		if err == nil {
			t.Errorf("write_file should have blocked protected path %q", p)
			continue
		}
		if !strings.Contains(err.Error(), "protected file") {
			t.Errorf("write to %q: expected protected-file error, got: %v", p, err)
		}
		if _, statErr := os.Stat(filepath.Join(dir, p)); statErr == nil {
			t.Errorf("write to %q must NOT have hit disk", p)
		}
	}
}

func TestNativeWriteAllowsNormalFiles(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(dir)

	allowed := []string{
		"src/main.go",
		"README.md",
		"config/app.yaml",
		"env.example",       // not .env
		"my.settings.json.d/x.txt",
	}
	for _, p := range allowed {
		if _, err := r.Handle(context.Background(), "write_file",
			toJSON(map[string]string{"path": p, "content": "ok"})); err != nil {
			t.Errorf("write_file falsely blocked safe path %q: %v", p, err)
		}
	}
}

func TestNativeWriteKillSwitchOverride(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(dir)
	t.Setenv("R1_ALLOW_PROTECTED_WRITES", "1")

	if _, err := r.Handle(context.Background(), "write_file",
		toJSON(map[string]string{"path": "CLAUDE.md", "content": "harness edit"})); err != nil {
		t.Fatalf("kill-switch should permit protected write: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "CLAUDE.md")); statErr != nil {
		t.Error("protected write should have landed when kill-switch is set")
	}
}

func TestIsProtectedWritePath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/repo/CLAUDE.md", true},
		{"/repo/.env", true},
		{"/repo/.env.production", true},
		{"/repo/.claude/settings.json", true},
		{"/repo/.stoke/x", true},
		{"/repo/.r1/y", true},
		{"/repo/settings.json", true},
		{"/repo/stoke.policy.yaml", true},
		{"/repo/src/main.go", false},
		{"/repo/env.example", false},
		{"/repo/docs/CLAUDE.md.bak", false},
	}
	for _, c := range cases {
		if got := isProtectedWritePath(c.path); got != c.want {
			t.Errorf("isProtectedWritePath(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}
