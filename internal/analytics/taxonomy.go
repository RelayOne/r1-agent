package analytics

import (
	"regexp"
	"time"

	"github.com/RelayOne/r1/internal/hub"
)

// The 24-event PostHog taxonomy declared in specs/posthog-analytics.md §6.
//
// Event names follow the PostHog `category:object_action` convention:
// snake_case, present-tense verb-led, no PII anywhere in the name. The
// regex EventNameHygiene below enforces the shape and is asserted on
// every taxonomy entry by TestEventNameHygiene.
const (
	// 6.1 Session lifecycle (4)
	EvtSessionStarted EventName = "session_started"
	EvtSessionResumed EventName = "session_resumed"
	EvtSessionEnded   EventName = "session_ended"
	EvtSessionKilled  EventName = "session_killed"

	// 6.2 Mission lifecycle (7)
	EvtMissionStarted        EventName = "mission_started"
	EvtMissionPlanEmitted    EventName = "mission_plan_emitted"
	EvtMissionExecuteStarted EventName = "mission_execute_started"
	EvtMissionVerifyPassed   EventName = "mission_verify_passed"
	EvtMissionVerifyFailed   EventName = "mission_verify_failed"
	EvtMissionCompleted      EventName = "mission_completed"
	EvtMissionAborted        EventName = "mission_aborted"

	// 6.3 Cortex / Lobes (3)
	EvtLobeInvoked            EventName = "lobe_invoked"
	EvtCortexRoundCompleted   EventName = "cortex_round_completed"
	EvtCortexSpotlightChanged EventName = "cortex_spotlight_changed"

	// 6.4 Anti-truncation (2)
	EvtAntiTruncFired      EventName = "anti_trunc_fired"
	EvtAntiTruncOverridden EventName = "anti_trunc_overridden"

	// 6.5 Cost (2)
	EvtBudgetAlertFired  EventName = "budget_alert_fired"
	EvtCostEventRecorded EventName = "cost_event_recorded"

	// 6.6 Auth / B-track (3) — populated once A4 RelayOne SSO merges.
	EvtSSOLoginSucceeded     EventName = "sso_login_succeeded"
	EvtSSOLoginFailed        EventName = "sso_login_failed"
	EvtSessionTokenRefreshed EventName = "session_token_refreshed"

	// 6.7 Tooling (3)
	EvtToolCallSucceeded         EventName = "tool_call_succeeded"
	EvtToolCallFailed            EventName = "tool_call_failed"
	EvtPromptGuardThreatDetected EventName = "promptguard_threat_detected"
)

// EventName is the canonical analytics event name as it appears in
// PostHog. The type exists for compile-time safety on the lookup tables.
type EventName string

// String returns the canonical wire form.
func (n EventName) String() string { return string(n) }

// EventNameHygiene matches snake_case, lower-only, digit-allowed-after-letter
// — the same pattern declared in spec §11 ("event names match
// ^[a-z][a-z0-9_]*$"). TestEventNameHygiene asserts every taxonomy
// constant satisfies it.
var EventNameHygiene = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// AllEvents is the exhaustive, ordered list of taxonomy entries used by
// hygiene tests, smoke tests, and the integration test that emits one
// of every event type.
var AllEvents = []EventName{
	EvtSessionStarted, EvtSessionResumed, EvtSessionEnded, EvtSessionKilled,
	EvtMissionStarted, EvtMissionPlanEmitted, EvtMissionExecuteStarted,
	EvtMissionVerifyPassed, EvtMissionVerifyFailed, EvtMissionCompleted, EvtMissionAborted,
	EvtLobeInvoked, EvtCortexRoundCompleted, EvtCortexSpotlightChanged,
	EvtAntiTruncFired, EvtAntiTruncOverridden,
	EvtBudgetAlertFired, EvtCostEventRecorded,
	EvtSSOLoginSucceeded, EvtSSOLoginFailed, EvtSessionTokenRefreshed,
	EvtToolCallSucceeded, EvtToolCallFailed, EvtPromptGuardThreatDetected,
}

