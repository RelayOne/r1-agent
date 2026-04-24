// Package main is a deterministic projection runner for the prompt cache
// savings model exposed by internal/agentloop.CacheSavingsEstimate.
//
// This runner does NOT perform live API calls. It evaluates the pricing
// model against a fixed set of turn / token profiles so the docs team can
// show the shape of the savings curve without fabricating measurements.
//
// For real measurement, use the live telemetry path: every agentloop run
// writes cache_read_tokens / cache_write_tokens into RunResult (see
// bench/harnesses/iface.go) and into internal/stream.PromptCacheStats.
// Aggregate those fields across a corpus run to produce a measured figure.
//
// Usage:
//
//	go run ./bench/prompt_cache
//	go run ./bench/prompt_cache -json         # machine-readable output
//	go run ./bench/prompt_cache -model opus   # project opus pricing
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/ericmacdougall/stoke/internal/agentloop"
)

// profile names a representative agentic workload profile. None of these
// numbers are measured — they are illustrative inputs used to exercise the
// deterministic pricing model in internal/agentloop.CacheSavingsEstimate.
type profile struct {
	Name          string `json:"name"`
	SystemTokens  int    `json:"system_tokens"`
	ToolTokens    int    `json:"tool_tokens"`
	AvgTurnTokens int    `json:"avg_turn_tokens"`
	Turns         int    `json:"turns"`
}

// result is one row of the projection table.
type result struct {
	Profile              string  `json:"profile"`
	Model                string  `json:"model"`
	NoCacheUSD           float64 `json:"no_cache_usd"`
	CachedUSD            float64 `json:"cached_usd"`
	SavingsUSD           float64 `json:"savings_usd"`
	SavingsFraction      float64 `json:"savings_fraction"`
	InputCostReduction   string  `json:"input_cost_reduction_pct"`
}

func defaultProfiles() []profile {
	return []profile{
		{Name: "short_loop_5_turns", SystemTokens: 2_000, ToolTokens: 1_500, AvgTurnTokens: 400, Turns: 5},
		{Name: "standard_loop_20_turns", SystemTokens: 4_000, ToolTokens: 2_000, AvgTurnTokens: 600, Turns: 20},
		{Name: "long_loop_50_turns", SystemTokens: 8_000, ToolTokens: 2_500, AvgTurnTokens: 800, Turns: 50},
	}
}

func main() {
	var (
		model    = flag.String("model", "sonnet", "pricing model class: sonnet | opus | haiku")
		asJSON   = flag.Bool("json", false, "emit machine-readable JSON")
	)
	flag.Parse()

	rows := make([]result, 0, len(defaultProfiles()))
	for _, p := range defaultProfiles() {
		noCache, withCache := agentloop.CacheSavingsEstimate(
			p.SystemTokens, p.ToolTokens, p.AvgTurnTokens, p.Turns, *model,
		)
		savings := noCache - withCache
		frac := 0.0
		if noCache > 0 {
			frac = savings / noCache
		}
		rows = append(rows, result{
			Profile:            p.Name,
			Model:              *model,
			NoCacheUSD:         noCache,
			CachedUSD:          withCache,
			SavingsUSD:         savings,
			SavingsFraction:    frac,
			InputCostReduction: fmt.Sprintf("%.1f%%", frac*100),
		})
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rows); err != nil {
			fmt.Fprintln(os.Stderr, "encode:", err)
			os.Exit(1)
		}
		return
	}

	fmt.Printf("Prompt cache savings projection (model=%s)\n", *model)
	fmt.Println("Source: internal/agentloop.CacheSavingsEstimate (pricing model only; no API calls).")
	fmt.Println()
	fmt.Printf("%-26s %14s %14s %14s %12s\n",
		"PROFILE", "NO_CACHE_USD", "CACHED_USD", "SAVINGS_USD", "REDUCTION")
	for _, r := range rows {
		fmt.Printf("%-26s %14.6f %14.6f %14.6f %12s\n",
			r.Profile, r.NoCacheUSD, r.CachedUSD, r.SavingsUSD, r.InputCostReduction)
	}
	fmt.Println()
	fmt.Println("These numbers are PROJECTIONS from the pricing model, not measurements.")
	fmt.Println("For measured savings, aggregate CacheReadTokens / CacheWriteTokens from")
	fmt.Println("bench/harnesses.RunResult across a corpus run.")
}
