// Package nodes — hitl.go
//
// Dual-mode ledger node types for human-in-the-loop (HITL)
// interaction: HITLRequest (the agent asks a human for approval /
// guidance / a clarification), HITLResponse (the human's answer),
// Intervention (a human-initiated redirect / injection / abort
// mid-session), and Replanning (a planner-level regeneration of the
// plan tree following an Intervention or a terminal failure).
//
// These node types exist so every human touchpoint is auditable on
// the same content-addressed ledger as the agent's autonomous work.
// Without them, HITL exchanges live in chat logs that aren't part of
// the ledger's integrity chain — breaking the "every action is
// accountable" invariant Stoke depends on.
//
// All four types follow the existing pattern in this package:
// NodeTyper interface, SchemaVersion via the Version field, a
// Validate() that enforces required fields, and init-time
// Register() so nodes.New("hitl_request") returns a zero-value
// instance for deserialization.
package nodes

import (
	"fmt"
	"time"
)

// HITLRequest records an agent-initiated request for human input.
// ID prefix: hitl-req-
type HITLRequest struct {
	// Who asked: stance ID + session ID so the request is
	// traceable back to the exact execution context.
	StanceRole string `json:"stance_role"`
	SessionID  string `json:"session_id"`

	// What's being asked. Kind is one of:
	//   "approval"       — the agent needs a human to say yes/no
	//   "clarification"  — the agent hit ambiguity in the spec
	//   "guidance"       — the agent needs a judgment call on how
	//                       to proceed (not a binary)
	//   "escalation"     — the agent failed and is handing off
	Kind     string `json:"kind"`
	Question string `json:"question"`

	// Context the human needs to answer. Typically a ref to the
	// failing AC, the ambiguous spec excerpt, or a diff the human
	// needs to review.
	ContextRefs []string `json:"context_refs,omitempty"`

	// When the request was emitted.
	When time.Time `json:"when"`

	// DeadlineSeconds: how long before the agent treats silence as
	// a decline and falls back to a safe default (or escalates
	// further). Zero means "wait indefinitely".
	DeadlineSeconds int `json:"deadline_seconds,omitempty"`

	// LoopRef ties this request back to the consensus loop that
	// produced it, if any.
	LoopRef string `json:"loop_ref,omitempty"`

	Version int `json:"schema_version"`
}

func (r *HITLRequest) NodeType() string   { return "hitl_request" }
func (r *HITLRequest) SchemaVersion() int { return r.Version }

var validHITLKinds = map[string]bool{
	"approval":      true,
	"clarification": true,
	"guidance":      true,
	"escalation":    true,
}

func (r *HITLRequest) Validate() error {
	if r.StanceRole == "" {
		return fmt.Errorf("hitl_request: stance_role is required")
	}
	if r.SessionID == "" {
		return fmt.Errorf("hitl_request: session_id is required")
	}
	if r.Kind == "" {
		return fmt.Errorf("hitl_request: kind is required")
	}
	if !validHITLKinds[r.Kind] {
		return fmt.Errorf("hitl_request: invalid kind %q (want approval|clarification|guidance|escalation)", r.Kind)
	}
	if r.Question == "" {
		return fmt.Errorf("hitl_request: question is required")
	}
	if r.When.IsZero() {
		return fmt.Errorf("hitl_request: when is required")
	}
	if r.DeadlineSeconds < 0 {
		return fmt.Errorf("hitl_request: deadline_seconds cannot be negative")
	}
	return nil
}

func init() {
	Register("hitl_request", func() NodeTyper { return &HITLRequest{Version: 1} })
}

