package engine

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexPrepareUsesReadOnlyForVerify(t *testing.T) {
	dir := t.TempDir()
	runner := NewCodexRunner("codex")
	prepared, err := runner.Prepare(RunSpec{
		Prompt:        "review diff",
		WorktreeDir:   dir,
		RuntimeDir:    filepath.Join(dir, "runtime"),
		Mode:          AuthModeMode1,
		PoolConfigDir: dir,
		Phase:         PhaseSpec{Name: "verify", ReadOnly: true, MaxTurns: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(prepared.Args, " ")
	if !containsSequence(prepared.Args, "--sandbox", "read-only") {
		t.Fatalf("verify phase should use read-only sandbox, got %q", joined)
	}
	if !containsSequence(prepared.Args, "--profile", "review") {
		t.Error("verify phase should use review profile")
	}
}

func TestCodexPrepareUsesWorkspaceWriteForExecute(t *testing.T) {
	dir := t.TempDir()
	runner := NewCodexRunner("codex")
	prepared, err := runner.Prepare(RunSpec{
		Prompt:      "implement feature",
		WorktreeDir: dir,
		RuntimeDir:  filepath.Join(dir, "runtime"),
		Mode:        AuthModeMode2,
		Phase:       PhaseSpec{Name: "execute", MaxTurns: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSequence(prepared.Args, "--sandbox", "workspace-write") {
		t.Error("execute phase should use workspace-write sandbox")
	}
	if !containsSequence(prepared.Args, "--profile", "task") {
		t.Error("execute phase should use task profile")
	}
}

func TestCodexPrepareMode1IsolatesCredentials(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OPENAI_API_KEY", "leaked-key")
	t.Setenv("PATH", "/usr/bin")

	runner := NewCodexRunner("codex")
	prepared, err := runner.Prepare(RunSpec{
		Prompt:        "test",
		WorktreeDir:   dir,
		RuntimeDir:    filepath.Join(dir, "runtime"),
		Mode:          AuthModeMode1,
		PoolConfigDir: "/pool/codex-1",
		Phase:         PhaseSpec{Name: "execute", MaxTurns: 10},
	})
	if err != nil {
		t.Fatal(err)
	}

	envJoined := strings.Join(prepared.Env, "\n")
	if strings.Contains(envJoined, "OPENAI_API_KEY=") {
		t.Error("Mode 1 should strip OPENAI_API_KEY")
	}
	if !strings.Contains(envJoined, "CODEX_HOME=/pool/codex-1") {
		t.Error("Mode 1 should inject CODEX_HOME")
	}
}

func TestCodexPrepareOutputsLastMessage(t *testing.T) {
	dir := t.TempDir()
	runner := NewCodexRunner("codex")
	prepared, err := runner.Prepare(RunSpec{
		Prompt:      "test",
		WorktreeDir: dir,
		RuntimeDir:  filepath.Join(dir, "runtime"),
		Mode:        AuthModeMode2,
		Phase:       PhaseSpec{Name: "execute", MaxTurns: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(prepared.Args, " ")
	if !strings.Contains(joined, "--output-last-message") {
		t.Error("should include --output-last-message flag")
	}
	if !strings.Contains(joined, "--json") {
		t.Error("should include --json flag")
	}
}

func containsSequence(args []string, a, b string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == a && args[i+1] == b {
			return true
		}
	}
	return false
}
