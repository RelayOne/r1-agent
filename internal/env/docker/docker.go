// Package docker implements the Environment interface using local Docker containers.
// This is the default backend for isolated mission execution on the user's machine.
//
// Architecture:
//   - Main container: runs mission code with the repo mounted at /workspace
//   - Service containers: dependent services (postgres, redis, etc.) on a shared network
//   - All containers share a Docker network for inter-service communication
//   - Teardown removes all containers and the network
package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/ericmacdougall/stoke/internal/env"
)

// Backend implements env.Environment using Docker containers.
type Backend struct {
	mu     sync.Mutex
	active map[string]*envState
}

type envState struct {
	handle       *env.Handle
	containerID  string
	serviceIDs   map[string]string // service name -> container ID
	networkID    string
	servicePorts map[string]env.ServiceAddr
}

// New creates a Docker execution backend.
func New() *Backend {
	return &Backend{active: make(map[string]*envState)}
}

// Provision creates Docker containers for the mission environment.
func (b *Backend) Provision(ctx context.Context, spec env.Spec) (*env.Handle, error) {
	if err := checkDocker(ctx); err != nil {
		return nil, fmt.Errorf("docker: %w", err)
	}

	id := fmt.Sprintf("stoke-%d", time.Now().UnixNano())
	networkName := id + "-net"
	workDir := "/workspace"
	if spec.WorkDir != "" {
		workDir = spec.WorkDir
	}

	// Create a Docker network for inter-service communication.
	networkID, err := dockerRun(ctx, "network", "create", networkName)
	if err != nil {
		return nil, fmt.Errorf("docker: create network: %w", err)
	}
	networkID = strings.TrimSpace(networkID)

	state := &envState{
		networkID:    networkID,
		serviceIDs:   make(map[string]string),
		servicePorts: make(map[string]env.ServiceAddr),
	}

	// Start service containers (postgres, redis, etc.).
	for _, svc := range spec.Services {
		svcID, svcAddr, err := startService(ctx, networkName, id, svc)
		if err != nil {
			// Clean up on failure.
			cleanupState(ctx, state)
			return nil, fmt.Errorf("docker: start service %s: %w", svc.Name, err)
		}
		state.serviceIDs[svc.Name] = svcID
		if svcAddr.Port > 0 {
			state.servicePorts[svc.Name] = svcAddr
		}
	}

	// Build the main container run command.
	args := []string{
		"run", "-d",
		"--name", id,
		"--network", networkName,
		"-w", workDir,
	}

	// Mount the repo root.
	if spec.RepoRoot != "" {
		args = append(args, "-v", spec.RepoRoot+":"+workDir)
	}

	// Additional volume mounts.
	for _, vol := range spec.Volumes {
		args = append(args, "-v", vol.Source+":"+vol.Target)
	}

	// Port exposures.
	for _, exp := range spec.Expose {
		args = append(args, "-p", fmt.Sprintf("%d:%d", exp.Port, exp.Port))
	}

	// Environment variables.
	for k, v := range spec.Env {
		args = append(args, "-e", k+"="+v)
	}

	// Resource limits.
	if spec.CPUs > 0 {
		args = append(args, "--cpus", fmt.Sprintf("%d", spec.CPUs))
	}
	if spec.MemoryMB > 0 {
		args = append(args, "-m", fmt.Sprintf("%dm", spec.MemoryMB))
	}

	image := spec.BaseImage
	if image == "" {
		image = "ubuntu:22.04"
	}

	// Keep container alive with a long sleep.
	args = append(args, image, "sleep", "infinity")

	containerID, err := dockerRun(ctx, args...)
	if err != nil {
		cleanupState(ctx, state)
		return nil, fmt.Errorf("docker: create container: %w", err)
	}
	containerID = strings.TrimSpace(containerID)

	h := &env.Handle{
		ID:      id,
		Backend: env.BackendDocker,
		WorkDir: workDir,
		Meta: map[string]string{
			"container_id": containerID,
			"network_id":   networkID,
			"network_name": networkName,
		},
		CreatedAt: time.Now(),
	}

	state.handle = h
	state.containerID = containerID

	// Run setup commands inside the container.
	for _, cmd := range spec.SetupCommands {
		result, err := b.execInContainer(ctx, containerID, workDir, []string{"bash", "-lc", cmd}, env.ExecOpts{})
		if err != nil {
			cleanupState(ctx, state)
			return nil, fmt.Errorf("docker: setup command %q: %w", cmd, err)
		}
		if !result.Success() {
			cleanupState(ctx, state)
			return nil, fmt.Errorf("docker: setup command %q failed (exit %d): %s",
				cmd, result.ExitCode, result.CombinedOutput())
		}
	}

	b.mu.Lock()
	b.active[h.ID] = state
	b.mu.Unlock()

	return h, nil
}

