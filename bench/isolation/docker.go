// Package isolation provides container-per-task execution for the benchmark framework.
package isolation

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// ContainerConfig defines the parameters for launching a benchmark container.
type ContainerConfig struct {
	// Image is the Docker image to run.
	Image string

	// Name is the container name (must be unique per concurrent run).
	Name string

	// TaskDir is the host path mounted read-only at /task.
	TaskDir string

	// OutputDir is the host path mounted at /output for results.
	OutputDir string

	// Env holds additional environment variables passed to the container.
	Env map[string]string

	// Cmd is the command and arguments to execute inside the container.
	Cmd []string

	// CPUs limits CPU count (e.g. "2.0").
	CPUs string

	// MemoryMB limits memory in megabytes.
	MemoryMB int

	// PidsLimit limits the number of processes.
	PidsLimit int

	// Network is the Docker network to attach to. Defaults to "stoke-bench-internal".
	Network string
}

// ContainerResult captures the outcome of a container run.
type ContainerResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// RunContainer launches a Docker container with the specified configuration and
// waits for it to complete or for ctx to be cancelled. The container runs with:
//   - Read-only root filesystem
//   - tmpfs at /tmp and /workspace
//   - Task data mounted read-only at /task
//   - Output volume at /output
//   - Network isolation
//   - Resource limits (CPU, memory, pids)
//   - Security hardening (no-new-privileges, cap-drop ALL)
func RunContainer(ctx context.Context, cfg ContainerConfig) (ContainerResult, error) {
	network := cfg.Network
	if network == "" {
		network = "stoke-bench-internal"
	}

	args := []string{
		"run",
		"--rm",
		"--name", cfg.Name,

		// Read-only root.
		"--read-only",

		// Writable scratch areas.
		"--tmpfs", "/tmp:rw,noexec,nosuid,size=512m",
		"--tmpfs", "/workspace:rw,exec,nosuid,size=2g",

		// Task data (read-only).
		"-v", cfg.TaskDir + ":/task:ro",

		// Output volume (writable).
		"-v", cfg.OutputDir + ":/output:rw",

		// Network isolation.
		"--network", network,

		// Security hardening.
		"--security-opt", "no-new-privileges",
		"--cap-drop", "ALL",
	}

	// Resource limits.
	if cfg.CPUs != "" {
		args = append(args, "--cpus", cfg.CPUs)
	}
	if cfg.MemoryMB > 0 {
		args = append(args, "--memory", strconv.Itoa(cfg.MemoryMB)+"m")
	}
	if cfg.PidsLimit > 0 {
		args = append(args, "--pids-limit", strconv.Itoa(cfg.PidsLimit))
	}

	// Environment variables.
	for k, v := range cfg.Env {
		args = append(args, "-e", k+"="+v)
	}

	// Image.
	args = append(args, cfg.Image)

	// Command.
	args = append(args, cfg.Cmd...)

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	result := ContainerResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else {
			result.ExitCode = -1
			return result, fmt.Errorf("docker run failed: %w", err)
		}
	}

	return result, nil
}

// EnsureNetwork creates the Docker network if it does not already exist.
func EnsureNetwork(ctx context.Context, name string) error {
	// Check if network exists.
	check := exec.CommandContext(ctx, "docker", "network", "inspect", name)
	if check.Run() == nil {
		return nil
	}

	// Create an internal network with no external access.
	cmd := exec.CommandContext(ctx, "docker", "network", "create",
		"--internal",
		"--driver", "bridge",
		name,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker network create %s: %s: %w", name, strings.TrimSpace(string(out)), err)
	}
	return nil
}
