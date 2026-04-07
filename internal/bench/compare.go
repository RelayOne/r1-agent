package bench

// Compare computes the difference between a baseline and current RunResult.
func Compare(baseline, current *RunResult) *ComparisonResult {
	delta := map[string]float64{
		"wall_time_ms":     float64(current.WallTimeMs - baseline.WallTimeMs),
		"cost_usd":         current.CostUSD - baseline.CostUSD,
		"tokens_used":      float64(current.TokensUsed - baseline.TokensUsed),
		"loop_iterations":  float64(current.LoopIterations - baseline.LoopIterations),
		"trust_firings":    float64(current.TrustFirings - baseline.TrustFirings),
		"dissent_count":    float64(current.DissentCount - baseline.DissentCount),
		"escalation_count": float64(current.EscalationCount - baseline.EscalationCount),
		"acceptance_met":   float64(current.AcceptanceMet - baseline.AcceptanceMet),
	}

	return &ComparisonResult{
		Mission:    baseline.MissionID,
		Baseline:   *baseline,
		Current:    *current,
		Regression: DetectRegression(baseline, current),
		Delta:      delta,
	}
}

// DetectRegression returns true if current is worse than baseline on key
// metrics: fewer acceptance criteria met, higher cost, or escalated when
// baseline converged.
func DetectRegression(baseline, current *RunResult) bool {
	// Acceptance regression: fewer criteria met.
	if current.AcceptanceMet < baseline.AcceptanceMet {
		return true
	}

	// Terminal state regression: escalated or timed out when baseline converged.
	if baseline.TerminalState == "converged" && current.TerminalState != "converged" {
		return true
	}

	// Cost regression: more than 50% increase.
	if baseline.CostUSD > 0 && current.CostUSD > baseline.CostUSD*1.5 {
		return true
	}

	// Wall time regression: more than 2x.
	if baseline.WallTimeMs > 0 && current.WallTimeMs > baseline.WallTimeMs*2 {
		return true
	}

	return false
}