// Exec runs a command inside the main container.
func (b *Backend) Exec(ctx context.Context, h *env.Handle, cmdArgs []string, opts env.ExecOpts) (*env.ExecResult, error) {
	if h == nil {
		return nil, env.ErrNotProvisioned
	}

	b.mu.Lock()
	state, ok := b.active[h.ID]
	b.mu.Unlock()
	if !ok {
		return nil, env.ErrAlreadyTornDown
	}

	dir := h.WorkDir
	if opts.Dir != "" {
		dir = opts.Dir
	}

	return b.execInContainer(ctx, state.containerID, dir, cmdArgs, opts)
}

// CopyIn copies a local file into the container.
func (b *Backend) CopyIn(ctx context.Context, h *env.Handle, srcLocal, dstRemote string) error {
	if h == nil {
		return env.ErrNotProvisioned
	}
	b.mu.Lock()
	state, ok := b.active[h.ID]
	b.mu.Unlock()
	if !ok {
		return env.ErrAlreadyTornDown
	}
	_, err := dockerRun(ctx, "cp", srcLocal, state.containerID+":"+dstRemote)
	return err
}

// CopyOut copies a file from the container to the host.
func (b *Backend) CopyOut(ctx context.Context, h *env.Handle, srcRemote, dstLocal string) error {
	if h == nil {
		return env.ErrNotProvisioned
	}
	b.mu.Lock()
	state, ok := b.active[h.ID]
	b.mu.Unlock()
	if !ok {
		return env.ErrAlreadyTornDown
	}
	_, err := dockerRun(ctx, "cp", state.containerID+":"+srcRemote, dstLocal)
	return err
}

// Service returns the network address of a named service.
func (b *Backend) Service(_ context.Context, h *env.Handle, name string) (env.ServiceAddr, error) {
	if h == nil {
		return env.ServiceAddr{}, env.ErrNotProvisioned
	}
	b.mu.Lock()
	state, ok := b.active[h.ID]
	b.mu.Unlock()
	if !ok {
		return env.ServiceAddr{}, env.ErrAlreadyTornDown
	}
	addr, found := state.servicePorts[name]
	if !found {
		return env.ServiceAddr{}, env.ErrServiceNotFound
	}
	return addr, nil
}

// Teardown stops and removes all containers and the network.
func (b *Backend) Teardown(ctx context.Context, h *env.Handle) error {
	if h == nil {
		return nil
	}
	b.mu.Lock()
	state, ok := b.active[h.ID]
	if ok {
		delete(b.active, h.ID)
	}
	b.mu.Unlock()
	if !ok {
		return nil // already torn down
	}
	cleanupState(ctx, state)
	return nil
}

// Cost returns zero — local Docker has no external costs.
func (b *Backend) Cost(_ context.Context, h *env.Handle) (env.CostEstimate, error) {
	if h == nil {
		return env.CostEstimate{}, env.ErrNotProvisioned
	}
	return env.CostEstimate{Elapsed: time.Since(h.CreatedAt)}, nil
}

// --- internal helpers ---

// execInContainerFunc executes a docker exec command and returns the result.
// It is a variable to allow replacement in tests.
var execInContainerFunc = defaultExecInContainer

func (b *Backend) execInContainer(ctx context.Context, containerID, dir string, cmdArgs []string, opts env.ExecOpts) (*env.ExecResult, error) {
	return execInContainerFunc(ctx, containerID, dir, cmdArgs, opts)
}

func defaultExecInContainer(ctx context.Context, containerID, dir string, cmdArgs []string, opts env.ExecOpts) (*env.ExecResult, error) {
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	args := []string{"exec"}
	if dir != "" {
		args = append(args, "-w", dir)
	}
	for k, v := range opts.Env {
		args = append(args, "-e", k+"="+v)
	}
	args = append(args, containerID)
	args = append(args, cmdArgs...)

	cmd := exec.CommandContext(ctx, "docker", args...)
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
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		} else if ctx.Err() != nil {
			result.ExitCode = -1
			result.Stderr = strings.TrimSpace(result.Stderr + "\ncommand timed out")
		} else {
			return nil, fmt.Errorf("docker exec: %w", err)
		}
	}

	return result, nil
}

