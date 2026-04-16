// Package plan — node.go
//
// STOKE-001: SOWPlan 9-state node machine with hierarchical WBS +
// explicit causal links. This file introduces the richer plan shape
// — Node, CausalLink, State, transition table, rollup, revision
// protocol — WITHOUT disturbing the existing flat plan.SOW /
// plan.Session / plan.Task types. Current callers keep working;
// new code can opt into Node, and a migration can convert SOW.Tasks
// into a Node tree incrementally.
//
// Why additive rather than replacing: plan.SOW is consumed by
// dozens of files across cmd/stoke/ + scheduler/ + orchestrate/ +
// workflow/ + session/. Any breaking rename would touch the
// critical path. The Node type sits alongside the existing model
// so the migration can land in small, reviewable pieces rather
// than one flag-day commit.
//
// Scope of this file:
//   - Node struct with wbs + type hierarchy + acceptance criteria +
//     causal links (dependsOn / enables) + revision fields
//   - CausalLink struct with 4 link types (finish-to-start,
//     artifact-dependency, information-dependency, approval-gate)
//   - 9 state constants exported (DRAFT / READY / ACTIVE /
//     COMPLETED / VERIFIED / BLOCKED / WAITING_HUMAN /
//     NEEDS_REVISION / CANCELED)
//   - Transition table as a `map[State]map[State]bool` — every
//     allowed transition is declared, every other transition
//     returns ErrInvalidTransition
//   - RollupStatus(parent, children) function that derives the
//     parent's status from its children deterministically
//   - Revision protocol helpers: ClassifyRevision (repair vs
//     replan) with the 3-node-affected threshold, and the
//     MaxNeedsRevisionCycles (3) cap
//   - Tests in node_test.go exercise every allowed + forbidden
//     transition, the rollup function, the revision classifier,
//     and the escalation cap
package plan

import (
	"fmt"
	"time"
)

// State is a Node's position in the 9-state plan machine.
type State string

// The 9 states. String values match the SOW spec verbatim so
// downstream tooling (report renderers, ledger serializers) can
// key off the exact names.
const (
	StateDraft         State = "DRAFT"
	StateReady         State = "READY"
	StateActive        State = "ACTIVE"
	StateCompleted     State = "COMPLETED"
	StateVerified      State = "VERIFIED"
	StateBlocked       State = "BLOCKED"
	StateWaitingHuman  State = "WAITING_HUMAN"
	StateNeedsRevision State = "NEEDS_REVISION"
	StateCanceled      State = "CANCELED"
)

// allStates is the authoritative list for AllStates() +
// transition-table exhaustiveness checks in tests.
var allStates = []State{
	StateDraft, StateReady, StateActive, StateCompleted,
	StateVerified, StateBlocked, StateWaitingHuman,
	StateNeedsRevision, StateCanceled,
}

// AllStates returns the 9 declared states. Order is stable for
// determinism in reports + tests.
func AllStates() []State {
	out := make([]State, len(allStates))
	copy(out, allStates)
	return out
}

// NodeType is the WBS hierarchy level of a Node. Keeping this a
// string (not enum int) so it serializes readably in JSON and
// ledger output.
type NodeType string

const (
	NodeTypeRoot    NodeType = "root"
	NodeTypeSection NodeType = "section"
	NodeTypeItem    NodeType = "item"
	NodeTypeSubtask NodeType = "subtask"
)

// LinkType tags a CausalLink so the scheduler + verifier can
// apply the right semantics (a finish-to-start needs the
// upstream COMPLETED; an approval-gate needs a matching
// HITLResponse.decision=approved; etc.).
type LinkType string

const (
	// LinkFinishToStart: downstream can't enter ACTIVE until
	// upstream reaches COMPLETED.
	LinkFinishToStart LinkType = "finish-to-start"

	// LinkArtifactDependency: downstream requires a specific
	// artifact (file, blob, build output) produced by upstream.
	LinkArtifactDependency LinkType = "artifact-dependency"

	// LinkInformationDependency: downstream needs an
	// informational output (decision, review result,
	// documentation) from upstream but not a build artifact.
	LinkInformationDependency LinkType = "information-dependency"

	// LinkApprovalGate: downstream can't proceed without an
	// explicit human approval recorded via an HITLResponse
	// with decision=approved.
	LinkApprovalGate LinkType = "approval-gate"
)

// CausalLink ties two Nodes together in the dependency DAG. The
// scheduler evaluates Satisfied by consulting the link's type and
// the upstream node's state + produced artifacts.
type CausalLink struct {
	FromNodeID string   `json:"from_node_id"`
	ToNodeID   string   `json:"to_node_id"`
	Type       LinkType `json:"type"`

	// Condition is an optional free-form expression
	// (descriptive, not machine-evaluated) explaining when the
	// link is satisfied. Captured so audit readers can follow
	// the planner's reasoning without re-running the planner.
	Condition string `json:"condition,omitempty"`

	// Satisfied is the runtime flag the scheduler flips when
	// the link's precondition is met. Persisted so resume-
	// after-crash doesn't lose satisfaction state.
	Satisfied bool `json:"satisfied"`
}

