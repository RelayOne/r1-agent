package hub

// LaneStatus is the lifecycle state of a lane in the lanes protocol.
//
// See specs/lanes-protocol.md §3.1 for the full state machine, and §3.3 for
// the allowed transition table. The six values below are exhaustive; a
// seventh requires a wire-version bump per spec §5.6.
type LaneStatus string

// Lane lifecycle states. Three are non-terminal (pending, running, blocked)
// and three are terminal (done, errored, cancelled). Terminal lanes emit no
// further lane.delta events; surfaces receiving deltas after terminal SHOULD
// discard them and log a protocol.violation warning per spec §3.3.
const (
	// LaneStatusPending: lane created, not yet running. Queued behind a
	// concurrency cap or awaiting a parent gate. Non-terminal.
	LaneStatusPending LaneStatus = "pending"

	// LaneStatusRunning: actively producing deltas (tool executing, Lobe
	// thinking, main thread streaming). Non-terminal.
	LaneStatusRunning LaneStatus = "running"

	// LaneStatusBlocked: paused on external input. The reason_code on the
	// transitioning lane.status event names the gate (awaiting_user,
	// awaiting_review, awaiting_dependency). Non-terminal.
	LaneStatusBlocked LaneStatus = "blocked"

	// LaneStatusDone: completed normally. Final lane.status carries
	// reason="ok". Terminal.
	LaneStatusDone LaneStatus = "done"

	// LaneStatusErrored: failed. Final lane.status carries
	// reason="<error_taxonomy_code>" mapped from internal/stokerr.
	// Terminal.
	LaneStatusErrored LaneStatus = "errored"

	// LaneStatusCancelled: killed by operator, parent, or budget gate.
	// Final lane.status carries reason="cancelled_by_<actor>". Terminal.
	LaneStatusCancelled LaneStatus = "cancelled"
)

// IsValid reports whether s is one of the six declared LaneStatus values.
// The empty string and any other value return false; this is the gate used
// at every wire-format ingress (subscribe input, MCP tool input, and the
// Lane.Transition validation in cortex-core).
func (s LaneStatus) IsValid() bool {
	switch s {
	case LaneStatusPending,
		LaneStatusRunning,
		LaneStatusBlocked,
		LaneStatusDone,
		LaneStatusErrored,
		LaneStatusCancelled:
		return true
	default:
		return false
	}
}

// IsTerminal reports whether s is one of the three terminal states
// (done, errored, cancelled). Terminal lanes accept no further deltas
// and no further state transitions per spec §3.3.
func (s LaneStatus) IsTerminal() bool {
	switch s {
	case LaneStatusDone, LaneStatusErrored, LaneStatusCancelled:
		return true
	default:
		return false
	}
}
