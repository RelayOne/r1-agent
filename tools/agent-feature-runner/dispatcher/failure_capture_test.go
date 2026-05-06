package dispatcher

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSafeScenarioDir_BasicSlug(t *testing.T) {
	got := safeScenarioDir("User sends a message")
	if got != "user-sends-a-message" {
		t.Errorf("got %q", got)
	}
}

func TestSafeScenarioDir_PunctuationCollapsed(t *testing.T) {
	got := safeScenarioDir(`Click "Send" — twice!`)
	if got != "click-send-twice" {
		t.Errorf("got %q", got)
	}
}

func TestSafeScenarioDir_LeadingTrailingDashesTrimmed(t *testing.T) {
	got := safeScenarioDir("---Special---")
	if got != "special" {
		t.Errorf("got %q", got)
	}
}

func TestSafeScenarioDir_Empty(t *testing.T) {
	if got := safeScenarioDir(""); got != "" {
		t.Errorf("empty input should yield empty slug; got %q", got)
	}
}

func TestCaptureFailure_WritesSummaryAlways(t *testing.T) {
	root := t.TempDir()
	dir, err := CaptureFailure(root, FailureContext{
		ScenarioName: "demo failure",
		FeaturePath:  "tests/agent/web/foo.agent.feature.md",
		StepLine:     12,
		StepText:     "Then the chat log contains 'pong'",
		Cause:        "assertion missed",
	})
	if err != nil {
		t.Fatalf("CaptureFailure: %v", err)
	}
	summary := filepath.Join(dir, "summary.json")
	data, err := os.ReadFile(summary)
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["scenario_name"] != "demo failure" {
		t.Errorf("scenario_name = %v", got["scenario_name"])
	}
	if got["cause"] != "assertion missed" {
		t.Errorf("cause = %v", got["cause"])
	}
}

func TestCaptureFailure_WritesSnapshotAndViewWhenProvided(t *testing.T) {
	root := t.TempDir()
	snap := json.RawMessage(`{"role":"button","name":"Send"}`)
	dir, err := CaptureFailure(root, FailureContext{
		ScenarioName: "demo",
		Snapshot:     snap,
		View:         "  > alpha\n    beta\n",
	})
	if err != nil {
		t.Fatalf("CaptureFailure: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "snapshot.json"))
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if !strings.Contains(string(got), "Send") {
		t.Errorf("snapshot.json missing content: %s", got)
	}
	view, err := os.ReadFile(filepath.Join(dir, "view.txt"))
	if err != nil {
		t.Fatalf("read view: %v", err)
	}
	if !strings.Contains(string(view), "alpha") {
		t.Errorf("view.txt missing content: %s", view)
	}
}

func TestCaptureFailure_WritesNDJSONForBusAndLanes(t *testing.T) {
	root := t.TempDir()
	dir, err := CaptureFailure(root, FailureContext{
		ScenarioName: "evt",
		BusEvents: []json.RawMessage{
			json.RawMessage(`{"seq":1,"type":"start"}`),
			json.RawMessage(`{"seq":2,"type":"step"}`),
		},
		LaneEvents: []json.RawMessage{
			json.RawMessage(`{"lane":"alpha","status":"running"}`),
		},
	})
	if err != nil {
		t.Fatalf("CaptureFailure: %v", err)
	}
	bus, err := os.ReadFile(filepath.Join(dir, "bus.ndjson"))
	if err != nil {
		t.Fatalf("read bus.ndjson: %v", err)
	}
	if lines := strings.Count(string(bus), "\n"); lines != 2 {
		t.Errorf("bus.ndjson has %d lines, want 2", lines)
	}
	lanes, err := os.ReadFile(filepath.Join(dir, "lanes.ndjson"))
	if err != nil {
		t.Fatalf("read lanes.ndjson: %v", err)
	}
	if lines := strings.Count(string(lanes), "\n"); lines != 1 {
		t.Errorf("lanes.ndjson has %d lines, want 1", lines)
	}
}

func TestCaptureFailure_OmitsFilesWhenSourceEmpty(t *testing.T) {
	root := t.TempDir()
	dir, err := CaptureFailure(root, FailureContext{ScenarioName: "minimal"})
	if err != nil {
		t.Fatalf("CaptureFailure: %v", err)
	}
	for _, fname := range []string{"snapshot.json", "view.txt", "bus.ndjson", "lanes.ndjson"} {
		_, err := os.Stat(filepath.Join(dir, fname))
		if !os.IsNotExist(err) {
			t.Errorf("%s should be absent when source empty; got err=%v", fname, err)
		}
	}
}

func TestCaptureFailure_IdempotentClearsPriorRun(t *testing.T) {
	root := t.TempDir()
	dir1, _ := CaptureFailure(root, FailureContext{
		ScenarioName: "idempotent",
		Cause:        "first run",
	})
	// Drop a stale file under the dir; CaptureFailure should remove it.
	stale := filepath.Join(dir1, "stale.txt")
	if err := os.WriteFile(stale, []byte("stale"), 0o644); err != nil {
		t.Fatalf("write stale: %v", err)
	}
	dir2, _ := CaptureFailure(root, FailureContext{
		ScenarioName: "idempotent",
		Cause:        "second run",
	})
	if dir1 != dir2 {
		t.Errorf("dir paths differ: %q vs %q", dir1, dir2)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale file should have been cleared; got err=%v", err)
	}
	// summary.json should reflect the second run.
	data, _ := os.ReadFile(filepath.Join(dir2, "summary.json"))
	if !strings.Contains(string(data), "second run") {
		t.Errorf("summary.json does not reflect second run: %s", data)
	}
}

func TestCaptureFailure_DefaultRootIsAgentFailures(t *testing.T) {
	// Use a chdir into TempDir so the relative .agent-failures lands
	// somewhere we can clean up.
	prev, _ := os.Getwd()
	defer func() { _ = os.Chdir(prev) }()
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	dir, err := CaptureFailure("", FailureContext{ScenarioName: "default"})
	if err != nil {
		t.Fatalf("CaptureFailure: %v", err)
	}
	if !strings.Contains(dir, ".agent-failures") {
		t.Errorf("expected default root '.agent-failures' in %q", dir)
	}
}
