// Package research implements supervisor rules for dispatching and managing
// researcher stances that answer questions on behalf of workers.
package research

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ericmacdougall/stoke/internal/bus"
	"github.com/ericmacdougall/stoke/internal/ledger"
	"github.com/ericmacdougall/stoke/internal/schemaval"
	"github.com/ericmacdougall/stoke/internal/supervisor"
)

// RequestDispatchesResearchers pauses the requesting worker and spawns one or
// more researcher stances to answer the question.
type RequestDispatchesResearchers struct {
	// MaxParallelResearchers caps how many researchers can run concurrently.
	MaxParallelResearchers int
	// HighStakesThreshold is the minimum stakes level that triggers multiple researchers.
	HighStakesThreshold int
}

// NewRequestDispatchesResearchers returns a rule with default configuration.
func NewRequestDispatchesResearchers() *RequestDispatchesResearchers {
	return &RequestDispatchesResearchers{
		MaxParallelResearchers: 3,
		HighStakesThreshold:    2,
	}
}

func (r *RequestDispatchesResearchers) Name() string {
	return "research.request_dispatches_researchers"
}

func (r *RequestDispatchesResearchers) Pattern() bus.Pattern {
	return bus.Pattern{TypePrefix: "worker.research.requested"}
}

func (r *RequestDispatchesResearchers) Priority() int { return 85 }

func (r *RequestDispatchesResearchers) Rationale() string {
	return "Research requests require dedicated stances; high-stakes questions get multiple researchers for cross-verification."
}

// researchRequestPayload is the expected shape of a research request event.
type researchRequestPayload struct {
	WorkerID     string `json:"worker_id"`
	Question     string `json:"question"`
	ConcernField string `json:"concern_field"`
	StakesLevel  int    `json:"stakes_level"` // 0=low, 1=medium, 2+=high
	WebSearch    bool   `json:"web_search"`
}

func (r *RequestDispatchesResearchers) Evaluate(_ context.Context, _ bus.Event, _ *ledger.Ledger) (bool, error) {
	// Always fires on research requests.
	return true, nil
}

func (r *RequestDispatchesResearchers) Action(_ context.Context, evt bus.Event, b *bus.Bus) error {
	var rp researchRequestPayload
	if err := json.Unmarshal(evt.Payload, &rp); err != nil {
		return fmt.Errorf("unmarshal research request: %w", err)
	}

	workerID := rp.WorkerID
	if workerID == "" {
		workerID = evt.EmitterID
	}

	// Pause the requesting worker.
	pausePayload, _ := json.Marshal(map[string]string{
		"worker_id": workerID,
		"reason":    "awaiting_research",
	})
	if err := b.Publish(bus.Event{
		Type:      bus.EvtWorkerPaused,
		Scope:     evt.Scope,
		Payload:   pausePayload,
		CausalRef: evt.ID,
	}); err != nil {
		return fmt.Errorf("publish pause: %w", err)
	}

	// Determine number of researchers.
	numResearchers := 1
	if rp.StakesLevel >= r.HighStakesThreshold {
		numResearchers = r.MaxParallelResearchers
	}

	// Spawn researchers.
	for i := 0; i < numResearchers; i++ {
		spawnPayload, _ := json.Marshal(map[string]any{
			"role":          "Researcher",
			"question":      rp.Question,
			"concern_field": rp.ConcernField,
			"web_search":    rp.WebSearch,
			"requester_id":  workerID,
			"researcher_index": i,
			"total_researchers": numResearchers,
		})
		if err := b.Publish(bus.Event{
			Type:      "supervisor.spawn.requested",
			Scope:     evt.Scope,
			Payload:   spawnPayload,
			CausalRef: evt.ID,
		}); err != nil {
			return fmt.Errorf("publish spawn researcher %d: %w", i, err)
		}
	}

	return nil
}

// PayloadSchema declares the worker.paused shape. Closes A3.
func (r *RequestDispatchesResearchers) PayloadSchema() *schemaval.Schema {
	return supervisor.WorkerPausedSchema()
}
