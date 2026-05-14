package bench

import (
	"strings"
	"testing"
)

func TestBuildLeaderboard_GroupsByAgent(t *testing.T) {
	results := []RunResult{
		{AgentID: "r1", MissionID: "m1", CompletionAttempted: true, CompletionTruthful: true},
		{AgentID: "r1", MissionID: "m2", CompletionAttempted: true, CompletionTruthful: true},
		{AgentID: "r1", MissionID: "m3", CompletionAttempted: true, CompletionTruthful: false},
		{AgentID: "cline", MissionID: "m1", CompletionAttempted: true, CompletionTruthful: false},
		{AgentID: "cline", MissionID: "m2", CompletionAttempted: false, CompletionSilentlyFailed: true},
	}
	lb := BuildLeaderboard(results)
	if len(lb.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(lb.Rows))
	}
	// r1 should rank first (2/3 = 66.7% > cline's 0/1 = 0%).
	if lb.Rows[0].AgentID != "r1" {
		t.Errorf("top row = %q, want r1", lb.Rows[0].AgentID)
	}
	if lb.Rows[0].CompletionTruthfulCount != 2 {
		t.Errorf("r1 truthful count = %d, want 2", lb.Rows[0].CompletionTruthfulCount)
	}
	if lb.Rows[0].CompletionAttemptCount != 3 {
		t.Errorf("r1 attempt count = %d, want 3", lb.Rows[0].CompletionAttemptCount)
	}
	if lb.Rows[1].AgentID != "cline" {
		t.Errorf("second row = %q, want cline", lb.Rows[1].AgentID)
	}
	if lb.Rows[1].SilentFailureCount != 1 {
		t.Errorf("cline silent fail count = %d, want 1", lb.Rows[1].SilentFailureCount)
	}
}

func TestBuildLeaderboardForAgent_PopulatesCI(t *testing.T) {
	results := []RunResult{
		{AgentID: "r1", MissionID: "m1", CompletionAttempted: true, CompletionTruthful: true},
		{AgentID: "r1", MissionID: "m2", CompletionAttempted: true, CompletionTruthful: true},
		{AgentID: "r1", MissionID: "m3", CompletionAttempted: true, CompletionTruthful: false},
	}
	row := BuildLeaderboardForAgent("r1", results)
	if row.TruthfulCompletionRate < 0.66 || row.TruthfulCompletionRate > 0.67 {
		t.Errorf("rate = %f, want ~0.667", row.TruthfulCompletionRate)
	}
	if row.TruthfulCIRangeLow >= row.TruthfulCompletionRate {
		t.Errorf("CI low %f should be < rate %f", row.TruthfulCIRangeLow, row.TruthfulCompletionRate)
	}
	if row.TruthfulCIRangeHigh <= row.TruthfulCompletionRate {
		t.Errorf("CI high %f should be > rate %f", row.TruthfulCIRangeHigh, row.TruthfulCompletionRate)
	}
}

func TestLeaderboard_RenderMarkdown(t *testing.T) {
	lb := BuildLeaderboard([]RunResult{
		{AgentID: "r1-antitrunc", CompletionAttempted: true, CompletionTruthful: true},
		{AgentID: "r1-antitrunc", CompletionAttempted: true, CompletionTruthful: true},
		{AgentID: "aider", CompletionAttempted: true, CompletionTruthful: false},
	})
	out := lb.RenderMarkdown()
	if !strings.Contains(out, "| Agent | Missions |") {
		t.Errorf("header missing: %q", out)
	}
	if !strings.Contains(out, "r1-antitrunc") {
		t.Errorf("r1-antitrunc row missing: %q", out)
	}
	if !strings.Contains(out, "100.0%") {
		t.Errorf("100%% row missing: %q", out)
	}
	if !strings.Contains(out, "aider") {
		t.Errorf("aider row missing: %q", out)
	}
}

