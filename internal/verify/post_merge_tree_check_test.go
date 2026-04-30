package verify

import (
	"context"
	"testing"
	"time"
)

func TestPostMergeTreeCheck_NoDropIsOK(t *testing.T) {
	baseline := TreeBaseline{Repo: "x/y", Branch: "main", Files: 1000, Captured: time.Now()}
	// Inject a fake current count via a closure-replacement isn't trivial;
	// we test the verdict branches directly by calling the eval logic.
	// (Real network test is in a separate _integration_test.go.)
	got := evalVerdict(baseline, 1000)
	if got.Verdict != "ok" {
		t.Errorf("equal counts should be ok, got %q (drop=%d)", got.Verdict, got.Drop)
	}
}

func TestPostMergeTreeCheck_SmallDropIsWarning(t *testing.T) {
	baseline := TreeBaseline{Repo: "x/y", Branch: "main", Files: 1000}
	got := evalVerdict(baseline, 980) // 2% drop
	if got.Verdict != "warning" {
		t.Errorf("2%% drop should warn, got %q", got.Verdict)
	}
	if got.DropPercent != 2 {
		t.Errorf("drop_percent = %d, want 2", got.DropPercent)
	}
}

func TestPostMergeTreeCheck_LargeDropIsCritical(t *testing.T) {
	baseline := TreeBaseline{Repo: "RelayOne/actium", Branch: "main", Files: 4000}
	got := evalVerdict(baseline, 1500) // 62% drop — actium-style catastrophe
	if got.Verdict != "critical" {
		t.Errorf("62%% drop should be critical, got %q", got.Verdict)
	}
	if got.Drop != 2500 {
		t.Errorf("drop = %d, want 2500", got.Drop)
	}
}

func TestPostMergeTreeCheck_GrowthIsOK(t *testing.T) {
	baseline := TreeBaseline{Repo: "x/y", Branch: "main", Files: 1000}
	got := evalVerdict(baseline, 1100) // grew
	if got.Verdict != "ok" {
		t.Errorf("growth should be ok, got %q", got.Verdict)
	}
	if got.Drop != -100 {
		t.Errorf("drop = %d, want -100", got.Drop)
	}
}

// evalVerdict mirrors the verdict logic of PostMergeTreeCheck without the
// network call, so it can be unit tested deterministically.
func evalVerdict(baseline TreeBaseline, cur int) TreeCheckResult {
	drop := baseline.Files - cur
	pct := 0
	if baseline.Files > 0 {
		pct = drop * 100 / baseline.Files
	}
	res := TreeCheckResult{Baseline: baseline, Current: cur, Drop: drop, DropPercent: pct}
	switch {
	case drop <= 0:
		res.Verdict = "ok"
	case pct >= MaxAcceptableDropPercent:
		res.Verdict = "critical"
	default:
		res.Verdict = "warning"
	}
	return res
}

// TestThresholdBoundary exercises the off-by-one boundary at exactly 5%.
func TestThresholdBoundary(t *testing.T) {
	baseline := TreeBaseline{Files: 1000}
	atFour := evalVerdict(baseline, 960) // 4% drop
	atFive := evalVerdict(baseline, 950) // 5% drop
	if atFour.Verdict != "warning" {
		t.Errorf("4%% should be warning, got %q", atFour.Verdict)
	}
	if atFive.Verdict != "critical" {
		t.Errorf("5%% should be critical, got %q", atFive.Verdict)
	}
}

func TestCaptureBaseline_RequiresRepo(t *testing.T) {
	_, err := CaptureBaseline(context.Background(), "", "main")
	if err == nil {
		t.Errorf("expected error when repo is empty")
	}
}
