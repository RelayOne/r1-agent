package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/daemon"
)

func TestLoadDaemonExecutor(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name     string
		execName string
		wantType string
	}{
		{name: "noop", execName: "noop", wantType: "noop"},
		{name: "codex", execName: "codex", wantType: "codex"},
		{name: "claude-code", execName: "claude-code", wantType: "claude-code"},
		{name: "bash", execName: "bash", wantType: "bash"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			exec, err := loadDaemonExecutor(tc.execName, dir, "", 250*time.Millisecond)
			if err != nil {
				t.Fatalf("loadDaemonExecutor: %v", err)
			}
			if exec.Type() != tc.wantType {
				t.Fatalf("executor type = %q want %q", exec.Type(), tc.wantType)
			}
		})
	}
	if _, err := loadDaemonExecutor("bogus", dir, "", 250*time.Millisecond); err == nil {
		t.Fatalf("expected error for unknown executor")
	}
}

func TestLoadDaemonExecutorCodexJobsDirPriority(t *testing.T) {
	dir := t.TempDir()
	sharedDefault := filepath.Join(mustUserHomeDir(t), "repos", "plans", "codex-jobs")

	t.Run("default shared path", func(t *testing.T) {
		t.Setenv("STOKE_CODEXJOB_JOBS_DIR", "")
		exec, err := loadDaemonExecutor("codex", dir, "", 250*time.Millisecond)
		if err != nil {
			t.Fatalf("loadDaemonExecutor: %v", err)
		}
		codexExec, ok := exec.(daemon.CodexExecutor)
		if !ok {
			t.Fatalf("executor type %T is not CodexExecutor", exec)
		}
		if codexExecJobsDir := reflectCodexJobsDir(t, codexExec); codexExecJobsDir != sharedDefault {
			t.Fatalf("jobs dir = %q want %q", codexExecJobsDir, sharedDefault)
		}
	})

	t.Run("env override", func(t *testing.T) {
		envDir := filepath.Join(dir, "env-jobs")
		t.Setenv("STOKE_CODEXJOB_JOBS_DIR", envDir)
		exec, err := loadDaemonExecutor("codex", dir, "", 250*time.Millisecond)
		if err != nil {
			t.Fatalf("loadDaemonExecutor: %v", err)
		}
		codexExec, ok := exec.(daemon.CodexExecutor)
		if !ok {
			t.Fatalf("executor type %T is not CodexExecutor", exec)
		}
		if codexExecJobsDir := reflectCodexJobsDir(t, codexExec); codexExecJobsDir != envDir {
			t.Fatalf("jobs dir = %q want %q", codexExecJobsDir, envDir)
		}
	})

	t.Run("flag override", func(t *testing.T) {
		flagDir := filepath.Join(dir, "flag-jobs")
		t.Setenv("STOKE_CODEXJOB_JOBS_DIR", filepath.Join(dir, "env-jobs"))
		exec, err := loadDaemonExecutor("codex", dir, flagDir, 250*time.Millisecond)
		if err != nil {
			t.Fatalf("loadDaemonExecutor: %v", err)
		}
		codexExec, ok := exec.(daemon.CodexExecutor)
		if !ok {
			t.Fatalf("executor type %T is not CodexExecutor", exec)
		}
		if codexExecJobsDir := reflectCodexJobsDir(t, codexExec); codexExecJobsDir != flagDir {
			t.Fatalf("jobs dir = %q want %q", codexExecJobsDir, flagDir)
		}
	})
}

func mustUserHomeDir(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	return home
}

func reflectCodexJobsDir(t *testing.T, exec daemon.CodexExecutor) string {
	t.Helper()
	value := reflect.ValueOf(exec)
	field := value.FieldByName("jobsDir")
	if !field.IsValid() {
		t.Fatalf("CodexExecutor.jobsDir missing")
	}
	return field.String()
}
