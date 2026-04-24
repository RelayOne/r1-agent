package research

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/schemaval"
)

// Timeout handles researcher timeout events. If the researcher has not yet
// committed a report, it marks them as timed out and optionally spawns a
// replacement.
type Timeout struct {
	// DefaultTimeout is the standard research timeout.
	DefaultTimeout time.Duration
	// DeepResearchTimeout is the timeout for deep research questions.
	DeepResearchTimeout time.Duration
}

// NewTimeout returns a rule with default configuration.
func NewTimeout() *Timeout {
	return &Timeout{
		DefaultTimeout:      10 * time.Minute,
		DeepResearchTimeout: 30 * time.Minute,
	}
}

func (r *Timeout) Name() string { return "research.timeout" }

func (r *Timeout) Pattern() bus.Pattern {
	return bus.Pattern{TypePrefix: "research.timeout"}
}

func (r *Timeout) Priority() int { return 75 }

func (r *Timeout) Rationale() string {
	return "Researchers that exceed their time budget must be replaced or the requester informed."
}

// researchTimeoutPayload is the expected shape of a research timeout event.
type researchTimeoutPayload struct {
	ResearcherID string `json:"researcher_id"`
	RequesterID  string `json:"requester_id"`
	Question     string `json:"question"`
	Deep         bool   `json:"deep"`
}

func (r *Timeout) Evaluate(ctx context.Context, evt bus.Event, l *ledger.Ledger) (bool, error) {
	var tp researchTimeoutPayload
	if err := json.Unmarshal(evt.Payload, &tp); err != nil {
		return false, fmt.Errorf("unmarshal timeout payload: %w", err)
	}

	if tp.ResearcherID == "" {
		return false, nil
	}

	// Check if the researcher has already committed a report.
	// On ledger error, be conservative and fire: we'd rather act
	// on a stale report-absence signal than swallow a potential
	// timeout. The ledger error itself is logged when encountered
	// elsewhere; here an empty result means "no report visible",
	// which is equivalent to "fire".
	nodes, _ := l.Query(ctx, ledger.QueryFilter{
		Type:      "research.report",
		CreatedBy: tp.ResearcherID,
	})

	// If any report exists from this researcher, skip.
	if len(nodes) > 0 {
		return false, nil
	}
	return true, nil
}

func (r *Timeout) Action(_ context.Context, evt bus.Event, b *bus.Bus) error {
	var tp researchTimeoutPayload
	if err := json.Unmarshal(evt.Payload, &tp); err != nil {
		return fmt.Errorf("unmarshal timeout payload: %w", err)
	}

	// Mark the researcher as timed out.
	timeoutPayload, _ := json.Marshal(map[string]any{
		"worker_id": tp.ResearcherID,
		"reason":    "research_timeout",
	})
	if err := b.Publish(bus.Event{
		Type:      bus.EvtWorkerTerminated,
		Scope:     evt.Scope,
		Payload:   timeoutPayload,
		CausalRef: evt.ID,
	}); err != nil {
		return fmt.Errorf("publish termination: %w", err)
	}

	if tp.Question != "" {
		// Spawn a replacement researcher.
		spawnPayload, _ := json.Marshal(map[string]any{
			"role":         "Researcher",
			"question":     tp.Question,
			"requester_id": tp.RequesterID,
			"reason":       "replacement_after_timeout",
		})
		return b.Publish(bus.Event{
			Type:      "supervisor.spawn.requested",
			Scope:     evt.Scope,
			Payload:   spawnPayload,
			CausalRef: evt.ID,
		})
	}

	// No question available — signal that research couldn't be completed.
	failPayload, _ := json.Marshal(map[string]any{
		"requester_id": tp.RequesterID,
		"reason":       "research_timeout_no_replacement",
	})
	return b.Publish(bus.Event{
		Type:      "worker.research.completed",
		Scope:     evt.Scope,
		Payload:   failPayload,
		CausalRef: evt.ID,
	})
}
// PayloadSchema returns nil — this rule emits mixed: emits worker.terminated, worker.research.completed, or spawn depending on branch,
// for which no shared schema exists in internal/supervisor/schemas.go
// yet. Equivalent to not implementing PayloadSchemaProvider.
// Tightening pass: add the specific schema + wire ValidatePayload.
func (r *Timeout) PayloadSchema() *schemaval.Schema {
	return nil
}
