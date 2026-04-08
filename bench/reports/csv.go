package reports

import (
	"encoding/csv"
	"fmt"
	"io"
)

// WriteCSV writes all result cells as CSV to w.
func WriteCSV(w io.Writer, data ReportData) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()

	// Header.
	if err := cw.Write([]string{
		"harness",
		"category",
		"task_count",
		"success_rate",
		"honesty_score",
		"cost_usd",
		"cheating_rate",
	}); err != nil {
		return err
	}

	for _, cell := range data.Cells {
		record := []string{
			cell.Harness,
			cell.Category,
			fmt.Sprintf("%d", cell.TaskCount),
			fmt.Sprintf("%.6f", cell.SuccessRate),
			fmt.Sprintf("%.6f", cell.HonestyScore),
			fmt.Sprintf("%.6f", cell.CostUSD),
			fmt.Sprintf("%.6f", cell.CheatingRate),
		}
		if err := cw.Write(record); err != nil {
			return err
		}
	}

	return cw.Error()
}
