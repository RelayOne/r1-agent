package costtrack

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/r1dir"
)

type HonestCostReport struct {
	TaskID             string             `json:"task_id,omitempty"`
	GeneratedAt        time.Time          `json:"generated_at"`
	TotalUSD           float64            `json:"total_usd"`
	TokenCostUSD       float64            `json:"token_cost_usd"`
	EnvironmentCostUSD float64            `json:"environment_cost_usd"`
	HumanHourlyUSD     float64            `json:"human_hourly_usd"`
	HumanMinutes       float64            `json:"human_minutes"`
	ByModel            map[string]float64 `json:"by_model,omitempty"`
	ByProvider         map[string]float64 `json:"by_provider,omitempty"`
}

func BuildHonestCostReport(tracker *Tracker, taskID string, humanHourlyUSD float64) HonestCostReport {
	if tracker == nil {
		return HonestCostReport{TaskID: taskID, GeneratedAt: time.Now().UTC(), HumanHourlyUSD: humanHourlyUSD}
	}
	byModel := tracker.ByModel()
	byProvider := make(map[string]float64)
	for model, usd := range byModel {
		byProvider[providerForModel(model)] += usd
	}
	total := tracker.Total()
	humanMinutes := 0.0
	if humanHourlyUSD > 0 {
		humanMinutes = (total / humanHourlyUSD) * 60.0
	}
	return HonestCostReport{
		TaskID:             taskID,
		GeneratedAt:        time.Now().UTC(),
		TotalUSD:           total,
		TokenCostUSD:       total - tracker.EnvCost(),
		EnvironmentCostUSD: tracker.EnvCost(),
		HumanHourlyUSD:     humanHourlyUSD,
		HumanMinutes:       humanMinutes,
		ByModel:            byModel,
		ByProvider:         byProvider,
	}
}

func SaveHonestCostReport(repo string, report HonestCostReport) error {
	if report.GeneratedAt.IsZero() {
		report.GeneratedAt = time.Now().UTC()
	}
	taskID := report.TaskID
	if taskID == "" {
		taskID = "session"
	}
	path := filepath.Join("cost", taskID+".json")
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("honest cost: marshal: %w", err)
	}
	return r1dir.WriteFileFor(repo, path, append(data, '\n'), 0o644)
}

func providerForModel(model string) string {
	lower := strings.ToLower(model)
	switch {
	case strings.Contains(lower, "claude"):
		return "anthropic"
	case strings.Contains(lower, "gpt"), strings.Contains(lower, "o3"), strings.Contains(lower, "codex"):
		return "openai"
	default:
		return "other"
	}
}

func LoadSavedHonestCostReports(repo string) ([]HonestCostReport, error) {
	dir := r1dir.CanonicalPathFor(repo, "cost")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []HonestCostReport
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var rep HonestCostReport
		if err := json.Unmarshal(data, &rep); err != nil {
			continue
		}
		out = append(out, rep)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GeneratedAt.After(out[j].GeneratedAt) })
	return out, nil
}
