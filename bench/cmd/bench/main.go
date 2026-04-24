// Command bench runs the Stoke benchmark framework.
//
// Usage:
//
//	bench run --corpus <dir> --harnesses <list> --reps <n>
//	bench report --format <html|csv|markdown> --output <path>
//	bench analyze --report <dir>
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ericmacdougall/stoke/bench/cost"
	"github.com/ericmacdougall/stoke/bench/harnesses"
	"github.com/ericmacdougall/stoke/bench/judge"
	"github.com/ericmacdougall/stoke/bench/reports"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	cmd := os.Args[1]
	switch cmd {
	case "run":
		cmdRun(os.Args[2:])
	case "report":
		cmdReport(os.Args[2:])
	case "analyze":
		cmdAnalyze(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `Usage: bench <command> [options]

Commands:
  run       Run benchmark tasks across harnesses
  report    Generate reports from benchmark results
  analyze   Analyze results for reproducibility and variance`)
}

// BenchResult is a single (task × harness × rep) result.
type BenchResult struct {
	TaskID      string              `json:"task_id"`
	HarnessName string              `json:"harness_name"`
	Rep         int                 `json:"rep"`
	DurationMs  int64               `json:"duration_ms"`
	RunResult   harnesses.RunResult `json:"run_result"`
	Verdict     judge.Verdict       `json:"verdict"`
}

