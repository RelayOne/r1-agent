// Package inproc implements the Environment interface by running commands
// directly on the host via os/exec. No isolation — the fastest path for
// trusted missions where the host is already controlled.
package inproc

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/ericmacdougall/stoke/internal/env"
)

// Backend implements env.Environment using direct host execution.
type Backend struct {
	mu     sync.Mutex
	active map[string]*env.Handle
}

// New creates an in-process execution backend.
func New() *Backend {
	return &Backend{active: make(map[string]*env.Handle)}
}

// Provision creates a handle pointing at the repo root. No real provisioning
// needed — the host IS the environment. Setup commands run directly.
func (b *Backend) Provision(ctx context.Context, spec env.Spec) (*env.Handle, error) {
	workDir := spec.RepoRoot
	if spec.WorkDir != "" {
		workDir = spec.WorkDir
	}
	if workDir == "" {
		return nil, fmt.Errorf("inproc: RepoRoot or WorkDir required")
	}

	h := &env.Handle{
		ID:        fmt.Sprintf("inproc-%d", time.Now().UnixNano()),
		Backend:   env.BackendInProc,
		WorkDir:   workDir,
		Meta:      map[string]string{},
		CreatedAt: time.Now(),
	}

	// Run setup commands.
	for _, cmd := range spec.SetupCommands {
		result, err := b.Exec(ctx, h, []string{"bash", "-lc", cmd}, env.ExecOpts{})
		if err != nil {
			return nil, fmt.Errorf("inproc: setup command %q: %w", cmd, err)
		}
		if !result.Success() {
			return nil, fmt.Errorf("inproc: setup command %q failed (exit %d): %s",
				cmd, result.ExitCode, result.CombinedOutput())
		}
	}

	b.mu.Lock()
	b.active[h.ID] = h
	b.mu.Unlock()

	return h, nil
}

// Exec runs a command directly on the host.
func (b *Backend) Exec(ctx context.Context, h *env.Handle, cmdArgs []string, opts env.ExecOpts) (*env.ExecResult, error) {
	if h == nil {
		return nil, env.ErrNotProvisioned
	}
	if len(cmdArgs) == 0 {
		return nil, fmt.Errorf("inproc: empty command")
	}

	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, cmdArgs[0], cmdArgs[1:]...)
	cmd.Dir = h.WorkDir
	if opts.Dir != "" {
		cmd.Dir = opts.Dir
	}

	// Merge environment variables.
	cmd.Env = os.Environ()
	for k, v := range opts.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	if opts.Stdin != nil {
		cmd.Stdin = bytes.NewReader(opts.Stdin)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	err := cmd.Run()
	duration := time.Since(start)

	result := &env.ExecResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Duration: duration,
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else if ctx.Err() != nil {
			result.ExitCode = -1
			result.Stderr = strings.TrimSpace(result.Stderr + "\ncommand timed out")
		} else {
			return nil, fmt.Errorf("inproc: exec %v: %w", cmdArgs, err)
		}
	}

	return result, nil
}

// CopyIn is a no-op for inproc — files are already on the host.
func (b *Backend) CopyIn(_ context.Context, h *env.Handle, _, _ string) error {
	if h == nil {
		return env.ErrNotProvisioned
	}
	return nil // files are already local
}

// CopyOut is a no-op for inproc — files are already on the host.
func (b *Backend) CopyOut(_ context.Context, h *env.Handle, _, _ string) error {
	if h == nil {
		return env.ErrNotProvisioned
	}
	return nil // files are already local
}

// Service is not supported by the inproc backend.
func (b *Backend) Service(_ context.Context, _ *env.Handle, _ string) (env.ServiceAddr, error) {
	return env.ServiceAddr{}, env.ErrServiceNotFound
}

// Teardown removes the handle from the active set.
func (b *Backend) Teardown(_ context.Context, h *env.Handle) error {
	if h == nil {
		return nil
	}
	b.mu.Lock()
	delete(b.active, h.ID)
	b.mu.Unlock()
	return nil
}

// Cost returns zero — inproc has no external costs.
func (b *Backend) Cost(_ context.Context, h *env.Handle) (env.CostEstimate, error) {
	if h == nil {
		return env.CostEstimate{}, env.ErrNotProvisioned
	}
	return env.CostEstimate{Elapsed: time.Since(h.CreatedAt)}, nil
}
