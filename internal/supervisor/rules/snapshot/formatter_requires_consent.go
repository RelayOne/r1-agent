package snapshot

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/RelayOne/r1-agent/internal/bus"
	"github.com/RelayOne/r1-agent/internal/ledger"
	"github.com/RelayOne/r1-agent/internal/schemaval"
	"github.com/RelayOne/r1-agent/internal/supervisor"
)

// FormatterRequiresConsent pauses workers that propose running an
// auto-formatter on a snapshot file and spawns a CTO stance for approval.
type FormatterRequiresConsent struct {
	// FormatterOnSnapshot controls whether formatting on snapshot files
	// is allowed at all. When false (default), the rule always fires.
	FormatterOnSnapshot bool
}

// NewFormatterRequiresConsent returns a new rule with default settings.
func NewFormatterRequiresConsent() *FormatterRequiresConsent {
	return &FormatterRequiresConsent{FormatterOnSnapshot: false}
}

func (r *FormatterRequiresConsent) Name() string {
	return "snapshot.formatter_requires_consent"
}

func (r *FormatterRequiresConsent) Pattern() bus.Pattern {
	return bus.Pattern{TypePrefix: "worker.action.proposed"}
}

func (r *FormatterRequiresConsent) Priority() int { return 90 }

func (r *FormatterRequiresConsent) Rationale() string {
	return "Auto-formatters may alter snapshot files in unintended ways; CTO approval is required."
}

// formatterActions are action types considered auto-formatting.
var formatterActions = map[string]bool{
	"format":     true,
	"autoformat": true,
	"lint-fix":   true,
	"gofmt":      true,
	"prettier":   true,
}

func (r *FormatterRequiresConsent) Evaluate(ctx context.Context, evt bus.Event, l *ledger.Ledger) (bool, error) {
	var ap actionPayload
	if err := json.Unmarshal(evt.Payload, &ap); err != nil {
		return false, fmt.Errorf("unmarshal action payload: %w", err)
	}

	// Must be a formatter action on a snapshot file.
	if !formatterActions[ap.ActionType] {
		return false, nil
	}
	if !ap.IsSnapshot {
		return false, nil
	}

	// If config allows formatting on snapshots, skip.
	if r.FormatterOnSnapshot {
		return false, nil
	}

	// Check for existing CTO approval. On ledger error, be
	// conservative and fire — we'd rather re-prompt for consent
	// than silently allow a formatter to rewrite a protected file.
	nodes, _ := l.Query(ctx, ledger.QueryFilter{Type: "cto.approval"})

	for _, n := range nodes {
		var ca ctoApprovalContent
		if err := json.Unmarshal(n.Content, &ca); err != nil {
			continue
		}
		if ca.Approved {
			// Check path overlap using a simple set.
			set := make(map[string]bool, len(ca.FilePaths))
			for _, p := range ca.FilePaths {
				set[p] = true
			}
			for _, p := range ap.FilePaths {
				if set[p] {
					return false, nil
				}
			}
		}
	}

	return true, nil
}

func (r *FormatterRequiresConsent) Action(ctx context.Context, evt bus.Event, b *bus.Bus) error {
	var ap actionPayload
	if err := json.Unmarshal(evt.Payload, &ap); err != nil {
		return fmt.Errorf("unmarshal action payload: %w", err)
	}

	workerID := ap.WorkerID
	if workerID == "" {
		workerID = evt.EmitterID
	}

	pauseMap := map[string]any{
		"worker_id": workerID,
		"reason":    "awaiting_cto_approval_formatter",
	}
	if vErr := supervisor.ValidatePayload(r, pauseMap); vErr != nil {
		return fmt.Errorf("payload schema violation on worker.paused: %w", vErr)
	}
	pausePayload, _ := json.Marshal(pauseMap)
	if err := b.Publish(bus.Event{
		Type:      bus.EvtWorkerPaused,
		Scope:     evt.Scope,
		Payload:   pausePayload,
		CausalRef: evt.ID,
	}); err != nil {
		return fmt.Errorf("publish pause: %w", err)
	}

	spawnPayload, _ := json.Marshal(map[string]any{
		"role":        "CTO",
		"file_paths":  ap.FilePaths,
		"action_type": ap.ActionType,
		"worker_id":   workerID,
		"reason":      "auto-formatter on snapshot file",
	})
	return b.Publish(bus.Event{
		Type:      "supervisor.spawn.requested",
		Scope:     evt.Scope,
		Payload:   spawnPayload,
		CausalRef: evt.ID,
	})
}

// PayloadSchema declares the worker.paused shape. Closes A3.
func (r *FormatterRequiresConsent) PayloadSchema() *schemaval.Schema {
	return supervisor.WorkerPausedSchema()
}