func cmdRun(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	corpusDir := fs.String("corpus", "corpus/", "Task corpus directory")
	harnessNames := fs.String("harnesses", "stoke", "Comma-separated harness names")
	reps := fs.Int("reps", 1, "Number of repetitions per task")
	maxParallel := fs.Int("max-parallel", 5, "Maximum parallel task runs")
	costCapFlag := fs.Float64("cost-cap", 3.0, "Per-task cost cap in USD")
	outputDir := fs.String("output", "reports/", "Output directory for results")
	varianceOut := fs.String("variance-out", "", "Optional: write a slim JSON per harness at this path prefix (shape consumed by bench/cmd/variance-regression)")
	varianceLabel := fs.String("variance-label", "", "Optional: label to record in the variance-out JSON (e.g. git SHA, scaffold version). Defaults to 'HEAD' when empty.")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	ctx := context.Background()

	// Load tasks
	tasks, err := loadTasks(*corpusDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading tasks: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Loaded %d tasks from %s\n", len(tasks), *corpusDir)

	// Create harnesses
	hs := createHarnesses(strings.Split(*harnessNames, ","))
	fmt.Printf("Harnesses: %v\n", *harnessNames)

	// Create judge stack
	det := &judge.DeterministicJudge{}
	honestyJudge := &judge.HonestyJudge{Deterministic: det}

	// Create cost tracker
	tracker := cost.NewRunTracker()

	os.MkdirAll(*outputDir, 0o755)
	sem := make(chan struct{}, *maxParallel)
	var mu sync.Mutex
	var allResults []BenchResult

	for rep := 0; rep < *reps; rep++ {
		for _, task := range tasks {
			for _, h := range hs {
				sem <- struct{}{}
				go func(t *judge.Task, h harnesses.Harness, rep int) {
					defer func() { <-sem }()

					fmt.Printf("[rep=%d] %s × %s starting\n", rep, h.Name(), t.ID)
					start := time.Now()

					// Run harness
					runResult := h.Run(ctx, filepath.Join(*corpusDir, t.ID))

					// Track cost
					tracker.Record(cost.CostEntry{
						TaskID:   t.ID,
						Harness:  h.Name(),
						Category: t.Category,
						CostUSD:  runResult.CostUSD,
					})

					// Judge
					workspace := filepath.Join(*corpusDir, t.ID, "initial")
					verdict := honestyJudge.Judge(ctx, t, workspace)

					// Check cost cap
					if runResult.CostUSD > *costCapFlag {
						verdict.Passed = false
						verdict.Failures = append(verdict.Failures,
							fmt.Sprintf("cost exceeded cap: $%.2f > $%.2f", runResult.CostUSD, *costCapFlag))
					}

					result := BenchResult{
						TaskID:      t.ID,
						HarnessName: h.Name(),
						Rep:         rep,
						DurationMs:  time.Since(start).Milliseconds(),
						RunResult:   runResult,
						Verdict:     verdict,
					}

					mu.Lock()
					allResults = append(allResults, result)
					mu.Unlock()

					status := "PASS"
					if !verdict.Passed {
						status = "FAIL"
					}
					fmt.Printf("[rep=%d] %s × %s %s (%.2fs, $%.4f)\n",
						rep, h.Name(), t.ID, status,
						float64(result.DurationMs)/1000, runResult.CostUSD)
				}(&task, h, rep)
			}
		}
	}

	// Wait for all tasks
	for i := 0; i < cap(sem); i++ {
		sem <- struct{}{}
	}

	// Save raw results
	resultsPath := filepath.Join(*outputDir, "results.json")
	data, _ := json.MarshalIndent(allResults, "", "  ")
	os.WriteFile(resultsPath, data, 0o644) // #nosec G306 -- benchmark output artefact; user-readable.
	fmt.Printf("\nResults saved to %s (%d runs)\n", resultsPath, len(allResults))

	// Variance regression export (B3). One JSON per harness in the
	// slim shape bench/cmd/variance-regression consumes. Success rate
	// is the mean of Verdict.Passed across reps for each task. A
	// write failure here is fatal — callers that request the
	// artifact need it, and silently exiting 0 without it breaks CI
	// pipelines that don't notice the gap until the later
	// variance-regression step fails on a missing file (codex P3).
	if *varianceOut != "" {
		label := *varianceLabel
		if label == "" {
			label = "HEAD"
		}
		if err := writeVarianceReports(allResults, *varianceOut, label); err != nil {
			fmt.Fprintf(os.Stderr, "variance-out: %v\n", err)
			os.Exit(1)
		}
	}

	// Print summary
	printSummary(allResults, tracker)
}

// writeVarianceReports emits one JSON per harness in the shape the
// variance-regression CLI expects: {label, tasks: [{task_id,
// success_rate}]}. Path prefix + "-" + harness + ".json". Success
// rate is mean Verdict.Passed (0 or 1) across reps for each task.
func writeVarianceReports(results []BenchResult, pathPrefix, label string) error {
	type key struct{ harness, task string }
	type agg struct{ passed, total int }
	byHarness := map[string]map[string]*agg{}
	for _, r := range results {
		if byHarness[r.HarnessName] == nil {
			byHarness[r.HarnessName] = map[string]*agg{}
		}
		a := byHarness[r.HarnessName][r.TaskID]
		if a == nil {
			a = &agg{}
			byHarness[r.HarnessName][r.TaskID] = a
		}
		a.total++
		if r.Verdict.Passed {
			a.passed++
		}
	}
	for harness, taskAgg := range byHarness {
		type outcome struct {
			TaskID      string  `json:"task_id"`
			SuccessRate float64 `json:"success_rate"`
		}
		type out struct {
			Label string    `json:"label"`
			Tasks []outcome `json:"tasks"`
		}
		doc := out{Label: fmt.Sprintf("%s:%s", label, harness)}
		for tid, a := range taskAgg {
			rate := 0.0
			if a.total > 0 {
				rate = float64(a.passed) / float64(a.total)
			}
			doc.Tasks = append(doc.Tasks, outcome{TaskID: tid, SuccessRate: rate})
		}
		data, _ := json.MarshalIndent(doc, "", "  ")
		path := fmt.Sprintf("%s-%s.json", pathPrefix, harness)
		if err := os.WriteFile(path, data, 0o644); err != nil { // #nosec G306 -- benchmark output artefact; user-readable.
			return fmt.Errorf("write %s: %w", path, err)
		}
		fmt.Printf("variance-out saved: %s (%d tasks)\n", path, len(doc.Tasks))
	}
	return nil
}

func loadTasks(dir string) ([]judge.Task, error) {
	var tasks []judge.Task
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		taskFile := filepath.Join(dir, entry.Name(), "task.yaml")
		if _, err := os.Stat(taskFile); err != nil {
			continue
		}
		task := judge.Task{
			ID:       entry.Name(),
			Category: inferCategory(entry.Name()),
		}
		tasks = append(tasks, task)
	}
	return tasks, nil
}

func inferCategory(taskID string) string {
	parts := strings.SplitN(taskID, "-", 2)
	if len(parts) > 0 {
		return parts[0]
	}
	return "unknown"
}

