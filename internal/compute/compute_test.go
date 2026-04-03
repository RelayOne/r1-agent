package compute

import (
	"context"
	"testing"
)

func TestLocalBackend_Name(t *testing.T) {
	b := NewLocalBackend("/tmp/repo")
	if got := b.Name(); got != "local" {
		t.Errorf("Name() = %q, want %q", got, "local")
	}
}

func TestLocalBackend_Spawn(t *testing.T) {
	b := NewLocalBackend("/tmp/repo")
	w, err := b.Spawn(context.Background(), SpawnOpts{TaskID: "task-42"})
	if err != nil {
		t.Fatalf("Spawn() error: %v", err)
	}
	if w == nil {
		t.Fatal("Spawn() returned nil worker")
	}
	if got := w.ID(); got != "local-task-42" {
		t.Errorf("ID() = %q, want %q", got, "local-task-42")
	}
}

func TestLocalWorker_ID_Hostname(t *testing.T) {
	b := NewLocalBackend("/tmp/repo")
	w, err := b.Spawn(context.Background(), SpawnOpts{TaskID: "abc"})
	if err != nil {
		t.Fatalf("Spawn() error: %v", err)
	}
	if got := w.ID(); got != "local-abc" {
		t.Errorf("ID() = %q, want %q", got, "local-abc")
	}
	if got := w.Hostname(); got != "localhost" {
		t.Errorf("Hostname() = %q, want %q", got, "localhost")
	}
}

func TestLocalWorker_Exec(t *testing.T) {
	dir := t.TempDir()
	b := NewLocalBackend(dir)
	w, err := b.Spawn(context.Background(), SpawnOpts{TaskID: "exec-test"})
	if err != nil {
		t.Fatalf("Spawn() error: %v", err)
	}

	result, err := w.Exec(context.Background(), "echo", "hello world")
	if err != nil {
		t.Fatalf("Exec() error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}
	if result.Stdout != "hello world" {
		t.Errorf("Stdout = %q, want %q", result.Stdout, "hello world")
	}
	if result.Duration <= 0 {
		t.Errorf("Duration = %d, want > 0", result.Duration)
	}
}

func TestLocalWorker_Exec_Failure(t *testing.T) {
	dir := t.TempDir()
	b := NewLocalBackend(dir)
	w, err := b.Spawn(context.Background(), SpawnOpts{TaskID: "fail-test"})
	if err != nil {
		t.Fatalf("Spawn() error: %v", err)
	}

	_, err = w.Exec(context.Background(), "nonexistent_command_xyz_12345")
	if err == nil {
		t.Fatal("Exec() expected error for nonexistent command, got nil")
	}
}

func TestLocalWorker_NoOp(t *testing.T) {
	dir := t.TempDir()
	b := NewLocalBackend(dir)
	w, err := b.Spawn(context.Background(), SpawnOpts{TaskID: "noop-test"})
	if err != nil {
		t.Fatalf("Spawn() error: %v", err)
	}
	ctx := context.Background()

	if err := w.Upload(ctx, "/tmp/a", "/tmp/b"); err != nil {
		t.Errorf("Upload() = %v, want nil", err)
	}
	if err := w.Download(ctx, "/tmp/a", "/tmp/b"); err != nil {
		t.Errorf("Download() = %v, want nil", err)
	}
	if err := w.Stop(ctx); err != nil {
		t.Errorf("Stop() = %v, want nil", err)
	}
	if err := w.Destroy(ctx); err != nil {
		t.Errorf("Destroy() = %v, want nil", err)
	}

	// Stdout should return a non-nil reader
	if w.Stdout() == nil {
		t.Error("Stdout() returned nil")
	}
}
