// Package coderadar — canonical CodeRadar event schema (B3 dogfood).
//
// This file defines the 18 canonical R1 → CodeRadar events, the
// per-event property allowlist (T7 enforcement), the schema version
// table, and the canonical-name slice used by smoke tests + cardinality
// lints.
//
// Adding or removing an entry here is a breaking change for the
// CodeRadar admin dashboard. Bump schema_version and coordinate with
// the sibling coderadar-admin repo before merging.
package coderadar

import "time"

// Event is the canonical CodeRadar event envelope. All 18 R1 events
// share this shape. Props is allowlisted per Name (see AllowedProps).
type Event struct {
	Name          string         `json:"name"`
	SchemaVersion string         `json:"schema_version"`
	Service       string         `json:"service"`
	Env           string         `json:"env"`
	Timestamp     time.Time      `json:"timestamp"`
	CorrelationID string         `json:"correlation_id,omitempty"`
	TenantID      string         `json:"tenant_id,omitempty"`
	LatencyMs     int64          `json:"latency_ms,omitempty"`
	EventID       string         `json:"event_id,omitempty"`
	Props         map[string]any `json:"props,omitempty"`
}

// Canonical event names (snake_case, dotted) — 18 total.
const (
	// Service lifecycle (3)
	EventServiceStarted     = "service_started"
	EventServiceStopped     = "service_stopped"
	EventServiceHealthCheck = "service_health_check"

	// Mission (6)
	EventMissionPlan      = "mission.plan"
	EventMissionExecute   = "mission.execute"
	EventMissionVerify    = "mission.verify"
	EventMissionReview    = "mission.review"
	EventMissionCompleted = "mission.completed"
	EventMissionAborted   = "mission.aborted"

	// Cortex (2)
	EventCortexLobeInvoked     = "cortex.lobe_invoked"
	EventCortexRoundCompleted  = "cortex.round_completed"

	// Anti-trunc (2)
	EventAntiTruncFired      = "antitrunc.fired"
	EventAntiTruncOverridden = "antitrunc.overridden"

	// Provider (4)
	EventProviderCallStarted   = "provider.call_started"
	EventProviderCallCompleted = "provider.call_completed"
	EventProviderCallErrored   = "provider.call_errored"
	EventProviderFallback      = "provider.fallback"

	// Tooling (1)
	EventToolCallCompleted = "tool.call_completed"
)

// CanonicalEvents enumerates all 18 canonical event names in spec order.
// Smoke tests and cardinality lints iterate over this slice to assert
// that no unexpected names leak from the subscriber path.
var CanonicalEvents = []string{
	EventServiceStarted,
	EventServiceStopped,
	EventServiceHealthCheck,
	EventMissionPlan,
	EventMissionExecute,
	EventMissionVerify,
	EventMissionReview,
	EventMissionCompleted,
	EventMissionAborted,
	EventCortexLobeInvoked,
	EventCortexRoundCompleted,
	EventAntiTruncFired,
	EventAntiTruncOverridden,
	EventProviderCallStarted,
	EventProviderCallCompleted,
	EventProviderCallErrored,
	EventProviderFallback,
	EventToolCallCompleted,
}

// SchemaVersions pins the wire-schema version per canonical event.
// Bumping a version requires a coordinated change in coderadar-admin.
var SchemaVersions = map[string]string{
	EventServiceStarted:        "1.0.0",
	EventServiceStopped:        "1.0.0",
	EventServiceHealthCheck:    "1.0.0",
	EventMissionPlan:           "1.0.0",
	EventMissionExecute:        "1.0.0",
	EventMissionVerify:         "1.0.0",
	EventMissionReview:         "1.0.0",
	EventMissionCompleted:      "1.0.0",
	EventMissionAborted:        "1.0.0",
	EventCortexLobeInvoked:     "1.0.0",
	EventCortexRoundCompleted:  "1.0.0",
	EventAntiTruncFired:        "1.0.0",
	EventAntiTruncOverridden:   "1.0.0",
	EventProviderCallStarted:   "1.0.0",
	EventProviderCallCompleted: "1.0.0",
	EventProviderCallErrored:   "1.0.0",
	EventProviderFallback:      "1.0.0",
	EventToolCallCompleted:     "1.0.0",
}

// Standard correlation props attached on every event when present.
const (
	PropR1SessionID = "r1.session_id"
	PropR1AgentID   = "r1.agent_id"
	PropR1TaskID    = "r1.task_id"
)

// AllowedProps is the per-event HARD allowlist enforced by the
// redactor. Any prop not listed is dropped silently before the event
// reaches the wire. The correlation-id props (r1.*) are appended to
// every list at registration time below.
var AllowedProps = map[string][]string{
	EventServiceStarted: {
		"version", "pid", "commit_sha",
	},
	EventServiceStopped: {
		"reason", "uptime_ms",
	},
	EventServiceHealthCheck: {
		"status", "subsystems", "coderadar_circuit_open",
	},

	EventMissionPlan: {
		"mission_id", "plan_steps", "model",
	},
	EventMissionExecute: {
		"mission_id", "tasks_dispatched",
	},
	EventMissionVerify: {
		"mission_id", "verify_kind",
	},
	EventMissionReview: {
		"mission_id", "reviewer",
	},
	EventMissionCompleted: {
		"mission_id", "duration_ms", "total_cost_usd",
	},
	EventMissionAborted: {
		"mission_id", "abort_reason",
	},

	EventCortexLobeInvoked: {
		"lobe_name", "mission_id",
	},
	EventCortexRoundCompleted: {
		"round", "notes_published",
	},

	EventAntiTruncFired: {
		"pattern_matched", "phase", "evt_id",
	},
	EventAntiTruncOverridden: {
		"override_actor", "justification_hash",
	},

	EventProviderCallStarted: {
		"provider", "model",
		"gen_ai.operation.name", "gen_ai.system", "gen_ai.request.model",
	},
	EventProviderCallCompleted: {
		"provider", "model",
		"input_tokens", "output_tokens", "cost_usd", "cached_tokens",
		"stop_reason", "duration_ms",
		"gen_ai.system", "gen_ai.request.model",
		"gen_ai.usage.input_tokens", "gen_ai.usage.output_tokens",
		"gen_ai.operation.name",
	},
	EventProviderCallErrored: {
		"provider", "model", "error_class", "http_status",
		"error_message", "error_message_sha256",
	},
	EventProviderFallback: {
		"from_model", "to_model", "reason",
	},

	EventToolCallCompleted: {
		"tool_name", "exit_code", "duration_ms",
		"file_path_sha256",
	},
}

// commonProps is appended to every AllowedProps entry — correlation
// IDs are always emittable when present.
var commonProps = []string{
	PropR1SessionID, PropR1AgentID, PropR1TaskID,
}

func init() {
	// Append correlation props to every event's allowlist so callers
	// never have to re-declare them.
	for name, ps := range AllowedProps {
		AllowedProps[name] = append(ps, commonProps...)
	}
}

// IsCanonical reports whether name appears in CanonicalEvents.
func IsCanonical(name string) bool {
	_, ok := AllowedProps[name]
	return ok
}
