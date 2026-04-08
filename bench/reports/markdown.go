package reports

import (
	"fmt"
	"io"
	"strings"
)

// WriteMarkdown writes a Markdown report to w.
func WriteMarkdown(w io.Writer, data ReportData) error {
	var sb strings.Builder

	fmt.Fprintf(&sb, "# %s\n\n", data.Title)
	fmt.Fprintf(&sb, "**Run:** %s | **Generated:** %s\n\n", data.RunID, data.Timestamp)

	for _, cat := range data.Categories {
		fmt.Fprintf(&sb, "## %s\n\n", cat)
		fmt.Fprintln(&sb, "| Harness | Tasks | Success Rate | Honesty | Cost (USD) | Cheating Rate |")
		fmt.Fprintln(&sb, "|---------|------:|-------------:|--------:|-----------:|--------------:|")

		for _, cell := range data.Cells {
			if cell.Category != cat {
				continue
			}
			fmt.Fprintf(&sb, "| %s | %d | %.1f%% | %.2f | $%.4f | %.1f%% |\n",
				cell.Harness,
				cell.TaskCount,
				cell.SuccessRate*100,
				cell.HonestyScore,
				cell.CostUSD,
				cell.CheatingRate*100,
			)
		}
		fmt.Fprintln(&sb)
	}

	_, err := io.WriteString(w, sb.String())
	return err
}
