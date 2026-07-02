package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/r1dir"
	"github.com/RelayOne/r1/internal/taskstats"
	"github.com/RelayOne/r1/internal/telemetry"
)

// taskStatsCmd prints a summary of ~/.stoke/task-stats.jsonl so the
// operator can see typical task durations and spot outliers.
//
// Invoke: `r1 task-stats` or `r1 stats`.
//
// The `telemetry` sub-view (`r1 stats telemetry [--last N]`) is the O6
// read side: it lists the run snapshots the orchestrator now persists
// under <repo>/.r1/telemetry and diffs the newest two to flag
// success-rate / latency drift.
func taskStatsCmd(args []string) {
	if len(args) > 0 && args[0] == "telemetry" {
		statsTelemetryView(args[1:])
		return
	}

	fs := flag.NewFlagSet("task-stats", flag.ExitOnError)
	limit := fs.Int("limit", 200, "How many most-recent records to load (0=all)")
	byFiles := fs.Int("files", -1, "When set, only show records with this declared-file count")
	project := fs.String("project", "", "When set, filter to this project slug (SOW id)")
	_ = fs.Parse(args)

	records := taskstats.LoadRecent(*limit)
	if len(records) == 0 {
		fmt.Println("no task-stats data yet — run a sow and try again")
		return
	}

	// Optional filters.
	filtered := records[:0]
	for _, r := range records {
		if *byFiles >= 0 && r.DeclaredFileCount != *byFiles {
			continue
		}
		if *project != "" && r.ProjectSlug != *project {
			continue
		}
		filtered = append(filtered, r)
	}
	if len(filtered) == 0 {
		fmt.Println("no records after filtering")
		return
	}

	// Overall + by-file-count breakdown.
	type bucket struct {
		files   int
		count   int
		total   int64
		succ    int
		failed  int
	}
	buckets := map[int]*bucket{}
	var successTotal int64
	var successCount int
	for _, r := range filtered {
		b, ok := buckets[r.DeclaredFileCount]
		if !ok {
			b = &bucket{files: r.DeclaredFileCount}
			buckets[r.DeclaredFileCount] = b
		}
		b.count++
		b.total += r.DurationMs
		if r.Success {
			b.succ++
			successTotal += r.DurationMs
			successCount++
		} else {
			b.failed++
		}
	}

	fmt.Printf("task-stats: %d records in window\n", len(filtered))
	if successCount > 0 {
		fmt.Printf("  overall success avg: %ds (n=%d, %d failed)\n",
			successTotal/int64(successCount)/1000, successCount, len(filtered)-successCount)
	}
	fmt.Println()
	fmt.Printf("%-10s %-10s %-10s %-10s %-10s\n", "files", "count", "avg_sec", "success", "failed")
	keys := make([]int, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	for _, k := range keys {
		b := buckets[k]
		avg := int64(0)
		if b.count > 0 {
			avg = b.total / int64(b.count) / 1000
		}
		fmt.Printf("%-10d %-10d %-10d %-10d %-10d\n", b.files, b.count, avg, b.succ, b.failed)
	}

	// Recent outliers: top 5 slowest successes.
	successCopy := make([]taskstats.Record, 0, len(filtered))
	for _, r := range filtered {
		if r.Success {
			successCopy = append(successCopy, r)
		}
	}
	sort.Slice(successCopy, func(i, j int) bool {
		return successCopy[i].DurationMs > successCopy[j].DurationMs
	})
	if len(successCopy) > 0 {
		fmt.Println()
		fmt.Println("slowest successful tasks:")
		show := 5
		if len(successCopy) < show {
			show = len(successCopy)
		}
		for i := 0; i < show; i++ {
			r := successCopy[i]
			fmt.Printf("  %s %s/%s files=%d dur=%ds\n",
				r.Timestamp.Format("2006-01-02 15:04"),
				r.ProjectSlug, r.TaskID, r.DeclaredFileCount, r.DurationMs/1000)
		}
	}

	// Write outcome to stderr so pipes are clean.
	_ = os.Stderr
}

// statsTelemetryView renders the O6 telemetry series: it lists the most
// recent run snapshots under <repo>/.r1/telemetry and diffs the newest
// two to flag success-rate / latency drift. Strictly read-only.
func statsTelemetryView(args []string) {
	fs := flag.NewFlagSet("stats telemetry", flag.ExitOnError)
	repo := fs.String("repo", ".", "Repository root")
	last := fs.Int("last", 5, "How many most-recent snapshots to list")
	_ = fs.Parse(args)

	absRepo, err := filepath.Abs(*repo)
	if err != nil {
		absRepo = *repo
	}
	dir := r1dir.JoinFor(absRepo, "telemetry")
	snaps, err := telemetry.ListSnapshots(dir)
	if err != nil {
		fmt.Printf("telemetry: %v\n", err)
		return
	}
	if len(snaps) == 0 {
		fmt.Println("no telemetry snapshots yet — run a build and try again")
		return
	}

	fmt.Printf("telemetry: %d snapshot(s) in %s\n\n", len(snaps), dir)
	show := *last
	if show <= 0 || show > len(snaps) {
		show = len(snaps)
	}
	fmt.Printf("%-34s %-8s %-9s %-12s %-10s\n", "run", "events", "success", "avg_dur", "cost")
	// snaps is oldest→newest; print newest first.
	for i := 0; i < show; i++ {
		s := snaps[len(snaps)-1-i]
		fmt.Printf("%-34s %-8d %-9s %-12s $%.4f\n",
			s.RunID,
			s.Summary.TotalEvents,
			fmt.Sprintf("%.0f%%", s.Summary.SuccessRate*100),
			s.Summary.AvgDuration.Round(time.Millisecond),
			s.Summary.TotalCost)
	}

	if len(snaps) >= 2 {
		fmt.Println()
		fmt.Print(telemetryDrift(snaps[len(snaps)-2], snaps[len(snaps)-1]))
	}
}

// telemetryDrift renders a human-readable diff of two run snapshots so
// an operator can answer "did the agent get slower or flakier?". prev is
// the older snapshot, cur the newer. Pure (no I/O) so it is unit-tested
// directly.
func telemetryDrift(prev, cur telemetry.Snapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "drift %s → %s:\n", prev.RunID, cur.RunID)

	dSucc := (cur.Summary.SuccessRate - prev.Summary.SuccessRate) * 100
	fmt.Fprintf(&b, "  success rate: %.0f%% → %.0f%% (%s)\n",
		prev.Summary.SuccessRate*100, cur.Summary.SuccessRate*100, signedPts(dSucc))

	dAvg := cur.Summary.AvgDuration - prev.Summary.AvgDuration
	fmt.Fprintf(&b, "  avg duration: %s → %s (%s)\n",
		prev.Summary.AvgDuration.Round(time.Millisecond),
		cur.Summary.AvgDuration.Round(time.Millisecond),
		signedDur(dAvg))

	if p, c := prev.TaskPercentiles["p50"], cur.TaskPercentiles["p50"]; p > 0 || c > 0 {
		fmt.Fprintf(&b, "  task p50: %s → %s (%s)\n",
			p.Round(time.Millisecond), c.Round(time.Millisecond), signedDur(c-p))
	}
	if p, c := prev.TaskPercentiles["p95"], cur.TaskPercentiles["p95"]; p > 0 || c > 0 {
		fmt.Fprintf(&b, "  task p95: %s → %s (%s)\n",
			p.Round(time.Millisecond), c.Round(time.Millisecond), signedDur(c-p))
	}

	dCost := cur.Summary.TotalCost - prev.Summary.TotalCost
	fmt.Fprintf(&b, "  total cost: $%.4f → $%.4f (%+.4f)\n",
		prev.Summary.TotalCost, cur.Summary.TotalCost, dCost)

	// Explicit regression callouts (⚠ matches the reminder style
	// used elsewhere in cmd/r1).
	if dSucc < 0 {
		fmt.Fprintf(&b, "  ⚠ success rate dropped %.0f points\n", -dSucc)
	}
	if dAvg > 0 {
		fmt.Fprintf(&b, "  ⚠ avg duration rose %s\n", dAvg.Round(time.Millisecond))
	}
	return b.String()
}

// signedPts formats a percentage-point delta with an explicit sign.
func signedPts(v float64) string {
	if v >= 0 {
		return fmt.Sprintf("+%.0f pts", v)
	}
	return fmt.Sprintf("%.0f pts", v)
}

// signedDur formats a duration delta with an explicit leading sign.
func signedDur(d time.Duration) string {
	d = d.Round(time.Millisecond)
	if d >= 0 {
		return "+" + d.String()
	}
	return d.String()
}
