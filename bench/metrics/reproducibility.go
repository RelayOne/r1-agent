package metrics

import (
	"math"
)

// ReproducibilityMetrics captures how consistent results are across
// repeated benchmark runs.
type ReproducibilityMetrics struct {
	// CoefficientOfVariation is the ratio of standard deviation to mean
	// of the success rates across runs. Lower is more reproducible.
	CoefficientOfVariation float64 `json:"coefficient_of_variation"`

	// ICC is the intraclass correlation coefficient, estimating the
	// proportion of variance attributable to real task differences
	// vs. random run-to-run noise. Range [0, 1]; higher is better.
	ICC float64 `json:"icc"`

	// TARrN is the Total Agreement Rate at N repetitions: the fraction
	// of tasks that had the same outcome (all pass or all fail) across
	// all N runs.
	TARrN float64 `json:"tar_r_n"`

	// VarianceWarnings lists tasks with high outcome variance across runs.
	VarianceWarnings []string `json:"variance_warnings,omitempty"`
}

// ComputeReproducibility computes reproducibility metrics from a matrix of
// outcomes. outcomes[run][task] is true if the task passed in that run.
// taskIDs provides the task identifier for each column index.
func ComputeReproducibility(outcomes [][]bool, taskIDs []string) ReproducibilityMetrics {
	nRuns := len(outcomes)
	if nRuns == 0 {
		return ReproducibilityMetrics{}
	}
	nTasks := len(outcomes[0])
	if nTasks == 0 {
		return ReproducibilityMetrics{}
	}

	// Per-run success rates.
	rates := make([]float64, nRuns)
	for r := 0; r < nRuns; r++ {
		successes := 0
		for t := 0; t < nTasks; t++ {
			if outcomes[r][t] {
				successes++
			}
		}
		rates[r] = float64(successes) / float64(nTasks)
	}

	m := ReproducibilityMetrics{}

	// Coefficient of variation of success rates.
	meanRate := mean(rates)
	stdRate := stddev(rates, meanRate)
	if meanRate > 0 {
		m.CoefficientOfVariation = stdRate / meanRate
	}

	// TARr@N: fraction of tasks with unanimous outcome across all runs.
	unanimous := 0
	for t := 0; t < nTasks; t++ {
		allSame := true
		first := outcomes[0][t]
		for r := 1; r < nRuns; r++ {
			if outcomes[r][t] != first {
				allSame = false
				break
			}
		}
		if allSame {
			unanimous++
		}
	}
	m.TARrN = float64(unanimous) / float64(nTasks)

	// ICC (one-way random effects, single measures).
	// For each task, compute per-task mean and overall mean.
	overallMean := 0.0
	taskMeans := make([]float64, nTasks)
	for t := 0; t < nTasks; t++ {
		sum := 0.0
		for r := 0; r < nRuns; r++ {
			if outcomes[r][t] {
				sum += 1.0
			}
		}
		taskMeans[t] = sum / float64(nRuns)
		overallMean += taskMeans[t]
	}
	overallMean /= float64(nTasks)

	// Between-task variance (MSB) and within-task variance (MSW).
	msb := 0.0
	for t := 0; t < nTasks; t++ {
		d := taskMeans[t] - overallMean
		msb += d * d
	}
	if nTasks > 1 {
		msb = msb * float64(nRuns) / float64(nTasks-1)
	}

	msw := 0.0
	for t := 0; t < nTasks; t++ {
		for r := 0; r < nRuns; r++ {
			val := 0.0
			if outcomes[r][t] {
				val = 1.0
			}
			d := val - taskMeans[t]
			msw += d * d
		}
	}
	denom := nTasks * (nRuns - 1)
	if denom > 0 {
		msw /= float64(denom)
	}

	if msb+float64(nRuns-1)*msw > 0 {
		m.ICC = (msb - msw) / (msb + float64(nRuns-1)*msw)
		if m.ICC < 0 {
			m.ICC = 0
		}
	}

	// Variance warnings: tasks that are not unanimous.
	for t := 0; t < nTasks; t++ {
		passCount := 0
		for r := 0; r < nRuns; r++ {
			if outcomes[r][t] {
				passCount++
			}
		}
		if passCount > 0 && passCount < nRuns {
			id := ""
			if t < len(taskIDs) {
				id = taskIDs[t]
			}
			if id != "" {
				m.VarianceWarnings = append(m.VarianceWarnings, id)
			}
		}
	}

	return m
}

func mean(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range vals {
		sum += v
	}
	return sum / float64(len(vals))
}

func stddev(vals []float64, m float64) float64 {
	if len(vals) < 2 {
		return 0
	}
	sum := 0.0
	for _, v := range vals {
		d := v - m
		sum += d * d
	}
	return math.Sqrt(sum / float64(len(vals)-1))
}