// HITLResponse records the human's answer to an HITLRequest. Never
// emitted without a matching RequestID so the two nodes can be
// tied together by downstream consumers (the ledger's edge model
// also ties them structurally; RequestID is the content-level
// redundancy in case an edge is missing).
//
// ID prefix: hitl-resp-
type HITLResponse struct {
	// RequestID is the content ID of the HITLRequest this answers.
	RequestID string `json:"request_id"`

	// Decision is one of:
	//   "approved"  — proceed as requested
	//   "rejected"  — do not proceed; optional Reasoning explains
	//   "modified"  — proceed but with adjustments; ModifiedScope
	//                  carries the adjusted plan/scope
	//   "deferred"  — human can't answer yet; Reasoning should say
	//                  when to retry
	//   "timed_out" — emitted by the system when DeadlineSeconds
	//                  elapsed; no human acted
	Decision string `json:"decision"`

	// ResponderID is the identity of the human who responded.
	// Empty for "timed_out" (system-emitted). Should be an opaque
	// user identifier — don't put PII in the ledger.
	ResponderID string `json:"responder_id,omitempty"`

	// Reasoning is the human's free-form justification. Kept so
	// the agent's next turn can read WHY, not just WHAT.
	Reasoning string `json:"reasoning,omitempty"`

	// ModifiedScope is set when Decision == "modified". Carries
	// the adjusted plan / constraints / scope the agent must now
	// respect.
	ModifiedScope string `json:"modified_scope,omitempty"`

	// When the response was recorded.
	When time.Time `json:"when"`

	Version int `json:"schema_version"`
}

func (r *HITLResponse) NodeType() string   { return "hitl_response" }
func (r *HITLResponse) SchemaVersion() int { return r.Version }

var validHITLDecisions = map[string]bool{
	"approved": true, "rejected": true, "modified": true,
	"deferred": true, "timed_out": true,
}

func (r *HITLResponse) Validate() error {
	if r.RequestID == "" {
		return fmt.Errorf("hitl_response: request_id is required")
	}
	if r.Decision == "" {
		return fmt.Errorf("hitl_response: decision is required")
	}
	if !validHITLDecisions[r.Decision] {
		return fmt.Errorf("hitl_response: invalid decision %q", r.Decision)
	}
	if r.Decision == "modified" && r.ModifiedScope == "" {
		return fmt.Errorf("hitl_response: modified_scope is required when decision=modified")
	}
	if r.When.IsZero() {
		return fmt.Errorf("hitl_response: when is required")
	}
	// timed_out can have empty responder. All other decisions
	// require a responder.
	if r.Decision != "timed_out" && r.ResponderID == "" {
		return fmt.Errorf("hitl_response: responder_id is required for decision %q", r.Decision)
	}
	return nil
}

func init() {
	Register("hitl_response", func() NodeTyper { return &HITLResponse{Version: 1} })
}

// Intervention records a human-initiated action taken on a running
// session WITHOUT the agent having asked first. Covers dual-mode
// chat priority-arbiter events: ABORT, REDIRECT, INJECT, PAUSE.
// Distinguished from HITLResponse because the agent didn't ask —
// the human broke in.
//
// ID prefix: intv-
type Intervention struct {
	// SessionID + TaskID identify the targeted execution.
	SessionID string `json:"session_id"`
	TaskID    string `json:"task_id,omitempty"`

	// Kind is one of:
	//   "abort"    — terminate the session; revert in-flight work
	//   "redirect" — cancel current plan, execute new directive
	//   "inject"   — add a constraint/instruction without cancel
	//   "pause"    — halt execution, preserve state, await resume
	//   "resume"   — unpause
	Kind string `json:"kind"`

	// Directive is the human's instruction. For "pause" and
	// "abort" it's usually empty / a brief reason. For "redirect"
	// and "inject" it's the actual new guidance.
	Directive string `json:"directive,omitempty"`

	// InitiatorID is the identity of the human who intervened.
	InitiatorID string `json:"initiator_id"`

	// When the intervention hit the event bus.
	When time.Time `json:"when"`

	// Priority is set by the priority arbiter so downstream
	// handlers know the preemption rank
	// (abort > redirect > inject > continue).
	Priority int `json:"priority"`

	Version int `json:"schema_version"`
}

func (i *Intervention) NodeType() string   { return "intervention" }
func (i *Intervention) SchemaVersion() int { return i.Version }

