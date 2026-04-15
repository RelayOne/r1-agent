// Package costtrack — baselines.go
//
// Loader for AmplificationBudget entries from a JSON baseline file
// (see bench/baselines/token-baselines-2026-Q2.json for shape). Keeps
// the token measurement separate from code — baselines are meant to
// be replaced quarterly without code changes, so they live as data.

package costtrack

import (
	"encoding/json"
	"fmt"
	"os"
)

// baselineEntry matches one record in bench/baselines/*.json.
type baselineEntry struct {
	TaskClass       string  `json:"task_class"`
	BaselineTokens  int     `json:"baseline_tokens"`
	MaxMultiplier   float64 `json:"max_multiplier"`
	AlertMultiplier float64 `json:"alert_multiplier"`
	Source          string  `json:"source"` // "measured" | "conservative_estimate"
}

// BaselineFile is the top-level JSON shape in
// bench/baselines/token-baselines-*.json.
type BaselineFile struct {
	BaselineLabel string          `json:"baseline_label"`
	MeasuredAt    string          `json:"measured_at"`
	Methodology   string          `json:"methodology"`
	Baselines     []baselineEntry `json:"baselines"`
	Notes         string          `json:"notes,omitempty"`
}

// LoadBaselines reads a baseline JSON file and returns a
// task-class → AmplificationBudget map. File not found or parse
// failure returns an empty map with the underlying error — callers
// should treat missing baselines as "enforcement disabled" rather
// than a fatal condition, so B2 remains no-op safe when the file
// hasn't been deployed yet.
func LoadBaselines(path string) (map[string]AmplificationBudget, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var f BaselineFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	out := make(map[string]AmplificationBudget, len(f.Baselines))
	for _, e := range f.Baselines {
		if e.TaskClass == "" || e.BaselineTokens <= 0 {
			continue
		}
		out[e.TaskClass] = AmplificationBudget{
			TaskClass:       e.TaskClass,
			BaselineTokens:  e.BaselineTokens,
			MaxMultiplier:   e.MaxMultiplier,
			AlertMultiplier: e.AlertMultiplier,
		}
	}
	return out, nil
}

// BudgetForClass returns the AmplificationBudget for the named task
// class, or a zero-value disabled budget when no entry exists.
// Useful when callers want a single lookup-with-fallback rather than
// managing the map themselves.
func BudgetForClass(baselines map[string]AmplificationBudget, class string) AmplificationBudget {
	if b, ok := baselines[class]; ok {
		return b
	}
	return AmplificationBudget{TaskClass: class} // BaselineTokens=0 → disabled
}
