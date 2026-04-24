package plan

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LatestWorkerLogForTask returns the most-recently-written worker JSONL log
// for the given task ID under repoRoot/.stoke/worker-logs/, matching the
// filename pattern <taskID>-<nanoseconds>.jsonl produced by execNativeTask.
//
// Used by the reviewer path to recover the worker's tool-call trail without
// threading the path explicitly through every recursive review call. Empty
// string on no match / missing directory — callers degrade gracefully.
func LatestWorkerLogForTask(repoRoot, taskID string) string {
	if repoRoot == "" || taskID == "" {
		return ""
	}
	dir := filepath.Join(repoRoot, ".stoke", "worker-logs")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	prefix := taskID + "-"
	var matches []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		matches = append(matches, name)
	}
	if len(matches) == 0 {
		return ""
	}
	// Filenames embed nanoseconds, so lexicographic sort is chronological.
	sort.Strings(matches)
	return filepath.Join(dir, matches[len(matches)-1])
}

// LoadWorkerLogExcerpt reads the per-worker JSONL log at path and returns
// a compact, reviewer-friendly rendering of every tool call recorded.
//
// The raw JSONL is produced by engine.NativeRunner when RunSpec.WorkerLogPath
// is set: one line per tool call with {ts, tool, input, result, duration_ms,
// err}. This loader renders each call as:
//
//	[hh:mm:ss] tool result_len=N duration_ms=M
//	  input:  {args...}
//	  result: <first 300 chars>
//	  err:    <error if any>
//
// Only the last `maxCalls` tool calls are kept (newest first by file order),
// and each input/result is truncated to a short snippet so the excerpt fits
// comfortably in a reviewer prompt (target ~6 KB, matches the dispatch-time
// truncation used by task_judge.go).
//
// Returns empty string if the path is empty, doesn't exist, or can't be
// parsed — reviewers are expected to degrade gracefully to the no-log path.
func LoadWorkerLogExcerpt(path string, maxCalls int) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	type entry struct {
		TS         string          `json:"ts"`
		Type       string          `json:"type"` // "dispatch_start" header line
		Tool       string          `json:"tool"`
		Input      json.RawMessage `json:"input"`
		Result     string          `json:"result"`
		ResultLen  int             `json:"result_len"`
		DurationMs int64           `json:"duration_ms"`
		Err        string          `json:"err"`
	}

	scanner := bufio.NewScanner(f)
	// JSONL lines can contain large bash stdout — raise the buffer so
	// long result lines don't truncate mid-line.
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	var entries []entry
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e entry
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		// Skip the dispatch header; it's infra metadata, not a tool call.
		if e.Type != "" && e.Tool == "" {
			continue
		}
		entries = append(entries, e)
	}

	if maxCalls > 0 && len(entries) > maxCalls {
		entries = entries[len(entries)-maxCalls:]
	}

	var b strings.Builder
	fmt.Fprintf(&b, "(%d tool call(s) captured)\n", len(entries))
	for _, e := range entries {
		ts := e.TS
		// Render just HH:MM:SS from RFC3339 timestamp for compactness.
		if len(ts) >= 19 {
			ts = ts[11:19]
		}
		fmt.Fprintf(&b, "[%s] %s duration_ms=%d result_len=%d\n", ts, e.Tool, e.DurationMs, e.ResultLen)
		if len(e.Input) > 0 {
			fmt.Fprintf(&b, "  input:  %s\n", shortenOneLine(string(e.Input), 300))
		}
		if e.Result != "" {
			fmt.Fprintf(&b, "  result: %s\n", shortenOneLine(e.Result, 400))
		}
		if e.Err != "" {
			fmt.Fprintf(&b, "  err:    %s\n", shortenOneLine(e.Err, 200))
		}
	}
	return b.String()
}

// shortenOneLine collapses whitespace and truncates s for inline display in
// the reviewer prompt. Very long bash outputs become a single visible line
// with "..." suffix so one call takes at most ~1-2 lines in the excerpt.
func shortenOneLine(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", " \\n ")
	s = strings.ReplaceAll(s, "\t", " ")
	if len(s) > maxLen {
		s = s[:maxLen] + "…"
	}
	return s
}
