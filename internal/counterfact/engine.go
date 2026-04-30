package counterfact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/RelayOne/r1/internal/specexec"
)

// Knob applies a dotted-path override to a mission config snapshot.
type Knob struct {
	Path     string      `json:"path"`
	OldValue interface{} `json:"old_value,omitempty"`
	NewValue interface{} `json:"new_value"`
}

// MissionSnapshot is the minimum deterministic payload needed to replay a mission.
type MissionSnapshot struct {
	MissionID string                 `json:"mission_id"`
	Config    map[string]interface{} `json:"config"`
	Actual    OutcomeSummary         `json:"actual"`
}

// OutcomeSummary is a reduced view of a mission result for divergence reporting.
type OutcomeSummary struct {
	Status       string   `json:"status"`
	Score        float64  `json:"score,omitempty"`
	Dissents     []string `json:"dissents,omitempty"`
	Gates        []string `json:"gates,omitempty"`
	ChangedFiles []string `json:"changed_files,omitempty"`
}

// Result captures one counterfactual replay.
type Result struct {
	RunID        string                 `json:"run_id"`
	MissionID    string                 `json:"mission_id"`
	Strategy     specexec.Strategy      `json:"strategy"`
	AppliedKnobs []Knob                 `json:"applied_knobs"`
	Config       map[string]interface{} `json:"config"`
	Outcome      OutcomeSummary         `json:"outcome"`
}

// Runner executes one counterfactual strategy.
type Runner func(context.Context, specexec.Strategy, map[string]interface{}) (OutcomeSummary, error)

// Engine materializes and runs counterfactuals.
type Engine struct {
	Runner Runner
}

// Run applies knobs deterministically and executes the replay via the configured runner.
func (e Engine) Run(ctx context.Context, mission MissionSnapshot, knobs []Knob) (Result, error) {
	if e.Runner == nil {
		return Result{}, fmt.Errorf("counterfact: runner is required")
	}
	config, err := cloneMap(mission.Config)
	if err != nil {
		return Result{}, err
	}
	applied := make([]Knob, len(knobs))
	copy(applied, knobs)
	sort.Slice(applied, func(i, j int) bool {
		if applied[i].Path == applied[j].Path {
			return canonicalValue(applied[i].NewValue) < canonicalValue(applied[j].NewValue)
		}
		return applied[i].Path < applied[j].Path
	})
	for _, knob := range applied {
		if err := applyKnob(config, knob); err != nil {
			return Result{}, err
		}
	}
	runID, err := deriveRunID(mission.MissionID, applied, config)
	if err != nil {
		return Result{}, err
	}
	strategy := specexec.Strategy{
		ID:   runID,
		Name: "counterfactual",
		Tags: map[string]string{
			"mission_id": mission.MissionID,
			"mode":       "counterfactual",
		},
	}
	outcome, err := e.Runner(ctx, strategy, config)
	if err != nil {
		return Result{}, err
	}
	return Result{
		RunID:        runID,
		MissionID:    mission.MissionID,
		Strategy:     strategy,
		AppliedKnobs: applied,
		Config:       config,
		Outcome:      outcome,
	}, nil
}

func deriveRunID(missionID string, knobs []Knob, config map[string]interface{}) (string, error) {
	payload := struct {
		MissionID string                 `json:"mission_id"`
		Knobs     []Knob                 `json:"knobs"`
		Config    map[string]interface{} `json:"config"`
	}{
		MissionID: missionID,
		Knobs:     knobs,
		Config:    config,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("counterfact: derive run id: %w", err)
	}
	sum := sha256.Sum256(raw)
	return "cf-" + hex.EncodeToString(sum[:8]), nil
}

func cloneMap(src map[string]interface{}) (map[string]interface{}, error) {
	raw, err := json.Marshal(src)
	if err != nil {
		return nil, fmt.Errorf("counterfact: clone config: %w", err)
	}
	var dst map[string]interface{}
	if err := json.Unmarshal(raw, &dst); err != nil {
		return nil, fmt.Errorf("counterfact: clone config: %w", err)
	}
	return dst, nil
}

func applyKnob(config map[string]interface{}, knob Knob) error {
	if strings.TrimSpace(knob.Path) == "" {
		return fmt.Errorf("counterfact: knob path is required")
	}
	parts := strings.Split(knob.Path, ".")
	current := config
	for i := 0; i < len(parts)-1; i++ {
		part := parts[i]
		next, ok := current[part]
		if !ok {
			child := make(map[string]interface{})
			current[part] = child
			current = child
			continue
		}
		child, ok := next.(map[string]interface{})
		if !ok {
			return fmt.Errorf("counterfact: path %q crosses non-object at %q", knob.Path, part)
		}
		current = child
	}
	current[parts[len(parts)-1]] = knob.NewValue
	return nil
}

func canonicalValue(v interface{}) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(raw)
}
