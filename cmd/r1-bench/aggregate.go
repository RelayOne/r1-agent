// aggregate.go — reads a directory of RunResult JSON files and folds
// them into a leaderboard. Closes the deferred TODO in
// cmd/r1-bench/reproduction-kit/README.md ("for now, point your
// favorite scripting language at the JSON files").
//
// Spec: specs/truthful-completion-benchmark.md §T7.3 (item 55b).
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/RelayOne/r1/internal/bench"
)

// AggregateDir walks dir for *.json files, decodes each as a
// bench.RunResult, and returns a Markdown rendering selected by
// format: "markdown" (leaderboard only), "per-mission" (drill-down
// only), or "both" (concatenated with a separator).
//
// Files that can't be parsed as RunResult are skipped with a comment
// at the top of the output — the operator can decide whether a parse
// failure is a real concern.
func AggregateDir(dir, format string) (string, error) {
	results, skipped, err := loadResults(dir)
	if err != nil {
		return "", err
	}
	if len(results) == 0 {
		return "", fmt.Errorf("AggregateDir: no RunResult JSON files found under %s (skipped %d unparseable files)", dir, skipped)
	}

	var out strings.Builder
	if skipped > 0 {
		fmt.Fprintf(&out, "<!-- %d files skipped (parse error or not a RunResult) -->\n\n", skipped)
	}

	switch format {
	case "", "markdown":
		out.WriteString("# TruthfulCompletion Leaderboard\n\n")
		out.WriteString(bench.BuildLeaderboard(results).RenderMarkdown())
	case "per-mission":
		out.WriteString("# Per-Mission Breakdown\n\n")
		out.WriteString(bench.BuildPerMissionTable(results).RenderMarkdown())
	case "both":
		out.WriteString("# TruthfulCompletion Leaderboard\n\n")
		out.WriteString(bench.BuildLeaderboard(results).RenderMarkdown())
		out.WriteString("\n# Per-Mission Breakdown\n\n")
		out.WriteString(bench.BuildPerMissionTable(results).RenderMarkdown())
	default:
		return "", fmt.Errorf("AggregateDir: unknown format %q (expected markdown, per-mission, or both)", format)
	}
	return out.String(), nil
}

// loadResults walks dir for *.json files and decodes each as a
// bench.RunResult. Returns the parsed results plus a count of files
// that were JSON but didn't shape-match RunResult.
func loadResults(dir string) (results []bench.RunResult, skipped int, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0, fmt.Errorf("loadResults: read dir %s: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(e.Name()), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		body, rerr := os.ReadFile(path)
		if rerr != nil {
			skipped++
			continue
		}
		var r bench.RunResult
		if jerr := json.Unmarshal(body, &r); jerr != nil {
			skipped++
			continue
		}
		// A RunResult must have at least an AgentID or a MissionID
		// to be meaningful; pure {} files are skipped.
		if r.AgentID == "" && r.MissionID == "" {
			skipped++
			continue
		}
		results = append(results, r)
	}
	return results, skipped, nil
}
