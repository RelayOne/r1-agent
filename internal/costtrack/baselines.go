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

	"github.com/RelayOne/r1/internal/r1env"
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

// LoadBaselinesFromSearchPaths walks a standard set of locations
// looking for the named baseline file and loads the first match. CI
// gates, dev environments, and deployed binaries all have different
// layouts; a single relative path only works in the source checkout.
// Order:
//
//  1. Env override: $STOKE_BASELINES_PATH (full path to the file).
//  2. Relative to the current working directory:
//     bench/baselines/<filename> — matches the source checkout.
//  3. Relative to the stoke binary: ../bench/baselines/<filename>.
//  4. $HOME/.stoke/baselines/<filename> — user-level override.
//  5. /etc/stoke/baselines/<filename> — system-level deployment.
//
// Returns (nil, nil) when no file matches so callers can treat
// missing baselines as "enforcement disabled" rather than fatal.
func LoadBaselinesFromSearchPaths(filename string) (map[string]AmplificationBudget, error) {
	candidates := []string{}
	if p := r1env.Get("R1_BASELINES_PATH", "STOKE_BASELINES_PATH"); p != "" {
		candidates = append(candidates, p)
	}
	candidates = append(candidates, fmt.Sprintf("bench/baselines/%s", filename))
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, fmt.Sprintf("%s/../bench/baselines/%s", filepathDir(exe), filename))
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, fmt.Sprintf("%s/.stoke/baselines/%s", home, filename))
	}
	candidates = append(candidates, fmt.Sprintf("/etc/stoke/baselines/%s", filename))
	for _, path := range candidates {
		if _, err := os.Stat(path); err != nil {
			continue
		}
		return LoadBaselines(path)
	}
	return nil, nil // no file found; caller treats as disabled
}

// filepathDir is a tiny inline helper so baselines.go doesn't need a
// path/filepath import; the dir-extraction is trivial.
func filepathDir(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return "."
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
