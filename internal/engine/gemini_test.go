package engine

import (
	"context"
	"strings"
	"testing"
)

func TestGeminiPrepare(t *testing.T) {
	dir := t.TempDir()
	runner := NewGeminiRunner("gemini")
	prepared, err := runner.Prepare(RunSpec{
		Prompt:      "write docs for the API",
		WorktreeDir: dir,
		Phase:       PhaseSpec{Name: "execute"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Binary != "gemini" {
		t.Errorf("binary = %q, want %q", prepared.Binary, "gemini")
	}
	if prepared.Dir != dir {
		t.Errorf("dir = %q, want %q", prepared.Dir, dir)
	}
	// Verify args contain --prompt and --dir with correct values
	joined := strings.Join(prepared.Args, " ")
	if !strings.Contains(joined, "--prompt") {
		t.Error("args should contain --prompt")
	}
	if !strings.Contains(joined, "--dir") {
		t.Error("args should contain --dir")
	}
	if !strings.Contains(joined, "write docs for the API") {
		t.Error("args should contain the prompt text")
	}
	if len(prepared.Notes) == 0 || !strings.Contains(prepared.Notes[0], "Gemini") {
		t.Error("notes should mention Gemini")
	}
}

func TestGeminiPrepareDefaultBinary(t *testing.T) {
	runner := NewGeminiRunner("")
	if runner.Binary != "gemini" {
		t.Errorf("default binary = %q, want %q", runner.Binary, "gemini")
	}
}

func TestGeminiPrepareCustomBinary(t *testing.T) {
	runner := NewGeminiRunner("/usr/local/bin/gemini-2")
	if runner.Binary != "/usr/local/bin/gemini-2" {
		t.Errorf("binary = %q, want /usr/local/bin/gemini-2", runner.Binary)
	}
}

func TestGeminiPrepareMissingWorktree(t *testing.T) {
	runner := NewGeminiRunner("gemini")
	_, err := runner.Prepare(RunSpec{
		Prompt: "test",
	})
	if err == nil {
		t.Fatal("expected error for missing worktree dir")
	}
	if !strings.Contains(err.Error(), "missing worktree dir") {
		t.Errorf("error = %q, want mention of missing worktree dir", err.Error())
	}
}

func TestGeminiPrepareAPIKeyInjection(t *testing.T) {
	dir := t.TempDir()
	runner := NewGeminiRunner("gemini")
	prepared, err := runner.Prepare(RunSpec{
		Prompt:      "test",
		WorktreeDir: dir,
		PoolAPIKey:  "test-gemini-key-123",
		Phase:       PhaseSpec{Name: "execute"},
	})
	if err != nil {
		t.Fatal(err)
	}
	envJoined := strings.Join(prepared.Env, "\n")
	if !strings.Contains(envJoined, "GEMINI_API_KEY=test-gemini-key-123") {
		t.Error("env should contain GEMINI_API_KEY when PoolAPIKey is set")
	}
}

func TestGeminiPrepareAPIKey(t *testing.T) {
	dir := t.TempDir()
	runner := NewGeminiRunner("gemini")

	// With PoolAPIKey set
	prepared, err := runner.Prepare(RunSpec{
		Prompt:      "test task",
		WorktreeDir: dir,
		PoolAPIKey:  "gem-key-abc123",
		Phase:       PhaseSpec{Name: "execute"},
	})
	if err != nil {
		t.Fatal(err)
	}

	envJoined := strings.Join(prepared.Env, "\n")
	if !strings.Contains(envJoined, "GEMINI_API_KEY=gem-key-abc123") {
		t.Error("GEMINI_API_KEY should be in env when PoolAPIKey is set")
	}

	// Without PoolAPIKey set -- the env should not contain a GEMINI_API_KEY
	// entry injected by Prepare (safeEnvMode2 may pass through the OS env,
	// so we check that no "GEMINI_API_KEY=gem-key-abc123" entry exists).
	prepared2, err := runner.Prepare(RunSpec{
		Prompt:      "test task",
		WorktreeDir: dir,
		Phase:       PhaseSpec{Name: "execute"},
	})
	if err != nil {
		t.Fatal(err)
	}

	envJoined2 := strings.Join(prepared2.Env, "\n")
	if strings.Contains(envJoined2, "GEMINI_API_KEY=gem-key-abc123") {
		t.Error("GEMINI_API_KEY should not contain the pool key when PoolAPIKey is empty")
	}
}

func TestGeminiRunBinaryNotFound(t *testing.T) {
	dir := t.TempDir()
	runner := NewGeminiRunner("nonexistent-gemini-binary-xyz")
	_, err := runner.Run(context.Background(), RunSpec{
		Prompt:      "test",
		WorktreeDir: dir,
		Phase:       PhaseSpec{Name: "execute"},
	}, nil)
	if err == nil {
		t.Fatal("expected error when binary not found")
	}
	if !strings.Contains(err.Error(), "gemini binary not found") {
		t.Errorf("error = %q, want mention of binary not found", err.Error())
	}
}
