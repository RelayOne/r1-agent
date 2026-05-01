package main

import (
	"testing"
	"time"
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
			exec, err := loadDaemonExecutor(tc.execName, dir, 250*time.Millisecond)
			if err != nil {
				t.Fatalf("loadDaemonExecutor: %v", err)
			}
			if exec.Type() != tc.wantType {
				t.Fatalf("executor type = %q want %q", exec.Type(), tc.wantType)
			}
		})
	}
	if _, err := loadDaemonExecutor("bogus", dir, 250*time.Millisecond); err == nil {
		t.Fatalf("expected error for unknown executor")
	}
}
