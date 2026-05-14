// Package bench leaderboard renderer.
//
// leaderboard.go aggregates RunResult records across (agent, mission)
// pairs and renders a Markdown table sorted by truthful-completion
// rate. The output is the public face of the benchmark — every
// numeric column ships a Wilson 95% CI so readers see the precision
// of each rate.
//
// Spec: specs/truthful-completion-benchmark.md §T7 (items 51-53).
package bench

import (
	"fmt"
	"sort"
	"strings"
)

// LeaderboardRow is one aggregated row in the published table.
type LeaderboardRow struct {
	AgentID                 string
	DisplayName             string
	MissionsRun             int
	CompletionAttemptCount  int
	CompletionTruthfulCount int
	SilentFailureCount      int

	// TruthfulCompletionRate is truthful / attempted.
	TruthfulCompletionRate float64
	// TruthfulCIRangeLow/High are the Wilson 95% CI bounds on
	// TruthfulCompletionRate.
	TruthfulCIRangeLow  float64
	TruthfulCIRangeHigh float64
}

// Leaderboard is the rendered table input — a sorted slice of rows.
type Leaderboard struct {
	Rows []LeaderboardRow
}

// BuildLeaderboard folds a slice of RunResult into a Leaderboard.
// Each row corresponds to one unique AgentID. Rows are sorted by
// TruthfulCompletionRate descending; ties broken by attempt count
// (more attempts = more confidence → higher rank).
//
// The AgentID is read from results[*].AgentID (the runner populates
// this); for fixtures that pre-date the field, callers can group
// externally and call BuildLeaderboardForAgent for each group.
func BuildLeaderboard(results []RunResult) Leaderboard {
	groups := map[string][]RunResult{}
	for _, r := range results {
		groups[r.AgentID] = append(groups[r.AgentID], r)
	}
	var rows []LeaderboardRow
	for id, group := range groups {
		rows = append(rows, BuildLeaderboardForAgent(id, group))
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].TruthfulCompletionRate != rows[j].TruthfulCompletionRate {
			return rows[i].TruthfulCompletionRate > rows[j].TruthfulCompletionRate
		}
		if rows[i].CompletionAttemptCount != rows[j].CompletionAttemptCount {
			return rows[i].CompletionAttemptCount > rows[j].CompletionAttemptCount
		}
		return rows[i].AgentID < rows[j].AgentID
	})
	return Leaderboard{Rows: rows}
}

// BuildLeaderboardForAgent folds one group of RunResults — all for the
// same agent — into a single LeaderboardRow. Public so leaderboard
// callers that already group externally don't have to set AgentID.
func BuildLeaderboardForAgent(agentID string, results []RunResult) LeaderboardRow {
	row := LeaderboardRow{AgentID: agentID, DisplayName: agentID}
	for _, r := range results {
		row.MissionsRun++
		if r.CompletionAttempted {
			row.CompletionAttemptCount++
			if r.CompletionTruthful {
				row.CompletionTruthfulCount++
			}
		}
		if r.CompletionSilentlyFailed {
			row.SilentFailureCount++
		}
	}
	if row.CompletionAttemptCount > 0 {
		row.TruthfulCompletionRate = float64(row.CompletionTruthfulCount) / float64(row.CompletionAttemptCount)
	}
	row.TruthfulCIRangeLow, row.TruthfulCIRangeHigh = WilsonCI(row.TruthfulCompletionRate, row.CompletionAttemptCount, 1.96)
	return row
}

// RenderMarkdown returns the leaderboard as a GitHub-flavored
// Markdown table. The table includes a header row and one row per
// agent. Rates are formatted as percentages with 1 decimal place;
// CI bounds appear in [low, high] form.
func (l Leaderboard) RenderMarkdown() string {
	var b strings.Builder
	b.WriteString("| Agent | Missions | Attempts | Truthful | Silent Fails | Truthful Rate | 95% CI |\n")
	b.WriteString("|---|---:|---:|---:|---:|---:|:---:|\n")
	for _, r := range l.Rows {
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %.1f%% | [%.1f%%, %.1f%%] |\n",
			r.DisplayName,
			r.MissionsRun,
			r.CompletionAttemptCount,
			r.CompletionTruthfulCount,
			r.SilentFailureCount,
			r.TruthfulCompletionRate*100,
			r.TruthfulCIRangeLow*100,
			r.TruthfulCIRangeHigh*100,
		)
	}
	return b.String()
}
