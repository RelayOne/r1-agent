package research

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/schemaval"
)

// ReportUnblocksRequester fires when all dispatched researchers have completed
// (or timed out), and unpauses the requesting worker with the research report.
type ReportUnblocksRequester struct{}

// NewReportUnblocksRequester returns a new rule instance.
func NewReportUnblocksRequester() *ReportUnblocksRequester {
	return &ReportUnblocksRequester{}
}

func (r *ReportUnblocksRequester) Name() string {
	return "research.report_unblocks_requester"
}

func (r *ReportUnblocksRequester) Pattern() bus.Pattern {
	return bus.Pattern{TypePrefix: "worker.research.completed"}
}

func (r *ReportUnblocksRequester) Priority() int { return 85 }

func (r *ReportUnblocksRequester) Rationale() string {
	return "The requesting worker must remain paused until all dispatched researchers report back."
}

// researchCompletedPayload is the expected shape of a research completion event.
type researchCompletedPayload struct {
	RequesterID      string `json:"requester_id"`
	ResearcherIndex  int    `json:"researcher_index"`
	TotalResearchers int    `json:"total_researchers"`
	Report           string `json:"report"`
	ConcernField     string `json:"concern_field"`
}

func (r *ReportUnblocksRequester) Evaluate(ctx context.Context, evt bus.Event, l *ledger.Ledger) (bool, error) {
	var cp researchCompletedPayload
	if err := json.Unmarshal(evt.Payload, &cp); err != nil {
		return false, fmt.Errorf("unmarshal research completed: %w", err)
	}

	if cp.Report == "" {
		return false, nil // report missing required fields
	}

	if cp.TotalResearchers <= 1 {
		return true, nil // single researcher, fire immediately
	}

	// Check if all researchers have completed by querying ledger for
	// research report nodes in this mission/task scope.
	nodes, err := l.Query(ctx, ledger.QueryFilter{
		Type:      "research.report",
		MissionID: evt.Scope.MissionID,
	})
	if err != nil {
		return false, fmt.Errorf("query research reports: %w", err)
	}

	// Count completed reports for this requester (including the current one).
	completedCount := 1 // count current event
	for _, n := range nodes {
		var rc struct {
			RequesterID string `json:"requester_id"`
			TaskID      string `json:"task_id"`
		}
		if err := json.Unmarshal(n.Content, &rc); err != nil {
			continue
		}
		if rc.RequesterID == cp.RequesterID && rc.TaskID == evt.Scope.TaskID {
			completedCount++
		}
	}

	return completedCount >= cp.TotalResearchers, nil
}

func (r *ReportUnblocksRequester) Action(_ context.Context, evt bus.Event, b *bus.Bus) error {
	var cp researchCompletedPayload
	if err := json.Unmarshal(evt.Payload, &cp); err != nil {
		return fmt.Errorf("unmarshal research completed: %w", err)
	}

	requesterID := cp.RequesterID
	if requesterID == "" {
		requesterID = evt.EmitterID
	}

	resumePayload, _ := json.Marshal(map[string]any{
		"worker_id":     requesterID,
		"reason":        "research_completed",
		"report":        cp.Report,
		"concern_field": cp.ConcernField,
	})
	return b.Publish(bus.Event{
		Type:      bus.EvtWorkerResumed,
		Scope:     evt.Scope,
		Payload:   resumePayload,
		CausalRef: evt.ID,
	})
}
// PayloadSchema returns nil — this rule emits worker.resumed — shape not in shared schemas yet,
// for which no shared schema exists in internal/supervisor/schemas.go
// yet. Equivalent to not implementing PayloadSchemaProvider.
// Tightening pass: add the specific schema + wire ValidatePayload.
func (r *ReportUnblocksRequester) PayloadSchema() *schemaval.Schema {
	return nil
}
