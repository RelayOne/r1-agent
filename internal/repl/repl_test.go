package repl

import (
	"testing"
)

func TestNew(t *testing.T) {
	r := New("/tmp/test-repo")
	if r == nil {
		t.Fatal("New() returned nil")
	}
	if r.RepoRoot != "/tmp/test-repo" {
		t.Errorf("RepoRoot = %q, want %q", r.RepoRoot, "/tmp/test-repo")
	}
	if r.Commands == nil {
		t.Error("Commands map is nil")
	}
	if len(r.Commands) != 0 {
		t.Errorf("Commands has %d entries, want 0", len(r.Commands))
	}
}

func TestRegister(t *testing.T) {
	r := New("/tmp/repo")

	called := false
	r.Register(Command{
		Name:        "test",
		Description: "A test command",
		Usage:       "/test [args]",
		Run:         func(args string) { called = true },
	})

	if len(r.Commands) != 1 {
		t.Fatalf("Commands has %d entries, want 1", len(r.Commands))
	}

	cmd, ok := r.Commands["test"]
	if !ok {
		t.Fatal("command 'test' not found in Commands map")
	}
	if cmd.Name != "test" {
		t.Errorf("Name = %q, want %q", cmd.Name, "test")
	}
	if cmd.Description != "A test command" {
		t.Errorf("Description = %q, want %q", cmd.Description, "A test command")
	}
	if cmd.Usage != "/test [args]" {
		t.Errorf("Usage = %q, want %q", cmd.Usage, "/test [args]")
	}

	// Verify Run callback works
	cmd.Run("some args")
	if !called {
		t.Error("Run callback was not invoked")
	}
}

func TestRegister_Multiple(t *testing.T) {
	r := New("/tmp/repo")

	r.Register(Command{Name: "build", Description: "Build", Run: func(string) {}})
	r.Register(Command{Name: "scan", Description: "Scan", Run: func(string) {}})
	r.Register(Command{Name: "audit", Description: "Audit", Run: func(string) {}})

	if len(r.Commands) != 3 {
		t.Errorf("Commands has %d entries, want 3", len(r.Commands))
	}

	for _, name := range []string{"build", "scan", "audit"} {
		if _, ok := r.Commands[name]; !ok {
			t.Errorf("command %q not found", name)
		}
	}
}

func TestRegister_Overwrite(t *testing.T) {
	r := New("/tmp/repo")

	r.Register(Command{Name: "test", Description: "v1", Run: func(string) {}})
	r.Register(Command{Name: "test", Description: "v2", Run: func(string) {}})

	if len(r.Commands) != 1 {
		t.Errorf("Commands has %d entries, want 1 (overwrite)", len(r.Commands))
	}
	if r.Commands["test"].Description != "v2" {
		t.Errorf("Description = %q, want %q (overwrite)", r.Commands["test"].Description, "v2")
	}
}
