// Package taskstats persists per-task timing data across stoke runs
// so the dispatcher can tell each worker "similar tasks historically
// took ~X seconds" and the watchdog can flag spirals before they
// consume an hour of budget.
//
// Storage: ~/.stoke/task-stats.jsonl (append-only, one record per
// line). Safe to read/write from multiple processes as long as each
// writes its own line atomically — we use a single Write call per
// record. No locking — worst case a partial write produces an
// invalid JSON line that the reader skips. Acceptable tradeoff for a
// stats store that never blocks work.
//
// Records are keyed by DeclaredFileCount + ProjectSlug. The avg-
// by-file-count lookup is the primary use case: "how long do tasks
// with 1 declared file typically take?" Matches the per-task shape
// most accurately without needing task-description NLP.
package taskstats

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Record is one completed task's observed telemetry. Serialized as
// JSON one-per-line to ~/.stoke/task-stats.jsonl.
type Record struct {
	Timestamp         time.Time `json:"ts"`
	ProjectSlug       string    `json:"project,omitempty"`
	SessionID         string    `json:"session,omitempty"`
	TaskID            string    `json:"task"`
	DescriptionHash  string     `json:"desc_hash,omitempty"`
	DeclaredFileCount int       `json:"files"`
	DurationMs        int64     `json:"duration_ms"`
	Turns             int       `json:"turns,omitempty"`
	CostUSD           float64   `json:"cost,omitempty"`
	Success           bool      `json:"success"`
	Mode              string    `json:"mode,omitempty"` // worker | followup | repair
}

// statsPath returns the on-disk location, or "" when HOME is unset.
func statsPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".stoke", "task-stats.jsonl")
}

// Append writes a record. Creates ~/.stoke/ if missing. Silent no-op
// on any error — stats are best-effort observability, never block
// the real work.
func Append(rec Record) {
	path := statsPath()
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	if rec.Timestamp.IsZero() {
		rec.Timestamp = time.Now()
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	b = append(b, '\n')
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(b)
}

// LoadRecent returns the last N valid records (most recent first).
// When N <= 0 returns every record. Invalid JSON lines are skipped.
// Silent empty-slice return on any error.
func LoadRecent(n int) []Record {
	path := statsPath()
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var all []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 4096), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			continue
		}
		all = append(all, r)
	}
	// Reverse to put most recent first.
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}
	if n > 0 && len(all) > n {
		all = all[:n]
	}
	return all
}

// AvgForFileCount computes mean+std-dev duration for tasks with a
// matching declared-file count (±tolerance). Returns (avgMs, stdevMs,
// samples). Samples of zero means no history — caller should skip
// the ETA line.
func AvgForFileCount(records []Record, fileCount, tolerance int) (avgMs int64, stdevMs int64, samples int) {
	if len(records) == 0 {
		return 0, 0, 0
	}
	durations := make([]int64, 0, len(records))
	for _, r := range records {
		if !r.Success {
			continue
		}
		diff := r.DeclaredFileCount - fileCount
		if diff < 0 {
			diff = -diff
		}
		if diff > tolerance {
			continue
		}
		durations = append(durations, r.DurationMs)
	}
	if len(durations) == 0 {
		return 0, 0, 0
	}
	var sum int64
	for _, d := range durations {
		sum += d
	}
	avg := sum / int64(len(durations))
	var sumSq int64
	for _, d := range durations {
		diff := d - avg
		sumSq += diff * diff
	}
	variance := sumSq / int64(len(durations))
	// Cheap int sqrt — good enough for a formatted log line.
	stdev := int64(0)
	for s := int64(1); s*s <= variance; s++ {
		stdev = s
	}
	return avg, stdev, len(durations)
}

// FormatETA renders "expected ~Xs ±Ys (samples=N)" for a task's
// dispatch log line. Empty string when samples==0.
func FormatETA(avgMs, stdevMs int64, samples int) string {
	if samples == 0 {
		return ""
	}
	return fmt.Sprintf("expected ~%ds ±%ds (n=%d)", avgMs/1000, stdevMs/1000, samples)
}
