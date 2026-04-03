package sandbox

import (
	"testing"
)

func TestDetect(t *testing.T) {
	env := Detect()
	// We're running in CI/container, so just verify it returns something valid
	if env.Platform == "" {
		t.Error("expected non-empty platform")
	}
	if env.ContainerType == "" {
		t.Error("expected non-empty container type")
	}
}

func TestDefaultPolicy(t *testing.T) {
	p := DefaultPolicy()
	if !p.Enabled {
		t.Error("expected sandbox enabled by default")
	}
	if !p.FailIfUnavailable {
		t.Error("expected fail-closed by default")
	}
	if len(p.AllowedDomains) == 0 {
		t.Error("expected default allowed domains")
	}
}

func TestReadOnlyPolicy(t *testing.T) {
	p := ReadOnlyPolicy()
	if !p.DenyNetworkAccess {
		t.Error("expected network denied in read-only")
	}
}

func TestSandboxArgs(t *testing.T) {
	p := Policy{
		Enabled:        true,
		AllowedDomains: []string{"api.anthropic.com", "api.github.com"},
		AllowedReadPaths: []string{"/tmp"},
	}
	args := p.SandboxArgs()
	if len(args) != 6 { // 2 domains * 2 args + 1 read * 2 args
		t.Errorf("expected 6 args, got %d: %v", len(args), args)
	}
}

func TestSandboxArgsDisabled(t *testing.T) {
	p := Policy{Enabled: false}
	args := p.SandboxArgs()
	if len(args) != 0 {
		t.Errorf("expected no args when disabled, got %v", args)
	}
}