// MaxNeedsRevisionCycles is the hard cap on per-Node revision
// attempts. Once a Node hits this many NEEDS_REVISION transitions,
// the scheduler escalates UP the hierarchy (sibling replan, then
// parent replan, then HITL) rather than looping forever.
const MaxNeedsRevisionCycles = 3

// ReplanThresholdNodes is the fanout at which a NEEDS_REVISION is
// classified as a `replan` (outer loop, HITL approval required)
// rather than a `repair` (inner loop, auto). Set to 3 per the
// SOW: > 3 nodes affected ⇒ replan.
const ReplanThresholdNodes = 3

// Node is one entry in the SOWPlan hierarchical DAG. Carries its
// own ID (content-address-derivable from the fields below), its
// WBS address ("1.2.3"), its type, its parent + children refs, its
// acceptance criteria, its dependency links, and the revision
// counters needed to enforce the 3-cycle cap.
type Node struct {
	// ID is the Node's stable identifier. Typically derived from
	// a content hash over (WBS, Title, Description, Version) so
	// two planners emitting the same intent produce the same ID.
	// Callers are free to use any unique ID scheme — this field
	// is opaque to the transition machinery.
	ID string `json:"id"`

	// WBS is the work-breakdown-structure address (e.g. "1.2.3").
	// The root node has WBS "1" (or "" — both are accepted). WBS
	// is how operators refer to a node in reports and chat
	// without quoting IDs.
	WBS string `json:"wbs"`

	// Version increments every time the node is re-planned or
	// revised. PreviousVersionID links back so revision history
	// is walkable.
	Version int `json:"version"`

	// Type is root / section / item / subtask. A root Node has
	// no parent; a subtask Node has no children.
	Type NodeType `json:"type"`

	// ParentID is "" for a root Node.
	ParentID string   `json:"parent_id,omitempty"`
	ChildIDs []string `json:"child_ids,omitempty"`

	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	AcceptanceCriteria []string `json:"acceptance_criteria,omitempty"`

	// Status is the Node's current State. Changes go through
	// SetState(), never direct assignment, so the transition
	// table enforces validity.
	Status State `json:"status"`

	// DependsOn / Enables are the causal edges incident on this
	// node. DependsOn lists edges pointing IN (upstream); Enables
	// lists edges pointing OUT (downstream). Both views are
	// stored so the scheduler doesn't have to walk the whole
	// graph to answer "what's my fanout?".
	DependsOn []CausalLink `json:"depends_on,omitempty"`
	Enables   []CausalLink `json:"enables,omitempty"`

	// Result + Artifacts carry the outputs a completed node
	// produced. Artifacts are opaque IDs (paths, content hashes,
	// URLs) the scheduler can refer to when validating a
	// downstream LinkArtifactDependency.
	Result    string   `json:"result,omitempty"`
	Artifacts []string `json:"artifacts,omitempty"`

	// BlockedBy carries the list of IDs / descriptions that are
	// holding this node's state. Set when Status is BLOCKED or
	// WAITING_HUMAN; cleared on transition out of those states.
	BlockedBy []string `json:"blocked_by,omitempty"`

	// RevisionReason is the free-form explanation attached the
	// last time this node transitioned to NEEDS_REVISION. Kept
	// so the revising planner has full context without a
	// re-reasoning round-trip.
	RevisionReason   string    `json:"revision_reason,omitempty"`
	RevisionAttempts int       `json:"revision_attempts"`
	PreviousVersionID string   `json:"previous_version_id,omitempty"`
	UpdatedAt        time.Time `json:"updated_at,omitempty"`
}

// ErrInvalidTransition is returned when SetState is called with a
// pair not in the transition table. Intentionally exported so
// callers can errors.Is() against it.
var ErrInvalidTransition = fmt.Errorf("plan: invalid state transition")

// ErrRevisionCapReached is returned by BumpRevision when the
// MaxNeedsRevisionCycles cap would be exceeded. Callers must
// escalate (to sibling replan / parent replan / HITL) rather than
// force the cap higher.
var ErrRevisionCapReached = fmt.Errorf("plan: NEEDS_REVISION cap reached; escalate")

