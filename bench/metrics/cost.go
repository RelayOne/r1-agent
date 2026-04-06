// Package metrics provides benchmark metric computation for the Stoke
// benchmark framework.
package metrics

// CostMetrics holds computed cost-related metrics for a benchmark run.
type CostMetrics struct {
	// TotalUSD is the total cost across all tasks.
	TotalUSD float64 `json:"total_usd"`

	// USDPerTask is the average cost per task.
	USDPerTask float64 `json:"usd_per_task"`

	// USDPerSuccess is the average cost per successful task.
	// Zero if there were no successes.
	USDPerSuccess float64 `json:"usd_per_success"`

	// CostEfficiencyRatio is successes per dollar spent.
	// Higher is better. Zero if no money was spent.
	CostEfficiencyRatio float64 `json:"cost_efficiency_ratio"`
}

// ComputeCostMetrics calculates cost metrics from per-task cost and outcome data.
// costs and successes must be the same length; successes[i] is true if task i passed.
func ComputeCostMetrics(costs []float64, successes []bool) CostMetrics {
	if len(costs) != len(successes) {
		return CostMetrics{}
	}

	n := len(costs)
	if n == 0 {
		return CostMetrics{}
	}

	var total float64
	var successCost float64
	var successCount int

	for i, c := range costs {
		total += c
		if successes[i] {
			successCost += c
			successCount++
		}
	}

	m := CostMetrics{
		TotalUSD:   total,
		USDPerTask: total / float64(n),
	}

	if successCount > 0 {
		m.USDPerSuccess = successCost / float64(successCount)
	}

	if total > 0 {
		m.CostEfficiencyRatio = float64(successCount) / total
	}

	return m
}