func createHarnesses(names []string) []harnesses.Harness {
	var hs []harnesses.Harness
	for _, name := range names {
		name = strings.TrimSpace(name)
		switch name {
		case "stoke":
			hs = append(hs, &harnesses.Stoke{Model: "claude-sonnet-4-6"})
		case "claude-code":
			hs = append(hs, &harnesses.ClaudeCode{Model: "claude-sonnet-4-6"})
		case "codex":
			hs = append(hs, &harnesses.Codex{Model: "o3"})
		case "aider":
			hs = append(hs, &harnesses.Aider{Model: "claude-sonnet-4-6"})
		}
	}
	return hs
}

func printSummary(results []BenchResult, tracker *cost.RunTracker) {
	byHarness := make(map[string][]BenchResult)
	for _, r := range results {
		byHarness[r.HarnessName] = append(byHarness[r.HarnessName], r)
	}

	fmt.Println("\n=== SUMMARY ===")
	for name, hrs := range byHarness {
		passed := 0
		var totalHonesty float64
		for _, r := range hrs {
			if r.Verdict.Passed {
				passed++
			}
			totalHonesty += r.Verdict.HonestyScore
		}
		avgHonesty := totalHonesty / float64(len(hrs))
		perHarness := tracker.PerHarness()
		fmt.Printf("%-15s: %d/%d passed (%.0f%%), avg honesty=%.2f, total cost=$%.2f\n",
			name, passed, len(hrs), float64(passed)/float64(len(hrs))*100,
			avgHonesty, perHarness[name])
	}
}

func cmdReport(args []string) {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	format := fs.String("format", "markdown", "Report format: html, csv, markdown")
	input := fs.String("input", "reports/results.json", "Results JSON file")
	output := fs.String("output", "", "Output file path")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	data, err := os.ReadFile(*input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading results: %v\n", err)
		os.Exit(1)
	}

	var results []BenchResult
	if err := json.Unmarshal(data, &results); err != nil {
		fmt.Fprintf(os.Stderr, "error parsing results: %v\n", err)
		os.Exit(1)
	}

	// Convert to reports CellData
	cells := make([]reports.CellData, len(results))
	for i, r := range results {
		cells[i] = reports.CellData{
			Harness:      r.HarnessName,
			Category:     inferCategory(r.TaskID),
			SuccessRate:  boolToFloat(r.Verdict.Passed),
			HonestyScore: r.Verdict.HonestyScore,
			CostUSD:      r.RunResult.CostUSD,
		}
	}

	reportData := reports.BuildReportData("Stoke Benchmark", "run-1", time.Now().Format(time.RFC3339), cells)

	outPath := *output
	switch *format {
	case "html":
		if outPath == "" {
			outPath = "reports/report.html"
		}
		f, _ := os.Create(outPath)
		defer f.Close()
		reports.WriteHTML(f, reportData)
	case "csv":
		if outPath == "" {
			outPath = "reports/report.csv"
		}
		f, _ := os.Create(outPath)
		defer f.Close()
		reports.WriteCSV(f, reportData)
	case "markdown":
		if outPath == "" {
			outPath = "reports/report.md"
		}
		f, _ := os.Create(outPath)
		defer f.Close()
		reports.WriteMarkdown(f, reportData)
	default:
		fmt.Fprintf(os.Stderr, "unknown format: %s\n", *format)
		os.Exit(1)
	}
	fmt.Printf("Report written to %s\n", outPath)
}

func boolToFloat(b bool) float64 {
	if b {
		return 1.0
	}
	return 0.0
}

func cmdAnalyze(args []string) {
	fs := flag.NewFlagSet("analyze", flag.ExitOnError)
	input := fs.String("input", "reports/results.json", "Results JSON file")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	data, err := os.ReadFile(*input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	var results []BenchResult
	if err := json.Unmarshal(data, &results); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("=== ANALYSIS ===")
	fmt.Printf("Total runs: %d\n", len(results))

	// Honesty analysis
	var totalHonesty float64
	var cheatingCount, passCount int
	for _, r := range results {
		totalHonesty += r.Verdict.HonestyScore
		if r.Verdict.HonestyScore == 0 {
			cheatingCount++
		}
		if r.Verdict.Passed {
			passCount++
		}
	}
	if len(results) > 0 {
		fmt.Printf("Pass Rate: %.2f%% (%d/%d)\n",
			float64(passCount)/float64(len(results))*100, passCount, len(results))
		fmt.Printf("Avg Honesty Score: %.4f\n", totalHonesty/float64(len(results)))
		fmt.Printf("Cheating Rate: %.2f%% (%d/%d)\n",
			float64(cheatingCount)/float64(len(results))*100, cheatingCount, len(results))
	}
}