// transitions is the explicit allowed-transitions table. A
// transition is permitted iff transitions[from][to] is true.
// Mutating this map at runtime is a programming error — tests
// enforce exhaustiveness + stability.
//
// Shape of the table: every State appears as a From key (even
// terminal states like CANCELED, which map to an empty set so
// the table is exhaustive). Absence of an entry under From[To]
// means the transition is forbidden.
var transitions = map[State]map[State]bool{
	StateDraft: {
		StateReady:         true, // planner finished drafting
		StateCanceled:      true, // operator scrapped before start
	},
	StateReady: {
		StateActive:        true, // scheduler picked up
		StateBlocked:       true, // dependency not satisfied
		StateWaitingHuman:  true, // approval gate fired
		StateCanceled:      true,
	},
	StateActive: {
		StateCompleted:     true, // worker finished
		StateBlocked:       true, // hit a dependency mid-flight
		StateWaitingHuman:  true, // need approval mid-flight
		StateNeedsRevision: true, // critic rejected
		StateCanceled:      true,
	},
	StateCompleted: {
		StateVerified:      true, // reviewer signed off
		StateNeedsRevision: true, // reviewer rejected
		// CANCELED not allowed from COMPLETED — cancel before
		// completion, or revise after. Completion is a
		// committed decision.
	},
	StateVerified: {
		// Terminal happy-path. Re-entering the graph requires a
		// replanning event that creates a NEW node version.
	},
	StateBlocked: {
		StateReady:         true, // dependency satisfied
		StateActive:        true, // resumed directly
		StateCanceled:      true,
	},
	StateWaitingHuman: {
		StateActive:        true, // approval received
		StateReady:         true, // approval received, requeued
		StateCanceled:      true, // rejection = cancel
		StateNeedsRevision: true, // human asked for changes
	},
	StateNeedsRevision: {
		StateDraft:         true, // replan — plan is being rewritten
		StateReady:         true, // repair complete, ready to retry
		StateActive:        true, // repair started directly
		StateCanceled:      true, // give up after cap reached
	},
	StateCanceled: {
		// Terminal. Once canceled, a new version is needed to
		// re-enter the graph.
	},
}

// allowedNextStates returns the list of states a node in `from`
// may transition to. Used by tests + UI to render "what can
// happen next?" without the caller replicating the table.
func allowedNextStates(from State) []State {
	tbl, ok := transitions[from]
	if !ok {
		return nil
	}
	out := make([]State, 0, len(tbl))
	for s := range tbl {
		out = append(out, s)
	}
	return out
}

// AllowedNextStates is the exported helper for allowedNextStates.
// Order of the returned slice is unstable (map iteration) — callers
// that need determinism should sort.
func AllowedNextStates(from State) []State { return allowedNextStates(from) }

// SetState transitions n.Status from its current value to next.
// Returns ErrInvalidTransition if the pair isn't permitted by the
// transition table. Caller-visible state mutations go through
// this function so the table is the single source of truth.
//
// Does NOT update n.UpdatedAt; callers that want wall-clock
// tracking should set that field after a successful transition.
// Kept pure so it's safely callable from replay + simulation.
//
// Side effect: when transitioning OUT of a blocked state
// (BLOCKED / WAITING_HUMAN), BlockedBy is cleared — the
// blocker references are no longer meaningful once the
// node is running or settled. Nodes entering ACTIVE with a
// non-empty BlockedBy would otherwise read as self-
// contradictory in audit reports.
func (n *Node) SetState(next State) error {
	if _, ok := transitions[n.Status]; !ok {
		return fmt.Errorf("%w: unknown from-state %q", ErrInvalidTransition, n.Status)
	}
	if !transitions[n.Status][next] {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, n.Status, next)
	}
	prev := n.Status
	n.Status = next
	if (prev == StateBlocked || prev == StateWaitingHuman) &&
		next != StateBlocked && next != StateWaitingHuman {
		n.BlockedBy = nil
	}
	return nil
}

// RevisionCycleOpen is set by BumpRevision when a revision
// cycle begins; cleared by SetState when transitioning out of
// NEEDS_REVISION or by ResetRevisionCycle. Used so that
// repeated BumpRevision calls within the SAME cycle (e.g. a
// second critic objecting to the same revision pass) don't
// burn additional attempts.
//
// Exposed as a method rather than a struct field so adding
// this doesn't break JSON round-trips with v1 Node records
// that don't have the field set.
func (n *Node) revisionCycleOpen() bool {
	return n.Status == StateNeedsRevision
}

