// Command variance-regression is the B3 CI gate: given two
// already-produced bench result JSON files — one from a frozen
// baseline scaffold, one from current HEAD — compute the aggregate
// success-rate delta in points and fail if it exceeds the tolerance.
//
// Design: the runner accepts RESULTS as input, not RUNS. Running the
// corpus twice (once per scaffold) is inherently slow + expensive;
// CI parallelizes those two runs independently and feeds the output
// files here. This tool is pure delta-math + report generation.
//
// USAGE
//
//	variance-regression \
//	    --baseline bench/baselines/stoke-scaffold-v1.0.json \
//	    --current  /tmp/bench-head-results.json \
//	    --tolerance 3.0 \
//	    --report docs/bench-results/variance-2026-Q2.md
//
// Exit codes:
//
//	0  within tolerance
//	1  regression exceeds tolerance
//	2  input error (file missing, malformed JSON, baseline/current
//	   corpora disagree)
//
// Result JSON shape (produced by bench/cmd/bench with --variance-out):
//
//	{
//	    "label": "stoke-scaffold-v1.0" | "HEAD:<commit>",
//	    "tasks": [ { "task_id": "t01", "success_rate": 0.85 }, ... ]
//	}
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/ericmacdougall/stoke/bench/metrics"
)

type resultsFile struct {
	Label string                 `json:"label"`
	Tasks []metrics.TaskOutcome  `json:"tasks"`
}

func main() {
	baselinePath := flag.String("baseline", "", "path to baseline-scaffold results JSON")
	currentPath := flag.String("current", "", "path to current-scaffold results JSON (typically HEAD)")
	tolerance := flag.Float64("tolerance", metrics.DefaultVarianceTolerance, "max aggregate delta in points (|current - baseline| * 100)")
	reportPath := flag.String("report", "", "optional: path to write Markdown quarterly report")
	flag.Parse()

	if *baselinePath == "" || *currentPath == "" {
		fmt.Fprintln(os.Stderr, "variance-regression: both --baseline and --current are required")
		os.Exit(2)
	}

	baseline, err := loadResults(*baselinePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "variance-regression: load baseline: %v\n", err)
		os.Exit(2)
	}
	current, err := loadResults(*currentPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "variance-regression: load current: %v\n", err)
		os.Exit(2)
	}

	if err := requireSameCorpus(baseline.Tasks, current.Tasks); err != nil {
		fmt.Fprintf(os.Stderr, "variance-regression: %v\n", err)
		os.Exit(2)
	}

	run := metrics.NewVarianceRun(baseline.Label, current.Label)
	bMap := map[string]float64{}
	for _, t := range baseline.Tasks {
		bMap[t.TaskID] = t.SuccessRate
	}
	for _, c := range current.Tasks {
		run.Record(c.TaskID, bMap[c.TaskID], c.SuccessRate)
	}

	fmt.Println(run.Summary())

	if *reportPath != "" {
		if err := run.WriteReport(*reportPath); err != nil {
			fmt.Fprintf(os.Stderr, "variance-regression: write report: %v\n", err)
			os.Exit(2)
		}
		fmt.Printf("report written: %s\n", *reportPath)
	}

	if !run.WithinTolerance(*tolerance) {
		fmt.Fprintf(os.Stderr, "\nFAIL: aggregate delta %.2f points exceeds tolerance ±%.2f\n",
			run.AggregateDelta(), *tolerance)
		for _, d := range run.PerTaskDeltas() {
			fmt.Fprintf(os.Stderr, "  %s %+.2f\n", d.TaskID, d.Delta)
		}
		os.Exit(1)
	}
	fmt.Printf("PASS: delta %+.2f points within tolerance ±%.2f\n", run.AggregateDelta(), *tolerance)
}

func loadResults(path string) (*resultsFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var rf resultsFile
	if err := json.Unmarshal(data, &rf); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if rf.Label == "" {
		return nil, fmt.Errorf("%s: missing 'label' field", path)
	}
	if len(rf.Tasks) == 0 {
		return nil, fmt.Errorf("%s: empty 'tasks' array", path)
	}
	return &rf, nil
}

// requireSameCorpus rejects runs where baseline + current don't cover
// the same task set. Regression comparison across different corpora
// is mathematically meaningful but operationally deceptive — CI
// should flag this loudly rather than silently average different
// populations.
func requireSameCorpus(b, c []metrics.TaskOutcome) error {
	bSet := map[string]bool{}
	bDup := []string{}
	for _, t := range b {
		if bSet[t.TaskID] {
			bDup = append(bDup, t.TaskID)
		}
		bSet[t.TaskID] = true
	}
	if len(bDup) > 0 {
		return fmt.Errorf("baseline has duplicate task_id(s): %v", bDup)
	}
	cSet := map[string]bool{}
	cDup := []string{}
	for _, t := range c {
		if cSet[t.TaskID] {
			cDup = append(cDup, t.TaskID)
		}
		cSet[t.TaskID] = true
	}
	if len(cDup) > 0 {
		return fmt.Errorf("current has duplicate task_id(s): %v", cDup)
	}
	missing := []string{}
	for _, t := range c {
		if !bSet[t.TaskID] {
			missing = append(missing, t.TaskID)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("current has %d tasks not in baseline: %v", len(missing), missing)
	}
	// Check the reverse direction too — baseline tasks missing from
	// current means the regression would silently drop them.
	extra := []string{}
	for _, t := range b {
		if !cSet[t.TaskID] {
			extra = append(extra, t.TaskID)
		}
	}
	if len(extra) > 0 {
		return fmt.Errorf("baseline has %d tasks not in current: %v", len(extra), extra)
	}
	if len(b) != len(c) {
		return fmt.Errorf("corpus size mismatch: baseline %d tasks, current %d tasks", len(b), len(c))
	}
	return nil
}
