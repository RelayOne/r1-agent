// Package compute provides pluggable execution backends for Stoke tasks.
//
// Without an Ember key, tasks run locally via goroutines + git worktrees.
// With an Ember key, heavy tasks can burst to Flare microVMs.
package compute

import (
	"context"
	"io"
)

// Backend creates isolated execution environments for tasks.
type Backend interface {
	// Spawn creates a worker environment for a single task.
	Spawn(ctx context.Context, opts SpawnOpts) (Worker, error)
	// Name returns the backend identifier ("local" or "flare").
	Name() string
}

// SpawnOpts configures a new worker environment.
type SpawnOpts struct {
	TaskID      string
	RepoURL     string            // git clone URL
	Branch      string            // worktree branch (e.g. "stoke/TASK-3")
	Size        string            // machine size: "4x", "8x", "16x"
	Env         map[string]string // environment variables
	Image       string            // container/VM image (default: current Ember image)
	AutoDestroy bool              // destroy worker when task completes
	ParentID    string            // parent Ember machine ID (for dashboard grouping)
}

// Worker is an isolated execution environment for one task.
type Worker interface {
	// Exec runs a command in the worker. Returns result with exit code, stdout, stderr.
	Exec(ctx context.Context, cmd string, args ...string) (ExecResult, error)
	// Upload copies a local file/dir to the worker filesystem.
	Upload(ctx context.Context, localPath, remotePath string) error
	// Download copies a file/dir from the worker to local filesystem.
	Download(ctx context.Context, remotePath, localPath string) error
	// Stdout returns a live stream of stdout (for TUI progress).
	Stdout() io.Reader
	// Stop halts the worker but preserves state (can restart).
	Stop(ctx context.Context) error
	// Destroy terminates the worker and releases all resources.
	Destroy(ctx context.Context) error
	// ID returns the unique worker identifier.
	ID() string
	// Hostname returns the public hostname (for Ember dashboard display).
	Hostname() string
}

// ExecResult captures the output of a command execution.
type ExecResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Duration int64 // milliseconds
}
