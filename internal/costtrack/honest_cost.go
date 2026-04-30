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
	TaskID                string              `json:"task_id,omitempty"`
	GeneratedAt           time.Time           `json:"generated_at"`
	TotalUSD              float64             `json:"total_usd"`
	TokenCostUSD          float64             `json:"token_cost_usd"`
	EnvironmentCostUSD    float64             `json:"environment_cost_usd"`
	HumanHourlyUSD        float64             `json:"human_hourly_usd"`
	HumanMinutes          float64             `json:"human_minutes"`
	ByModel               map[string]float64  `json:"by_model,omitempty"`
	ByProvider            map[string]float64  `json:"by_provider,omitempty"`
	ProviderGroups        []ProviderCostGroup `json:"provider_groups,omitempty"`
	EquivalentMarginUSD   float64             `json:"equivalent_margin_usd"`
	EquivalentMarginPct   float64             `json:"equivalent_margin_pct"`
	EquivalentMeteredUSD  float64             `json:"equivalent_metered_usd"`
	SubscriptionActualUSD float64             `json:"subscription_actual_usd"`
}

type ProviderCostGroup struct {
	Provider             string   `json:"provider"`
	Models               []string `json:"models,omitempty"`
	Requests             int      `json:"requests"`
	InputTokens          int      `json:"input_tokens"`
	OutputTokens         int      `json:"output_tokens"`
	CacheReadTokens      int      `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens     int      `json:"cache_write_tokens,omitempty"`
	ActualUSD            float64  `json:"actual_usd"`
	MeteredEquivalentUSD float64  `json:"metered_equivalent_usd"`
	EquivalentMarginUSD  float64  `json:"equivalent_margin_usd"`
	EquivalentMarginPct  float64  `json:"equivalent_margin_pct"`
}

type providerAggregate struct {
	models     map[string]struct{}
	requests   int
	input      int
	output     int
	cacheRead  int
	cacheWrite int
	actualUSD  float64
	meteredUSD float64
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
	providerGroups, equivalentMeteredUSD, subscriptionActualUSD := buildProviderGroups(tracker.Records())
	total := tracker.Total()
	humanMinutes := 0.0
	if humanHourlyUSD > 0 {
		humanMinutes = (total / humanHourlyUSD) * 60.0
	}
	equivalentMarginUSD := equivalentMeteredUSD - total
	equivalentMarginPct := 0.0
	if equivalentMeteredUSD > 0 {
		equivalentMarginPct = equivalentMarginUSD / equivalentMeteredUSD
	}
	return HonestCostReport{
		TaskID:                taskID,
		GeneratedAt:           time.Now().UTC(),
		TotalUSD:              total,
		TokenCostUSD:          total - tracker.EnvCost(),
		EnvironmentCostUSD:    tracker.EnvCost(),
		HumanHourlyUSD:        humanHourlyUSD,
		HumanMinutes:          humanMinutes,
		ByModel:               byModel,
		ByProvider:            byProvider,
		ProviderGroups:        providerGroups,
		EquivalentMarginUSD:   equivalentMarginUSD,
		EquivalentMarginPct:   equivalentMarginPct,
		EquivalentMeteredUSD:  equivalentMeteredUSD,
		SubscriptionActualUSD: subscriptionActualUSD,
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

func buildProviderGroups(records []Usage) ([]ProviderCostGroup, float64, float64) {
	groups := make(map[string]*providerAggregate)
	equivalentMeteredUSD := 0.0
	subscriptionActualUSD := 0.0
	for _, record := range records {
		provider := providerForModel(record.Model)
		group := groups[provider]
		if group == nil {
			group = &providerAggregate{models: map[string]struct{}{}}
			groups[provider] = group
		}
		meteredUSD := ComputeCost(record.Model, record.InputTokens, record.OutputTokens, record.CacheRead, record.CacheWrite)
		group.models[record.Model] = struct{}{}
		group.requests++
		group.input += record.InputTokens
		group.output += record.OutputTokens
		group.cacheRead += record.CacheRead
		group.cacheWrite += record.CacheWrite
		group.actualUSD += record.Cost
		group.meteredUSD += meteredUSD
		equivalentMeteredUSD += meteredUSD
		if record.Cost == 0 && meteredUSD > 0 {
			subscriptionActualUSD += record.Cost
		}
	}
	out := make([]ProviderCostGroup, 0, len(groups))
	providers := make([]string, 0, len(groups))
	for provider := range groups {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	for _, provider := range providers {
		group := groups[provider]
		models := make([]string, 0, len(group.models))
		for model := range group.models {
			models = append(models, model)
		}
		sort.Strings(models)
		meteredEquivalentUSD := group.meteredUSD
		marginUSD := meteredEquivalentUSD - group.actualUSD
		marginPct := 0.0
		if meteredEquivalentUSD > 0 {
			marginPct = marginUSD / meteredEquivalentUSD
		}
		out = append(out, ProviderCostGroup{
			Provider:             provider,
			Models:               models,
			Requests:             group.requests,
			InputTokens:          group.input,
			OutputTokens:         group.output,
			CacheReadTokens:      group.cacheRead,
			CacheWriteTokens:     group.cacheWrite,
			ActualUSD:            group.actualUSD,
			MeteredEquivalentUSD: meteredEquivalentUSD,
			EquivalentMarginUSD:  marginUSD,
			EquivalentMarginPct:  marginPct,
		})
	}
	return out, equivalentMeteredUSD, subscriptionActualUSD
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