// BusToAnalytics maps hub.EventType (the in-process bus taxonomy) onto
// the PostHog event names for the event types whose analytics event
// name is static.
//
// Missing entries (e.g. EventModelPreCall) are deliberate: PostHog gets
// the post-call cost record, not the pre-call no-op.
//
// Verify-result events are routed dynamically in LookupTaxonomy based on
// the pass/fail outcome carried on hub.Event.Test. They are intentionally
// omitted from this static map so mission_verify_failed is only emitted
// for actual failing verification outcomes.
var BusToAnalytics = map[hub.EventType]EventName{
	// Session lifecycle.
	hub.EventSessionInit:       EvtSessionStarted,
	hub.EventSessionConfigured: EvtSessionResumed,
	hub.EventSessionDispose:    EvtSessionEnded,

	// Mission lifecycle.
	hub.EventMissionCreated:      EvtMissionStarted,
	hub.EventMissionPlanDone:     EvtMissionPlanEmitted,
	hub.EventMissionExecuteStart: EvtMissionExecuteStarted,
	hub.EventMissionConverged:    EvtMissionCompleted,
	hub.EventMissionFailed:       EvtMissionAborted,
	hub.EventMissionCancelled:    EvtMissionAborted,

	// Cortex / Lobes.
	hub.EventCortexLobeStarted:      EvtLobeInvoked,
	hub.EventCortexRouterDecided:    EvtCortexRoundCompleted,
	hub.EventCortexSpotlightChanged: EvtCortexSpotlightChanged,

	// Cost.
	hub.EventCostBudget50:       EvtBudgetAlertFired,
	hub.EventCostBudget80:       EvtBudgetAlertFired,
	hub.EventCostBudget90:       EvtBudgetAlertFired,
	hub.EventCostBudgetExceeded: EvtBudgetAlertFired,
	hub.EventModelPostCall:      EvtCostEventRecorded,

	// Tooling.
	hub.EventToolPostUse: EvtToolCallSucceeded,
	hub.EventToolError:   EvtToolCallFailed,
	hub.EventToolBlocked: EvtToolCallFailed,

	// Security / prompt guard.
	hub.EventSecurityInjectionRisk:  EvtPromptGuardThreatDetected,
	hub.EventSecuritySecretDetected: EvtPromptGuardThreatDetected,
}

// PropertyAdapter extracts the allowlisted property bag for one bus
// event. The adapter table is the SOLE place property extraction
// logic lives. Each adapter:
//
//   - returns only allowlisted property names (no PII, no raw text)
//   - returns enum strings, not free-form
//   - is safe on nil / partial payloads
type PropertyAdapter func(*hub.Event) map[string]any

// PropertyAdapters is the per-bus-event property hydrator. Lookup that
// misses falls through to baseAdapter, which only emits the always-on
// envelope fields (mission_id, task_id, agent_id, phase).
var PropertyAdapters = map[hub.EventType]PropertyAdapter{
	hub.EventSessionInit:       baseAdapter,
	hub.EventSessionConfigured: baseAdapter,
	hub.EventSessionDispose:    lifecycleAdapter,

	hub.EventMissionCreated:      baseAdapter,
	hub.EventMissionPlanDone:     baseAdapter,
	hub.EventMissionExecuteStart: baseAdapter,
	hub.EventMissionConverged:    lifecycleAdapter,
	hub.EventMissionFailed:       lifecycleAdapter,
	hub.EventMissionCancelled:    lifecycleAdapter,

	hub.EventVerifyBuildResult:       verifyAdapter,
	hub.EventVerifyTestResult:        verifyAdapter,
	hub.EventVerifyLintResult:        verifyAdapter,
	hub.EventVerifyConvergenceResult: verifyAdapter,

	hub.EventCortexLobeStarted:      cortexAdapter,
	hub.EventCortexRouterDecided:    cortexAdapter,
	hub.EventCortexSpotlightChanged: cortexAdapter,

	hub.EventCostBudget50:       costAdapter,
	hub.EventCostBudget80:       costAdapter,
	hub.EventCostBudget90:       costAdapter,
	hub.EventCostBudgetExceeded: costAdapter,
	hub.EventModelPostCall:      modelAdapter,

	hub.EventToolPostUse: toolAdapter,
	hub.EventToolError:   toolAdapter,
	hub.EventToolBlocked: toolAdapter,

	hub.EventSecurityInjectionRisk:  securityAdapter,
	hub.EventSecuritySecretDetected: securityAdapter,
}

// baseAdapter emits only envelope fields. All adapters call it first and
// then layer their payload-specific fields on top.
func baseAdapter(ev *hub.Event) map[string]any {
	props := make(map[string]any, 6)
	if ev == nil {
		return props
	}
	if ev.MissionID != "" {
		props["mission_id"] = ev.MissionID
	}
	if ev.TaskID != "" {
		props["task_id"] = ev.TaskID
	}
	if ev.AgentID != "" {
		props["agent_id"] = ev.AgentID
	}
	if ev.WorktreeID != "" {
		props["worktree_id"] = ev.WorktreeID
	}
	if ev.Phase != "" {
		props["phase"] = ev.Phase
	}
	props["event_id"] = ev.ID
	return props
}

func lifecycleAdapter(ev *hub.Event) map[string]any {
	props := baseAdapter(ev)
	if ev == nil || ev.Lifecycle == nil {
		return props
	}
	if ev.Lifecycle.Entity != "" {
		props["entity"] = ev.Lifecycle.Entity
	}
	if ev.Lifecycle.State != "" {
		props["state"] = ev.Lifecycle.State
	}
	if ev.Lifecycle.Duration > 0 {
		props["duration_ms"] = ev.Lifecycle.Duration / time.Millisecond
	}
	if ev.Lifecycle.Attempt > 0 {
		props["attempt"] = ev.Lifecycle.Attempt
	}
	return props
}

