// Package fly implements the env.Environment interface for Fly.io and Flare machines.
//
// Since the Fly/Flare Machines API does not support exec in v1, command execution
// is performed via SSH to the machine's IP address. The provisioned machine must
// have an SSH server running and accept the configured SSH key.
//
// This backend works identically with Fly.io and Flare — the API surface is the same.
package fly

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/env"
	"github.com/RelayOne/r1/internal/env/flyclient"
)

// Backend implements env.Environment for Fly/Flare machines.
type Backend struct {
	client  *flyclient.Client
	appName string
	region  string
	sshKey  string // path to SSH private key
	sshUser string // SSH user (default: "root")
}

// Config configures the Fly backend.
type Config struct {
	// APIURL is the Fly/Flare control plane URL.
	APIURL string

	// Token is the API bearer token (FLARE_API_KEY or FLY_API_TOKEN).
	Token string

	// AppName is the Fly app to create machines under.
	AppName string

	// Region is the deployment region (e.g. "us-central1", "iad").
	Region string

	// SSHKeyPath is the path to the SSH private key for command execution.
	SSHKeyPath string

	// SSHUser is the SSH user on the machine. Default: "root".
	SSHUser string
}

// New creates a Fly/Flare environment backend.
func New(cfg Config) *Backend {
	user := cfg.SSHUser
	if user == "" {
		user = "root"
	}
	return &Backend{
		client:  flyclient.New(cfg.APIURL, cfg.Token),
		appName: cfg.AppName,
		region:  cfg.Region,
		sshKey:  cfg.SSHKeyPath,
		sshUser: user,
	}
}

