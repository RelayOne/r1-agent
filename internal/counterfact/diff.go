package counterfact

import "sort"

// DivergenceReport summarizes how a counterfactual differed from actual execution.
type DivergenceReport struct {
	StatusChanged       bool     `json:"status_changed"`
	ScoreDelta          float64  `json:"score_delta"`
	NewDissents         []string `json:"new_dissents,omitempty"`
	ResolvedDissents    []string `json:"resolved_dissents,omitempty"`
	NewGates            []string `json:"new_gates,omitempty"`
	RemovedGates        []string `json:"removed_gates,omitempty"`
	NewChangedFiles     []string `json:"new_changed_files,omitempty"`
	RemovedChangedFiles []string `json:"removed_changed_files,omitempty"`
}

// Diff compares the actual mission outcome with the counterfactual outcome.
func Diff(actual, counterfactual OutcomeSummary) DivergenceReport {
	return DivergenceReport{
		StatusChanged:       actual.Status != counterfactual.Status,
		ScoreDelta:          counterfactual.Score - actual.Score,
		NewDissents:         diffSet(counterfactual.Dissents, actual.Dissents),
		ResolvedDissents:    diffSet(actual.Dissents, counterfactual.Dissents),
		NewGates:            diffSet(counterfactual.Gates, actual.Gates),
		RemovedGates:        diffSet(actual.Gates, counterfactual.Gates),
		NewChangedFiles:     diffSet(counterfactual.ChangedFiles, actual.ChangedFiles),
		RemovedChangedFiles: diffSet(actual.ChangedFiles, counterfactual.ChangedFiles),
	}
}

func diffSet(left, right []string) []string {
	index := make(map[string]struct{}, len(right))
	for _, item := range right {
		index[item] = struct{}{}
	}
	out := make([]string, 0)
	for _, item := range left {
		if _, ok := index[item]; ok {
			continue
		}
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}
