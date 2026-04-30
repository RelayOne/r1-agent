package selftune

import (
	"testing"
	"time"
)

func TestRecommend(t *testing.T) {
	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	baseline := Trial{
		ID: "baseline",
		Metrics: map[string]float64{
			"ac_pass_rate":     0.80,
			"false_merge_rate": 0.10,
		},
	}
	candidates := []Trial{
		{
			ID: "bad",
			Metrics: map[string]float64{
				"ac_pass_rate":     0.84,
				"false_merge_rate": 0.09,
			},
		},
		{
			ID: "good",
			Metrics: map[string]float64{
				"ac_pass_rate":     0.85,
				"false_merge_rate": 0.10,
			},
		},
	}
	rec, err := Recommend(baseline, candidates, now)
	if err != nil {
		t.Fatalf("Recommend: %v", err)
	}
	if rec.Selected.ID != "good" {
		t.Fatalf("selected %q, want good", rec.Selected.ID)
	}
}
