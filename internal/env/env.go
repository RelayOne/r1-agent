// Package env defines the execution environment abstraction for Stoke missions.
//
// An execution environment is a sandbox where mission code runs — for building,
// testing, and verification. Stoke's stances use environments to actually execute
// code rather than inferring whether it works from reading it.
//
// Five backends implement the Environment interface:
//   - inproc:  Direct os/exec on the host (no isolation, fastest)
//   - docker:  Local Docker containers with optional service dependencies
//   - ssh:     Remote machine via SSH
//   - fly:     Fly-compatible REST API (works with both Fly.io and Flare)
//   - ember:   Ember's /v1/workers burst worker endpoint
//
// The verify pipeline uses Environment.Exec to run build/test/lint commands.
// The harness exposes env_exec/env_copy_in/env_copy_out as stance tools.
package env

import (
	"context"
	"fmt"
	"time"
)

// Backend identifies an execution environment backend.
type Backend string

const (
	BackendInProc Backend = "inproc"
	BackendDocker Backend = "docker"
	BackendSSH    Backend = "ssh"
	BackendFly    Backend = "fly"
	BackendEmber  Backend = "ember"
)

// Environment manages the lifecycle of an execution sandbox.
type Environment interface {
	// Provision creates and starts an execution environment from the given spec.
	Provision(ctx context.Context, spec Spec) (*Handle, error)

	// Exec runs a command inside the environment and returns its result.
	Exec(ctx context.Context, h *Handle, cmd []string, opts ExecOpts) (*ExecResult, error)

	// CopyIn transfers a local file or directory into the environment.
	CopyIn(ctx context.Context, h *Handle, srcLocal, dstRemote string) error

	// CopyOut transfers a file or directory from the environment to the host.
	CopyOut(ctx context.Context, h *Handle, srcRemote, dstLocal string) error

	// Service returns the network address of a named service running in the env.
	// Returns ErrServiceNotFound if the service doesn't exist.
	Service(ctx context.Context, h *Handle, name string) (ServiceAddr, error)

	// Teardown destroys the environment and releases all resources.
	// Must be safe to call multiple times (idempotent).
	Teardown(ctx context.Context, h *Handle) error

	// Cost returns the estimated cost of the environment so far.
	// Returns zero for free backends (inproc, docker).
	Cost(ctx context.Context, h *Handle) (CostEstimate, error)
}

// Snapshotter is an optional capability for backends that support
// point-in-time snapshots of the environment state.
// Not all backends support this — check with a type assertion.
type Snapshotter interface {
	Snapshot(ctx context.Context, h *Handle) (SnapshotID, error)
	Restore(ctx context.Context, h *Handle, snap SnapshotID) error
}

// SnapshotID is an opaque identifier for a point-in-time snapshot.
type SnapshotID string

// Handle is an opaque reference to a running execution environment.
type Handle struct {
	ID        string            // unique identifier for this environment instance
	Backend   Backend           // which backend manages this handle
	WorkDir   string            // working directory inside the environment
	Meta      map[string]string // backend-specific metadata (container ID, machine ID, etc.)
	CreatedAt time.Time
}

