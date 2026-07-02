package app

// honesty_boot.go — registers the anti-deception honesty suite
// (internal/hub/builtin.HonestyGate + internal/hub/builtin/honesty
// checkers) on the run's event bus, gated on the policy's honesty:
// block (config.HonestyConfig, parsed since A058).
//
// Registration matrix (see audit/complete-systems-2026-07-01.md A065):
//
//   - honesty.enabled          → HonestyGate (regex placeholder /
//     suppression / test-removal gate) + TestIntegrityChecker
//     (AST-shape test-weakening gate)
//   - honesty.check_imports    → ImportChecker (hallucinated-package gate)
//   - honesty.cot_monitoring   → CoTMonitor (read-only deception-marker log)
//   - honesty.claim_decomposition / honesty.confession → LLM-backed
//     ClaimDecomposer / ConfessionElicitor, registered ONLY when a judge
//     provider is available (they cost tokens). The judge model comes
//     from honesty.judge_model, falling back to the run's native model.
//
// MultiSampleChecker remains unregistered: HonestyConfig declares no
// flag for it, and inventing one is a product decision outside this
// wiring fix.

import (
	"github.com/RelayOne/r1/internal/config"
	"github.com/RelayOne/r1/internal/hub"
	hubbuiltin "github.com/RelayOne/r1/internal/hub/builtin"
	"github.com/RelayOne/r1/internal/hub/builtin/honesty"
	"github.com/RelayOne/r1/internal/provider"
)

// registerHonestySubscribers wires the honesty enforcement suite onto
// the hub event bus according to hc. judge may be nil (no LLM judge
// available); the LLM-backed checkers are skipped in that case even
// when their flags are set. Safe to call with a nil bus (no-op).
func registerHonestySubscribers(bus *hub.Bus, hc config.HonestyConfig, judge provider.Provider, judgeModel string) {
	if bus == nil || !hc.Enabled {
		return
	}

	(&hubbuiltin.HonestyGate{}).Register(bus)
	honesty.NewTestIntegrityChecker().Register(bus)

	if hc.CheckImports {
		honesty.NewImportChecker().Register(bus)
	}
	if hc.CoTMonitoring {
		honesty.NewCoTMonitor().Register(bus)
	}

	if judge == nil {
		return
	}
	if hc.ClaimDecomposition {
		honesty.NewClaimDecomposer(judge, judgeModel).Register(bus)
	}
	if hc.Confession {
		honesty.NewConfessionElicitor(judge, judgeModel).Register(bus)
	}
}