func TestBuildPerMissionTable_SortedByAgentThenMission(t *testing.T) {
	results := []RunResult{
		{AgentID: "r1", MissionID: "z-mission", CompletionAttempted: true, CompletionTruthful: true, PlanItemsCompleted: 2, PlanItemsTotal: 2, DeliveryRatioPercent: 88},
		{AgentID: "aider", MissionID: "a-mission", CompletionAttempted: true, CompletionTruthful: false, PlanItemsCompleted: 0, PlanItemsTotal: 3, DeliveryRatioPercent: 12},
		{AgentID: "r1", MissionID: "a-mission", CompletionAttempted: false, CompletionSilentlyFailed: true, PlanItemsTotal: 2},
	}
	tbl := BuildPerMissionTable(results)
	if len(tbl.Rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(tbl.Rows))
	}
	// Expected order: aider/a-mission, r1/a-mission, r1/z-mission.
	wants := []struct{ agent, mission string }{
		{"aider", "a-mission"},
		{"r1", "a-mission"},
		{"r1", "z-mission"},
	}
	for i, w := range wants {
		if tbl.Rows[i].AgentID != w.agent || tbl.Rows[i].MissionID != w.mission {
			t.Errorf("row %d = %s/%s, want %s/%s", i, tbl.Rows[i].AgentID, tbl.Rows[i].MissionID, w.agent, w.mission)
		}
	}
}

func TestPerMissionTable_RenderMarkdown_VerdictGlyphs(t *testing.T) {
	tbl := BuildPerMissionTable([]RunResult{
		{AgentID: "r1", MissionID: "ok-m", CompletionAttempted: true, CompletionTruthful: true, PlanItemsCompleted: 1, PlanItemsTotal: 1, DeliveryRatioPercent: 95},
		{AgentID: "r1", MissionID: "bad-m", CompletionAttempted: true, CompletionTruthful: false, PlanItemsCompleted: 0, PlanItemsTotal: 2, DeliveryRatioPercent: 10},
		{AgentID: "r1", MissionID: "silent-m", CompletionAttempted: false, CompletionSilentlyFailed: true, PlanItemsTotal: 2},
	})
	out := tbl.RenderMarkdown()
	// Each row's verdict column must appear.
	if !strings.Contains(out, "| OK |") {
		t.Errorf("OK verdict glyph missing: %q", out)
	}
	if !strings.Contains(out, "| FAIL |") {
		t.Errorf("FAIL verdict glyph missing: %q", out)
	}
	// Silent failures render with em-dash. The hyphen-vs-em-dash
	// disambiguation matters — assert via substring of the row.
	if !strings.Contains(out, "silent-m") {
		t.Errorf("silent-m row missing: %q", out)
	}
}

func TestBuildLeaderboard_EmptyResults(t *testing.T) {
	lb := BuildLeaderboard(nil)
	if len(lb.Rows) != 0 {
		t.Errorf("empty input should produce empty rows; got %d", len(lb.Rows))
	}
	if out := lb.RenderMarkdown(); !strings.Contains(out, "| Agent |") {
		t.Errorf("empty leaderboard must still render header; got %q", out)
	}
}

func TestBuildLeaderboard_ZeroAttemptsHandled(t *testing.T) {
	// Every attempt was a silent failure; rate must be 0, not NaN.
	row := BuildLeaderboardForAgent("aider", []RunResult{
		{CompletionAttempted: false, CompletionSilentlyFailed: true},
		{CompletionAttempted: false, CompletionSilentlyFailed: true},
	})
	if row.TruthfulCompletionRate != 0 {
		t.Errorf("zero-attempt rate = %f, want 0", row.TruthfulCompletionRate)
	}
	if row.TruthfulCIRangeLow < 0 || row.TruthfulCIRangeHigh > 1 {
		t.Errorf("CI [%f, %f] out of [0,1]", row.TruthfulCIRangeLow, row.TruthfulCIRangeHigh)
	}
}
