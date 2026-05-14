// Package bench per-mission renderer.
//
// permission_render.go produces a per-mission breakdown: for each
// agent/mission pair, a row showing the verdict, plan-completion
// fraction, delivery ratio, and judge verdict. The output is the
// drill-down view linked from each leaderboard row.
//
// Spec: specs/truthful-completion-benchmark.md §T7.2 (item 54).
package bench

import (
	"fmt"
	"sort"
	"strings"
)

// MissionResultRow is one (agent, mission) row in the per-mission
// breakdown table.
type MissionResultRow struct {
	AgentID            string
	MissionID          string
	CompletionAttempted bool
	CompletionTruthful  bool
	PlanCompleted       int
	PlanTotal           int
	DeliveryRatioPct    int
	JudgeVerdict        string
	JudgeRationale      string
	ExitReason          string
}

// PerMissionTable is the rendered input — a slice of rows.
type PerMissionTable struct {
	Rows []MissionResultRow
}

// BuildPerMissionTable folds a slice of RunResult into a sorted
// MissionResultRow list. Sort order: agent ID alphabetical, then
// mission ID alphabetical, so the same agent's missions are
// contiguous.
func BuildPerMissionTable(results []RunResult) PerMissionTable {
	rows := make([]MissionResultRow, 0, len(results))
	for _, r := range results {
		rows = append(rows, MissionResultRow{
			AgentID:             r.AgentID,
			MissionID:           r.MissionID,
			CompletionAttempted: r.CompletionAttempted,
			CompletionTruthful:  r.CompletionTruthful,
			PlanCompleted:       r.PlanItemsCompleted,
			PlanTotal:           r.PlanItemsTotal,
			DeliveryRatioPct:    r.DeliveryRatioPercent,
			JudgeVerdict:        r.JudgeVerdict,
			JudgeRationale:      r.JudgeRationale,
			ExitReason:          "", // not tracked on RunResult; could be added later
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].AgentID != rows[j].AgentID {
			return rows[i].AgentID < rows[j].AgentID
		}
		return rows[i].MissionID < rows[j].MissionID
	})
	return PerMissionTable{Rows: rows}
}

// RenderMarkdown returns the per-mission table as a GFM table.
// Verdict column uses ✓ for truthful, ✗ for untruthful, "—" for
// silent failure.
func (p PerMissionTable) RenderMarkdown() string {
	var b strings.Builder
	b.WriteString("| Agent | Mission | Verdict | Plan | Delivery | Judge |\n")
	b.WriteString("|---|---|:---:|:---:|---:|:---:|\n")
	for _, r := range p.Rows {
		verdict := "—"
		if r.CompletionAttempted {
			if r.CompletionTruthful {
				verdict = "OK"
			} else {
				verdict = "FAIL"
			}
		}
		plan := "—"
		if r.PlanTotal > 0 {
			plan = fmt.Sprintf("%d/%d", r.PlanCompleted, r.PlanTotal)
		}
		judge := r.JudgeVerdict
		if judge == "" {
			judge = "—"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %d%% | %s |\n",
			r.AgentID,
			r.MissionID,
			verdict,
			plan,
			r.DeliveryRatioPct,
			judge,
		)
	}
	return b.String()
}
