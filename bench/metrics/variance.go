// Package metrics — variance.go
//
// B3 — Continuous Scaffold Variance Regression.
//
// The "scaffold accounts for 22 points of success-rate" claim in the
// thesis is a citation until Stoke measures it on Stoke's OWN corpus.
// This file is the measurement contract: the data structures the
// regression harness writes after each baseline-vs-current run, and
// the gate function that decides whether a delta is acceptable.
//
// USAGE
//
//	res := metrics.NewVarianceRun("stoke-scaffold-v1.0", "HEAD")
//	for _, task := range corpus {
//	    res.Record(task.ID, baselineSuccess, currentSuccess)
//	}
//	if !res.WithinTolerance(metrics.DefaultVarianceTolerance) {
//	    log.Fatalf("variance regression: %s", res.Summary())
//	}
//	res.WriteReport("docs/bench-results/variance-2026-Q2.md")

package metrics

import (
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"
)

// DefaultVarianceTolerance is the per-merge CI gate threshold.
// Tightens over time as the harness stabilizes — initial 3-point
// envelope matches the addendum's recommendation.
const DefaultVarianceTolerance = 3.0

// TaskOutcome records one task's success rate under a single
// scaffold + model combination. SuccessRate is in [0, 1].
type TaskOutcome struct {
	TaskID      string  `json:"task_id"`
	SuccessRate float64 `json:"success_rate"`
}

// VarianceRun captures a single regression measurement: how the
// current HEAD's scaffold compares to a frozen baseline scaffold on
// the same task corpus.
type VarianceRun struct {
	BaselineLabel string        `json:"baseline_label"` // e.g. "stoke-scaffold-v1.0"
	CurrentLabel  string        `json:"current_label"`  // e.g. "HEAD" or commit SHA
	Timestamp     time.Time     `json:"timestamp"`
	Baseline      []TaskOutcome `json:"baseline"`
	Current       []TaskOutcome `json:"current"`
}

// NewVarianceRun constructs a regression-measurement record.
func NewVarianceRun(baselineLabel, currentLabel string) *VarianceRun {
	return &VarianceRun{
		BaselineLabel: baselineLabel,
		CurrentLabel:  currentLabel,
		Timestamp:     time.Now().UTC(),
	}
}

// Record adds one task's baseline + current outcomes.
func (r *VarianceRun) Record(taskID string, baselineSuccess, currentSuccess float64) {
	r.Baseline = append(r.Baseline, TaskOutcome{TaskID: taskID, SuccessRate: baselineSuccess})
	r.Current = append(r.Current, TaskOutcome{TaskID: taskID, SuccessRate: currentSuccess})
}

// AggregateDelta returns (current_mean - baseline_mean) * 100 — the
// per-merge CI gate compares the absolute value of this against
// DefaultVarianceTolerance.
func (r *VarianceRun) AggregateDelta() float64 {
	if len(r.Baseline) == 0 || len(r.Current) == 0 {
		return 0
	}
	var bSum, cSum float64
	for _, o := range r.Baseline {
		bSum += o.SuccessRate
	}
	for _, o := range r.Current {
		cSum += o.SuccessRate
	}
	bMean := bSum / float64(len(r.Baseline))
	cMean := cSum / float64(len(r.Current))
	return (cMean - bMean) * 100
}

// WithinTolerance reports whether |AggregateDelta()| ≤ tolerance.
// CI gates use this as the merge precondition.
func (r *VarianceRun) WithinTolerance(tolerance float64) bool {
	return math.Abs(r.AggregateDelta()) <= tolerance
}

// PerTaskDeltas returns each task's (current - baseline) success
// rate in points, sorted by largest-magnitude regression first.
// Useful for the quarterly report — operators see exactly which
// tasks regressed and which improved.
func (r *VarianceRun) PerTaskDeltas() []TaskDelta {
	bMap := map[string]float64{}
	for _, o := range r.Baseline {
		bMap[o.TaskID] = o.SuccessRate
	}
	out := make([]TaskDelta, 0, len(r.Current))
	for _, c := range r.Current {
		bSucc, ok := bMap[c.TaskID]
		if !ok {
			continue
		}
		out = append(out, TaskDelta{
			TaskID: c.TaskID,
			Delta:  (c.SuccessRate - bSucc) * 100,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return math.Abs(out[i].Delta) > math.Abs(out[j].Delta)
	})
	return out
}

// TaskDelta is one task's regression magnitude in points.
type TaskDelta struct {
	TaskID string  `json:"task_id"`
	Delta  float64 `json:"delta_points"`
}

// Summary renders a short operator-facing line.
func (r *VarianceRun) Summary() string {
	d := r.AggregateDelta()
	return fmt.Sprintf("%s vs %s: %+.2f points across %d tasks",
		r.CurrentLabel, r.BaselineLabel, d, len(r.Current))
}

// WriteReport renders the Markdown quarterly report.
func (r *VarianceRun) WriteReport(path string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# Stoke Scaffold Variance — %s\n\n", r.Timestamp.Format("2006-01-02"))
	fmt.Fprintf(&b, "Baseline: `%s`\nCurrent:  `%s`\nTasks:    %d\n\n",
		r.BaselineLabel, r.CurrentLabel, len(r.Current))
	fmt.Fprintf(&b, "## Aggregate\n\n")
	fmt.Fprintf(&b, "**%+.2f points** across the corpus.\n\n", r.AggregateDelta())
	fmt.Fprintf(&b, "## Per-task deltas (largest magnitude first)\n\n")
	fmt.Fprintf(&b, "| Task | Delta (points) |\n|---|---|\n")
	for _, d := range r.PerTaskDeltas() {
		fmt.Fprintf(&b, "| %s | %+.2f |\n", d.TaskID, d.Delta)
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// Wiring note (B3 follow-up): the VarianceRun contract is in place.
// The CI gate runs `bench/cmd/variance-regression` against a frozen
// baseline scaffold + the corpus from bench/harnesses on every
// merge, calls WithinTolerance(DefaultVarianceTolerance), and fails
// the merge on regression. The baseline scaffold itself is captured
// as a pinned git ref + frozen prompt set; pinning lives in
// bench/baselines/<label>.json so the regression set never silently
// drifts.