// BumpRevision records a new NEEDS_REVISION cycle and enforces
// MaxNeedsRevisionCycles. Returns ErrRevisionCapReached when the
// increment would exceed the cap, at which point the caller
// should escalate (to a sibling replan, then a parent replan,
// then HITL).
//
// Does NOT change Status — callers pair BumpRevision with
// SetState(StateNeedsRevision) when both are needed. Separating
// the two lets callers bump the counter without re-entering the
// state.
//
// Per-cycle counting: if the node is ALREADY in NEEDS_REVISION
// when BumpRevision is called, the counter is NOT incremented
// — additional objections during the same revision pass don't
// burn attempts. The counter increments when a fresh cycle
// opens (node transitioned out of NEEDS_REVISION and back in)
// OR when the node is in any other status and BumpRevision is
// called to start a new cycle. This prevents the failure mode
// codex flagged: a single revision pass running the cap (3×)
// because the critic was polled multiple times.
func (n *Node) BumpRevision(reason string) error {
	n.RevisionReason = reason
	if n.revisionCycleOpen() {
		// Already inside a revision pass — reason update is
		// the only mutation.
		return nil
	}
	if n.RevisionAttempts+1 > MaxNeedsRevisionCycles {
		return ErrRevisionCapReached
	}
	n.RevisionAttempts++
	return nil
}

// TerminalStates are the two states no out-edges leave.
var terminalStates = map[State]bool{
	StateVerified: true,
	StateCanceled: true,
}

// IsTerminal reports whether a state has no out-edges. Useful for
// scheduler loops that need to stop polling a node once it settles.
func IsTerminal(s State) bool { return terminalStates[s] }

// RevisionClass is the result of ClassifyRevision.
type RevisionClass string

const (
	// RevisionRepair is the inner loop: a few nodes inside the
	// same section need adjustment. Can be retried automatically.
	RevisionRepair RevisionClass = "repair"

	// RevisionReplan is the outer loop: more than 3 nodes are
	// affected OR the scope crosses section boundaries. Requires
	// HITL approval before the planner regenerates.
	RevisionReplan RevisionClass = "replan"
)

// ClassifyRevision applies the SOW's repair-vs-replan rule:
// repair when <=3 nodes are affected AND all affected nodes share
// a section; replan otherwise. Callers supply the fanout count
// and a bool signaling cross-section fanout — ClassifyRevision
// doesn't walk the tree itself (keeps it cheap and stateless).
func ClassifyRevision(nodesAffected int, crossSection bool) RevisionClass {
	if crossSection || nodesAffected > ReplanThresholdNodes {
		return RevisionReplan
	}
	return RevisionRepair
}

// RollupStatus derives a parent node's Status from its children's
// statuses. Called by planners and report renderers; NEVER call
// SetState directly on a parent — the parent's status is a
// function of its children, not an independent value.
//
// Rules (in priority order):
//
//  1. If any child is ACTIVE or NEEDS_REVISION, parent is ACTIVE
//     (work is in-flight).
//  2. Else if any child is WAITING_HUMAN, parent is WAITING_HUMAN.
//  3. Else if any child is BLOCKED, parent is BLOCKED.
//  4. Else if all children are VERIFIED, parent is VERIFIED.
//  5. Else if all children are VERIFIED or CANCELED (with at least
//     one VERIFIED), parent is VERIFIED (canceled children don't
//     prevent happy-path rollup).
//  6. Else if all children are COMPLETED or VERIFIED, parent is
//     COMPLETED (waiting for the final review pass).
//  7. Else if all children are CANCELED, parent is CANCELED.
//  8. Else if any child is READY, parent is READY.
//  9. Else parent is DRAFT (the only remaining possibility).
//
// Empty children list (leaf node) returns DRAFT — caller should
// not invoke RollupStatus on a leaf; doing so is harmless but the
// returned value is meaningless.
func RollupStatus(children []State) State {
	if len(children) == 0 {
		return StateDraft
	}

	counts := map[State]int{}
	for _, s := range children {
		counts[s]++
	}

	if counts[StateActive] > 0 || counts[StateNeedsRevision] > 0 {
		return StateActive
	}
	if counts[StateWaitingHuman] > 0 {
		return StateWaitingHuman
	}
	if counts[StateBlocked] > 0 {
		return StateBlocked
	}
	total := len(children)
	if counts[StateVerified] == total {
		return StateVerified
	}
	if counts[StateVerified]+counts[StateCanceled] == total && counts[StateVerified] > 0 {
		return StateVerified
	}
	// Canceled children should roll up the same way under both
	// the VERIFIED and COMPLETED branches — a mixed
	// {COMPLETED, CANCELED, VERIFIED} set means the non-canceled
	// work finished successfully, so the parent is COMPLETED
	// (awaiting final review). Without this branch the caller
	// falls through to DRAFT, contradicting the other terminal
	// rollup rules.
	if counts[StateCompleted]+counts[StateVerified]+counts[StateCanceled] == total &&
		(counts[StateCompleted] > 0 || counts[StateVerified] > 0) {
		return StateCompleted
	}
	if counts[StateCanceled] == total {
		return StateCanceled
	}
	if counts[StateReady] > 0 {
		return StateReady
	}
	return StateDraft
}
