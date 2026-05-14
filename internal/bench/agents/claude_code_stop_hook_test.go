package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeCodeStopHookDispatcher_Agent(t *testing.T) {
	d := &ClaudeCodeStopHookDispatcher{}
	a := d.Agent()
	if a.ID != "claude-code-stop-hook" {
		t.Errorf("ID = %q, want claude-code-stop-hook", a.ID)
	}
}

func TestInstallStopHook_WritesSettingsWithExpectedCommand(t *testing.T) {
	dir := t.TempDir()
	if err := installStopHook(dir, "/usr/local/bin/r1"); err != nil {
		t.Fatalf("installStopHook: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	var got stopHookSettings
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("settings.json is not valid JSON: %v", err)
	}
	stops, ok := got.Hooks["Stop"]
	if !ok || len(stops) == 0 {
		t.Fatalf("settings.json missing Stop hook entry")
	}
	if len(stops[0].Hooks) == 0 {
		t.Fatalf("Stop hook entry has no commands")
	}
	cmd := stops[0].Hooks[0].Command
	if !strings.Contains(cmd, "antitrunc") {
		t.Errorf("hook command = %q, expected to contain 'antitrunc'", cmd)
	}
	if !strings.Contains(cmd, "--hook-mode") {
		t.Errorf("hook command = %q, expected to contain '--hook-mode'", cmd)
	}
	if !strings.Contains(cmd, "/usr/local/bin/r1") {
		t.Errorf("hook command = %q, expected to invoke configured r1 binary path", cmd)
	}
}

func TestInstallStopHook_DefaultR1BinaryFromCallerControl(t *testing.T) {
	// installStopHook itself does not default — its caller does.
	// Confirm passing "r1" (the caller's default) produces a relative
	// command that the user's PATH will resolve at runtime.
	dir := t.TempDir()
	if err := installStopHook(dir, "r1"); err != nil {
		t.Fatalf("installStopHook: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	if !strings.Contains(string(body), `"command": "r1 antitrunc`) {
		t.Errorf("unexpected default command form: %s", body)
	}
}

func TestClaudeCodeStopHookDispatcher_RegisteredInRegistry(t *testing.T) {
	if _, ok := Registry["claude-code-stop-hook"]; !ok {
		t.Errorf("registry missing claude-code-stop-hook")
	}
}
