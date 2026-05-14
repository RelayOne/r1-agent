package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/bench"
)

func writeResultJSON(t *testing.T, dir, name string, r bench.RunResult) {
	t.Helper()
	body, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestAggregateDir_MarkdownLeaderboard(t *testing.T) {
	dir := t.TempDir()
	writeResultJSON(t, dir, "r1--m1.json", bench.RunResult{
		AgentID: "r1", MissionID: "m1",
		CompletionAttempted: true, CompletionTruthful: true,
	})
	writeResultJSON(t, dir, "r1--m2.json", bench.RunResult{
		AgentID: "r1", MissionID: "m2",
		CompletionAttempted: true, CompletionTruthful: true,
	})
	writeResultJSON(t, dir, "aider--m1.json", bench.RunResult{
		AgentID: "aider", MissionID: "m1",
		CompletionAttempted: true, CompletionTruthful: false,
	})

	out, err := AggregateDir(dir, "markdown")
	if err != nil {
		t.Fatalf("AggregateDir: %v", err)
	}
	if !strings.Contains(out, "# TruthfulCompletion Leaderboard") {
		t.Errorf("missing leaderboard header: %q", out)
	}
	if !strings.Contains(out, "| r1 |") {
		t.Errorf("r1 row missing: %q", out)
	}
	if !strings.Contains(out, "| aider |") {
		t.Errorf("aider row missing: %q", out)
	}
	if !strings.Contains(out, "100.0%") {
		t.Errorf("r1's 100%% rate missing: %q", out)
	}
}

func TestAggregateDir_PerMissionFormat(t *testing.T) {
	dir := t.TempDir()
	writeResultJSON(t, dir, "r1--m1.json", bench.RunResult{
		AgentID: "r1", MissionID: "m1",
		CompletionAttempted: true, CompletionTruthful: true,
		PlanItemsCompleted: 1, PlanItemsTotal: 1, DeliveryRatioPercent: 95,
	})
	out, err := AggregateDir(dir, "per-mission")
	if err != nil {
		t.Fatalf("AggregateDir: %v", err)
	}
	if !strings.Contains(out, "# Per-Mission Breakdown") {
		t.Errorf("missing per-mission header: %q", out)
	}
	if !strings.Contains(out, "| OK |") {
		t.Errorf("OK verdict glyph missing: %q", out)
	}
}

func TestAggregateDir_BothFormat(t *testing.T) {
	dir := t.TempDir()
	writeResultJSON(t, dir, "r1--m1.json", bench.RunResult{
		AgentID: "r1", MissionID: "m1",
		CompletionAttempted: true, CompletionTruthful: true,
	})
	out, err := AggregateDir(dir, "both")
	if err != nil {
		t.Fatalf("AggregateDir: %v", err)
	}
	if !strings.Contains(out, "# TruthfulCompletion Leaderboard") {
		t.Errorf("missing leaderboard header")
	}
	if !strings.Contains(out, "# Per-Mission Breakdown") {
		t.Errorf("missing per-mission header")
	}
}

func TestAggregateDir_UnknownFormatErrors(t *testing.T) {
	dir := t.TempDir()
	writeResultJSON(t, dir, "r1--m1.json", bench.RunResult{AgentID: "r1", MissionID: "m1"})
	_, err := AggregateDir(dir, "csv")
	if err == nil {
		t.Errorf("unknown format should error")
	}
}

func TestAggregateDir_EmptyDirErrors(t *testing.T) {
	_, err := AggregateDir(t.TempDir(), "markdown")
	if err == nil {
		t.Errorf("empty dir should error")
	}
}

func TestAggregateDir_SkipsBadFiles(t *testing.T) {
	dir := t.TempDir()
	// One valid result.
	writeResultJSON(t, dir, "r1--m1.json", bench.RunResult{
		AgentID: "r1", MissionID: "m1",
		CompletionAttempted: true, CompletionTruthful: true,
	})
	// One invalid: empty file.
	if err := os.WriteFile(filepath.Join(dir, "empty.json"), []byte{}, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	// One invalid: junk JSON.
	if err := os.WriteFile(filepath.Join(dir, "junk.json"), []byte("not json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	// One non-JSON file (ignored, not counted as skipped).
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("blah"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	// One JSON that parses but has no AgentID/MissionID.
	if err := os.WriteFile(filepath.Join(dir, "empty-obj.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	out, err := AggregateDir(dir, "markdown")
	if err != nil {
		t.Fatalf("AggregateDir: %v", err)
	}
	if !strings.Contains(out, "files skipped") {
		t.Errorf("expected skipped-files comment in output: %q", out)
	}
	if !strings.Contains(out, "| r1 |") {
		t.Errorf("valid result missing from output: %q", out)
	}
}

func TestAggregateDir_DefaultFormatIsMarkdown(t *testing.T) {
	dir := t.TempDir()
	writeResultJSON(t, dir, "r1--m1.json", bench.RunResult{
		AgentID: "r1", MissionID: "m1",
		CompletionAttempted: true, CompletionTruthful: true,
	})
	out, err := AggregateDir(dir, "")
	if err != nil {
		t.Fatalf("AggregateDir: %v", err)
	}
	if !strings.Contains(out, "# TruthfulCompletion Leaderboard") {
		t.Errorf("default format should produce leaderboard: %q", out)
	}
}
