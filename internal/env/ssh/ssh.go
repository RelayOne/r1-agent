// Package ssh implements the env.Environment interface for remote machines via SSH.
//
// This is the simplest remote backend — it requires only SSH access to a machine.
// No container runtime, no API, no cloud provider. The machine must already be
// provisioned and reachable via SSH.
//
// Use cases:
//   - Bare-metal build servers
//   - Pre-provisioned VMs (AWS EC2, GCP, etc.)
//   - Dev machines behind a jump host
//   - CI runners accessible via SSH
package ssh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/env"
)

// Backend implements env.Environment for remote machines accessed via SSH.
type Backend struct {
	host    string // hostname or IP
	user    string // SSH user
	keyPath string // path to SSH private key (empty = use agent)
	port    int    // SSH port (0 = default 22)
}

// Config configures the SSH backend.
type Config struct {
	Host    string // hostname or IP address
	User    string // SSH user (default: "root")
	KeyPath string // path to SSH private key (empty = use ssh-agent)
	Port    int    // SSH port (default: 22)
}

// New creates an SSH environment backend.
func New(cfg Config) *Backend {
	user := cfg.User
	if user == "" {
		user = "root"
	}
	port := cfg.Port
	if port == 0 {
		port = 22
	}
	return &Backend{
		host:    cfg.Host,
		user:    user,
		keyPath: cfg.KeyPath,
		port:    port,
	}
}

func (b *Backend) Provision(ctx context.Context, spec env.Spec) (*env.Handle, error) {
	if b.host == "" {
		return nil, fmt.Errorf("ssh: host is required")
	}

	// Verify connectivity.
	result, err := b.sshExec(ctx, "/", "echo ok", nil, nil, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("ssh: connectivity check failed: %w", err)
	}
	if !result.Success() {
		return nil, fmt.Errorf("ssh: connectivity check failed (exit %d): %s", result.ExitCode, result.CombinedOutput())
	}

	workDir := spec.WorkDir
	if workDir == "" {
		workDir = "/workspace"
	}

	// Create working directory.
	result, err = b.sshExec(ctx, "/", fmt.Sprintf("mkdir -p %s", workDir), nil, nil, 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("ssh: mkdir %s: %w", workDir, err)
	}
	if !result.Success() {
		return nil, fmt.Errorf("ssh: mkdir %s failed (exit %d): %s", workDir, result.ExitCode, result.CombinedOutput())
	}

	// Run setup commands.
	for _, cmd := range spec.SetupCommands {
		result, err = b.sshExec(ctx, workDir, cmd, spec.Env, nil, 5*time.Minute)
		if err != nil {
			return nil, fmt.Errorf("ssh setup %q: %w", cmd, err)
		}
		if !result.Success() {
			return nil, fmt.Errorf("ssh setup %q failed (exit %d): %s", cmd, result.ExitCode, result.CombinedOutput())
		}
	}

	return &env.Handle{
		ID:      fmt.Sprintf("ssh-%s-%d", b.host, time.Now().UnixMilli()),
		Backend: env.BackendSSH,
		WorkDir: workDir,
		Meta: map[string]string{
			"host": b.host,
			"user": b.user,
			"port": fmt.Sprintf("%d", b.port),
		},
		CreatedAt: time.Now(),
	}, nil
}

func (b *Backend) Exec(ctx context.Context, h *env.Handle, cmd []string, opts env.ExecOpts) (*env.ExecResult, error) {
	if h == nil {
		return nil, env.ErrNotProvisioned
	}
	dir := h.WorkDir
	if opts.Dir != "" {
		dir = opts.Dir
	}
	timeout := 10 * time.Minute
	if opts.Timeout > 0 {
		timeout = opts.Timeout
	}
	return b.sshExec(ctx, dir, strings.Join(cmd, " "), opts.Env, opts.Stdin, timeout)
}

func (b *Backend) CopyIn(ctx context.Context, h *env.Handle, srcLocal, dstRemote string) error {
	if h == nil {
		return env.ErrNotProvisioned
	}
	cmd := exec.CommandContext(ctx, "scp", b.scpArgs(srcLocal, fmt.Sprintf("%s@%s:%s", b.user, b.host, dstRemote))...) // #nosec G204 -- sandbox/container binary invoked with Stoke-generated args.
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ssh copy-in: %w: %s", err, out)
	}
	return nil
}

func (b *Backend) CopyOut(ctx context.Context, h *env.Handle, srcRemote, dstLocal string) error {
	if h == nil {
		return env.ErrNotProvisioned
	}
	cmd := exec.CommandContext(ctx, "scp", b.scpArgs(fmt.Sprintf("%s@%s:%s", b.user, b.host, srcRemote), dstLocal)...) // #nosec G204 -- sandbox/container binary invoked with Stoke-generated args.
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ssh copy-out: %w: %s", err, out)
	}
	return nil
}

func (b *Backend) Service(_ context.Context, h *env.Handle, name string) (env.ServiceAddr, error) {
	if h == nil {
		return env.ServiceAddr{}, env.ErrNotProvisioned
	}
	// SSH backend doesn't manage services — just return the host.
	return env.ServiceAddr{}, env.ErrServiceNotFound
}

func (b *Backend) Teardown(_ context.Context, h *env.Handle) error {
	// SSH backend doesn't own the machine — nothing to tear down.
	// The caller is responsible for machine lifecycle.
	return nil
}

func (b *Backend) Cost(_ context.Context, h *env.Handle) (env.CostEstimate, error) {
	if h == nil {
		return env.CostEstimate{}, env.ErrNotProvisioned
	}
	// SSH backend has no cost model — the machine is pre-provisioned.
	return env.CostEstimate{Elapsed: time.Since(h.CreatedAt)}, nil
}

// --- SSH helpers ---

func (b *Backend) sshExec(ctx context.Context, dir, cmdStr string, envVars map[string]string, stdin []byte, timeout time.Duration) (*env.ExecResult, error) {
	start := time.Now()

	var remote strings.Builder
	if dir != "" && dir != "/" {
		fmt.Fprintf(&remote, "cd %s && ", dir)
	}
	for k, v := range envVars {
		fmt.Fprintf(&remote, "%s=%s ", k, v)
	}
	remote.WriteString(cmdStr)

	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, "ssh", b.sshArgs(remote.String())...) // #nosec G204 -- sandbox/container binary invoked with Stoke-generated args.
	if len(stdin) > 0 {
		cmd.Stdin = bytes.NewReader(stdin)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, err
		}
	}

	return &env.ExecResult{
		ExitCode: exitCode,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Duration: time.Since(start),
	}, nil
}

func (b *Backend) sshArgs(command string) []string {
	args := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ConnectTimeout=10",
		"-p", fmt.Sprintf("%d", b.port),
	}
	if b.keyPath != "" {
		args = append(args, "-i", b.keyPath)
	}
	args = append(args, fmt.Sprintf("%s@%s", b.user, b.host), command)
	return args
}

func (b *Backend) scpArgs(srcdst ...string) []string {
	args := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-P", fmt.Sprintf("%d", b.port),
		"-r",
	}
	if b.keyPath != "" {
		args = append(args, "-i", b.keyPath)
	}
	args = append(args, srcdst...)
	return args
}
