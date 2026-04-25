package drift

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/RelayOne/r1-agent/internal/bus"
	"github.com/RelayOne/r1-agent/internal/ledger"
	"github.com/RelayOne/r1-agent/internal/schemaval"
)

// BudgetThreshold monitors mission cost against configurable thresholds and
// takes progressively stronger action as spending increases.
type BudgetThreshold struct {
	WarningPct  float64 // emit warning (default 50)
	CheckPct    float64 // spawn Judge (default 80)
	EscalatePct float64 // escalate to user via PO (default 100)
	StopPct     float64 // hard stop (default 120)
}

// NewBudgetThreshold returns a rule with default configuration.
func NewBudgetThreshold() *BudgetThreshold {
	return &BudgetThreshold{
		WarningPct:  50,
		CheckPct:    80,
		EscalatePct: 100,
		StopPct:     120,
	}
}

func (r *BudgetThreshold) Name() string { return "drift.budget_threshold" }

func (r *BudgetThreshold) Pattern() bus.Pattern {
	return bus.Pattern{TypePrefix: "mission.budget.update"}
}

func (r *BudgetThreshold) Priority() int { return 80 }

func (r *BudgetThreshold) Rationale() string {
	return "Progressive budget enforcement prevents runaway cost without abrupt termination."
}

// budgetPayload is the expected shape of a budget update event.
type budgetPayload struct {
	SpentUSD  float64 `json:"spent_usd"`
	BudgetUSD float64 `json:"budget_usd"`
}

func (r *BudgetThreshold) Evaluate(_ context.Context, evt bus.Event, _ *ledger.Ledger) (bool, error) {
	var bp budgetPayload
	if err := json.Unmarshal(evt.Payload, &bp); err != nil {
		return false, fmt.Errorf("unmarshal budget payload: %w", err)
	}
	if bp.BudgetUSD <= 0 {
		return false, nil
	}
	pct := (bp.SpentUSD / bp.BudgetUSD) * 100
	return pct >= r.WarningPct, nil
}

func (r *BudgetThreshold) Action(_ context.Context, evt bus.Event, b *bus.Bus) error {
	var bp budgetPayload
	if err := json.Unmarshal(evt.Payload, &bp); err != nil {
		return fmt.Errorf("unmarshal budget payload: %w", err)
	}
	if bp.BudgetUSD <= 0 {
		return nil
	}

	pct := (bp.SpentUSD / bp.BudgetUSD) * 100

	if pct >= r.StopPct {
		payload, _ := json.Marshal(map[string]any{
			"action":      "hard_stop",
			"spent_usd":   bp.SpentUSD,
			"budget_usd":  bp.BudgetUSD,
			"pct_used":    pct,
			"reason":      "budget_exceeded",
			"threshold":   r.StopPct,
		})
		return b.Publish(bus.Event{
			Type:      "mission.budget.hard_stop",
			Scope:     evt.Scope,
			Payload:   payload,
			CausalRef: evt.ID,
		})
	}

	if pct >= r.EscalatePct {
		payload, _ := json.Marshal(map[string]any{
			"action":     "escalate_to_user",
			"role":       "PO",
			"spent_usd":  bp.SpentUSD,
			"budget_usd": bp.BudgetUSD,
			"pct_used":   pct,
			"threshold":  r.EscalatePct,
		})
		return b.Publish(bus.Event{
			Type:      "supervisor.escalation.requested",
			Scope:     evt.Scope,
			Payload:   payload,
			CausalRef: evt.ID,
		})
	}

	if pct >= r.CheckPct {
		payload, _ := json.Marshal(map[string]any{
			"role":       "Judge",
			"reason":     "budget_check",
			"spent_usd":  bp.SpentUSD,
			"budget_usd": bp.BudgetUSD,
			"pct_used":   pct,
			"threshold":  r.CheckPct,
		})
		return b.Publish(bus.Event{
			Type:      "supervisor.spawn.requested",
			Scope:     evt.Scope,
			Payload:   payload,
			CausalRef: evt.ID,
		})
	}

	// Warning level.
	payload, _ := json.Marshal(map[string]any{
		"action":     "warning",
		"spent_usd":  bp.SpentUSD,
		"budget_usd": bp.BudgetUSD,
		"pct_used":   pct,
		"threshold":  r.WarningPct,
	})
	return b.Publish(bus.Event{
		Type:      "mission.budget.warning",
		Scope:     evt.Scope,
		Payload:   payload,
		CausalRef: evt.ID,
	})
}
// PayloadSchema returns nil — this rule emits mixed: emits worker.paused + supervisor.spawn.requested on different paths; no single shape fits,
// for which no shared schema exists in internal/supervisor/schemas.go
// yet. Equivalent to not implementing PayloadSchemaProvider.
// Tightening pass: add the specific schema + wire ValidatePayload.
func (r *BudgetThreshold) PayloadSchema() *schemaval.Schema {
	return nil
}