func verifyAdapter(ev *hub.Event) map[string]any {
	props := baseAdapter(ev)
	if ev == nil || ev.Test == nil {
		return props
	}
	props["checks_run"] = 1
	if ev.Test.Phase != "" {
		props["phase"] = ev.Test.Phase
	}
	props["passed"] = ev.Test.Passed
	props["failed"] = ev.Test.Failed
	props["skipped"] = ev.Test.Skipped
	if ev.Test.Failed > 0 && ev.Test.Phase != "" {
		props["failure_class"] = ev.Test.Phase
	}
	if ev.Test.Duration > 0 {
		props["duration_ms"] = ev.Test.Duration / time.Millisecond
	}
	if ev.Test.Coverage > 0 {
		props["coverage_pct"] = ev.Test.Coverage
	}
	return props
}

func cortexAdapter(ev *hub.Event) map[string]any {
	props := baseAdapter(ev)
	if ev == nil {
		return props
	}
	for _, key := range []string{"lobe_name", "model", "round_id", "winner_lobe", "from_lobe", "to_lobe", "reason"} {
		if v, ok := ev.Custom[key]; ok {
			props[key] = v
		}
	}
	for _, key := range []string{"input_tokens", "output_tokens", "cached_tokens", "latency_ms", "lobe_invocations", "convergence"} {
		if v, ok := ev.Custom[key]; ok {
			props[key] = v
		}
	}
	return props
}

func costAdapter(ev *hub.Event) map[string]any {
	props := baseAdapter(ev)
	if ev == nil || ev.Cost == nil {
		return props
	}
	props["total_usd"] = ev.Cost.TotalSpent
	props["budget_usd"] = ev.Cost.BudgetLimit
	props["threshold_pct"] = ev.Cost.PercentUsed
	if ev.Cost.Threshold != "" {
		props["threshold"] = ev.Cost.Threshold
	}
	return props
}

func modelAdapter(ev *hub.Event) map[string]any {
	props := baseAdapter(ev)
	if ev == nil || ev.Model == nil {
		return props
	}
	props["model"] = ev.Model.Model
	props["provider"] = ev.Model.Provider
	props["input_tokens"] = ev.Model.InputTokens
	props["output_tokens"] = ev.Model.OutputTokens
	props["cached_tokens"] = ev.Model.CachedTokens
	props["cost_usd"] = ev.Model.CostUSD
	props["duration_ms"] = ev.Model.Duration / time.Millisecond
	if ev.Model.StopReason != "" {
		props["stop_reason"] = ev.Model.StopReason
	}
	props["tool_calls"] = ev.Model.ToolCalls
	props["category"] = "lobe"
	return props
}

func toolAdapter(ev *hub.Event) map[string]any {
	props := baseAdapter(ev)
	if ev == nil || ev.Tool == nil {
		return props
	}
	props["tool_name"] = ev.Tool.Name
	if ev.Tool.Duration > 0 {
		props["duration_ms"] = ev.Tool.Duration / time.Millisecond
	}
	if ev.Tool.ExitCode != 0 {
		props["exit_code"] = ev.Tool.ExitCode
	}
	return props
}

func securityAdapter(ev *hub.Event) map[string]any {
	props := baseAdapter(ev)
	if ev == nil || ev.Security == nil {
		return props
	}
	props["category"] = ev.Security.Category
	props["severity"] = ev.Security.Severity
	if ev.Security.Rule != "" {
		props["rule"] = ev.Security.Rule
	}
	return props
}

// LookupTaxonomy returns the analytics event name and adapter for one
// concrete bus event. Verify-result events are routed dynamically based
// on the outcome carried in ev.Test so failures emit mission_verify_failed
// instead of being mislabeled as mission_verify_passed.
func LookupTaxonomy(ev *hub.Event) (EventName, PropertyAdapter, bool) {
	if ev == nil {
		return "", nil, false
	}
	name, ok := lookupEventName(ev)
	if !ok {
		return "", nil, false
	}
	adapter := PropertyAdapters[ev.Type]
	if adapter == nil {
		adapter = baseAdapter
	}
	return name, adapter, true
}

func lookupEventName(ev *hub.Event) (EventName, bool) {
	switch ev.Type {
	case hub.EventVerifyBuildResult, hub.EventVerifyTestResult, hub.EventVerifyLintResult, hub.EventVerifyConvergenceResult:
		if ev.Test != nil && ev.Test.Failed > 0 {
			return EvtMissionVerifyFailed, true
		}
		return EvtMissionVerifyPassed, true
	default:
		name, ok := BusToAnalytics[ev.Type]
		return name, ok
	}
}
