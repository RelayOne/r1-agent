// Package snapshot implements supervisor rules that protect snapshot files
// from unauthorized modifications.
package snapshot

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/schemaval"
	"github.com/RelayOne/r1/internal/supervisor"
)

// ModificationRequiresCTO pauses workers that propose changes to snapshot
// files and spawns a CTO stance for approval.
type ModificationRequiresCTO struct {
	// SnapshotPaths lists file paths or prefixes considered snapshot files.
	// If empty, the rule checks the action payload for snapshot markers.
	SnapshotPaths []string
}

// NewModificationRequiresCTO returns a new rule with default settings.
func NewModificationRequiresCTO() *ModificationRequiresCTO {
	return &ModificationRequiresCTO{}
}

func (r *ModificationRequiresCTO) Name() string {
	return "snapshot.modification_requires_cto"
}

func (r *ModificationRequiresCTO) Pattern() bus.Pattern {
	return bus.Pattern{TypePrefix: "worker.action.proposed"}
}

func (r *ModificationRequiresCTO) Priority() int { return 95 }

func (r *ModificationRequiresCTO) Rationale() string {
	return "Snapshot files are immutable records; modifications require CTO approval."
}

// actionPayload is the expected structure inside a worker action proposed event.
type actionPayload struct {
	WorkerID   string   `json:"worker_id"`
	ActionType string   `json:"action_type"`
	FilePaths  []string `json:"file_paths"`
	IsSnapshot bool     `json:"is_snapshot"`
}

// ctoApprovalContent represents a CTO approval node in the ledger.
type ctoApprovalContent struct {
	FilePaths []string `json:"file_paths"`
	Approved  bool     `json:"approved"`
}

func (r *ModificationRequiresCTO) Evaluate(ctx context.Context, evt bus.Event, l *ledger.Ledger) (bool, error) {
	var ap actionPayload
	if err := json.Unmarshal(evt.Payload, &ap); err != nil {
		return false, fmt.Errorf("unmarshal action payload: %w", err)
	}

	targetsSnapshot := ap.IsSnapshot || r.matchesSnapshotPaths(ap.FilePaths)
	if !targetsSnapshot {
		return false, nil
	}

	// Check if CTO approval already exists. On ledger error, be
	// conservative and fire — missing approval view must not let
	// a snapshot modification through.
	nodes, _ := l.Query(ctx, ledger.QueryFilter{Type: "cto.approval"})

	for _, n := range nodes {
		var ca ctoApprovalContent
		if err := json.Unmarshal(n.Content, &ca); err != nil {
			continue
		}
		if ca.Approved && r.pathsOverlap(ca.FilePaths, ap.FilePaths) {
			return false, nil
		}
	}

	return true, nil
}

func (r *ModificationRequiresCTO) matchesSnapshotPaths(paths []string) bool {
	if len(r.SnapshotPaths) == 0 {
		return false
	}
	for _, fp := range paths {
		for _, sp := range r.SnapshotPaths {
			if fp == sp || len(fp) > len(sp) && fp[:len(sp)] == sp {
				return true
			}
		}
	}
	return false
}

func (r *ModificationRequiresCTO) pathsOverlap(a, b []string) bool {
	set := make(map[string]bool, len(a))
	for _, p := range a {
		set[p] = true
	}
	for _, p := range b {
		if set[p] {
			return true
		}
	}
	return false
}

func (r *ModificationRequiresCTO) Action(ctx context.Context, evt bus.Event, b *bus.Bus) error {
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
		"reason":    "awaiting_cto_approval_snapshot",
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
		"role":       "CTO",
		"file_paths": ap.FilePaths,
		"worker_id":  workerID,
		"reason":     "snapshot file modification",
	})
	return b.Publish(bus.Event{
		Type:      "supervisor.spawn.requested",
		Scope:     evt.Scope,
		Payload:   spawnPayload,
		CausalRef: evt.ID,
	})
}

// PayloadSchema declares the worker.paused shape. Closes A3.
func (r *ModificationRequiresCTO) PayloadSchema() *schemaval.Schema {
	return supervisor.WorkerPausedSchema()
}

// HookPriority places this rule near the top of the hook stack so a
// snapshot modification is paused BEFORE any lower-priority hook can
// inspect or annotate it. 1000 leaves room for both higher-priority
// gates and lower-priority observers.
func (r *ModificationRequiresCTO) HookPriority() bus.HookPriority { return 1000 }

// HookAction is the gate-authority counterpart of Action. Where
// Action publishes worker.paused + supervisor.spawn.requested AFTER
// the triggering event has been delivered to subscribers, HookAction
// returns those same effects as a *bus.HookAction so the bus can
// fold them into the publish path. The materialization done inside
// the bus's fireHooks (PauseWorker → worker.paused, SpawnWorker →
// supervisor.spawn.requested) means callers don't need to construct
// the events themselves.
//
// Returning a non-nil HookAction here makes this rule a privileged
// hook (gate). The subscribe-path Action() still runs so observability
// (supervisor.rule.fired) is unchanged; the hook path adds veto
// authority on top of observation.
func (r *ModificationRequiresCTO) HookAction(ctx context.Context, evt bus.Event) (*bus.HookAction, error) {
	var ap actionPayload
	if err := json.Unmarshal(evt.Payload, &ap); err != nil {
		return nil, fmt.Errorf("hook action unmarshal: %w", err)
	}
	workerID := ap.WorkerID
	if workerID == "" {
		workerID = evt.EmitterID
	}
	return &bus.HookAction{
		PauseWorker: workerID,
		SpawnWorker: &bus.SpawnRequest{
			Role:  "CTO",
			Scope: evt.Scope,
			Context: map[string]any{
				"file_paths": ap.FilePaths,
				"reason":     "snapshot file modification",
				"worker_id":  workerID,
				"rule":       r.Name(),
			},
		},
	}, nil
}
