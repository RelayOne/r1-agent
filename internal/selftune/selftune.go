package selftune

import (
	"fmt"
	"sort"
	"time"
)

// Axis captures one scored dimension for a trial.
type Axis struct {
	Name  string  `json:"name"`
	Value float64 `json:"value"`
}

// Trial is one evaluated candidate config.
type Trial struct {
	ID      string             `json:"id"`
	Config  map[string]float64 `json:"config"`
	Metrics map[string]float64 `json:"metrics"`
}

// Recommendation is the best acceptable trial.
type Recommendation struct {
	Baseline    Trial     `json:"baseline"`
	Selected    Trial     `json:"selected"`
	Improvement []Axis    `json:"improvement"`
	GeneratedAt time.Time `json:"generated_at"`
}

// Recommend selects a trial that improves one metric by at least 5% without regressing any other metric.
func Recommend(baseline Trial, candidates []Trial, now time.Time) (Recommendation, error) {
	bestIndex := -1
	bestPrimary := 0.0
	for idx, trial := range candidates {
		ok, primary := beatsBaseline(baseline, trial)
		if !ok {
			continue
		}
		if primary > bestPrimary {
			bestPrimary = primary
			bestIndex = idx
		}
	}
	if bestIndex == -1 {
		return Recommendation{}, fmt.Errorf("selftune: no non-regressing improvement found")
	}
	selected := candidates[bestIndex]
	return Recommendation{
		Baseline:    baseline,
		Selected:    selected,
		Improvement: improvements(baseline, selected),
		GeneratedAt: now.UTC(),
	}, nil
}

func beatsBaseline(baseline, candidate Trial) (bool, float64) {
	primary := 0.0
	improved := false
	for name, baseValue := range baseline.Metrics {
		candidateValue, ok := candidate.Metrics[name]
		if !ok {
			return false, 0
		}
		delta := candidateValue - baseValue
		if delta < 0 {
			return false, 0
		}
		if baseValue > 0 && delta/baseValue >= 0.05 {
			improved = true
			if delta > primary {
				primary = delta
			}
		}
	}
	return improved, primary
}

func improvements(baseline, selected Trial) []Axis {
	out := make([]Axis, 0, len(baseline.Metrics))
	for name, baseValue := range baseline.Metrics {
		out = append(out, Axis{
			Name:  name,
			Value: selected.Metrics[name] - baseValue,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}
