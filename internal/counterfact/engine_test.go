package counterfact

import (
	"context"
	"testing"

	"github.com/RelayOne/r1/internal/specexec"
)

func TestKnobDeterministic(t *testing.T) {
	engine := Engine{
		Runner: func(_ context.Context, strategy specexec.Strategy, config map[string]interface{}) (OutcomeSummary, error) {
			return OutcomeSummary{
				Status:       "success",
				Score:        float64(len(strategy.ID)),
				Gates:        []string{config["verify"].(map[string]interface{})["tier_max"].(string)},
				ChangedFiles: []string{"internal/auth/login.go"},
			}, nil
		},
	}
	mission := MissionSnapshot{
		MissionID: "MISSION-1",
		Config: map[string]interface{}{
			"reviewer": map[string]interface{}{"model": "codex"},
			"verify":   map[string]interface{}{"tier_max": "T8"},
		},
	}
	knobs := []Knob{
		{Path: "verify.tier_max", NewValue: "T6"},
		{Path: "reviewer.model", NewValue: "claude"},
	}
	first, err := engine.Run(context.Background(), mission, knobs)
	if err != nil {
		t.Fatalf("Run first: %v", err)
	}
	second, err := engine.Run(context.Background(), mission, []Knob{
		{Path: "reviewer.model", NewValue: "claude"},
		{Path: "verify.tier_max", NewValue: "T6"},
	})
	if err != nil {
		t.Fatalf("Run second: %v", err)
	}
	if first.RunID != second.RunID {
		t.Fatalf("RunID mismatch: %s != %s", first.RunID, second.RunID)
	}
	if first.Outcome.Gates[0] != "T6" {
		t.Fatalf("expected knob-applied tier T6, got %v", first.Outcome.Gates)
	}
}

func TestDiff(t *testing.T) {
	report := Diff(
		OutcomeSummary{
			Status:       "success",
			Score:        0.7,
			Dissents:     []string{"missing concurrency coverage"},
			Gates:        []string{"T8"},
			ChangedFiles: []string{"a.go"},
		},
		OutcomeSummary{
			Status:       "success",
			Score:        0.9,
			Dissents:     []string{"missing nil check"},
			Gates:        []string{"T6"},
			ChangedFiles: []string{"a.go", "b.go"},
		},
	)
	if report.ScoreDelta <= 0 {
		t.Fatalf("expected positive score delta, got %f", report.ScoreDelta)
	}
	if len(report.NewDissents) != 1 || report.NewDissents[0] != "missing nil check" {
		t.Fatalf("unexpected new dissents: %#v", report.NewDissents)
	}
	if len(report.NewChangedFiles) != 1 || report.NewChangedFiles[0] != "b.go" {
		t.Fatalf("unexpected new changed files: %#v", report.NewChangedFiles)
	}
}
