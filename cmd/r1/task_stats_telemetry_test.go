package main

// task_stats_telemetry_test.go — O6 read-side tests: the pure
// telemetryDrift diff and the end-to-end `r1 stats telemetry` view over
// persisted snapshots.

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/r1dir"
	"github.com/RelayOne/r1/internal/telemetry"
)

// captureTelStdout runs fn with os.Stdout redirected to a pipe and
// returns everything it printed.
func captureTelStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	fn()
	_ = w.Close()
	os.Stdout = old
	return <-done
}

// TestTelemetryDrift_FlagsRegressions proves the pure diff surfaces a
// success-rate drop and a latency rise between two runs.
func TestTelemetryDrift_FlagsRegressions(t *testing.T) {
	prev := telemetry.Snapshot{
		RunID: "run-old",
		Summary: telemetry.MetricsSummary{
			SuccessRate: 1.0,
			AvgDuration: 2 * time.Second,
			TotalCost:   0.50,
		},
		TaskPercentiles: map[string]time.Duration{"p50": 2 * time.Second, "p95": 2 * time.Second},
	}
	cur := telemetry.Snapshot{
		RunID: "run-new",
		Summary: telemetry.MetricsSummary{
			SuccessRate: 0.5,
			AvgDuration: 4 * time.Second,
			TotalCost:   1.00,
		},
		TaskPercentiles: map[string]time.Duration{"p50": 4 * time.Second, "p95": 5 * time.Second},
	}
	out := telemetryDrift(prev, cur)

	for _, want := range []string{
		"drift run-old", "run-new",
		"success rate dropped 50 points",
		"avg duration rose 2s",
		"task p95: 2s → 5s",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("drift output missing %q\n---\n%s", want, out)
		}
	}
}

// TestTelemetryDrift_NoRegression proves a stable/improving run does not
// emit the regression callouts.
func TestTelemetryDrift_NoRegression(t *testing.T) {
	prev := telemetry.Snapshot{RunID: "a", Summary: telemetry.MetricsSummary{SuccessRate: 0.8, AvgDuration: 3 * time.Second}}
	cur := telemetry.Snapshot{RunID: "b", Summary: telemetry.MetricsSummary{SuccessRate: 0.9, AvgDuration: 2 * time.Second}}
	out := telemetryDrift(prev, cur)
	if strings.Contains(out, "dropped") || strings.Contains(out, "rose") {
		t.Errorf("stable run should not warn; got:\n%s", out)
	}
	if !strings.Contains(out, "success rate: 80% → 90% (+10 pts)") {
		t.Errorf("expected +10 pts success delta; got:\n%s", out)
	}
}

// TestStatsTelemetryView_ListsAndDiffs is the O6 activation proof end to
// end: two persisted snapshots are listed newest-first and the drift
// block appears.
func TestStatsTelemetryView_ListsAndDiffs(t *testing.T) {
	repo := t.TempDir()
	dir := r1dir.JoinFor(repo, "telemetry")

	older := telemetry.Snapshot{
		RunID:      "20260701T000000.000000000Z",
		CapturedAt: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		Summary:    telemetry.MetricsSummary{TotalEvents: 4, SuccessRate: 1.0, AvgDuration: 2 * time.Second, TotalCost: 0.4},
	}
	newer := telemetry.Snapshot{
		RunID:      "20260702T000000.000000000Z",
		CapturedAt: time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC),
		Summary:    telemetry.MetricsSummary{TotalEvents: 4, SuccessRate: 0.5, AvgDuration: 5 * time.Second, TotalCost: 0.9},
	}
	if _, err := telemetry.WriteSnapshot(dir, older); err != nil {
		t.Fatalf("write older: %v", err)
	}
	if _, err := telemetry.WriteSnapshot(dir, newer); err != nil {
		t.Fatalf("write newer: %v", err)
	}

	out := captureTelStdout(t, func() {
		statsTelemetryView([]string{"--repo", repo})
	})

	if !strings.Contains(out, "2 snapshot(s)") {
		t.Errorf("view should report 2 snapshots; got:\n%s", out)
	}
	// Newest listed first.
	iNew := strings.Index(out, newer.RunID)
	iOld := strings.Index(out, older.RunID)
	if iNew < 0 || iOld < 0 || iNew > iOld {
		t.Errorf("expected newest run listed before older; new@%d old@%d\n%s", iNew, iOld, out)
	}
	// Drift block present with the regression callout.
	if !strings.Contains(out, "drift "+older.RunID+" → "+newer.RunID) {
		t.Errorf("view missing drift block; got:\n%s", out)
	}
	if !strings.Contains(out, "success rate dropped 50 points") {
		t.Errorf("view missing regression callout; got:\n%s", out)
	}
}

// TestStatsTelemetryView_EmptyIsGraceful proves the view reports "no
// snapshots" rather than erroring when the dir is absent.
func TestStatsTelemetryView_EmptyIsGraceful(t *testing.T) {
	repo := t.TempDir()
	out := captureTelStdout(t, func() {
		statsTelemetryView([]string{"--repo", repo})
	})
	if !strings.Contains(out, "no telemetry snapshots yet") {
		t.Errorf("empty view should be graceful; got:\n%s", out)
	}
}
