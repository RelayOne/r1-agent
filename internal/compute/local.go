package compute

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"strings"
	"time"
)

// LocalBackend runs tasks as local goroutines with git worktrees.
// This is the default when no Ember key is configured.
type LocalBackend struct {
	RepoRoot string
}

func NewLocalBackend(repoRoot string) *LocalBackend {
	return &LocalBackend{RepoRoot: repoRoot}
}

func (b *LocalBackend) Name() string { return "local" }

func (b *LocalBackend) Spawn(_ context.Context, opts SpawnOpts) (Worker, error) {
	return &localWorker{
		id:       "local-" + opts.TaskID,
		repoRoot: b.RepoRoot,
		taskID:   opts.TaskID,
	}, nil
}

type localWorker struct {
	id       string
	repoRoot string
	taskID   string
}

func (w *localWorker) ID() string       { return w.id }
func (w *localWorker) Hostname() string  { return "localhost" }
func (w *localWorker) Stdout() io.Reader { return &bytes.Buffer{} }
func (w *localWorker) Stop(_ context.Context) error    { return nil }
func (w *localWorker) Destroy(_ context.Context) error { return nil }
func (w *localWorker) Upload(_ context.Context, _, _ string) error   { return nil }
func (w *localWorker) Download(_ context.Context, _, _ string) error { return nil }

func (w *localWorker) Exec(ctx context.Context, cmd string, args ...string) (ExecResult, error) {
	start := time.Now()
	c := exec.CommandContext(ctx, cmd, args...)
	c.Dir = w.repoRoot
	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr
	err := c.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return ExecResult{}, err
		}
	}
	return ExecResult{
		ExitCode: exitCode,
		Stdout:   strings.TrimSpace(stdout.String()),
		Stderr:   strings.TrimSpace(stderr.String()),
		Duration: time.Since(start).Milliseconds(),
	}, nil
}
