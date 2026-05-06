package hub

// LaneKind is the category of a lane in the lanes protocol.
//
// See specs/lanes-protocol.md §4.1 (lane.created event, kind enum) for the
// full taxonomy. The five values below are exhaustive; a sixth requires a
// wire-version bump per spec §5.6.
type LaneKind string

// Lane kind taxonomy. Exactly one lane per session has Kind == LaneKindMain.
// LaneKindLobe is the largest population (one per cognitive Lobe). Tool
// lanes are spawned only when a tool call is promoted to its own surface
// thread (long-running, > 2s wall clock per spec §8.1). MissionTask lanes
// represent top-level mission-task work units and have no parent. Router
// lanes carry routing-decision activity from the cortex Router.
const (
	// LaneKindMain: the main agent thread driving Sonnet/Opus through the
	// agentloop.Loop. Exactly one per session.
	LaneKindMain LaneKind = "main"

	// LaneKindLobe: a cortex Lobe (MemoryRecallLobe, PlanUpdateLobe, etc.).
	// One lane per Lobe spawn.
	LaneKindLobe LaneKind = "lobe"

	// LaneKindTool: a long-running tool call promoted to its own surface
	// thread so the surface can show streaming progress and a kill control.
	LaneKindTool LaneKind = "tool"

	// LaneKindMissionTask: a top-level mission-task work unit. Has no
	// parent_lane_id; lives at the same level as the main lane.
	LaneKindMissionTask LaneKind = "mission_task"

	// LaneKindRouter: a routing-decision lane carrying the cortex Router
	// activity (see cortex Router.Decide).
	LaneKindRouter LaneKind = "router"
)

// IsValid reports whether k is one of the five declared LaneKind values.
// The empty string and any other value return false; this is the gate used
// at every wire-format ingress (subscribe input, MCP tool input, lane
// constructor argument validation in cortex-core).
func (k LaneKind) IsValid() bool {
	switch k {
	case LaneKindMain,
		LaneKindLobe,
		LaneKindTool,
		LaneKindMissionTask,
		LaneKindRouter:
		return true
	default:
		return false
	}
}