func (b *Backend) Provision(ctx context.Context, spec env.Spec) (*env.Handle, error) {
	guest := flyclient.GuestConfig{}
	if spec.CPUs > 0 {
		guest.CPUs = spec.CPUs
	}
	if spec.MemoryMB > 0 {
		guest.MemoryMB = spec.MemoryMB
	}

	machine, err := b.client.CreateMachine(ctx, b.appName, flyclient.CreateMachineRequest{
		Region: b.region,
		Config: flyclient.MachineConfig{
			Image: spec.BaseImage,
			Guest: guest,
			Env:   spec.Env,
			Metadata: map[string]string{
				"stoke":    "true",
				"repo":     spec.RepoRoot,
				"work_dir": spec.WorkDir,
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("fly provision: %w", err)
	}

	// Start the machine.
	if err := b.client.StartMachine(ctx, b.appName, machine.ID); err != nil {
		// Best-effort cleanup on start failure.
		_ = b.client.DeleteMachine(ctx, b.appName, machine.ID)
		return nil, fmt.Errorf("fly start: %w", err)
	}

	// Wait for SSH to become available (up to 60s).
	if err := b.waitForSSH(ctx, machine.IPAddress); err != nil {
		_ = b.client.StopMachine(ctx, b.appName, machine.ID)
		_ = b.client.DeleteMachine(ctx, b.appName, machine.ID)
		return nil, fmt.Errorf("fly ssh wait: %w", err)
	}

	// Run setup commands.
	workDir := spec.WorkDir
	if workDir == "" {
		workDir = "/workspace"
	}
	for _, cmd := range spec.SetupCommands {
		result, err := b.sshExec(ctx, machine.IPAddress, workDir, cmd, nil, nil, 5*time.Minute)
		if err != nil {
			_ = b.client.StopMachine(ctx, b.appName, machine.ID)
			_ = b.client.DeleteMachine(ctx, b.appName, machine.ID)
			return nil, fmt.Errorf("fly setup %q: %w", cmd, err)
		}
		if !result.Success() {
			_ = b.client.StopMachine(ctx, b.appName, machine.ID)
			_ = b.client.DeleteMachine(ctx, b.appName, machine.ID)
			return nil, fmt.Errorf("fly setup %q failed (exit %d): %s", cmd, result.ExitCode, result.CombinedOutput())
		}
	}

	return &env.Handle{
		ID:      machine.ID,
		Backend: env.BackendFly,
		WorkDir: workDir,
		Meta: map[string]string{
			"app":      b.appName,
			"ip":       machine.IPAddress,
			"hostname": machine.GeneratedHostname,
			"region":   b.region,
		},
		CreatedAt: time.Now(),
	}, nil
}

func (b *Backend) Exec(ctx context.Context, h *env.Handle, cmd []string, opts env.ExecOpts) (*env.ExecResult, error) {
	if h == nil {
		return nil, env.ErrNotProvisioned
	}
	ip := h.Meta["ip"]
	if ip == "" {
		return nil, fmt.Errorf("fly exec: no IP address in handle")
	}

	dir := h.WorkDir
	if opts.Dir != "" {
		dir = opts.Dir
	}
	timeout := 10 * time.Minute
	if opts.Timeout > 0 {
		timeout = opts.Timeout
	}

	return b.sshExec(ctx, ip, dir, strings.Join(cmd, " "), opts.Env, opts.Stdin, timeout)
}

func (b *Backend) CopyIn(ctx context.Context, h *env.Handle, srcLocal, dstRemote string) error {
	if h == nil {
		return env.ErrNotProvisioned
	}
	ip := h.Meta["ip"]

	args := b.scpArgs(ip, srcLocal, fmt.Sprintf("%s@%s:%s", b.sshUser, ip, dstRemote))
	cmd := exec.CommandContext(ctx, "scp", args...) // #nosec G204 -- sandbox/container binary invoked with Stoke-generated args.
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("fly copy-in: %w: %s", err, out)
	}
	return nil
}

func (b *Backend) CopyOut(ctx context.Context, h *env.Handle, srcRemote, dstLocal string) error {
	if h == nil {
		return env.ErrNotProvisioned
	}
	ip := h.Meta["ip"]

	args := b.scpArgs(ip, fmt.Sprintf("%s@%s:%s", b.sshUser, ip, srcRemote), dstLocal)
	cmd := exec.CommandContext(ctx, "scp", args...) // #nosec G204 -- sandbox/container binary invoked with Stoke-generated args.
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("fly copy-out: %w: %s", err, out)
	}
	return nil
}

func (b *Backend) Service(ctx context.Context, h *env.Handle, name string) (env.ServiceAddr, error) {
	if h == nil {
		return env.ServiceAddr{}, env.ErrNotProvisioned
	}
	hostname := h.Meta["hostname"]
	if hostname == "" {
		return env.ServiceAddr{}, env.ErrServiceNotFound
	}
	// Fly/Flare machines expose services via their generated hostname.
	return env.ServiceAddr{
		Protocol: "https",
		Host:     hostname,
		Port:     443,
	}, nil
}

func (b *Backend) Teardown(ctx context.Context, h *env.Handle) error {
	if h == nil {
		return nil
	}
	// Stop then delete.
	_ = b.client.StopMachine(ctx, b.appName, h.ID)
	if err := b.client.DeleteMachine(ctx, b.appName, h.ID); err != nil {
		return fmt.Errorf("fly teardown: %w", err)
	}
	return nil
}

func (b *Backend) Cost(ctx context.Context, h *env.Handle) (env.CostEstimate, error) {
	if h == nil {
		return env.CostEstimate{}, env.ErrNotProvisioned
	}
	elapsed := time.Since(h.CreatedAt)
	// Fly pricing: ~$0.0000463/s for performance-4x ($31/mo).
	// This is a rough estimate; real pricing depends on machine size.
	costPerSec := 0.0000463
	total := costPerSec * elapsed.Seconds()
	return env.CostEstimate{
		ComputeUSD: total,
		TotalUSD:   total,
		Elapsed:    elapsed,
	}, nil
}

// --- SSH helpers ---

func (b *Backend) sshExec(ctx context.Context, ip, dir, cmdStr string, envVars map[string]string, stdin []byte, timeout time.Duration) (*env.ExecResult, error) {
	start := time.Now()

	// Build the remote command with cd and env.
	var remote strings.Builder
	if dir != "" {
		fmt.Fprintf(&remote, "cd %s && ", dir)
	}
	for k, v := range envVars {
		fmt.Fprintf(&remote, "%s=%s ", k, v)
	}
	remote.WriteString(cmdStr)

	args := b.sshArgs(ip, remote.String())

	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, "ssh", args...) // #nosec G204 -- sandbox/container binary invoked with Stoke-generated args.
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

func (b *Backend) sshArgs(ip, command string) []string {
	args := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ConnectTimeout=10",
	}
	if b.sshKey != "" {
		args = append(args, "-i", b.sshKey)
	}
	args = append(args, fmt.Sprintf("%s@%s", b.sshUser, ip), command)
	return args
}

func (b *Backend) scpArgs(ip string, srcDst ...string) []string {
	args := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-r", // recursive
	}
	if b.sshKey != "" {
		args = append(args, "-i", b.sshKey)
	}
	args = append(args, srcDst...)
	return args
}

func (b *Backend) waitForSSH(ctx context.Context, ip string) error {
	deadline := time.After(60 * time.Second)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("timeout waiting for SSH on %s", ip)
		case <-ticker.C:
			result, err := b.sshExec(ctx, ip, "/", "echo ok", nil, nil, 5*time.Second)
			if err == nil && result.Success() {
				return nil
			}
		}
	}
}
