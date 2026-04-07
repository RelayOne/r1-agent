package bench

import (
	"fmt"
	"strings"
)

// Report generates a markdown report from bench results.
func Report(results []RunResult) string {
	var sb strings.Builder

	sb.WriteString("# Bench Report\n\n")
	sb.WriteString(fmt.Sprintf("Missions run: %d\n\n", len(results)))

	sb.WriteString("| Mission | State | Acceptance | Cost | Tokens | Wall Time | Loops | Trust | Dissent | Escalations |\n")
	sb.WriteString("|---------|-------|------------|------|--------|-----------|-------|-------|---------|-------------|\n")

	for _, r := range results {
		sb.WriteString(fmt.Sprintf("| %s | %s | %d/%d | $%.4f | %d | %dms | %d | %d | %d | %d |\n",
			r.MissionID,
			r.TerminalState,
			r.AcceptanceMet, r.AcceptanceTotal,
			r.CostUSD,
			r.TokensUsed,
			r.WallTimeMs,
			r.LoopIterations,
			r.TrustFirings,
			r.DissentCount,
			r.EscalationCount,
		))
	}

	// Summary.
	var converged, escalated, timedOut int
	var totalCost float64
	var totalTokens int64
	for _, r := range results {
		switch r.TerminalState {
		case "converged":
			converged++
		case "escalated":
			escalated++
		case "timed_out":
			timedOut++
		}
		totalCost += r.CostUSD
		totalTokens += r.TokensUsed
	}

	sb.WriteString("\n## Summary\n\n")
	sb.WriteString(fmt.Sprintf("- Converged: %d\n", converged))
	sb.WriteString(fmt.Sprintf("- Escalated: %d\n", escalated))
	sb.WriteString(fmt.Sprintf("- Timed out: %d\n", timedOut))
	sb.WriteString(fmt.Sprintf("- Total cost: $%.4f\n", totalCost))
	sb.WriteString(fmt.Sprintf("- Total tokens: %d\n", totalTokens))

	return sb.String()
}

// ComparisonReport generates a markdown comparison report.
func ComparisonReport(comparisons []ComparisonResult) string {
	var sb strings.Builder

	sb.WriteString("# Comparison Report\n\n")

	var regressions int
	for _, c := range comparisons {
		if c.Regression {
			regressions++
		}
	}
	sb.WriteString(fmt.Sprintf("Regressions detected: %d / %d\n\n", regressions, len(comparisons)))

	sb.WriteString("| Mission | Baseline State | Current State | Regression | Cost Delta | Acceptance Delta |\n")
	sb.WriteString("|---------|---------------|---------------|------------|------------|------------------|\n")

	for _, c := range comparisons {
		regStr := "no"
		if c.Regression {
			regStr = "YES"
		}
		sb.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %+.4f | %+.0f |\n",
			c.Mission,
			c.Baseline.TerminalState,
			c.Current.TerminalState,
			regStr,
			c.Delta["cost_usd"],
			c.Delta["acceptance_met"],
		))
	}

	return sb.String()
}