// Spec describes what execution environment a mission needs.
type Spec struct {
	// Backend selects the execution environment implementation.
	Backend Backend `yaml:"backend" json:"backend"`

	// BaseImage is the container/VM image to use (e.g. "golang:1.22-alpine").
	// Ignored by inproc backend.
	BaseImage string `yaml:"base_image,omitempty" json:"base_image,omitempty"`

	// Services are dependent containers/processes the mission needs.
	Services []ServiceSpec `yaml:"services,omitempty" json:"services,omitempty"`

	// Volumes maps host paths to environment paths.
	Volumes []VolumeSpec `yaml:"volumes,omitempty" json:"volumes,omitempty"`

	// Expose declares services that should be accessible from the host.
	Expose []ExposeSpec `yaml:"expose,omitempty" json:"expose,omitempty"`

	// SetupCommands run once after provisioning (before any mission work).
	SetupCommands []string `yaml:"setup_commands,omitempty" json:"setup_commands,omitempty"`

	// TestCommands are the verification commands (build, test, lint).
	TestCommands []string `yaml:"test_commands,omitempty" json:"test_commands,omitempty"`

	// E2ECommands run after services are up for integration testing.
	E2ECommands []string `yaml:"e2e_commands,omitempty" json:"e2e_commands,omitempty"`

	// WorkDir is the working directory inside the environment.
	// Defaults to "/workspace" for container backends, repo root for inproc.
	WorkDir string `yaml:"work_dir,omitempty" json:"work_dir,omitempty"`

	// Guest resource limits.
	CPUs     int `yaml:"cpus,omitempty" json:"cpus,omitempty"`
	MemoryMB int `yaml:"memory_mb,omitempty" json:"memory_mb,omitempty"`

	// Size is used by Fly/Ember backends (e.g. "performance-4x").
	Size string `yaml:"size,omitempty" json:"size,omitempty"`

	// TTLMinutes is the maximum lifetime for cloud backends.
	TTLMinutes int `yaml:"ttl_minutes,omitempty" json:"ttl_minutes,omitempty"`

	// Env are environment variables injected into the environment.
	Env map[string]string `yaml:"env,omitempty" json:"env,omitempty"`

	// RepoRoot is the local repo path to mount/copy into the environment.
	RepoRoot string `yaml:"-" json:"-"`
}

// ServiceSpec describes a dependent service (e.g. postgres, redis).
type ServiceSpec struct {
	Name  string            `yaml:"name" json:"name"`
	Image string            `yaml:"image" json:"image"`
	Env   map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	Ports []int             `yaml:"ports,omitempty" json:"ports,omitempty"`
}

// VolumeSpec maps a host path to an environment path.
type VolumeSpec struct {
	Source string `yaml:"source" json:"source"`
	Target string `yaml:"target" json:"target"`
}

// ExposeSpec declares a port to expose from the environment.
type ExposeSpec struct {
	Port    int    `yaml:"port" json:"port"`
	Service string `yaml:"service" json:"service"`
}

// ExecOpts configures a command execution.
type ExecOpts struct {
	// Dir overrides the working directory for this command.
	Dir string

	// Env adds environment variables for this command only.
	Env map[string]string

	// Timeout overrides the default command timeout.
	Timeout time.Duration

	// Stdin provides standard input to the command.
	Stdin []byte
}

// ExecResult captures the output of a command execution.
type ExecResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Duration time.Duration
}

// Success returns true if the command exited with code 0.
func (r *ExecResult) Success() bool {
	return r.ExitCode == 0
}

// CombinedOutput returns stdout and stderr concatenated.
func (r *ExecResult) CombinedOutput() string {
	if r.Stderr == "" {
		return r.Stdout
	}
	return r.Stdout + "\n" + r.Stderr
}

// ServiceAddr describes how to reach a service running in the environment.
type ServiceAddr struct {
	Protocol string // "http", "https", "tcp"
	Host     string
	Port     int
	Auth     string // optional auth token for authenticated services
}

// URL returns the service address as a URL string.
func (s ServiceAddr) URL() string {
	if s.Protocol == "" {
		s.Protocol = "http"
	}
	return fmt.Sprintf("%s://%s:%d", s.Protocol, s.Host, s.Port)
}

// CostEstimate tracks resource usage costs.
type CostEstimate struct {
	ComputeUSD float64       // compute cost so far
	StorageUSD float64       // storage cost (volumes, etc.)
	TotalUSD   float64       // combined total
	Elapsed    time.Duration // wall-clock time the env has been running
}

// Sentinel errors.
var (
	ErrServiceNotFound = fmt.Errorf("env: service not found")
	ErrNotProvisioned  = fmt.Errorf("env: environment not provisioned")
	ErrAlreadyTornDown = fmt.Errorf("env: environment already torn down")
)