var validInterventionKinds = map[string]bool{
	"abort": true, "redirect": true, "inject": true,
	"pause": true, "resume": true,
}

func (i *Intervention) Validate() error {
	if i.SessionID == "" {
		return fmt.Errorf("intervention: session_id is required")
	}
	if i.Kind == "" {
		return fmt.Errorf("intervention: kind is required")
	}
	if !validInterventionKinds[i.Kind] {
		return fmt.Errorf("intervention: invalid kind %q", i.Kind)
	}
	if i.InitiatorID == "" {
		return fmt.Errorf("intervention: initiator_id is required")
	}
	if i.When.IsZero() {
		return fmt.Errorf("intervention: when is required")
	}
	// redirect/inject require a directive (otherwise what's the
	// human asking for?). abort/pause/resume can be silent.
	if (i.Kind == "redirect" || i.Kind == "inject") && i.Directive == "" {
		return fmt.Errorf("intervention: directive is required for kind=%q", i.Kind)
	}
	return nil
}

func init() {
	Register("intervention", func() NodeTyper { return &Intervention{Version: 1} })
}

// Replanning records a planner-level regeneration of a session's
// plan tree. Triggered by a terminal execution failure, an
// Intervention with kind=redirect, a cross-session spec drift, or
// an operator-initiated replan.
//
// ID prefix: replan-
type Replanning struct {
	// SessionID is the session being replanned.
	SessionID string `json:"session_id"`

	// Trigger is the reason the replan fired. One of:
	//   "terminal_failure"     — session hit max-repair and escalated
	//   "intervention_redirect" — human issued redirect intervention
	//   "spec_drift"           — upstream spec changed, cascade replan
	//   "operator_request"     — manual `stoke replan`
	Trigger string `json:"trigger"`

	// TriggerRef is the content ID of the node that triggered the
	// replan (the failing task node, the Intervention, the new
	// spec node). Lets auditors walk back the causal chain.
	TriggerRef string `json:"trigger_ref,omitempty"`

	// OldPlanRef / NewPlanRef are the content IDs of the pre-
	// and post-replan plan tree roots, so the diff is
	// recoverable from the ledger alone.
	OldPlanRef string `json:"old_plan_ref,omitempty"`
	NewPlanRef string `json:"new_plan_ref"`

	// Rationale is the planner's explanation of what changed
	// and why. Required: a replan without a stated rationale is
	// exactly the kind of silent scope drift Stoke exists to
	// prevent.
	Rationale string `json:"rationale"`

	// NodesAffected is the count of plan nodes that changed from
	// old to new. Used to decide "repair" (inner, <3 nodes) vs
	// "replan" (outer, >=3 nodes) per the SOWPlan revision
	// protocol.
	NodesAffected int `json:"nodes_affected"`

	// When the replan completed.
	When time.Time `json:"when"`

	Version int `json:"schema_version"`
}

func (r *Replanning) NodeType() string   { return "replanning" }
func (r *Replanning) SchemaVersion() int { return r.Version }

var validReplanTriggers = map[string]bool{
	"terminal_failure":       true,
	"intervention_redirect":  true,
	"spec_drift":             true,
	"operator_request":       true,
}

func (r *Replanning) Validate() error {
	if r.SessionID == "" {
		return fmt.Errorf("replanning: session_id is required")
	}
	if r.Trigger == "" {
		return fmt.Errorf("replanning: trigger is required")
	}
	if !validReplanTriggers[r.Trigger] {
		return fmt.Errorf("replanning: invalid trigger %q", r.Trigger)
	}
	if r.NewPlanRef == "" {
		return fmt.Errorf("replanning: new_plan_ref is required")
	}
	if r.Rationale == "" {
		return fmt.Errorf("replanning: rationale is required")
	}
	if r.NodesAffected < 0 {
		return fmt.Errorf("replanning: nodes_affected cannot be negative")
	}
	if r.When.IsZero() {
		return fmt.Errorf("replanning: when is required")
	}
	return nil
}

func init() {
	Register("replanning", func() NodeTyper { return &Replanning{Version: 1} })
}
