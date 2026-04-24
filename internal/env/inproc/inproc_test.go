package inproc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/env"
)

func TestProvisionAndExec(t *testing.T) {
	dir := t.TempDir()
	b := New()

	h, err := b.Provision(context.Background(), env.Spec{
		Backend:  env.BackendInProc,
		RepoRoot: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Teardown(context.Background(), h)

	if h.Backend != env.BackendInProc {
		t.Errorf("backend=%s, want inproc", h.Backend)
	}
	if h.WorkDir != dir {
		t.Errorf("workdir=%s, want %s", h.WorkDir, dir)
	}

	// Run a simple command.
	result, err := b.Exec(context.Background(), h, []string{"echo", "hello"}, env.ExecOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success() {
		t.Errorf("exit=%d, want 0", result.ExitCode)
	}
	if result.Stdout != "hello\n" {
		t.Errorf("stdout=%q, want %q", result.Stdout, "hello\n")
	}
}

func TestExecFailure(t *testing.T) {
	b := New()
	h, err := b.Provision(context.Background(), env.Spec{
		Backend:  env.BackendInProc,
		RepoRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Teardown(context.Background(), h)

	result, err := b.Exec(context.Background(), h, []string{"bash", "-c", "exit 42"}, env.ExecOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 42 {
		t.Errorf("exit=%d, want 42", result.ExitCode)
	}
	if result.Success() {
		t.Error("should not be success")
	}
}

func TestExecTimeout(t *testing.T) {
	b := New()
	h, err := b.Provision(context.Background(), env.Spec{
		Backend:  env.BackendInProc,
		RepoRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Teardown(context.Background(), h)

	result, err := b.Exec(context.Background(), h, []string{"sleep", "60"}, env.ExecOpts{
		Timeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != -1 {
		t.Errorf("exit=%d, want -1 for timeout", result.ExitCode)
	}
}

func TestSetupCommands(t *testing.T) {
	dir := t.TempDir()
	b := New()

	h, err := b.Provision(context.Background(), env.Spec{
		Backend:       env.BackendInProc,
		RepoRoot:      dir,
		SetupCommands: []string{"touch " + filepath.Join(dir, "setup-marker")},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Teardown(context.Background(), h)

	if _, err := os.Stat(filepath.Join(dir, "setup-marker")); err != nil {
		t.Error("setup command should have created marker file")
	}
}

func TestSetupCommandFailure(t *testing.T) {
	b := New()
	_, err := b.Provision(context.Background(), env.Spec{
		Backend:       env.BackendInProc,
		RepoRoot:      t.TempDir(),
		SetupCommands: []string{"false"},
	})
	if err == nil {
		t.Error("should fail when setup command fails")
	}
}

func TestCopyInOutNoOp(t *testing.T) {
	b := New()
	h, _ := b.Provision(context.Background(), env.Spec{
		Backend:  env.BackendInProc,
		RepoRoot: t.TempDir(),
	})
	defer b.Teardown(context.Background(), h)

	if err := b.CopyIn(context.Background(), h, "/tmp/a", "/tmp/b"); err != nil {
		t.Errorf("CopyIn should be no-op: %v", err)
	}
	if err := b.CopyOut(context.Background(), h, "/tmp/a", "/tmp/b"); err != nil {
		t.Errorf("CopyOut should be no-op: %v", err)
	}
}

func TestServiceNotFound(t *testing.T) {
	b := New()
	h, _ := b.Provision(context.Background(), env.Spec{
		Backend:  env.BackendInProc,
		RepoRoot: t.TempDir(),
	})
	defer b.Teardown(context.Background(), h)

	_, err := b.Service(context.Background(), h, "postgres")
	if !errors.Is(err, env.ErrServiceNotFound) {
		t.Errorf("err=%v, want ErrServiceNotFound", err)
	}
}

func TestCostZero(t *testing.T) {
	b := New()
	h, _ := b.Provision(context.Background(), env.Spec{
		Backend:  env.BackendInProc,
		RepoRoot: t.TempDir(),
	})
	defer b.Teardown(context.Background(), h)

	cost, err := b.Cost(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if cost.TotalUSD != 0 {
		t.Errorf("cost=%f, want 0", cost.TotalUSD)
	}
}

func TestTeardownIdempotent(t *testing.T) {
	b := New()
	h, _ := b.Provision(context.Background(), env.Spec{
		Backend:  env.BackendInProc,
		RepoRoot: t.TempDir(),
	})

	if err := b.Teardown(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	// Second teardown should not error.
	if err := b.Teardown(context.Background(), h); err != nil {
		t.Errorf("second teardown should be no-op: %v", err)
	}
}

func TestNilHandleErrors(t *testing.T) {
	b := New()

	if _, err := b.Exec(context.Background(), nil, []string{"echo"}, env.ExecOpts{}); !errors.Is(err, env.ErrNotProvisioned) {
		t.Errorf("Exec nil handle: %v", err)
	}
	if err := b.CopyIn(context.Background(), nil, "", ""); !errors.Is(err, env.ErrNotProvisioned) {
		t.Errorf("CopyIn nil handle: %v", err)
	}
	if _, err := b.Cost(context.Background(), nil); !errors.Is(err, env.ErrNotProvisioned) {
		t.Errorf("Cost nil handle: %v", err)
	}
}
