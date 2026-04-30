package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/RelayOne/r1/internal/metrics"
	"github.com/RelayOne/r1/internal/r1skill/interp"
)

type metricsCollectionRuntimeInput struct {
	Prefix string   `json:"prefix"`
	Kinds  []string `json:"kinds"`
}

type metricsCollectionRuntimeOutput struct {
	QuerySlug string                          `json:"query_slug"`
	Mode      string                          `json:"mode"`
	Summary   string                          `json:"summary"`
	Filters   metricsCollectionRuntimeFilters `json:"filters"`
	Counters  []metricsValueEntry             `json:"counters"`
	Gauges    []metricsValueEntry             `json:"gauges"`
	Timers    []metricsTimerEntry             `json:"timers"`
	Costs     []metricsCostEntry              `json:"costs"`
	Followups []string                        `json:"followups"`
}

type metricsCollectionRuntimeFilters struct {
	Prefix string   `json:"prefix,omitempty"`
	Kinds  []string `json:"kinds,omitempty"`
}

type metricsValueEntry struct {
	Name  string `json:"name"`
	Value int64  `json:"value"`
}

type metricsTimerEntry struct {
	Name    string `json:"name"`
	Count   int64  `json:"count"`
	TotalNS int64  `json:"total_ns"`
	MinNS   int64  `json:"min_ns"`
	MaxNS   int64  `json:"max_ns"`
	AvgNS   int64  `json:"avg_ns"`
}

type metricsCostEntry struct {
	Name     string  `json:"name"`
	TotalUSD float64 `json:"total_usd"`
	Count    int64   `json:"count"`
}

func metricsCollectionRuntime(reg *metrics.Registry) interp.PureFunc {
	return func(input json.RawMessage) (json.RawMessage, error) {
		if reg == nil {
			return nil, fmt.Errorf("metrics registry not configured")
		}
		var req metricsCollectionRuntimeInput
		if len(input) > 0 && string(input) != "null" {
			if err := json.Unmarshal(input, &req); err != nil {
				return nil, fmt.Errorf("decode input: %w", err)
			}
		}
		req.Prefix = strings.TrimSpace(req.Prefix)
		kinds, err := normalizeMetricKinds(req.Kinds)
		if err != nil {
			return nil, err
		}

		snapshot := reg.Snapshot()
		out := metricsCollectionRuntimeOutput{
			QuerySlug: "metrics-collection",
			Mode:      "read-only",
			Filters: metricsCollectionRuntimeFilters{
				Prefix: req.Prefix,
				Kinds:  kinds,
			},
			Counters: metricValueEntries(snapshot.Counters, req.Prefix, includeMetricKind(kinds, "counters")),
			Gauges:   metricValueEntries(snapshot.Gauges, req.Prefix, includeMetricKind(kinds, "gauges")),
			Timers:   metricTimerEntries(snapshot.Timers, req.Prefix, includeMetricKind(kinds, "timers")),
			Costs:    metricCostEntries(snapshot.Costs, req.Prefix, includeMetricKind(kinds, "costs")),
			Followups: []string{
				"Re-run with a narrower prefix when you need a single subsystem slice for export or operator review.",
				"Capture a second snapshot after the critical action to compare rate, latency, or spend drift over the same prefix.",
			},
		}
		out.Summary = buildMetricsCollectionSummary(out)
		return json.Marshal(out)
	}
}

func normalizeMetricKinds(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		kind := strings.ToLower(strings.TrimSpace(item))
		switch kind {
		case "counters", "gauges", "timers", "costs":
		case "":
			continue
		default:
			return nil, fmt.Errorf("kinds must only contain counters, gauges, timers, or costs")
		}
		if _, ok := seen[kind]; ok {
			continue
		}
		seen[kind] = struct{}{}
		out = append(out, kind)
	}
	sort.Strings(out)
	return out, nil
}

func includeMetricKind(kinds []string, want string) bool {
	if len(kinds) == 0 {
		return true
	}
	for _, kind := range kinds {
		if kind == want {
			return true
		}
	}
	return false
}

func metricValueEntries(values map[string]int64, prefix string, include bool) []metricsValueEntry {
	if !include {
		return []metricsValueEntry{}
	}
	keys := filteredMetricKeysInt(values, prefix)
	out := make([]metricsValueEntry, 0, len(keys))
	for _, key := range keys {
		out = append(out, metricsValueEntry{Name: key, Value: values[key]})
	}
	return out
}

func metricTimerEntries(values map[string]metrics.TimerSnapshot, prefix string, include bool) []metricsTimerEntry {
	if !include {
		return []metricsTimerEntry{}
	}
	keys := filteredMetricKeysTimer(values, prefix)
	out := make([]metricsTimerEntry, 0, len(keys))
	for _, key := range keys {
		item := values[key]
		out = append(out, metricsTimerEntry{
			Name:    key,
			Count:   item.Count,
			TotalNS: int64(item.Total),
			MinNS:   int64(item.Min),
			MaxNS:   int64(item.Max),
			AvgNS:   int64(item.Avg),
		})
	}
	return out
}

func metricCostEntries(values map[string]metrics.CostSnapshot, prefix string, include bool) []metricsCostEntry {
	if !include {
		return []metricsCostEntry{}
	}
	keys := filteredMetricKeysCost(values, prefix)
	out := make([]metricsCostEntry, 0, len(keys))
	for _, key := range keys {
		item := values[key]
		out = append(out, metricsCostEntry{Name: key, TotalUSD: item.Total, Count: item.Count})
	}
	return out
}

func filteredMetricKeysInt(values map[string]int64, prefix string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func filteredMetricKeysTimer(values map[string]metrics.TimerSnapshot, prefix string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func filteredMetricKeysCost(values map[string]metrics.CostSnapshot, prefix string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func buildMetricsCollectionSummary(out metricsCollectionRuntimeOutput) string {
	parts := []string{"Collected runtime metrics snapshot"}
	if out.Filters.Prefix != "" {
		parts = append(parts, fmt.Sprintf("for prefix %q", out.Filters.Prefix))
	}
	if len(out.Filters.Kinds) > 0 {
		parts = append(parts, fmt.Sprintf("across %s", strings.Join(out.Filters.Kinds, ", ")))
	}
	parts = append(parts, fmt.Sprintf("counters=%d", len(out.Counters)))
	parts = append(parts, fmt.Sprintf("gauges=%d", len(out.Gauges)))
	parts = append(parts, fmt.Sprintf("timers=%d", len(out.Timers)))
	parts = append(parts, fmt.Sprintf("costs=%d", len(out.Costs)))
	return strings.Join(parts, "; ") + "."
}
