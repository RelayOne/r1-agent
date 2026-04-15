// Package supervisor — schemas.go
//
// Shared PayloadSchema definitions for the most common event types
// emitted by supervisor rules. A3 closes the "supervisor payloads"
// matrix row by making every rule-emitted event's shape declarable.
// Rules whose primary emitted event uses one of the shapes here
// reference the shared schema via a one-line PayloadSchema method:
//
//	func (r *MyRule) PayloadSchema() *schemaval.Schema {
//	    return WorkerPausedSchema()
//	}
//
// Rules with unique shapes define their own schema inline. See
// docs/anti-deception-matrix.md row "supervisor payloads" for the
// replay-verification rationale.

package supervisor

import "github.com/ericmacdougall/stoke/internal/schemaval"

// WorkerPausedSchema is the shape for bus.EvtWorkerPaused events:
// pause a running worker with a reason. Used by 9 rules across
// consensus, trust, snapshot, hierarchy, cross_team, and research.
func WorkerPausedSchema() *schemaval.Schema {
	return &schemaval.Schema{
		Name: "worker.paused",
		Fields: []schemaval.Field{
			{Name: "worker_id", Type: schemaval.TypeString, Required: true, MinLen: 1},
			{Name: "reason", Type: schemaval.TypeString, Required: true, MinLen: 1},
		},
	}
}

// SpawnRequestedSchema is the shape for "supervisor.spawn.requested"
// events: ask a spawner to create a fresh worker of a given role.
// Used by 14 rules across consensus, drift, hierarchy, research,
// skill, snapshot, trust, cross_team, and sdm.
//
// role + reason are required; task_id / loop_id / worker_id /
// artifact_id are context-specific optional fields. Keeping them
// all optional in the shared schema — rules with stricter contracts
// (e.g. consensus rules that MUST have loop_id) should publish
// under a rule-specific schema override.
func SpawnRequestedSchema() *schemaval.Schema {
	return &schemaval.Schema{
		Name: "supervisor.spawn.requested",
		Fields: []schemaval.Field{
			{Name: "role", Type: schemaval.TypeString, Required: true, MinLen: 1},
			{Name: "reason", Type: schemaval.TypeString, Required: false},
			{Name: "task_id", Type: schemaval.TypeString, Required: false},
			{Name: "loop_id", Type: schemaval.TypeString, Required: false},
			{Name: "worker_id", Type: schemaval.TypeString, Required: false},
			{Name: "artifact_id", Type: schemaval.TypeString, Required: false},
			{Name: "is_replacement", Type: schemaval.TypeBool, Required: false},
			{Name: "web_search", Type: schemaval.TypeBool, Required: false},
			{Name: "researcher_index", Type: schemaval.TypeNumber, Required: false},
		},
	}
}

// EscalationForwardedSchema is the shape for
// "supervisor.escalation.forwarded" events: propagate a consensus
// timeout or unresolved decision up the hierarchy. Used by partner
// timeout, escalation-forwards-upward, and user-escalation rules.
func EscalationForwardedSchema() *schemaval.Schema {
	return &schemaval.Schema{
		Name: "supervisor.escalation.forwarded",
		Fields: []schemaval.Field{
			{Name: "loop_id", Type: schemaval.TypeString, Required: true, MinLen: 1},
			{Name: "reason", Type: schemaval.TypeString, Required: true, MinLen: 1},
			{Name: "partner_id", Type: schemaval.TypeString, Required: false},
			{Name: "role", Type: schemaval.TypeString, Required: false},
		},
	}
}

// ConsensusLoopStateSchema is the shape for
// "consensus.loop.state.changed" events: report a state-machine
// transition in a consensus loop. Used by convergence-detected and
// dissent-requires-address rules.
func ConsensusLoopStateSchema() *schemaval.Schema {
	return &schemaval.Schema{
		Name: "consensus.loop.state.changed",
		Fields: []schemaval.Field{
			{Name: "loop_id", Type: schemaval.TypeString, Required: true, MinLen: 1},
			{Name: "state", Type: schemaval.TypeString, Required: true, MinLen: 1},
		},
	}
}
