package antitrunc

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/RelayOne/r1/internal/antitrunc"
	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/ledger"
)

// ScopeUnderdelivery fires when a worker declares completion but the
// referenced plan / spec checklist still has unchecked items. This
// catches the "spec 9 done — merging" pattern even when the worker's
// summary text avoids the truncation phrase regex.
//
// The rule is configured with a list of plan/spec paths the worker's
// scope covers. Each path is read and parsed via
// antitrunc.ScopeReportFromFile; the rule fires if ANY referenced
// path has unchecked items.
type ScopeUnderdelivery struct {
	// PlanPaths is the list of plan/spec markdown files this rule
	// covers. Empty disables the rule (Evaluate always returns
	// false). Wired by the manifest with the active mission's
	// plans/build-plan.md and the spec under construction.
	PlanPaths []string
}

// NewScopeUnderdelivery returns a rule with no paths configured.
// Manifest wiring populates PlanPaths.
func NewScopeUnderdelivery(paths ...string) *ScopeUnderdelivery {
	return &ScopeUnderdelivery{PlanPaths: paths}
}

func (r *ScopeUnderdelivery) Name() string { return "antitrunc.scope_underdelivery" }

// Pattern matches worker.declaration.done — that's where a worker
// claims a task is complete. The rule reads the plan and refuses
// the claim if the underlying scope is incomplete.
func (r *ScopeUnderdelivery) Pattern() bus.Pattern {
	return bus.Pattern{TypePrefix: string(bus.EvtWorkerDeclarationDone)}
}

// Priority MUST be higher than trust.completion_requires_second_opinion
// (priority 100) so we publish the underdelivery firing BEFORE the
// reviewer is spawned. Otherwise the reviewer might agree before
// this rule can flag scope drift.
func (r *ScopeUnderdelivery) Priority() int { return 150 }

func (r *ScopeUnderdelivery) Rationale() string {
	return "A done declaration must not pass while the underlying plan / spec has unchecked items."
}

func (r *ScopeUnderdelivery) Evaluate(ctx context.Context, evt bus.Event, l *ledger.Ledger) (bool, error) {
	if len(r.PlanPaths) == 0 {
		return false, nil
	}
	for _, p := range r.PlanPaths {
		rep, err := antitrunc.ScopeReportFromFile(p)
		if err != nil {
			continue // unreadable file is not a firing signal
		}
		if rep.Total > 0 && !rep.IsComplete() {
			return true, nil
		}
	}
	return false, nil
}

func (r *ScopeUnderdelivery) Action(ctx context.Context, evt bus.Event, b *bus.Bus) error {
	type report struct {
		Path  string `json:"path"`
		Done  int    `json:"done"`
		Total int    `json:"total"`
	}
	var reports []report
	for _, p := range r.PlanPaths {
		rep, err := antitrunc.ScopeReportFromFile(p)
		if err != nil {
			continue
		}
		if rep.Total > 0 && !rep.IsComplete() {
			reports = append(reports, report{
				Path:  rep.Path,
				Done:  rep.Done,
				Total: rep.Total,
			})
		}
	}
	payload := map[string]any{
		"category": "antitrunc",
		"rule":     r.Name(),
		"severity": "critical",
		"reports":  reports,
		"detail":   fmt.Sprintf("worker declared done with %d plans/specs underdelivered", len(reports)),
	}
	body, _ := json.Marshal(payload)
	return b.Publish(bus.Event{
		Type:      bus.EvtSupervisorRuleFired,
		Scope:     evt.Scope,
		Payload:   body,
		CausalRef: evt.ID,
	})
}
