package engine

import (
	"context"
	"strings"
	"testing"
)

func TestWrapInDocker(t *testing.T) {
	prepared := PreparedCommand{
		Binary: "claude",
		Args:   []string{"-p", "test prompt", "--output-format", "stream-json"},
		Dir:    "/workspace/task-1",
		Env:    []string{"HOME=/home/stoke", "CLAUDE_CONFIG_DIR=/config"},
	}

	spec := RunSpec{
		Prompt:             "test prompt",
		WorktreeDir:        "/workspace/task-1",
		RuntimeDir:         "/tmp/stoke-runtime",
		Phase:              PhaseSpec{Name: "execute", MaxTurns: 10},
		ContainerImage:     "ghcr.io/ericmacdougall/stoke-pool:latest",
		ContainerVol:       "stoke-pool-claude-1",
		ContainerConfigDir: "/config",
		PoolConfigDir:      "/config",
	}

	cmd := wrapInDocker(context.Background(), prepared, spec)

	// Should be a docker command
	if cmd.Path == "" {
		t.Fatal("cmd.Path is empty")
	}

	// Reconstruct args for inspection
	fullArgs := cmd.Args
	joined := strings.Join(fullArgs, " ")

	// Should include docker run --rm
	if !strings.Contains(joined, "run --rm") {
		t.Errorf("expected 'run --rm' in args, got: %s", joined)
	}

	// Should mount the credential volume
	if !strings.Contains(joined, "stoke-pool-claude-1:/config") {
		t.Errorf("expected volume mount in args, got: %s", joined)
	}

	// Should mount the worktree
	if !strings.Contains(joined, "/workspace/task-1:/workspace/task-1") {
		t.Errorf("expected worktree mount in args, got: %s", joined)
	}

	// Should mount the runtime dir
	if !strings.Contains(joined, "/tmp/stoke-runtime:/tmp/stoke-runtime") {
		t.Errorf("expected runtime dir mount in args, got: %s", joined)
	}

	// Should include the image
	if !strings.Contains(joined, "ghcr.io/ericmacdougall/stoke-pool:latest") {
		t.Errorf("expected image in args, got: %s", joined)
	}

	// Should end with the original binary and args
	if !strings.Contains(joined, "claude -p test prompt") {
		t.Errorf("expected original command at end, got: %s", joined)
	}
}

func TestWrapInDocker_DefaultConfigDir(t *testing.T) {
	prepared := PreparedCommand{
		Binary: "claude",
		Args:   []string{"-p", "test"},
		Dir:    "/work",
		Env:    []string{},
	}

	spec := RunSpec{
		Prompt:         "test",
		WorktreeDir:    "/work",
		RuntimeDir:     "/tmp/rt",
		Phase:          PhaseSpec{Name: "plan", MaxTurns: 5},
		ContainerImage: "stoke-pool:latest",
		ContainerVol:   "vol-1",
		// ContainerConfigDir not set — should default to /config
	}

	cmd := wrapInDocker(context.Background(), prepared, spec)
	joined := strings.Join(cmd.Args, " ")

	if !strings.Contains(joined, "vol-1:/config") {
		t.Errorf("expected default /config mount, got: %s", joined)
	}
}