func startService(ctx context.Context, networkName, prefix string, svc env.ServiceSpec) (string, env.ServiceAddr, error) {
	name := prefix + "-" + svc.Name
	args := []string{
		"run", "-d",
		"--name", name,
		"--network", networkName,
		"--network-alias", svc.Name, // allows `postgres` as hostname inside network
	}

	for k, v := range svc.Env {
		args = append(args, "-e", k+"="+v)
	}

	// Expose first port for host access.
	var addr env.ServiceAddr
	if len(svc.Ports) > 0 {
		args = append(args, "-p", fmt.Sprintf("%d", svc.Ports[0]))
		addr = env.ServiceAddr{
			Protocol: "tcp",
			Host:     svc.Name, // accessible by name inside Docker network
			Port:     svc.Ports[0],
		}
	}

	args = append(args, svc.Image)
	out, err := dockerRun(ctx, args...)
	if err != nil {
		return "", env.ServiceAddr{}, err
	}

	containerID := strings.TrimSpace(out)

	// If port was auto-assigned, inspect to get the host port.
	if len(svc.Ports) > 0 {
		if hostPort := inspectHostPort(ctx, containerID, svc.Ports[0]); hostPort != "" {
			addr.Host = "localhost"
			fmt.Sscanf(hostPort, "%d", &addr.Port)
		}
	}

	return containerID, addr, nil
}

func inspectHostPort(ctx context.Context, containerID string, containerPort int) string {
	out, err := dockerRun(ctx, "inspect",
		"--format", fmt.Sprintf(`{{(index (index .NetworkSettings.Ports "%d/tcp") 0).HostPort}}`, containerPort),
		containerID)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func cleanupState(ctx context.Context, state *envState) {
	// Stop and remove main container.
	if state.containerID != "" {
		dockerRun(ctx, "rm", "-f", state.containerID)
	}
	// Stop and remove service containers.
	for _, id := range state.serviceIDs {
		dockerRun(ctx, "rm", "-f", id)
	}
	// Remove the network.
	if state.networkID != "" {
		networkName := ""
		if state.handle != nil {
			networkName = state.handle.Meta["network_name"]
		}
		if networkName != "" {
			dockerRun(ctx, "network", "rm", networkName)
		}
	}
}

// checkDockerFunc verifies Docker is available. Variable for test replacement.
var checkDockerFunc = defaultCheckDocker

func checkDocker(ctx context.Context) error {
	return checkDockerFunc(ctx)
}

func defaultCheckDocker(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "docker", "version", "--format", "{{.Server.Version}}")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker not available: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// dockerRunFunc is the function used to execute docker commands.
// It is a variable to allow replacement in tests.
var dockerRunFunc = defaultDockerRun

func dockerRun(ctx context.Context, args ...string) (string, error) {
	return dockerRunFunc(ctx, args)
}

func defaultDockerRun(ctx context.Context, args []string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...) // #nosec G204 -- sandbox/container binary invoked with Stoke-generated args.
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// Snapshot implements env.Snapshotter via docker commit.
func (b *Backend) Snapshot(ctx context.Context, h *env.Handle) (env.SnapshotID, error) {
	if h == nil {
		return "", env.ErrNotProvisioned
	}
	b.mu.Lock()
	state, ok := b.active[h.ID]
	b.mu.Unlock()
	if !ok {
		return "", env.ErrAlreadyTornDown
	}

	tag := fmt.Sprintf("stoke-snap-%d", time.Now().UnixNano())
	_, err := dockerRun(ctx, "commit", state.containerID, tag)
	if err != nil {
		return "", fmt.Errorf("docker snapshot: %w", err)
	}
	return env.SnapshotID(tag), nil
}

// Restore implements env.Snapshotter by recreating the container from a snapshot.
func (b *Backend) Restore(ctx context.Context, h *env.Handle, snap env.SnapshotID) error {
	if h == nil {
		return env.ErrNotProvisioned
	}
	b.mu.Lock()
	state, ok := b.active[h.ID]
	b.mu.Unlock()
	if !ok {
		return env.ErrAlreadyTornDown
	}

	// Stop and remove the current container.
	dockerRun(ctx, "rm", "-f", state.containerID)

	// Create a new container from the snapshot image.
	networkName := h.Meta["network_name"]
	args := []string{
		"run", "-d",
		"--name", h.ID,
		"--network", networkName,
		"-w", h.WorkDir,
		string(snap),
		"sleep", "infinity",
	}

	newID, err := dockerRun(ctx, args...)
	if err != nil {
		return fmt.Errorf("docker restore: %w", err)
	}

	b.mu.Lock()
	state.containerID = strings.TrimSpace(newID)
	b.mu.Unlock()

	return nil
}

// Verify interface compliance at compile time.
var (
	_ env.Environment = (*Backend)(nil)
	_ env.Snapshotter = (*Backend)(nil)
)

