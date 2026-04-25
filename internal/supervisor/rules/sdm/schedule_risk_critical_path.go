package sdm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/RelayOne/r1-agent/internal/bus"
	"github.com/RelayOne/r1-agent/internal/ledger"
	"github.com/RelayOne/r1-agent/internal/schemaval"
)

// ScheduleRiskCriticalPath detects when one branch's progress is blocking
// others disproportionately.
type ScheduleRiskCriticalPath struct{}

// NewScheduleRiskCriticalPath returns a new rule instance.
func NewScheduleRiskCriticalPath() *ScheduleRiskCriticalPath {
	return &ScheduleRiskCriticalPath{}
}

func (r *ScheduleRiskCriticalPath) Name() string { return "sdm.schedule_risk_critical_path" }

func (r *ScheduleRiskCriticalPath) Pattern() bus.Pattern {
	return bus.Pattern{TypePrefix: "task.timing.update"}
}

func (r *ScheduleRiskCriticalPath) Priority() int { return 40 }

func (r *ScheduleRiskCriticalPath) Rationale() string {
	return "Critical path bottlenecks in one branch can cascade delays across the entire mission."
}

// timingUpdatePayload is the expected shape of a task.timing.update event.
type timingUpdatePayload struct {
	TaskID        string  `json:"task_id"`
	BranchID      string  `json:"branch_id"`
	ProgressPct   float64 `json:"progress_pct"`
	BlockedCount  int     `json:"blocked_count"` // number of tasks blocked by this one
	EstimatedMins float64 `json:"estimated_mins"`
}

func (r *ScheduleRiskCriticalPath) Evaluate(_ context.Context, evt bus.Event, _ *ledger.Ledger) (bool, error) {
	var tp timingUpdatePayload
	if err := json.Unmarshal(evt.Payload, &tp); err != nil {
		return false, fmt.Errorf("unmarshal timing update: %w", err)
	}

	// Fire if this task blocks multiple others and is behind schedule.
	if tp.BlockedCount >= 2 && tp.ProgressPct < 50 {
		return true, nil
	}
	return false, nil
}

func (r *ScheduleRiskCriticalPath) Action(_ context.Context, evt bus.Event, b *bus.Bus) error {
	var tp timingUpdatePayload
	_ = json.Unmarshal(evt.Payload, &tp)

	payload, _ := json.Marshal(map[string]any{
		"advisory":       true,
		"type":           "schedule_risk",
		"task_id":        tp.TaskID,
		"branch_id":      tp.BranchID,
		"progress_pct":   tp.ProgressPct,
		"blocked_count":  tp.BlockedCount,
		"estimated_mins": tp.EstimatedMins,
		"trigger_id":     evt.ID,
	})
	return b.Publish(bus.Event{
		Type:      "sdm.schedule_risk.detected",
		Scope:     evt.Scope,
		Payload:   payload,
		CausalRef: evt.ID,
	})
}
// PayloadSchema returns nil — this rule emits sdm.schedule_risk.detected — SDM-specific shape,
// for which no shared schema exists in internal/supervisor/schemas.go
// yet. Equivalent to not implementing PayloadSchemaProvider.
// Tightening pass: add the specific schema + wire ValidatePayload.
func (r *ScheduleRiskCriticalPath) PayloadSchema() *schemaval.Schema {
	return nil
}
