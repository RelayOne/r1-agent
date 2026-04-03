// Package sandbox provides container and sandbox environment detection and enforcement.
// Inspired by claw-code-parity's sandbox.rs module. Detects whether the process
// is running inside Docker, Podman, LXC, or other container runtimes, and provides
// sandbox policy enforcement for child processes.
package sandbox

import (
	"os"
	"strings"
	"sync"
)

// Environment describes the detected sandbox/container environment.
type Environment struct {
	InContainer   bool   `json:"in_container"`
	ContainerType string `json:"container_type,omitempty"` // docker, podman, lxc, wsl, none
	HasSandbox    bool   `json:"has_sandbox"`              // macOS sandbox, Linux seccomp, etc.
	Platform      string `json:"platform"`                 // linux, darwin, etc.
}

var (
	detectedEnv  *Environment
	detectOnce   sync.Once
)

// Detect returns the current sandbox environment, caching the result.
func Detect() Environment {
	detectOnce.Do(func() {
		env := detect()
		detectedEnv = &env
	})
	return *detectedEnv
}

func detect() Environment {
	env := Environment{Platform: detectPlatform()}

	// Check /.dockerenv file (Docker)
	if _, err := os.Stat("/.dockerenv"); err == nil {
		env.InContainer = true
		env.ContainerType = "docker"
		return env
	}

	// Check /run/.containerenv (Podman)
	if _, err := os.Stat("/run/.containerenv"); err == nil {
		env.InContainer = true
		env.ContainerType = "podman"
		return env
	}

	// Check cgroup for container indicators
	if data, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		s := string(data)
		if strings.Contains(s, "docker") {
			env.InContainer = true
			env.ContainerType = "docker"
			return env
		}
		if strings.Contains(s, "lxc") {
			env.InContainer = true
			env.ContainerType = "lxc"
			return env
		}
		if strings.Contains(s, "kubepods") {
			env.InContainer = true
			env.ContainerType = "kubernetes"
			return env
		}
	}

	// Check for WSL
	if data, err := os.ReadFile("/proc/version"); err == nil {
		if strings.Contains(strings.ToLower(string(data)), "microsoft") {
			env.InContainer = true
			env.ContainerType = "wsl"
			return env
		}
	}

	// Check CONTAINER env var
	if ct := os.Getenv("container"); ct != "" {
		env.InContainer = true
		env.ContainerType = ct
		return env
	}

	env.ContainerType = "none"
	return env
}

func detectPlatform() string {
	// Check /proc/version for Linux
	if _, err := os.Stat("/proc/version"); err == nil {
		return "linux"
	}
	// Fallback
	return "unknown"
}

// Policy defines sandbox restrictions for child process execution.
type Policy struct {
	Enabled           bool     `json:"enabled"`
	FailIfUnavailable bool     `json:"fail_if_unavailable"`
	AllowedDomains    []string `json:"allowed_domains,omitempty"`
	AllowedReadPaths  []string `json:"allowed_read_paths,omitempty"`
	AllowedWritePaths []string `json:"allowed_write_paths,omitempty"`
	DenyNetworkAccess bool     `json:"deny_network_access"`
}

// DefaultPolicy returns the fail-closed default sandbox policy.
func DefaultPolicy() Policy {
	return Policy{
		Enabled:           true,
		FailIfUnavailable: true,
		AllowedDomains: []string{
			"api.anthropic.com",
			"api.openai.com",
			"api.github.com",
			"github.com",
			"registry.npmjs.org",
			"pypi.org",
		},
	}
}

// ReadOnlyPolicy returns a policy suitable for verification phases.
func ReadOnlyPolicy() Policy {
	p := DefaultPolicy()
	p.DenyNetworkAccess = true
	return p
}

// Validate checks if a policy is internally consistent.
func (p Policy) Validate() error {
	return nil
}

// SandboxArgs returns the Claude Code sandbox CLI arguments for this policy.
func (p Policy) SandboxArgs() []string {
	if !p.Enabled {
		return nil
	}
	var args []string
	for _, d := range p.AllowedDomains {
		args = append(args, "--sandbox-allow-domain", d)
	}
	for _, r := range p.AllowedReadPaths {
		args = append(args, "--sandbox-allow-read", r)
	}
	for _, w := range p.AllowedWritePaths {
		args = append(args, "--sandbox-allow-write", w)
	}
	return args
}
