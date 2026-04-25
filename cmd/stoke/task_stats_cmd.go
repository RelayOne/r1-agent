package main

import (
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/RelayOne/r1-agent/internal/taskstats"
)

// taskStatsCmd prints a summary of ~/.stoke/task-stats.jsonl so the
// operator can see typical task durations and spot outliers.
//
// Invoke: `stoke task-stats` or `stoke stats`.
func taskStatsCmd(args []string) {
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
