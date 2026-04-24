package skill

import (
	"context"
	"encoding/json"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/schemaval"
)

// LoadAudit records every skill load in the supervisor's audit trail.
// It is non-blocking and never pauses any worker.
type LoadAudit struct{}

// NewLoadAudit returns a new rule instance.
func NewLoadAudit() *LoadAudit {
	return &LoadAudit{}
}

func (r *LoadAudit) Name() string { return "skill.load_audit" }

func (r *LoadAudit) Pattern() bus.Pattern {
	return bus.Pattern{TypePrefix: string(bus.EvtSkillLoaded)}
}

func (r *LoadAudit) Priority() int { return 30 }

func (r *LoadAudit) Rationale() string {
	return "All skill loads should be recorded for auditability and debugging."
}

func (r *LoadAudit) Evaluate(_ context.Context, _ bus.Event, _ *ledger.Ledger) (bool, error) {
	return true, nil
}

func (r *LoadAudit) Action(_ context.Context, evt bus.Event, b *bus.Bus) error {
	payload, _ := json.Marshal(map[string]any{
		"action":     "audit_recorded",
		"skill_event": evt.ID,
		"emitter":    evt.EmitterID,
	})
	return b.Publish(bus.Event{
		Type:      "supervisor.audit.skill_load",
		Scope:     evt.Scope,
		Payload:   payload,
		CausalRef: evt.ID,
	})
}
// PayloadSchema returns nil — this rule emits supervisor.audit.skill_load — unique audit shape,
// for which no shared schema exists in internal/supervisor/schemas.go
// yet. Equivalent to not implementing PayloadSchemaProvider.
// Tightening pass: add the specific schema + wire ValidatePayload.
func (r *LoadAudit) PayloadSchema() *schemaval.Schema {
	return nil
}
